// Package process turns stored documents into searchable ones: a queue
// worker claims a job, runs the external tools (poppler; OCRmyPDF and the
// image converters) in a private work directory and stores the
// text, the page count and the archive PDF.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Command is one run of an external tool.
type Command struct {
	// Name is the program, looked up in PATH.
	Name string
	Args []string
	// Dir is the working directory; it is also HOME and TMPDIR of the tool,
	// so nothing it writes ends up outside the job's work directory.
	Dir string
	// Env holds extra environment variables (KEY=value) on top of the
	// minimal environment.
	Env []string
}

// Runner runs external tools. The worker uses ExecRunner; tests use fakes.
type Runner interface {
	// Run runs c and returns its standard output. A tool that does not
	// start, exits with a status other than 0 or is killed gives a
	// *ToolError. When ctx ends first, the tool is stopped and the error
	// wraps ctx's error.
	Run(ctx context.Context, c Command) ([]byte, error)
}

// ToolError is a tool that failed. Its Error names the tool and the exit
// status only: tool output can contain file contents, so Stderr is for the
// server log, never for users.
type ToolError struct {
	Tool     string
	ExitCode int    // -1 when it did not start or was killed by a signal
	Stderr   string // the end of its standard error, for the log
	Err      error  // the cause when it did not start
}

func (e *ToolError) Error() string {
	if e.ExitCode >= 0 {
		return fmt.Sprintf("%s exited with status %d", e.Tool, e.ExitCode)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Tool, e.Err)
	}
	return e.Tool + " was killed"
}

// Unwrap returns the cause of a tool that did not start.
func (e *ToolError) Unwrap() error { return e.Err }

// Limits of ExecRunner.
const (
	// stderrTail is how much of a tool's standard error is kept, from
	// its end.
	stderrTail = 64 << 10
	// maxStdout bounds what a tool may write to standard output; the tools
	// write their real output to files.
	maxStdout = 4 << 20
	// waitDelay is how long a stopped tool gets to exit after SIGTERM
	// before it is killed.
	waitDelay = 10 * time.Second
)

// ExecRunner runs tools as child processes. Each runs in a process group of
// its own (on Unix), so cancelling stops the tools it started too: the
// group gets SIGTERM, and the tool is killed when it has not exited
// waitDelay later. The tools get a minimal environment: PATH, a UTF-8
// locale, and HOME and TMPDIR in the command's directory.
type ExecRunner struct {
	// started, when set, is called after the tool started (tests).
	started func(*exec.Cmd)
}

// Run implements Runner.
func (r *ExecRunner) Run(ctx context.Context, c Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...) //nolint:gosec // fixed tool names; arguments are file names the worker chose
	cmd.Dir = c.Dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"HOME=" + c.Dir,
		"TMPDIR=" + c.Dir,
	}
	cmd.Env = append(cmd.Env, c.Env...)
	stdout := &limitedBuffer{max: maxStdout}
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = waitDelay
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, ctx.Err())
		}
		return nil, &ToolError{Tool: c.Name, ExitCode: -1, Err: err}
	}
	if r.started != nil {
		r.started(cmd)
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		killProcessGroup(cmd)
		return nil, fmt.Errorf("%s: %w", c.Name, ctx.Err())
	}
	if err != nil {
		te := &ToolError{Tool: c.Name, ExitCode: -1, Stderr: stderr.String()}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			te.ExitCode = exitErr.ExitCode()
		} else {
			te.Err = err
		}
		return nil, te
	}
	if stdout.overflow {
		return nil, &ToolError{Tool: c.Name, ExitCode: -1, Stderr: stderr.String(), Err: errors.New("too much output")}
	}
	return stdout.buf, nil
}

// limitedBuffer keeps the first max bytes written to it and notes whether
// more came. Writes never fail, so the tool is not stopped by a broken
// pipe. (It has no ReadFrom, so that io.Copy goes through Write.)
type limitedBuffer struct {
	buf      []byte
	max      int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := b.max - len(b.buf)
	if len(p) > room {
		b.overflow = true
		p = p[:max(room, 0)]
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return n, nil
	}
	if over := len(b.buf) + len(p) - b.max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.buf) }
