package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A spec researched under the next_open fill is not armed by the close-time
// run: this run fills at the close, and trading a model the spec was not
// backtested on is exactly the paper-versus-backtest gap the plan treats as
// a bug. The refusal is on the record like any other.
func TestNextOpenSpecIsNotArmedAtTheClose(t *testing.T) {
	f := setup(t, nil)
	path := filepath.Join(f.specs, "gld_gdx_pairs.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "sizing:\n") {
		t.Fatal("spec has no sizing block to anchor on")
	}
	src = strings.Replace(src, "sizing:\n", "execution:\n  signal_at: close\n  fill_at: next_open\nsizing:\n", 1)
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := f.runner().Run(context.Background(), "2026-09-04")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Strategies) != 1 || out.Strategies[0].Armed || !strings.Contains(out.Strategies[0].Reason, "fills at next_open") {
		t.Fatalf("strategies = %+v, want one refused for its fill", out.Strategies)
	}
	if out.Status != "failed" {
		t.Errorf("status = %s, want failed (nothing armed)", out.Status)
	}
}
