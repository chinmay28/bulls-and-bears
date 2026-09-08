package spec

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// The example spec's generated_at is 2026-09-06 with a 90 day TTL; this clock
// sits comfortably inside it.
var now = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

type fakeStrategy struct{ universe []string }

func (f fakeStrategy) Targets(map[string][]bars.Bar, []strategy.Position) (map[string]float64, error) {
	return map[string]float64{}, nil
}
func (f fakeStrategy) Universe() []string { return f.universe }

var registerOnce sync.Once

// registerPairs makes pairs_zscore a known strategy for this package's tests.
// Nothing here imports the real implementation, so the name is free; the Once
// keeps a second test from tripping the registry's double-registration panic.
func registerPairs(t *testing.T) {
	t.Helper()
	registerOnce.Do(func() {
		strategy.Register("pairs_zscore", func(u []string, _ strategy.Params, _ strategy.Sizing) (strategy.Strategy, error) {
			return fakeStrategy{u}, nil
		})
	})
}

func example(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gld_gdx_pairs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// write puts src in a temp dir under name and returns the path.
func write(t *testing.T, name, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// replace edits the example spec, failing loudly if the text to edit is not
// there — a silent no-op would test the wrong thing.
func replace(t *testing.T, src, old, new string) string {
	t.Helper()
	if !strings.Contains(src, old) {
		t.Fatalf("example spec does not contain %q", old)
	}
	return strings.Replace(src, old, new, 1)
}

func TestSchemaMatchesRepoRoot(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "specs", "strategy.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, schemaJSON) {
		t.Fatal("server/internal/spec/strategy.schema.json differs from specs/strategy.schema.json; " +
			"the repo-root file is the source of truth (Python validates against it too): " +
			"cp specs/strategy.schema.json server/internal/spec/strategy.schema.json")
	}
}

func TestLoadExample(t *testing.T) {
	registerPairs(t)
	s, err := Load(filepath.Join("testdata", "gld_gdx_pairs.yaml"), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "gld_gdx_pairs" || s.Version != 1 || s.Strategy != "pairs_zscore" {
		t.Errorf("header = %q v%d %q", s.Name, s.Version, s.Strategy)
	}
	if len(s.Universe) != 2 || s.Universe[0] != "GLD" || s.Universe[1] != "GDX" {
		t.Errorf("universe = %v", s.Universe)
	}
	if s.Params["hedge_ratio"] != 1.631 || s.Params["lookback"] != 20 || s.Params["max_hold_days"] != 30 {
		t.Errorf("params = %v", s.Params)
	}
	if s.Sizing.GrossLeverage != 0.9 || s.Sizing.MaxNotionalPerLegUSD != 2000 {
		t.Errorf("sizing = %+v", s.Sizing)
	}
	p := s.Provenance
	if !p.TrainWindow.From.Equal(time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!p.TrainWindow.To.Equal(time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC)) ||
		!p.TestWindow.From.Equal(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!p.TestWindow.To.Equal(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("windows = %+v %+v", p.TrainWindow, p.TestWindow)
	}
	if p.OOSSharpe != 1.32 || p.OOSMaxDrawdown != -0.087 {
		t.Errorf("oos = %v %v", p.OOSSharpe, p.OOSMaxDrawdown)
	}
	if p.CostModel.CommissionUSD != 0 || p.CostModel.SlippageBps != 5 {
		t.Errorf("cost model = %+v", p.CostModel)
	}
	if p.ResearchGitSHA != "abc1234" || p.TTLDays != 90 {
		t.Errorf("sha %q ttl %d", p.ResearchGitSHA, p.TTLDays)
	}
	if !p.GeneratedAt.Equal(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)) || p.GeneratedAt.Location() != time.UTC {
		t.Errorf("generated_at = %v", p.GeneratedAt)
	}
}

func TestLoadRejects(t *testing.T) {
	registerPairs(t)
	ex := example(t)
	cases := []struct {
		name string
		src  string
		// Exactly one of these is set: the substring the Reason must carry.
		invalid, refused string
	}{
		{
			name:    "missing provenance",
			src:     ex[:strings.Index(ex, "provenance:")],
			invalid: "missing property 'provenance'",
		},
		{
			name:    "missing one provenance field",
			src:     replace(t, ex, "  oos_max_drawdown: -0.087\n", ""),
			invalid: "/provenance: missing property 'oos_max_drawdown'",
		},
		{
			name:    "sharpe below the floor",
			src:     replace(t, ex, "oos_sharpe: 1.32", "oos_sharpe: 0.9"),
			refused: "oos_sharpe 0.90 is below the 1.0 floor",
		},
		{
			name:    "expired ttl",
			src:     replace(t, ex, "generated_at: 2026-09-06T00:00:00Z", "generated_at: 2026-06-01T00:00:00Z"),
			refused: "expired 2026-08-30T00:00:00Z",
		},
		{
			// The schema's enum is the list of strategies that exist at
			// all; a name outside it is not a spec. A strategy the schema
			// knows but this binary lacks is TestRefusesUnregistered.
			name:    "unknown strategy",
			src:     replace(t, ex, "strategy: pairs_zscore", "strategy: momentum"),
			invalid: "/strategy:",
		},
		{
			name:    "bad date format",
			src:     replace(t, ex, "from: 2015-01-01", "from: 01/01/2015"),
			invalid: "/provenance/train_window/from:",
		},
		{
			name:    "date-time where a date belongs",
			src:     replace(t, ex, "to: 2022-12-31", "to: 2022-12-31T00:00:00Z"),
			invalid: "/provenance/train_window/to:",
		},
		{
			name:    "date where a date-time belongs",
			src:     replace(t, ex, "generated_at: 2026-09-06T00:00:00Z", "generated_at: 2026-09-06"),
			invalid: "/provenance/generated_at:",
		},
		{
			name:    "unknown field",
			src:     ex + "notes: illustrative\n",
			invalid: "additional properties 'notes' not allowed",
		},
		{
			name:    "unknown nested field",
			src:     replace(t, ex, "  ttl_days: 90", "  ttl_days: 90\n  author: me"),
			invalid: "/provenance: additional properties 'author' not allowed",
		},
		{
			name:    "duplicate key",
			src:     ex + "version: 2\n",
			invalid: `key "version" repeated`,
		},
		{
			name:    "not yaml",
			src:     "name: [unterminated",
			invalid: "yaml",
		},
		{
			name:    "empty file",
			src:     "",
			invalid: "empty file",
		},
		{
			name:    "non-numeric param",
			src:     replace(t, ex, "lookback: 20", "lookback: twenty"),
			invalid: "/params/lookback:",
		},
		{
			name:    "window ends before it starts",
			src:     replace(t, ex, "to: 2022-12-31", "to: 2014-12-31"),
			invalid: "train_window: window ends 2014-12-31 before it starts 2015-01-01",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, "s.yaml", tc.src)
			s, err := Load(path, now)
			if err == nil {
				t.Fatalf("loaded %+v, want an error", s)
			}
			if s != nil {
				t.Errorf("spec returned alongside error %v", err)
			}
			var inv *Invalid
			var ref *Refused
			switch {
			case tc.invalid != "":
				if !errors.As(err, &inv) {
					t.Fatalf("err = %v (%T), want *Invalid", err, err)
				}
				if !strings.Contains(inv.Reason, tc.invalid) {
					t.Errorf("reason = %q, want it to mention %q", inv.Reason, tc.invalid)
				}
				if inv.Path != path {
					t.Errorf("path = %q, want %q", inv.Path, path)
				}
			case tc.refused != "":
				if !errors.As(err, &ref) {
					t.Fatalf("err = %v (%T), want *Refused", err, err)
				}
				if !strings.Contains(ref.Reason, tc.refused) {
					t.Errorf("reason = %q, want it to mention %q", ref.Reason, tc.refused)
				}
				if ref.Path != path {
					t.Errorf("path = %q, want %q", ref.Path, path)
				}
				// A refusal is policy, not a parse failure: the spec rides
				// along for callers that knowingly look past it.
				if ref.Spec == nil || ref.Spec.Name != "gld_gdx_pairs" {
					t.Errorf("refused spec = %+v, want the parsed spec", ref.Spec)
				}
			}
		})
	}
}

