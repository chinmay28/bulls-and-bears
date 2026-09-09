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
