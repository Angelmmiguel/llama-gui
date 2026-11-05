package reaper

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// Reap collects orphans until ctx ends. Call it only as PID 1.
func Reap(ctx context.Context, log *slog.Logger) {
	sigchld := make(chan os.Signal, 1)
	signal.Notify(sigchld, syscall.SIGCHLD)
	defer signal.Stop(sigchld)
	for {
		reapOrphans(log)
		select {
		case <-ctx.Done():
			return
		case <-sigchld:
		case <-wake:
		}
	}
}

// reapOrphans reaps exited children until none is left or the next one is
// tracked. That child's Wait wakes the loop to continue past it, so an orphan
// behind it waits no longer than the tracked child's own collection.
func reapOrphans(log *slog.Logger) {
	for reapOne(log) {
	}
}

func reapOne(log *slog.Logger) bool {
	gate.Lock()
	defer gate.Unlock()
	pid, err := peekExited()
	if err != nil {
		if !errors.Is(err, syscall.ECHILD) {
			log.Warn("waiting for orphans", "error", err)
		}
		return false
	}
	if pid == 0 || isTracked(pid) {
		return false
	}
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); err != nil {
		log.Warn("reaping orphan", "pid", pid, "error", err)
		return false
	}
	log.Debug("reaped orphan", "pid", pid, "status", status.ExitStatus())
	return true
}

const (
	pAll    = 0          // P_ALL
	wExited = 0x00000004 // WEXITED
	wNoWait = 0x01000000 // WNOWAIT
)

// siginfo is the part of the kernel's siginfo_t that a child's exit fills in.
// The zero-length array aligns si_pid as the kernel's union does: offset 16 on
// 64-bit and 12 on 32-bit. The tail pads it to the kernel's 128 bytes.
type siginfo struct {
	_   [3]int32 // si_signo, si_errno, si_code
	_   [0]uintptr
	pid int32
	_   [112]byte
}

// peekExited returns the pid of an exited child, leaving it waitable, or 0 if
// no child has exited.
func peekExited() (int, error) {
	var info siginfo
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pAll, 0, uintptr(unsafe.Pointer(&info)), //nolint:gosec // G103: waitid has no wrapper in syscall.
			wExited|syscall.WNOHANG|wNoWait, 0, 0)
		switch errno {
		case 0:
			return int(info.pid), nil
		case syscall.EINTR:
		default:
			return 0, errno
		}
	}
}