// A spec is refused the instant its TTL runs out, not a moment later.
func TestTTLBoundary(t *testing.T) {
	registerPairs(t)
	path := filepath.Join("testdata", "gld_gdx_pairs.yaml")
	exp := time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC)
	if _, err := Load(path, exp.Add(-time.Second)); err != nil {
		t.Errorf("a second before expiry: %v", err)
	}
	var ref *Refused
	if _, err := Load(path, exp); !errors.As(err, &ref) {
		t.Errorf("at expiry: err = %v, want *Refused", err)
	}
}

// The schema knows pairs_zscore; a runtime built without it must still refuse
// the spec rather than trade on a name it cannot resolve.
func TestRefusesUnregistered(t *testing.T) {
	s := &Spec{Strategy: "not_built_in", Provenance: Provenance{OOSSharpe: 2, GeneratedAt: now, TTLDays: 1}}
	reason := refusal(s, now)
	if !strings.Contains(reason, `strategy "not_built_in" is not registered`) {
		t.Errorf("reason = %q", reason)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), now)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want ErrNotExist", err)
	}
	var inv *Invalid
	if errors.As(err, &inv) {
		t.Error("an unreadable file is not an invalid spec; the two must stay distinguishable")
	}
}

func TestExpiresAndFreshFor(t *testing.T) {
	s := &Spec{Provenance: Provenance{
		GeneratedAt: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		TTLDays:     90,
	}}
	exp := time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC)
	if got := Expires(s); !got.Equal(exp) {
		t.Errorf("Expires = %v, want %v", got, exp)
	}
	cases := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{"just generated", s.Provenance.GeneratedAt, 90 * 24 * time.Hour},
		{"halfway", s.Provenance.GeneratedAt.Add(45 * 24 * time.Hour), 45 * 24 * time.Hour},
		{"at expiry", exp, 0},
		{"long expired", exp.Add(400 * 24 * time.Hour), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FreshFor(s, tc.now); got != tc.want {
				t.Errorf("FreshFor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadDir(t *testing.T) {
	registerPairs(t)
	ex := example(t)
	dir := t.TempDir()
	files := map[string]string{
		"b_good.yml":           ex,
		"a_stale.yaml":         replace(t, ex, "generated_at: 2026-09-06T00:00:00Z", "generated_at: 2025-01-01T00:00:00Z"),
		"c_broken.yaml":        "strategy: [",
		"strategy.schema.json": "{}",
		"README.md":            "not a spec",
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "old.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}

	got := LoadDir(dir, now)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	var ref *Refused
	var inv *Invalid
	if filepath.Base(got[0].Path) != "a_stale.yaml" || got[0].Spec != nil || !errors.As(got[0].Err, &ref) {
		t.Errorf("[0] = %+v, want a_stale.yaml refused", got[0])
	}
	if filepath.Base(got[1].Path) != "b_good.yml" || got[1].Spec == nil || got[1].Err != nil {
		t.Errorf("[1] = %+v, want b_good.yml loaded", got[1])
	}
	if filepath.Base(got[2].Path) != "c_broken.yaml" || got[2].Spec != nil || !errors.As(got[2].Err, &inv) {
		t.Errorf("[2] = %+v, want c_broken.yaml invalid", got[2])
	}

	missing := filepath.Join(dir, "nowhere")
	if got := LoadDir(missing, now); len(got) != 1 || got[0].Path != missing || !errors.Is(got[0].Err, os.ErrNotExist) {
		t.Errorf("missing dir: %+v", got)
	}
}
