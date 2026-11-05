package hardware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ErrNoExec means files in a directory cannot be executed, typically a noexec mount.
var ErrNoExec = errors.New("directory does not allow execution")

// CanExecute checks that programs can run from dir by writing a tiny script and running it.
func CanExecute(dir string) error {
	f, err := os.CreateTemp(dir, ".exec-check-*")
	if err != nil {
		return fmt.Errorf("write to %s: %w", dir, err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	_, err = f.WriteString("#!/bin/sh\nexit 0\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(path, 0o700) //nolint:gosec // the script has to be executable
	}
	if err != nil {
		return fmt.Errorf("write to %s: %w", dir, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		err = exec.CommandContext(ctx, path).Run()
		// A concurrent fork can briefly inherit the write descriptor.
		if errors.Is(err, syscall.ETXTBSY) && attempt < 5 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		break
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: %s: %w", ErrNoExec, dir, err)
	}
	return fmt.Errorf("run from %s: %w", dir, err)
}
