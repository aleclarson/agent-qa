package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

type commandExit int

func (e commandExit) Error() string { return fmt.Sprintf("command exited with status %d", e) }

func exitStatus(err error) int {
	var code commandExit
	var subprocess *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	case errors.As(err, &subprocess):
		if status, ok := subprocess.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return subprocess.ExitCode()
	default:
		return 1
	}
}

func overlayEnv(base []string, overrides map[string]string) []string {
	// os/exec keeps the last value for each environment variable.
	for key, value := range overrides {
		base = append(base, key+"="+value)
	}
	return base
}

func (a app) runCommand(ctx context.Context, dir string, argv []string, env map[string]string) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, overlayEnv(projectEnv(dir), env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.in, a.out, a.err
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("%w: %w", commandExit(127), err)
		}
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOEXEC) {
			return fmt.Errorf("%w: %w", commandExit(126), err)
		}
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	group := -cmd.Process.Pid
	select {
	case err := <-done:
		drainGroup(group)
		return err
	case <-ctx.Done():
		drainGroup(group)
		<-done
		return context.Cause(ctx)
	}
}

func drainGroup(group int) {
	if syscall.Kill(group, syscall.SIGTERM) != nil {
		return
	}
	deadline := time.Now().Add(350 * time.Millisecond)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(group, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(group, syscall.SIGKILL)
}
