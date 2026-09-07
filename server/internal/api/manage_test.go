package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/bars/refill"
)

// body is a JSON request body, so the tests read as what they send.
func body(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// oosSharpeLine matches the spec's out-of-sample Sharpe whatever the golden
// run happened to produce, so a test can push it under the floor.
var oosSharpeLine = regexp.MustCompile(`oos_sharpe: [-0-9.]+`)

// specYAML is the spec the fixture installed, read back so a test can edit
// and re-import it.
func specYAML(t *testing.T, s *Server) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.SpecsDir, "gld_gdx_pairs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestImportInstallsASpec(t *testing.T) {
	s, h, _ := tradingServer(t)
	src := strings.Replace(specYAML(t, s), "name: gld_gdx_pairs", "name: second_pair", 1)

	w := do(t, h, "POST", "/api/strategies", body(t, map[string]any{"yaml": src}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := decode[strategyView](t, w)
	if got.Name != "second_pair" || got.Status != "armed" {
		t.Errorf("imported = %+v, want second_pair armed", got)
	}
	if _, err := os.Stat(filepath.Join(s.SpecsDir, "second_pair.yaml")); err != nil {
		t.Errorf("the file is not on disk: %v", err)
	}
	// And the run would now see it.
	list := decode[[]strategyView](t, do(t, h, "GET", "/api/strategies", ""))
	var found bool
	for _, v := range list {
		found = found || v.Name == "second_pair"
	}
	if !found {
		t.Error("the imported spec is not in the list")
	}
}

func TestImportRefusesWhatARunWouldRefuse(t *testing.T) {
	s, h, _ := tradingServer(t)
	good := specYAML(t, s)
	tests := []struct {
		name     string
		yaml     string
		want     int
		wantBody string
	}{
		{name: "empty", yaml: "  ", want: http.StatusBadRequest, wantBody: "no spec"},
		{name: "not a spec", yaml: "name: nope\n", want: http.StatusUnprocessableEntity, wantBody: "not a strategy spec"},
		{
			name:     "sharpe under the floor",
			yaml:     oosSharpeLine.ReplaceAllString(strings.Replace(good, "name: gld_gdx_pairs", "name: weak_pair", 1), "oos_sharpe: 0.2"),
			want:     http.StatusUnprocessableEntity,
			wantBody: "would refuse",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(t, h, "POST", "/api/strategies", body(t, map[string]any{"yaml": tt.yaml}))
			if w.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			if !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("body = %s, want it to mention %q", w.Body, tt.wantBody)
			}
			entries, _ := os.ReadDir(s.SpecsDir)
			if len(entries) != 2 {
				t.Errorf("specs dir holds %d files, want the fixture's 2 untouched", len(entries))
			}
		})
	}
}

func TestImportWillNotReplaceUnlessAsked(t *testing.T) {
	s, h, _ := tradingServer(t)
	src := specYAML(t, s)

	w := do(t, h, "POST", "/api/strategies", body(t, map[string]any{"yaml": src}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "already installed") {
		t.Errorf("body = %s, want it to say the spec is already there", w.Body)
	}

	w = do(t, h, "POST", "/api/strategies", body(t, map[string]any{"yaml": src, "replace": true}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201 with replace: %s", w.Code, w.Body)
	}
}

func TestDeleteRemovesASpec(t *testing.T) {
	s, h, _ := tradingServer(t)
	w := do(t, h, "DELETE", "/api/strategies/gld_gdx_pairs", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(s.SpecsDir, "gld_gdx_pairs.yaml")); !os.IsNotExist(err) {
		t.Errorf("the spec is still on disk: %v", err)
	}
	// The bars stay: they cost a fetch and belong to no one spec.
	if _, err := os.Stat(filepath.Join(s.BarsDir, "GLD.parquet")); err != nil {
		t.Errorf("deleting a spec took its bars with it: %v", err)
	}
	if w := do(t, h, "DELETE", "/api/strategies/gld_gdx_pairs", ""); w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 the second time", w.Code)
	}
	if w := do(t, h, "DELETE", "/api/strategies/..%2Fescape", ""); w.Code == http.StatusNoContent {
		t.Error("a name that is not a spec name must not delete anything")
	}
}

// fetcher is a stub Fetcher: it answers with n bars ending yesterday.
type fetcher struct {
	bars  int
	err   error
	asked []string
}

func (f *fetcher) Daily(_ context.Context, symbol string, _, _ time.Time) ([]bars.Bar, error) {
	f.asked = append(f.asked, symbol)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]bars.Bar, f.bars)
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -f.bars)
	for i := range out {
		out[i] = bars.Bar{
			Date: start.AddDate(0, 0, i), Open: 100, High: 101, Low: 99, Close: 100, AdjClose: 100,
			Volume: 1000, Source: "yahoo", FetchedAt: time.Now().UTC(),
		}
	}
	return out, nil
}

