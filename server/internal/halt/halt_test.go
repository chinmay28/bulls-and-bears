package halt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotHaltedByDefault(t *testing.T) {
	dir := t.TempDir()
	if Halted(dir) {
		t.Fatal("a fresh data directory reads as halted")
	}
	if _, err := Status(dir); !errors.Is(err, ErrNotHalted) {
		t.Fatalf("Status = %v, want ErrNotHalted", err)
	}
}

func TestHaltThenResume(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 2, 19, 50, 0, 0, time.UTC)
	if err := Halt(dir, Marker{Reason: "drawdown 10.2% from high water", By: "risk gate", At: at}); err != nil {
		t.Fatal(err)
	}
	if !Halted(dir) {
		t.Fatal("not halted after Halt")
	}
	m, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Reason != "drawdown 10.2% from high water" || m.By != "risk gate" || !m.At.Equal(at) {
		t.Errorf("marker = %+v", m)
	}
	if fi, err := os.Stat(File(dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v, err %v; want 0600", fi.Mode(), err)
	}

	if err := Resume(dir); err != nil {
		t.Fatal(err)
	}
	if Halted(dir) {
		t.Fatal("still halted after Resume")
	}
	if err := Resume(dir); err != nil {
		t.Errorf("a second Resume should be a no-op, got %v", err)
	}
}

// The first reason is the one that stopped trading. A second halt — the app's
// button pressed while the kill switch already tripped — must not overwrite it.
func TestSecondHaltKeepsTheFirstReason(t *testing.T) {
	dir := t.TempDir()
	if err := Halt(dir, Marker{Reason: "first", By: "risk gate"}); err != nil {
		t.Fatal(err)
	}
	if err := Halt(dir, Marker{Reason: "second", By: "app"}); err != nil {
		t.Fatal(err)
	}
	m, _ := Status(dir)
	if m.Reason != "first" {
		t.Errorf("reason = %q, want the first one kept", m.Reason)
	}
	if m.At.IsZero() {
		t.Error("At was not filled in")
	}
}

func TestUnreadableMarkerStillHalts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(File(dir), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Halted(dir) {
		t.Fatal("a corrupt marker must still halt")
	}
	m, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Reason == "" {
		t.Error("status of a corrupt marker should say so")
	}
}

func TestHaltWritesWholeFileNotHalf(t *testing.T) {
	// A data directory that does not exist yet is made, privately.
	dir := filepath.Join(t.TempDir(), "data")
	if err := Halt(dir, Marker{Reason: "x", By: "cli"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HALT.tmp")); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode = %v, err %v; want 0700", fi.Mode(), err)
	}
}
