package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// JSON tags must match the platform's read_file params schema. Pointer fields
// preserve presence so omitted values can default to 1 and defaultReadMaxLine
// before explicitly supplied values are validated.
type readFileArgs struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset"`
	Limit    *int   `json:"limit"`
}

// The platform returns multimodal blocks for these extensions; this implementation
// returns text because it has no local multimodal support.
var readFileMediaExts = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image",
	".gif": "image", ".bmp": "image", ".webp": "image",
	".mp4": "video", ".mov": "video", ".webm": "video",
	".mkv": "video", ".avi": "video", ".m4v": "video",
}

// Match the platform's 100 MiB text-file limit.
const readFileMaxSize = 100 << 20

const readFileEmptyText = "The file is empty."

var (
	readLineTruncatedMessage = fmt.Sprintf(
		"<response clipped><NOTE>Lines longer than **%d characters** will be **truncated**.</NOTE>",
		defaultReadLineLen)
	readTotalTruncatedMessage = fmt.Sprintf(
		"<response clipped><NOTE>Total length longer than **%d characters** will be "+
			"truncated. You should provide more specific `offset` and `limit` to narrow down the range of "+
			"lines to read.</NOTE>", defaultReadTotalLen)
)

// readFile matches cat -n output and the platform's truncation behavior. Media
// and non-UTF-8 files return shell-extraction guidance because local multimodal
// and server-side conversion are unavailable.
func (e *executor) readFile(ctx context.Context, call Call) (Result, error) {
	var args readFileArgs
	if err := parseArgs(call.Input, &args, "file_path"); err != nil {
		return errResult("invalid arguments: %v. Please fix the JSON arguments and retry.", err), nil
	}
	offset := 1
	if args.Offset != nil {
		offset = *args.Offset
	}
	limit := defaultReadMaxLine
	if args.Limit != nil {
		limit = *args.Limit
	}

	filePath := args.FilePath
	if res, failed := requireAbsPath(filePath); failed {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	st, res, failed := statFile(filePath)
	if failed {
		return res, nil
	}

	if kind, ok := readFileMediaExts[strings.ToLower(filepath.Ext(filePath))]; ok {
		return errResult("Error: reading %s files is not supported by the local read_file tool. "+
			"Inspect %s with shell tools (e.g. file, identify, ffprobe) instead.", kind, filePath), nil
	}
	// The post-read size check catches growth after Stat.
	if st.Size() > readFileMaxSize {
		return errResult("Error: the file %s is too large.", filePath), nil
	}
	data, err := readFileWithCtx(ctx, filePath, readFileMaxSize)
	var tooLarge *tooLargeError
	switch {
	case err == nil:
	case errors.As(err, &tooLarge):
		return errResult("Error: the file %s is too large.", filePath), nil
	default:
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return errResult("failed to read %s: %v. Make sure the file exists and is readable, then retry.", filePath, err), nil
	}
	if !utf8.Valid(data) {
		// Without the platform's conversion service, direct the model to extract
		// binary content with shell.
		return errResult("Error: the file %s is not a text file and binary conversion is not supported by "+
			"the local read_file tool. Extract the needed content with shell tools (e.g. pdftotext, unzip), "+
			"or split the file first.", filePath), nil
	}
	return readTextResult(filePath, string(data), offset, limit), nil
}

func readTextResult(filePath, text string, offset, limit int) Result {
	// Avoid numbering the artificial empty line returned by strings.Split.
	if text == "" {
		return textResult(readFileEmptyText)
	}
	lines := strings.Split(text, "\n")
	if res, failed := validateReadRange(filePath, offset, limit, len(lines)); failed {
		return res
	}

	end := min(offset+limit-1, len(lines))
	sliced := lines[offset-1 : end]

	var resultLines []string
	totalLen := 0
	for _, line := range sliced {
		n := utf8.RuneCountInString(line)
		totalLen += min(n, defaultReadLineLen)
		if n > defaultReadLineLen {
			line = string([]rune(line)[:defaultReadLineLen]) + readLineTruncatedMessage
		}
		resultLines = append(resultLines, line)
		if totalLen > defaultReadTotalLen {
			// Append the total-output NOTE as a new result_line, so it is numbered too.
			resultLines = append(resultLines, readTotalTruncatedMessage)
			break
		}
	}

	var b strings.Builder
	for i, line := range resultLines {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d\t%s", i+offset, line)
	}
	result := b.String()
	return textResult(fmt.Sprintf("Here's the result of running `cat -n` on %s:\n%s\n", filePath, result))
}

func validateReadRange(filePath string, offset, limit, n int) (Result, bool) {
	if offset < 1 || offset > n {
		return errResult("Error: the offset %d is not within the range of lines of the file %s. "+
			"Please provide a valid offset. The range of lines is from 1 to %d.", offset, filePath, n), true
	}
	if limit <= 0 {
		return errResult("Error: the limit %d is not positive. Please provide a valid limit.", limit), true
	}
	if limit > defaultReadMaxLine {
		return errResult("Error: the limit %d is greater than the max line %d. Please provide a valid limit.",
			limit, defaultReadMaxLine), true
	}
	return Result{}, false
}

// Chunked reads allow cooperative cancellation and catch file growth after Stat.
func readFileWithCtx(ctx context.Context, filePath string, limit int64) ([]byte, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var buf []byte
	chunk := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, rerr := f.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if int64(len(buf)) > limit {
			return nil, &tooLargeError{path: filePath, size: int64(len(buf)), limit: limit}
		}
		if rerr == io.EOF {
			return buf, nil
		}
		if rerr != nil {
			return nil, rerr
		}
	}
}
