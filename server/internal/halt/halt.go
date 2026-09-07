// Package halt is the one switch that stops trading.
//
// A halt is a marker file in the data directory. The scheduler checks for it
// before every run and does nothing while it is there; the drawdown kill switch
// in the risk gate, `bnb halt` on the command line and the Halt button in the
// app all write the same file, so there is exactly one way to be stopped and
// exactly one way to be started again. Resume removes it — nothing resumes
// on its own, which is the point of a kill switch.
//
// The marker carries why and when it was written, so a phone opened days
// later can say what happened rather than only that something did.
package halt

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Marker is what a halt file holds.
type Marker struct {
	// Reason is one sentence for a person: "drawdown 10.2% from high water".
	Reason string `json:"reason"`
	// By names what wrote it: "risk gate", "app", "cli".
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// File is the marker's path under a data directory.
func File(dataDir string) string { return filepath.Join(dataDir, "HALT") }

// Halt writes the marker. Writing it again keeps the original: the first
// reason is the one that stopped trading, and a later Halt while already
// halted is a no-op rather than a rewrite of history.
func Halt(dataDir string, m Marker) error {
	if _, err := Status(dataDir); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotHalted) {
		return err
	}
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// The directory is private from the start: it will hold a broker token.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	// Written whole and moved into place so a reader never sees half a file.
	tmp := File(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, File(dataDir))
}

// ErrNotHalted is Status's answer when there is no marker.
var ErrNotHalted = errors.New("not halted")

// Status reads the marker. ErrNotHalted when there is none. A marker that
// cannot be parsed still halts — a corrupt file is not permission to trade —
// and comes back with whatever could be read.
func Status(dataDir string) (Marker, error) {
	b, err := os.ReadFile(File(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return Marker{}, ErrNotHalted
	}
	if err != nil {
		return Marker{}, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return Marker{Reason: "halt marker is unreadable: " + err.Error(), By: "unknown"}, nil
	}
	return m, nil
}

// Halted is the yes/no form of Status. An unreadable data directory counts as
// halted: fail closed.
func Halted(dataDir string) bool {
	_, err := Status(dataDir)
	return !errors.Is(err, ErrNotHalted)
}

// Resume removes the marker. Resuming when not halted is fine and does nothing.
func Resume(dataDir string) error {
	err := os.Remove(File(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
