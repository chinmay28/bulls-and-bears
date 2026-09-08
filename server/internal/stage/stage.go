// Package stage records which specs the operator has promoted from paper to
// live. A spec earns its place through research; whether it may touch real
// money is the operator's separate decision (docs/PLAN.md Phase 4: go live
// small, after a month of paper agreeing with the backtest), and it lives
// in the data directory rather than in the spec so that research cannot
// make it and a re-run cannot unmake it.
//
// Every spec is Paper until promoted. In dry-run the stage changes nothing:
// paper is what the book is. In live mode the runner arms only Live specs.
package stage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Stage is where a spec may trade.
type Stage string

const (
	Paper Stage = "paper"
	Live  Stage = "live"
)

// Parse reads a stage off the wire.
func Parse(s string) (Stage, error) {
	switch Stage(strings.ToLower(strings.TrimSpace(s))) {
	case Paper:
		return Paper, nil
	case Live:
		return Live, nil
	}
	return "", fmt.Errorf("stage: %q is not paper or live", s)
}

// File is where a data directory keeps the promotions.
func File(dataDir string) string { return filepath.Join(dataDir, "stages.json") }

// Load reads every promotion: spec name to stage. A missing file is no
// promotions; an unreadable one is an error, because "nobody is live" is not
// a safe guess when the file that says who is cannot be read.
func Load(dataDir string) (map[string]Stage, error) {
	raw, err := os.ReadFile(File(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Stage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out map[string]Stage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("stage: %s: %w", File(dataDir), err)
	}
	if out == nil {
		out = map[string]Stage{}
	}
	for name, st := range out {
		if st != Paper && st != Live {
			return nil, fmt.Errorf("stage: %s: %q has stage %q", File(dataDir), name, st)
		}
	}
	return out, nil
}

// Of is a spec's stage: Live if promoted, Paper otherwise.
func Of(stages map[string]Stage, name string) Stage {
	if stages[name] == Live {
		return Live
	}
	return Paper
}

// Set records a spec's stage. Paper removes the entry: the file lists only
// what is promoted, so an empty file and no file mean the same thing.
func Set(dataDir, name string, st Stage) error {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("stage: bad spec name %q", name)
	}
	if st != Paper && st != Live {
		return fmt.Errorf("stage: %q is not paper or live", st)
	}
	stages, err := Load(dataDir)
	if err != nil {
		return err
	}
	if st == Live {
		stages[name] = Live
	} else {
		delete(stages, name)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(stages, "", "  ")
	if err != nil {
		return err
	}
	tmp := File(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, File(dataDir))
}

// LiveNames lists the promoted specs, sorted.
func LiveNames(stages map[string]Stage) []string {
	var out []string
	for name, st := range stages {
		if st == Live {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
