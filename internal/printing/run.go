package printing

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// execRun is the real runner: the command's output and exit status, or an
// error when it could not be run at all or ran out of time.
func execRun(cmd string, args []string, timeout time.Duration) (Completed, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd, args...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = time.Second
	err := c.Run()
	done := Completed{
		Stdout: strings.ToValidUTF8(stdout.String(), "�"),
		Stderr: strings.ToValidUTF8(stderr.String(), "�"),
	}
	if err == nil {
		return done, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Completed{}, errors.New(cmd + " timed out")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			done.Returncode = code
			return done, nil
		}
		// Killed by a signal: there is no exit status to report.
		return Completed{}, errors.New("Command failed: " + strings.Join(append([]string{cmd}, args...), " ") + "\n" + done.Stderr)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return Completed{}, errors.New("spawn " + cmd + " ENOENT")
	}
	return Completed{}, err
}
