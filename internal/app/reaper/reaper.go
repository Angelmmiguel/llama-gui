// Package reaper does PID 1's duty of collecting orphans without stealing the
// children that os/exec is waiting for.
//
// As PID 1 we inherit every orphan in the container, such as a router child
// whose parent died, and each stays a zombie until waited for. A plain
// wait4(-1) loop would also collect our own exec.Cmd children, and their
// Cmd.Wait would then fail with ECHILD. So the loop peeks at an exited child
// with waitid(WNOWAIT), which leaves it waitable, and reaps it only if it is
// not tracked.
//
// Every process this binary spawns must therefore use Start and Wait below in
// place of cmd.Start and cmd.Wait; the supervisor, build probes and hardware
// detection included. cmd.Run, cmd.Output and cmd.CombinedOutput bypass the
// registry and must not be used. Outside PID 1 Reap does not run and these are
// plain cmd.Start and cmd.Wait, so callers need no special case:
//
//	if err := reaper.Start(cmd); err != nil { ... }
//	err := reaper.Wait(cmd) // never ECHILD, whatever else exits meanwhile
//
// There is no separate register step because the pid must be registered before
// the loop can look: a child can exit before cmd.Start even returns.
package reaper

import (
	"os/exec"
	"sync"
)

var (
	// gate is held for reading across cmd.Start and for writing across a
	// peek-and-reap, so the loop never sees a child that is not yet registered.
	gate sync.RWMutex

	mu      sync.Mutex
	tracked = map[int]struct{}{}

	// wake re-runs the loop after Wait. It may have stopped at a tracked child
	// with orphans behind it, whose SIGCHLD has already been consumed.
	wake = make(chan struct{}, 1)
)

// Start is cmd.Start, tracking the child until Wait.
func Start(cmd *exec.Cmd) error {
	gate.RLock()
	defer gate.RUnlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	tracked[cmd.Process.Pid] = struct{}{}
	return nil
}

// Wait is cmd.Wait for a command started with Start.
func Wait(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if cmd.Process == nil {
		return err
	}
	mu.Lock()
	delete(tracked, cmd.Process.Pid)
	mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
	return err
}

func isTracked(pid int) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := tracked[pid]
	return ok
}
