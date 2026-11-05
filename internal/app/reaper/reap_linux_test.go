package reaper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

const prSetChildSubreaper = 36

// subreaper makes this test process adopt orphans, as PID 1 would.
func subreaper(t *testing.T) {
	t.Helper()
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		t.Skipf("PR_SET_CHILD_SUBREAPER: %v", errno)
	}
}

// state returns the process's /proc state letter, or "" once it is gone.
func state(pid int) string {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	_, rest, ok := strings.Cut(string(stat), ") ")
	if !ok {
		return ""
	}
	return rest[:1]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// orphan leaves behind an exited, unreaped grandchild adopted by this process
// and returns its pid. Reap must not be running yet if the caller wants it
// still there.
func orphan(t *testing.T) int {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command("sh", "-c", `sleep 0.05 >/dev/null & echo $!`)
	cmd.Stdout = &out
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	if err := Wait(cmd); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func exitCode7(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 7")
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	eventually(t, "tracked child to exit", func() bool { return state(pid) == "Z" })
	return cmd
}

func checkExit7(t *testing.T, err error) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Errorf("Wait = %v, want exit status 7", err)
	}
}

func TestTrackedChildIsNotStolen(t *testing.T) {
	cmd := exitCode7(t)
	reapOrphans(discard)
	if s := state(cmd.Process.Pid); s != "Z" {
		t.Errorf("state after reaping = %q, want the zombie left alone", s)
	}
	checkExit7(t, Wait(cmd))
}

func TestOrphanIsReaped(t *testing.T) {
	subreaper(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Reap(ctx, discard); close(done) }()
	defer func() { cancel(); <-done }()

	pid := orphan(t)
	eventually(t, "orphan to be reaped", func() bool { return state(pid) == "" })
}

// An orphan behind a tracked zombie cannot be peeked at until the tracked one
// is collected, and no further SIGCHLD will arrive; Wait has to wake the loop.
func TestOrphanBehindTrackedChildIsReapedAfterWait(t *testing.T) {
	subreaper(t)
	cmd := exec.Command("sh", "-c", "exit 7")
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	pid := orphan(t)
	eventually(t, "both to exit", func() bool {
		return state(pid) == "Z" && state(cmd.Process.Pid) == "Z"
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Reap(ctx, discard); close(done) }()
	defer func() { cancel(); <-done }()

	checkExit7(t, Wait(cmd))
	eventually(t, "orphan to be reaped", func() bool { return state(pid) == "" })
}

func TestConcurrentCommandsKeepTheirExitStatus(t *testing.T) {
	subreaper(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Reap(ctx, discard); close(done) }()
	defer func() { cancel(); <-done }()

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			cmd := exec.Command("sh", "-c", "exit 7")
			if err := Start(cmd); err != nil {
				t.Error(err)
				return
			}
			checkExit7(t, Wait(cmd))
		})
	}
	wg.Wait()
}

func TestFailedStartIsNotTracked(t *testing.T) {
	if err := Start(exec.Command("/nonexistent/binary")); err == nil {
		t.Fatal("want an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tracked) != 0 {
		t.Errorf("tracked = %v", tracked)
	}
}

func TestPeekWithNoExitedChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = Wait(cmd)
	}()
	pid, err := peekExited()
	if err != nil || pid != 0 {
		t.Errorf("peek = %d, %v", pid, err)
	}
}
