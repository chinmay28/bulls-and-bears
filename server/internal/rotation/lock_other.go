//go:build !unix

package rotation

import (
	"errors"
	"os"
)

// This runtime is deployed on Linux and developed on macOS; anywhere else
// the lock refuses rather than pretending to hold one, because a rotation
// that silently ran unlocked could open a second lot.
func lockFD(*os.File) error {
	return errors.New("file locking is not implemented on this platform")
}

func unlockFD(*os.File) error { return nil }

// Alive cannot be answered where the lock cannot be taken either.
func Alive(int) bool { return false }
