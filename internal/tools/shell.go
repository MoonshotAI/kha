package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// JSON tags must match the platform's shell params schema. As on the platform,
// the model cannot override the timeout; executor.shellTimeout owns it.
type shellArgs struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

var shellTruncatedMessage = fmt.Sprintf(
	`<response clipped><NOTE>Result is longer than **%d characters**, will be **truncated**.</NOTE>`,
	defaultShellMaxLen)

// Timeouts are model-correctable; parent cancellation returns a Go error.
// Child environments are scrubbed to keep secrets out of model-visible output.
func (e *executor) shell(ctx context.Context, call Call) (Result, error) {
	var args shellArgs
	if err := parseArgs(call.Input, &args, "command"); err != nil {
		return errResult("invalid arguments: %v. Please fix the JSON arguments and retry.", err), nil
	}

	timeout := e.shellTimeout
	if timeout <= 0 {
		timeout = defaultShellTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, argv := shellCommandLine(args.Command)
	cmd := exec.CommandContext(tctx, name, argv...)
	if call.Workdir != "" {
		cmd.Dir = call.Workdir
	}
	cmd.Env = scrubEnv(os.Environ())
	// Bound output so commands such as `yes` cannot exhaust host memory before
	// the timeout kills them; invalid UTF-8 is discarded when combining output.
	byteLimit := defaultShellMaxLen * 4 // UTF-8 uses at most 4 bytes per character
	stdout := &limitedWriter{limit: byteLimit}
	stderr := &limitedWriter{limit: byteLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	if tctx.Err() != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		// CommandContext has killed the child; report a model-correctable timeout.
		return errResult("The shell tool execution timed out after %d seconds", int(timeout.Seconds())), nil
	}

	// A non-zero exit code is result data, not an error: include it in the text
	// without setting IsError, matching platform behavior.
	code := 0
	if runErr != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			code = exitErr.ExitCode()
		} else {
			return errResult("failed to start command: %v", runErr), nil
		}
	}

	var parts []string
	if code != 0 {
		parts = append(parts, fmt.Sprintf("**Exit Code: %d**", code), "")
	}
	out := strings.ToValidUTF8(string(stdout.buf), "")
	errOut := strings.ToValidUTF8(string(stderr.buf), "")
	if out != "" {
		parts = append(parts, out)
	}
	if errOut != "" {
		parts = append(parts, "", errOut)
	}
	if len(parts) == 0 {
		parts = append(parts, "Command executed with empty output")
	}
	text := strings.Join(parts, "\n")
	if s, cut := truncateRunes(text, defaultShellMaxLen); cut || stdout.truncated || stderr.truncated {
		text = s + shellTruncatedMessage
	}
	return textResult(text), nil
}

func shellCommandLine(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", command}
	}
	return "sh", []string{"-c", command}
}

type limitedWriter struct {
	buf       []byte
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remain := w.limit - len(w.buf)
	if remain > 0 {
		n := min(len(p), remain)
		w.buf = append(w.buf, p[:n]...)
	}
	if len(p) > remain {
		w.truncated = true
	}
	return len(p), nil
}