func TestRefreshBarsFetchesEverySymbolTheSpecsName(t *testing.T) {
	s, h, _ := tradingServer(t)
	before, err := bars.Read(filepath.Join(s.BarsDir, "GLD.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	s.Fetcher = &fetcher{bars: len(before) + 5}

	w := do(t, h, "POST", "/api/bars/refresh", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := decode[[]refill.Result](t, w)
	if len(got) != 2 {
		t.Fatalf("results = %+v, want GLD and GDX", got)
	}
	for _, r := range got {
		if r.Error != "" {
			t.Errorf("%s: %s", r.Symbol, r.Error)
		}
		if r.Added != 5 {
			t.Errorf("%s added %d, want 5", r.Symbol, r.Added)
		}
	}
	if asked := s.Fetcher.(*fetcher).asked; len(asked) != 2 || asked[0] != "GDX" || asked[1] != "GLD" {
		t.Errorf("asked for %v, want both symbols", asked)
	}
}

func TestRefreshBarsTakesTheSymbolsAsked(t *testing.T) {
	s, h, _ := tradingServer(t)
	s.Fetcher = &fetcher{bars: 400}

	w := do(t, h, "POST", "/api/bars/refresh", body(t, map[string]any{"symbols": []string{"SPY"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := decode[[]refill.Result](t, w)
	if len(got) != 1 || got[0].Symbol != "SPY" || got[0].Error != "" {
		t.Fatalf("results = %+v, want SPY alone", got)
	}
	if _, err := bars.Read(filepath.Join(s.BarsDir, "SPY.parquet")); err != nil {
		t.Errorf("SPY was not written: %v", err)
	}
	// A symbol with bars but no spec still shows on the bars screen.
	list := decode[[]barsView](t, do(t, h, "GET", "/api/bars", ""))
	var found bool
	for _, v := range list {
		found = found || v.Symbol == "SPY"
	}
	if !found {
		t.Errorf("bars = %+v, want SPY listed even though no spec names it", list)
	}
}

func TestRefreshBarsReportsAFailureWithoutLosingTheFile(t *testing.T) {
	s, h, _ := tradingServer(t)
	before, err := bars.Read(filepath.Join(s.BarsDir, "GLD.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	s.Fetcher = &fetcher{err: errors.New("yahoo: GLD: http 503")}

	w := do(t, h, "POST", "/api/bars/refresh", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := decode[[]refill.Result](t, w)
	for _, r := range got {
		if r.Error == "" {
			t.Errorf("%s reported no error", r.Symbol)
		}
	}
	after, err := bars.Read(filepath.Join(s.BarsDir, "GLD.parquet"))
	if err != nil || len(after) != len(before) {
		t.Errorf("bars = %d (err %v), want the original %d untouched", len(after), err, len(before))
	}
}

func TestRefreshBarsWithoutAFetcher(t *testing.T) {
	_, h, _ := tradingServer(t)
	if w := do(t, h, "POST", "/api/bars/refresh", ""); w.Code != http.StatusConflict {
		t.Errorf("status %d, want 409 when the server has no fetcher", w.Code)
	}
}
