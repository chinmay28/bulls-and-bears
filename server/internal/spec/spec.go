// Package spec loads the strategy specs research emits and decides whether
// the runtime may run them.
//
// A spec (docs/PLAN.md §4.2) is YAML validated against
// specs/strategy.schema.json, the same file Python validates against. Load
// answers one of three ways: a *Spec the runtime may trade; an *Invalid,
// meaning the file is not a spec at all (unparseable, fails the schema); or a
// *Refused, meaning it is a perfectly good spec this runtime will not run —
// its out-of-sample Sharpe is under the floor, it is older than its TTL, or it
// names a strategy this binary does not have. Both errors carry a Reason
// short enough for a phone.
//
// The package does not know what a strategy needs: whether the universe and
// params make sense for pairs_zscore is the strategy package's business, and
// is decided when strategy.New builds it.
package spec

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// MinOOSSharpe is the floor a spec's out-of-sample Sharpe must reach. Below
// it the backtest did not earn the right to trade.
const MinOOSSharpe = 1.0

// Spec is a validated strategy spec, field for field the schema's shape.
type Spec struct {
	Name       string
	Version    int
	Strategy   string
	Universe   []string
	Params     strategy.Params
	Sizing     strategy.Sizing
	Provenance Provenance
}

// Provenance is the backtest that earned the spec the right to trade.
type Provenance struct {
	TrainWindow    Window
	TestWindow     Window
	OOSSharpe      float64
	OOSMaxDrawdown float64
	CostModel      CostModel
	ResearchGitSHA string
	GeneratedAt    time.Time
	TTLDays        int
}

// Window is a date range, inclusive, midnight UTC.
type Window struct {
	From, To time.Time
}

// CostModel is what the backtest charged per order and per unit of notional.
type CostModel struct {
	CommissionUSD float64
	SlippageBps   float64
}

// Invalid says the file is not a strategy spec: it does not parse, or it
// fails the schema. Fixing it means changing the file.
type Invalid struct {
	Path   string
	Reason string
}

func (e *Invalid) Error() string { return e.Path + ": not a strategy spec: " + e.Reason }

// Refused says the spec is well formed but this runtime will not run it.
// Fixing it means re-running research (or registering the strategy).
type Refused struct {
	Path   string
	Reason string
}

func (e *Refused) Error() string { return e.Path + ": refused: " + e.Reason }

// Load reads one spec. now is the clock the TTL is judged against, passed in
// rather than read so that a test and a replay can ask about any moment.
func Load(path string, now time.Time) (*Spec, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Check(src, path, now)
}

// Check answers for raw spec bytes exactly as Load answers for a file, so a
// spec arriving from somewhere other than the disk — an upload from the
// phone — is judged by the same rules and the same errors. path is only what
// the errors name; nothing is read or written.
func Check(src []byte, path string, now time.Time) (*Spec, error) {
	s, err := parse(src)
	if err != nil {
		return nil, &Invalid{Path: path, Reason: err.Error()}
	}
	if reason := refusal(s, now); reason != "" {
		return nil, &Refused{Path: path, Reason: reason}
	}
	return s, nil
}

// Loaded is LoadDir's answer for one file: the spec, or why not.
type Loaded struct {
	Path string
	Spec *Spec // nil when Err is set
	Err  error
}

// LoadDir loads every *.yaml and *.yml in dir, sorted by name, and keeps
// going past bad ones: the UI lists the refused specs alongside the good, with
// their reasons. Other files — the schema, a README — are not specs and are
// skipped. A directory that cannot be read comes back as a single Loaded whose
// Path is the directory and whose Err says why.
func LoadDir(dir string, now time.Time) []Loaded {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []Loaded{{Path: dir, Err: err}}
	}
	var out []Loaded
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml":
		default:
			continue
		}
		path := filepath.Join(dir, e.Name())
		s, err := Load(path, now)
		out = append(out, Loaded{Path: path, Spec: s, Err: err})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Expires is the instant the spec goes stale: generated_at plus ttl_days.
func Expires(s *Spec) time.Time {
	return s.Provenance.GeneratedAt.Add(time.Duration(s.Provenance.TTLDays) * 24 * time.Hour)
}

// FreshFor is how long the spec has left before Expires, never negative: an
// expired spec has nothing left, and how long ago it ran out is Expires's job.
func FreshFor(s *Spec, now time.Time) time.Duration {
	if d := Expires(s).Sub(now); d > 0 {
		return d
	}
	return 0
}

// refusal is the runtime's own rules (docs/PLAN.md §4.2, §6 "stale spec")
// applied to a spec the schema already accepts. Empty means run it.
func refusal(s *Spec, now time.Time) string {
	names := strategy.Names()
	if i := sort.SearchStrings(names, s.Strategy); i == len(names) || names[i] != s.Strategy {
		return fmt.Sprintf("strategy %q is not registered in this runtime (have %v)", s.Strategy, names)
	}
	if s.Provenance.OOSSharpe < MinOOSSharpe {
		return fmt.Sprintf("oos_sharpe %.2f is below the %.1f floor", s.Provenance.OOSSharpe, MinOOSSharpe)
	}
	if exp := Expires(s); !exp.After(now) {
		return fmt.Sprintf("expired %s (generated %s, ttl %d days)",
			exp.Format(time.RFC3339), s.Provenance.GeneratedAt.Format("2006-01-02"), s.Provenance.TTLDays)
	}
	return ""
}

