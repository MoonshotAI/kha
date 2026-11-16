package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// JSON tags must match the platform's edit_file params schema.
type editFileArgs struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// The platform schema requires a unique old_string unless replace_all is set,
// and requires old_string and new_string to differ.
func (e *executor) editFile(ctx context.Context, call Call) (Result, error) {
	var args editFileArgs
	if err := parseArgs(call.Input, &args, "file_path", "old_string", "new_string"); err != nil {
		return errResult("invalid arguments: %v. Please fix the JSON arguments and retry.", err), nil
	}
	if res, failed := requireAbsPath(args.FilePath); failed {
		return res, nil
	}
	if args.OldString == args.NewString {
		return errResult("Error: old_string and new_string must be different."), nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// Recheck the size after reading because the file may grow after Stat.
	st, res, failed := statFile(args.FilePath)
	if failed {
		return res, nil
	}
	if st.Size() > defaultEditMaxBytes {
		return errResult("Error: the file %s is too large to edit (%d bytes, limit %d bytes).",
			args.FilePath, st.Size(), defaultEditMaxBytes), nil
	}
	data, err := readFileWithCtx(ctx, args.FilePath, defaultEditMaxBytes)
	var tooLarge *tooLargeError
	switch {
	case err == nil:
	case errors.As(err, &tooLarge):
		return errResult("Error: the file %s is too large to edit (%d bytes, limit %d bytes).",
			tooLarge.path, tooLarge.size, tooLarge.limit), nil
	default:
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return errResult("failed to read %s: %v. "+
			"Make sure the file exists and is readable (check with read_file or shell), then retry.",
			args.FilePath, err), nil
	}
	if !utf8.Valid(data) {
		return errResult("Error: the file %s is not a text file. Please provide a valid file path.", args.FilePath), nil
	}
	oldContent := string(data)

	count := strings.Count(oldContent, args.OldString)
	if count == 0 {
		return errResult("Error: the string %s is not found in the file %s. Please provide a valid string.",
			args.OldString, args.FilePath), nil
	}
	if count >= 2 && !args.ReplaceAll {
		return errResult("Error: the string %s is found %d times in the file %s. "+
			"Please provide a valid string or use `replace_all: true` to replace **all** instances of `old_string`.",
			args.OldString, count, args.FilePath), nil
	}

	var newContent string
	if args.ReplaceAll {
		newContent = strings.ReplaceAll(oldContent, args.OldString, args.NewString)
	} else {
		newContent = strings.Replace(oldContent, args.OldString, args.NewString, 1)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// os.WriteFile preserves existing permissions; perm applies only to creation.
	if err := os.WriteFile(args.FilePath, []byte(newContent), 0o644); err != nil {
		return errResult("failed to write %s: %v. "+
			"Check that the file is writable (permissions or read-only filesystem), then retry.",
			args.FilePath, err), nil
	}

	return textResult(fmt.Sprintf("File %s has been edited successfully.", args.FilePath)), nil
}
