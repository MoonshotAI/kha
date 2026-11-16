package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// JSON tags must match the platform's write_file params schema.
type writeFileArgs struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
	Append   bool   `json:"append"`
}

// As on the platform, reading an existing file first is prompt guidance, not enforced.
func (e *executor) writeFile(ctx context.Context, call Call) (Result, error) {
	var args writeFileArgs
	if err := parseArgs(call.Input, &args, "file_path", "content"); err != nil {
		return errResult("invalid arguments: %v. Please fix the JSON arguments and retry.", err), nil
	}
	if res, failed := requireAbsPath(args.FilePath); failed {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	st, err := os.Stat(args.FilePath)
	switch {
	case err == nil && st.IsDir():
		return errResult("Error: the path %s is a directory. Please provide a valid file path.", args.FilePath), nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return errResult("failed to access %s: %v. Check that the path is accessible, or choose a different location.",
			args.FilePath, err), nil
	case errors.Is(err, fs.ErrNotExist):
		if merr := os.MkdirAll(filepath.Dir(args.FilePath), 0o755); merr != nil {
			return errResult("failed to create parent directory for %s: %v. "+
				"Check the parent path's permissions, or write into an existing directory.",
				args.FilePath, merr), nil
		}
	}

	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if args.Append {
		flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := os.OpenFile(args.FilePath, flag, 0o644)
	if err != nil {
		return errResult("failed to write %s: %v. "+
			"Check that the destination is writable (permissions or read-only filesystem), then retry.",
			args.FilePath, err), nil
	}
	werr := writeAllWithCtx(ctx, f, []byte(args.Content))
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return errResult("failed to write %s: %v. "+
			"Check that the destination is writable (permissions or read-only filesystem), then retry.",
			args.FilePath, werr), nil
	}

	text := fmt.Sprintf("File %s has been written successfully.", args.FilePath)
	return textResult(text), nil
}

// Chunked writes allow cooperative cancellation.
func writeAllWithCtx(ctx context.Context, w io.Writer, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(len(data), 256<<10)
		written, err := w.Write(data[:n])
		data = data[written:]
		if err != nil {
			return err
		}
	}
	return nil
}