//go:embed strategy.schema.json
var schemaJSON []byte

// schema compiles the embedded copy once. The repo-root file is the source of
// truth; TestSchemaMatchesRepoRoot keeps this copy honest.
var schema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	// Formats are annotations by default in draft 2020-12. Here they are the
	// whole point: a date the runtime cannot parse is a spec it cannot judge.
	c.AssertFormat()
	if err := c.AddResource("strategy.schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("strategy.schema.json")
})

// parse turns YAML into a Spec the schema accepts. The YAML is first made
// into the JSON the schema and Python see: the same document, so the two
// validators cannot disagree about what was in the file.
func parse(src []byte) (*Spec, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, errors.New("empty file")
	}
	doc, err := toJSON(root.Content[0])
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}

	sch, err := schema()
	if err != nil {
		return nil, fmt.Errorf("embedded schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if err := sch.Validate(inst); err != nil {
		return nil, errors.New(schemaReason(err))
	}

	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f.spec()
}

// toJSON converts a YAML node into the values encoding/json would decode from
// the equivalent JSON. Timestamps are the one place YAML is too clever: it
// would parse `2015-01-01` into a time.Time and lose whether the author
// wrote a date or a date-time, so those scalars keep their text and the
// schema's format assertions judge them as written.
func toJSON(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.AliasNode:
		return toJSON(n.Alias)
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, errors.New("expected one document")
		}
		return toJSON(n.Content[0])
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := toJSON(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: mapping key must be a string", k.Line)
			}
			if _, dup := out[k.Value]; dup {
				return nil, fmt.Errorf("line %d: key %q repeated", k.Line, k.Value)
			}
			v, err := toJSON(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[k.Value] = v
		}
		return out, nil
	case yaml.ScalarNode:
		if n.ShortTag() == "!!timestamp" {
			return n.Value, nil
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
}

// schemaReason flattens a validation error into one line per failure, each
// saying where in the file and what: "/provenance: missing property
// 'oos_sharpe'". The library's own message leads with the schema URL, which
// helps nobody reading a phone.
func schemaReason(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	var parts []string
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil {
			continue
		}
		loc := u.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		parts = append(parts, loc+": "+u.Error.String())
	}
	if len(parts) == 0 {
		return ve.Error()
	}
	return strings.Join(parts, "; ")
}

// file is the spec as JSON, dates still strings. The schema has already
// vouched for every field; spec() only converts.
type file struct {
	Name     string             `json:"name"`
	Version  int                `json:"version"`
	Strategy string             `json:"strategy"`
	Universe []string           `json:"universe"`
	Params   map[string]float64 `json:"params"`
	Sizing   struct {
		GrossLeverage        float64 `json:"gross_leverage"`
		MaxNotionalPerLegUSD float64 `json:"max_notional_per_leg_usd"`
	} `json:"sizing"`
	Provenance struct {
		TrainWindow    window  `json:"train_window"`
		TestWindow     window  `json:"test_window"`
		OOSSharpe      float64 `json:"oos_sharpe"`
		OOSMaxDrawdown float64 `json:"oos_max_drawdown"`
		CostModel      struct {
			CommissionUSD float64 `json:"commission_usd"`
			SlippageBps   float64 `json:"slippage_bps"`
		} `json:"cost_model"`
		ResearchGitSHA string `json:"research_git_sha"`
		GeneratedAt    string `json:"generated_at"`
		TTLDays        int    `json:"ttl_days"`
	} `json:"provenance"`
}

type window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (w window) window() (Window, error) {
	from, err := time.Parse("2006-01-02", w.From)
	if err != nil {
		return Window{}, err
	}
	to, err := time.Parse("2006-01-02", w.To)
	if err != nil {
		return Window{}, err
	}
	if to.Before(from) {
		return Window{}, fmt.Errorf("window ends %s before it starts %s", w.To, w.From)
	}
	return Window{From: from, To: to}, nil
}

func (f *file) spec() (*Spec, error) {
	p := f.Provenance
	train, err := p.TrainWindow.window()
	if err != nil {
		return nil, fmt.Errorf("train_window: %w", err)
	}
	test, err := p.TestWindow.window()
	if err != nil {
		return nil, fmt.Errorf("test_window: %w", err)
	}
	gen, err := time.Parse(time.RFC3339, p.GeneratedAt)
	if err != nil {
		return nil, fmt.Errorf("generated_at: %w", err)
	}
	params := strategy.Params{}
	for k, v := range f.Params {
		params[k] = v
	}
	return &Spec{
		Name:     f.Name,
		Version:  f.Version,
		Strategy: f.Strategy,
		Universe: append([]string(nil), f.Universe...),
		Params:   params,
		Sizing: strategy.Sizing{
			GrossLeverage:        f.Sizing.GrossLeverage,
			MaxNotionalPerLegUSD: f.Sizing.MaxNotionalPerLegUSD,
		},
		Provenance: Provenance{
			TrainWindow:    train,
			TestWindow:     test,
			OOSSharpe:      p.OOSSharpe,
			OOSMaxDrawdown: p.OOSMaxDrawdown,
			CostModel: CostModel{
				CommissionUSD: p.CostModel.CommissionUSD,
				SlippageBps:   p.CostModel.SlippageBps,
			},
			ResearchGitSHA: p.ResearchGitSHA,
			GeneratedAt:    gen.UTC(),
			TTLDays:        p.TTLDays,
		},
	}, nil
}
