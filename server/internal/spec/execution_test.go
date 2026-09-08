package spec

import (
	"errors"
	"strings"
	"testing"
)

// A spec without an execution block is the legacy fill; one with the block
// says what it says; anything the enum does not list is not a spec.
func TestExecutionBlock(t *testing.T) {
	registerPairs(t)
	ex := example(t)
	withBlock := replace(t, ex, "sizing:\n", "execution:\n  signal_at: close\n  fill_at: next_open\nsizing:\n")

	s, err := Load(write(t, "legacy.yaml", ex), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Execution != LegacyExecution {
		t.Errorf("no block: execution = %+v, want %+v", s.Execution, LegacyExecution)
	}

	s, err = Load(write(t, "next.yaml", withBlock), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Execution.SignalAt != SignalAtClose || s.Execution.FillAt != FillNextOpen {
		t.Errorf("with block: execution = %+v", s.Execution)
	}

	cases := map[string]string{
		"unknown fill":     strings.Replace(withBlock, "fill_at: next_open", "fill_at: at_noon", 1),
		"signal not close": strings.Replace(withBlock, "signal_at: close", "signal_at: open", 1),
		"fill missing":     strings.Replace(withBlock, "  fill_at: next_open\n", "", 1),
	}
	for name, src := range cases {
		_, err := Load(write(t, "bad.yaml", src), now)
		var inv *Invalid
		if !errors.As(err, &inv) || !strings.Contains(inv.Reason, "/execution") {
			t.Errorf("%s: err = %v, want an Invalid at /execution", name, err)
		}
	}
}

// research_study is optional and carried through when present.
func TestResearchStudy(t *testing.T) {
	registerPairs(t)
	ex := example(t)
	s, err := Load(write(t, "a.yaml", ex), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Provenance.ResearchStudy != "" {
		t.Errorf("research_study = %q, want empty", s.Provenance.ResearchStudy)
	}
	with := replace(t, ex, "  ttl_days: 90", "  research_study: gld_gdx_pairs\n  ttl_days: 90")
	s, err = Load(write(t, "b.yaml", with), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Provenance.ResearchStudy != "gld_gdx_pairs" {
		t.Errorf("research_study = %q", s.Provenance.ResearchStudy)
	}
	bad := replace(t, ex, "  ttl_days: 90", "  research_study: Sector-Rotation\n  ttl_days: 90")
	if _, err := Load(write(t, "c.yaml", bad), now); err == nil {
		t.Error("want a schema error for a study name outside the pattern")
	}
}
