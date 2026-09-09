package rotation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LockFile is the exclusive lock a running rotation holds on a data
// directory.
func LockFile(dataDir string) string { return filepath.Join(dataDir, "rotation.lock") }

// Lock is held for as long as a process may act on a data directory.
type Lock struct{ f *os.File }

// Acquire takes the data directory's lock, or reports who has it.
//
// This matters more here than it looks. The book is what stops a second lot
// being opened on top of the first, and it only works if one process at a
// time reads it, decides, and appends. Two rotations over one directory —
// a daemon and a scheduled run, say, or two copies of the daemon after a
// botched restart — would each read a flat book, each decide to buy, and
// each buy, because neither's order is visible to the other until it has
// already gone out.
//
// The lock is advisory and held by the open file descriptor, so the kernel
// drops it if the process dies: a crash leaves no stale lock to clear.
func Acquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(LockFile(dataDir), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFD(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("rotation: %s is already being run by another process (%s): %w",
			dataDir, holder(dataDir), err)
	}
	// Whose it is, for the next process's error message. Best effort: the
	// lock is the fd, not this text.
	_ = f.Truncate(0)
	if _, err := f.WriteAt([]byte(fmt.Sprintf("pid %d\n", os.Getpid())), 0); err == nil {
		_ = f.Sync()
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFD(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

func holder(dataDir string) string {
	pid, ok := Holder(dataDir)
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf("pid %d", pid)
}

// Holder reads the pid in the lock file without touching the lock itself, so
// something that only wants to report on a running rotation — the app's
// status card — cannot contend with the rotation for it.
func Holder(dataDir string) (int, bool) {
	b, err := os.ReadFile(LockFile(dataDir))
	if err != nil {
		return 0, false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "pid %d", &pid); err != nil {
		return 0, false
	}
	return pid, pid > 0
}
