//go:build unix

package rotation

import (
	"os"
	"syscall"
)

// lockFD takes an exclusive advisory lock without waiting for it. The kernel
// releases it when the file descriptor closes, including when the process
// dies, which is what keeps a crash from leaving a lock nobody can clear.
func lockFD(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFD(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// Alive reports whether a process is still running, which is how a reader
// tells a lock that is held from one whose holder died. Signal 0 performs
// the permission and existence checks and delivers nothing.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
