package yahoo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The exchange in these tests sits four hours behind UTC, so a bar stamped
// 13:30Z belongs to that day locally and the offset arithmetic is exercised
// rather than cancelled out.
const offsetSeconds = -4 * 60 * 60

// now is a Friday afternoon, mid-session on the exchange's clock: anything
// dated the 4th is the partial bar the fetcher must drop.
var now = time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)

func ts(year int, month time.Month, dayOfMonth int) int64 {
	return time.Date(year, month, dayOfMonth, 13, 30, 0, 0, time.UTC).Unix()
}

func date(year int, month time.Month, dayOfMonth int) time.Time {
	return time.Date(year, month, dayOfMonth, 0, 0, 0, 0, time.UTC)
}

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }

// body builds a chart response the way Yahoo shapes one.
type body struct {
	timestamps                 []int64
	open, high, low, cl, adj   []*float64
	volume                     []*int64
	omitAdjClose, omitQuote    bool
	errCode, errDesc           string
	omitResult, gmtOffsetUnset bool
}

func (b body) json(t *testing.T) string {
	t.Helper()
	type quote struct {
		Open   []*float64 `json:"open"`
		High   []*float64 `json:"high"`
		Low    []*float64 `json:"low"`
		Close  []*float64 `json:"close"`
		Volume []*int64   `json:"volume"`
	}
	doc := map[string]any{"chart": map[string]any{"result": nil, "error": nil}}
	chartOf := doc["chart"].(map[string]any)
	if b.errCode != "" {
		chartOf["error"] = map[string]string{"code": b.errCode, "description": b.errDesc}
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if b.omitResult {
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	offset := offsetSeconds
	if b.gmtOffsetUnset {
		offset = 0
	}
	indicators := map[string]any{}
	if !b.omitQuote {
		indicators["quote"] = []quote{{Open: b.open, High: b.high, Low: b.low, Close: b.cl, Volume: b.volume}}
	} else {
		indicators["quote"] = []quote{}
	}
	if !b.omitAdjClose {
		indicators["adjclose"] = []map[string][]*float64{{"adjclose": b.adj}}
	}
	chartOf["result"] = []map[string]any{{
		"meta":       map[string]any{"gmtoffset": offset},
		"timestamp":  b.timestamps,
		"indicators": indicators,
	}}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// threeDays is a clean response: the 1st, 2nd and 3rd, plus the partial bar
// for the 4th that a mid-session fetch always carries.
func threeDays() body {
	return body{
		timestamps: []int64{ts(2026, 9, 1), ts(2026, 9, 2), ts(2026, 9, 3), ts(2026, 9, 4)},
		open:       []*float64{f(100), f(101), f(102), f(103)},
		high:       []*float64{f(105), f(106), f(107), f(108)},
		low:        []*float64{f(99), f(100), f(101), f(102)},
		cl:         []*float64{f(104), f(105), f(106), f(107)},
		adj:        []*float64{f(103.5), f(104.5), f(106), f(107)},
		volume:     []*int64{i(1000), i(1100), i(1200), i(1300)},
	}
}

// serve stands up a server returning one canned body and counts the requests.
func serve(t *testing.T, status int, payload string) (*Client, *int, *string) {
	t.Helper()
	calls := 0
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		lastQuery = r.URL.RawQuery
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return &Client{
		HTTP: srv.Client(), BaseURL: srv.URL + "/",
		Now:   func() time.Time { return now },
		Sleep: func(time.Duration) {},
	}, &calls, &lastQuery
}

func TestDailyReadsAChartResponse(t *testing.T) {
	c, _, query := serve(t, http.StatusOK, threeDays().json(t))
	got, err := c.Daily(context.Background(), "GLD", date(2026, 8, 1), date(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d bars, want 3: the current day's partial bar must be dropped", len(got))
	}
	first := got[0]
	if !first.Date.Equal(date(2026, 9, 1)) {
		t.Errorf("date = %s, want 2026-09-01 at midnight UTC", first.Date)
	}
	if first.Open != 100 || first.High != 105 || first.Low != 99 || first.Close != 104 || first.AdjClose != 103.5 {
		t.Errorf("prices = %+v, want the response's own numbers", first)
	}
	if first.Volume != 1000 {
		t.Errorf("volume = %d, want 1000", first.Volume)
	}
	if first.Source != "yahoo" {
		t.Errorf("source = %q, want yahoo: provenance is not optional", first.Source)
	}
	if !first.FetchedAt.Equal(now) {
		t.Errorf("fetchedAt = %s, want the fetch clock %s", first.FetchedAt, now)
	}
	if !strings.Contains(*query, "interval=1d") {
		t.Errorf("query = %q, want a daily interval", *query)
	}
}

func TestDailyDropsDaysYahooCouldNotPrice(t *testing.T) {
	b := threeDays()
	b.cl[1] = nil // A holiday Yahoo lists but has no close for.
	c, _, _ := serve(t, http.StatusOK, b.json(t))
	got, err := c.Daily(context.Background(), "GLD", date(2026, 8, 1), date(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d bars, want 2: a day with no close is not a bar", len(got))
	}
	for _, bar := range got {
		if bar.Date.Equal(date(2026, 9, 2)) {
			t.Errorf("kept %s, the day with no close", bar.Date)
		}
	}
}

func TestDailyFallsBackToCloseWithoutAnAdjCloseColumn(t *testing.T) {
	b := threeDays()
	b.omitAdjClose = true
	c, _, _ := serve(t, http.StatusOK, b.json(t))
	got, err := c.Daily(context.Background(), "GLD", date(2026, 8, 1), date(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	for _, bar := range got {
		if bar.AdjClose != bar.Close {
			t.Errorf("adjclose %v != close %v with no adjclose column", bar.AdjClose, bar.Close)
		}
	}
}

func TestDailyHonoursTheRange(t *testing.T) {
	c, _, _ := serve(t, http.StatusOK, threeDays().json(t))
	got, err := c.Daily(context.Background(), "GLD", date(2026, 9, 2), date(2026, 9, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Date.Equal(date(2026, 9, 2)) {
		t.Fatalf("got %+v, want only the 2nd: both ends are inclusive", got)
	}
}

func TestDailyDedupesARepeatedDate(t *testing.T) {
	b := threeDays()
	b.timestamps[1] = b.timestamps[0] // Yahoo does this around splits.
	c, _, _ := serve(t, http.StatusOK, b.json(t))
	got, err := c.Daily(context.Background(), "GLD", date(2026, 8, 1), date(2026, 9, 4))
	if err != nil {
		t.Fatalf("a repeated date must be deduped, not refused: %v", err)
	}
	seen := map[time.Time]int{}
	for _, bar := range got {
		seen[bar.Date]++
	}
	for d, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times", d.Format("2006-01-02"), n)
		}
	}
}

func TestDailyRefuses(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		payload func(t *testing.T) string
		want    string
	}{
		{
			// Yahoo answers some bad symbols with a 200 and an error object,
			// which is the branch worth pinning: a 404 never reaches parse.
			name:    "yahoo's own error object",
			status:  http.StatusOK,
			payload: body{errCode: "Not Found", errDesc: "No data found, symbol may be delisted"}.json,
			want:    "symbol may be delisted",
		},
		{
			name:    "no result",
			status:  http.StatusOK,
			payload: body{omitResult: true}.json,
			want:    "no result",
		},
		{
			name:    "no quote indicators",
			status:  http.StatusOK,
			payload: body{timestamps: []int64{ts(2026, 9, 1)}, omitQuote: true}.json,
			want:    "no quote indicators",
		},
		{
			name:    "not json at all",
			status:  http.StatusOK,
			payload: func(*testing.T) string { return "<html>rate limited</html>" },
			want:    "unreadable response",
		},
		{
			name:   "prices that fail the bar invariants",
			status: http.StatusOK,
			payload: func(t *testing.T) string {
				b := threeDays()
				b.high[0] = f(1) // below open and close.
				return b.json(t)
			},
			want: "high",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, _ := serve(t, tt.status, tt.payload(t))
			_, err := c.Daily(context.Background(), "GLD", date(2026, 8, 1), date(2026, 9, 4))
			if err == nil {
				t.Fatal("want an error, got none: a bad answer must never become bars")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "GLD") {
				t.Errorf("error = %q, want it to name the symbol", err)
			}
		})
	}
}

func TestDailyRetriesWhatIsWorthRetrying(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantCalls int
	}{
		{name: "server error", status: http.StatusInternalServerError, wantCalls: 3},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCalls: 3},
		{name: "not found is the answer", status: http.StatusNotFound, wantCalls: 1},
		{name: "unauthorized is the answer", status: http.StatusUnauthorized, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, calls, _ := serve(t, tt.status, "nope")
			if _, err := c.Daily(context.Background(), "GLD", date(2026, 8, 1), date(2026, 9, 4)); err == nil {
				t.Fatal("want an error")
			}
			if *calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", *calls, tt.wantCalls)
			}
		})
	}
}

func TestDailyRecoversWhenARetrySucceeds(t *testing.T) {
	calls := 0
	payload := threeDays().json(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
	got, err := c.Daily(context.Background(), "GLD", date(2026, 8, 1), date(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || calls != 2 {
		t.Errorf("got %d bars after %d calls, want 3 after 2", len(got), calls)
	}
}

func TestDailyRefusesABadRequestBeforeAsking(t *testing.T) {
	c, calls, _ := serve(t, http.StatusOK, threeDays().json(t))
	if _, err := c.Daily(context.Background(), "", date(2026, 8, 1), date(2026, 9, 4)); err == nil {
		t.Error("an empty symbol must be refused")
	}
	if _, err := c.Daily(context.Background(), "GLD", date(2026, 9, 4), date(2026, 8, 1)); err == nil {
		t.Error("a backwards range must be refused")
	}
	if *calls != 0 {
		t.Errorf("calls = %d, want 0: neither is worth a request", *calls)
	}
}
