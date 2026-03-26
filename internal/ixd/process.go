package ixd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// runProcess executes a command via bash, streaming stdout/stderr as SSE events.
// It returns the process exit code. The process runs in its own process group
// so the entire tree can be killed on cancellation or timeout.
//
// Cherry-picked patterns from OpenSandbox execd pkg/runtime/command.go:
//   - Process group via SysProcAttr{Setpgid: true}
//   - Signal forwarding to process group on context cancellation
//   - Bounded scanner buffer (1MB max line)
//   - WaitGroup to drain pipe goroutines before sending "complete"
//   - Exit code extraction from exec.ExitError
func runProcess(ctx context.Context, command, cwd string, timeout int, sse *sseWriter) (int, error) {
	// Apply timeout if specified.
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}

	start := time.Now()

	if err := cmd.Start(); err != nil {
		sse.Send("error", map[string]any{
			"text": err.Error(),
			"code": 1,
		})
		return 1, nil
	}

	// Kill the entire process group on context cancellation.
	// This goroutine exits when ctx is done (timeout, cancel, or normal completion).
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	// Stream stdout and stderr concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go streamPipe(&wg, stdout, "stdout", sse)
	go streamPipe(&wg, stderr, "stderr", sse)

	// Wait for the process to finish, then wait for pipe goroutines to drain.
	waitErr := cmd.Wait()
	wg.Wait()

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// Non-exit error (e.g., context cancelled before process started).
			sse.Send("error", map[string]any{
				"text": waitErr.Error(),
				"code": 1,
			})
			return 1, nil
		}
	}

	sse.Send("complete", map[string]any{
		"exit_code":  exitCode,
		"elapsed_ms": time.Since(start).Milliseconds(),
	})

	return exitCode, nil
}

// streamPipe reads lines from a pipe and sends each as an SSE event.
// Uses a bounded 1MB buffer to prevent OOM on binary output.
func streamPipe(wg *sync.WaitGroup, pipe io.ReadCloser, event string, sse *sseWriter) {
	defer wg.Done()
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		sse.Send(event, map[string]string{"text": scanner.Text() + "\n"})
	}
}
