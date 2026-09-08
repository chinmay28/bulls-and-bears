package trend

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// goldenDir is where the research side writes the bars, the spec and the
// expected signals for the sma_trend study (docs/PLAN.md §4.3).
const goldenDir = "../../../../golden/sma_trend"

// parityTolerance is the contract's allowance for float order-of-operations
// differences between pandas and this package.
const parityTolerance = 1e-9

// goldenSpec is the slice of the strategy spec the parity test needs.
type goldenSpec struct {
	Strategy string             `yaml:"strategy"`
	Universe []string           `yaml:"universe"`
	Params   map[string]float64 `yaml:"params"`
	Sizing   struct {
		GrossLeverage float64 `yaml:"gross_leverage"`
	} `yaml:"sizing"`
}

// TestParity checks every row of expected_signals.csv against Signals. It
// skips, rather than fails, until the research side has produced the golden
// directory; once it exists every row must match.
func TestParity(t *testing.T) {
	if _, err := os.Stat(goldenDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no golden directory at %s yet", goldenDir)
	} else if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(goldenDir, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var spec goldenSpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("spec.yaml: %v", err)
	}
	if spec.Strategy != "" && spec.Strategy != Name {
		t.Fatalf("spec.yaml is for strategy %q, this test checks %q", spec.Strategy, Name)
	}
	st, err := Parse(spec.Universe, strategy.Params(spec.Params), strategy.Sizing{GrossLeverage: spec.Sizing.GrossLeverage})
	if err != nil {
		t.Fatalf("spec.yaml: %v", err)
	}

	hist := map[string][]bars.Bar{}
	for _, sym := range spec.Universe {
		path := filepath.Join(goldenDir, "bars_"+sym+".parquet")
		series, err := bars.Read(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		hist[sym] = series
	}

	sig, err := st.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	byDate := make(map[string]Signal, len(sig))
	for _, s := range sig {
		byDate[s.Date.UTC().Format("2006-01-02")] = s
	}

	rows, err := readExpected(filepath.Join(goldenDir, "expected_signals.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if want := len(spec.Universe) * len(sig); len(rows) != want {
		t.Errorf("expected_signals.csv has %d rows, Go aligned %d bars x %d symbols = %d", len(rows), len(sig), len(spec.Universe), want)
	}

	var (
		mismatches int
		first      string
	)
	for _, row := range rows {
		s, ok := byDate[row.date]
		if !ok {
			mismatches++
			if first == "" {
				first = fmt.Sprintf("%s %s: date in golden but not in Go alignment", row.date, row.symbol)
			}
			continue
		}
		got, ok := s.Weights[row.symbol]
		if !ok {
			mismatches++
			if first == "" {
				first = fmt.Sprintf("%s %s: symbol not in Go weights %v", row.date, row.symbol, s.Weights)
			}
			continue
		}
		if diff := math.Abs(got - row.weight); !(diff <= parityTolerance) {
			mismatches++
			if first == "" {
				first = fmt.Sprintf("%s %s: Go %.17g, Python %.17g, off by %.3g (Go signal %+v)",
					row.date, row.symbol, got, row.weight, diff, s)
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d rows differ beyond %g; first: %s", mismatches, len(rows), parityTolerance, first)
	}
}

type expectedRow struct {
	date, symbol string
	weight       float64
}

// readExpected parses the golden CSV by header name, so a column reorder on
// the Python side is not a silent mismatch.
func readExpected(path string) ([]expectedRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: header: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, want := range []string{"date", "symbol", "target_weight"} {
		if _, ok := col[want]; !ok {
			return nil, fmt.Errorf("%s: no %q column in header %v", path, want, header)
		}
	}
	var rows []expectedRow
	for line := 2; ; line++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		date := rec[col["date"]]
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return nil, fmt.Errorf("%s line %d: date %q: %w", path, line, date, err)
		}
		w, err := strconv.ParseFloat(rec[col["target_weight"]], 64)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: target_weight: %w", path, line, err)
		}
		rows = append(rows, expectedRow{date: date, symbol: rec[col["symbol"]], weight: w})
	}
	return rows, nil
}
