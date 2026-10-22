package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

var errNotFoundForTest = exec.ErrNotFound

// TestHelperProcess is the child process of the ExecRunner tests, started
// from this test binary: the mode follows "--".
func TestHelperProcess(*testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 {
		return
	}
	switch os.Args[i+1] {
	case "stdout":
		fmt.Print("hello")
		os.Exit(0)
	case "env":
		fmt.Print(strings.Join(os.Environ(), "\n"))
		os.Exit(0)
	case "fail":
		// More standard error than the runner keeps, ending in a marker.
		fmt.Fprint(os.Stderr, strings.Repeat("x", 2*stderrTail)+"END")
		os.Exit(3)
	case "flood":
		fmt.Print(strings.Repeat("y", maxStdout+1))
		os.Exit(0)
	case "block":
		select {}
	}
	os.Exit(2)
}

func helper(t *testing.T, mode string) Command {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Command{Name: exe, Args: []string{"-test.run=^TestHelperProcess$", "--", mode}, Dir: t.TempDir()}
}

func TestExecRunnerOutput(t *testing.T) {
	out, err := (&ExecRunner{}).Run(context.Background(), helper(t, "stdout"))
	if err != nil || string(out) != "hello" {
		t.Fatalf("Run: %q, %v", out, err)
	}
}

func TestExecRunnerEnvironment(t *testing.T) {
	t.Setenv("PAPERLESS_GO_DATABASE_URL", "postgres://secret")
	c := helper(t, "env")
	c.Env = []string{"OMP_THREAD_LIMIT=1"}
	out, err := (&ExecRunner{}).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(string(out), "\n")
	slices.Sort(env)
	want := []string{"HOME=" + c.Dir, "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "OMP_THREAD_LIMIT=1", "PATH=" + os.Getenv("PATH"), "TMPDIR=" + c.Dir}
	if !slices.Equal(env, want) {
		t.Fatalf("environment %q, want %q", env, want)
	}
}

func TestExecRunnerExitStatus(t *testing.T) {
	_, err := (&ExecRunner{}).Run(context.Background(), helper(t, "fail"))
	var te *ToolError
	if !errors.As(err, &te) || te.ExitCode != 3 {
		t.Fatalf("Run: %v", err)
	}
	if len(te.Stderr) != stderrTail || !strings.HasSuffix(te.Stderr, "END") {
		t.Fatalf("stderr: %d bytes, ends %q", len(te.Stderr), te.Stderr[max(len(te.Stderr)-5, 0):])
	}
	if strings.Contains(te.Error(), "xxx") {
		t.Fatalf("error shows standard error: %.80s", te.Error())
	}
}

func TestExecRunnerTooMuchOutput(t *testing.T) {
	_, err := (&ExecRunner{}).Run(context.Background(), helper(t, "flood"))
	var te *ToolError
	if !errors.As(err, &te) || te.ExitCode != -1 || !strings.Contains(te.Error(), "too much output") {
		t.Fatalf("Run: %v", err)
	}
}

func TestExecRunnerNotInstalled(t *testing.T) {
	_, err := (&ExecRunner{}).Run(context.Background(), Command{Name: "paperless-go-no-such-tool", Dir: t.TempDir()})
	var te *ToolError
	if !errors.As(err, &te) || !errors.Is(err, exec.ErrNotFound) || errorMessage(err) != "paperless-go-no-such-tool is not installed." {
		t.Fatalf("Run: %v", err)
	}
}

func TestExecRunnerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &ExecRunner{started: func(*exec.Cmd) { cancel() }}
	_, err := r.Run(ctx, helper(t, "block"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 5}
	for _, s := range []string{"ab", "cd", "ef", "ghijklmn", "o"} {
		_, _ = b.Write([]byte(s))
	}
	if b.String() != "klmno" {
		t.Fatalf("tail %q", b.String())
	}
}
