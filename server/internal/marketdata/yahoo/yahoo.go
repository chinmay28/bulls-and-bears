// Package yahoo fetches daily bars from Yahoo Finance's chart endpoint and
// returns them in the docs/PLAN.md §4.1 shape, so the phone can refill the
// bars directory without a terminal and without the Python research side.
//
// It owns one thing: turning one symbol and a date range into a checked
// series of bars stamped source "yahoo". It does not write files, does not
// know what a strategy needs, and never decides whether the data is good
// enough to trade — bars.Check says whether the series is well formed, and
// the runner decides whether it is fresh enough.
//
// Two deliberate refusals. A response missing any of open/high/low/close for
// a day drops that day rather than guessing it, and the current trading day
// is dropped entirely: while a session is open Yahoo returns a partial bar
// for it, and the runtime builds today from quotes anyway (runner step 3).
// Yahoo is unofficial and survivorship-biased (docs/PLAN.md §2); its answers
// are checked here, not trusted.
package yahoo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
)

// DefaultBaseURL is Yahoo's chart endpoint; the symbol is appended to it.
const DefaultBaseURL = "https://query1.finance.yahoo.com/v8/finance/chart/"

// userAgent is sent because Yahoo answers a bare Go client with 429 often
// enough to matter.
const userAgent = "bulls-and-bears/1 (+https://github.com/chinmay28/bulls-and-bears)"

// maxBody caps what a single response may be, so a wrong URL answering with
// something enormous cannot exhaust the machine.
const maxBody = 32 << 20

// Client fetches bars. The zero value works: it uses http.DefaultClient,
// the real endpoint and the wall clock.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	// Now is the clock the current trading day is judged against; nil means
	// time.Now.
	Now func() time.Time
	// Attempts is how many times a request is tried before giving up; 0
	// means 3. Yahoo is unreliable enough that one refusal means little.
	Attempts int
	// Sleep is how the retry waits, so a test does not. nil means time.Sleep.
	Sleep func(time.Duration)
}

// Daily returns symbol's daily bars with dates in [from, to], oldest first.
// An empty result is not an error: whether nothing is acceptable is the
// caller's call, the same rule bars.Read follows.
func (c *Client) Daily(ctx context.Context, symbol string, from, to time.Time) ([]bars.Bar, error) {
	if symbol == "" {
		return nil, errors.New("yahoo: no symbol")
	}
	// Bars are dated at midnight UTC, so the range must be too: a from with
	// a time on it would drop its own first day.
	from, to = day(from), day(to)
	if to.Before(from) {
		return nil, fmt.Errorf("yahoo: %s: range ends %s before it starts %s", symbol,
			to.Format("2006-01-02"), from.Format("2006-01-02"))
	}
	body, err := c.get(ctx, c.chartURL(symbol, from, to))
	if err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	series, err := parse(body, c.now())
	if err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	series = bars.Window(series, from, to)
	if err := bars.Check(series); err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	return series, nil
}

func (c *Client) chartURL(symbol string, from, to time.Time) string {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	q := url.Values{
		"interval": {"1d"},
		"events":   {"div,split"},
		// A day either side, so a range that starts on a holiday still
		// includes the first session inside it; Window trims the excess.
		"period1": {fmt.Sprint(from.AddDate(0, 0, -1).Unix())},
		"period2": {fmt.Sprint(to.AddDate(0, 0, 1).Unix())},
	}
	return base + url.PathEscape(symbol) + "?" + q.Encode()
}

// get performs the request, retrying what is worth retrying: a transport
// error, a 429, or a 5xx. A 404 is the symbol's answer and is returned as is.
func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	var last error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			sleep(time.Duration(1<<(attempt-1)) * time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		switch {
		case readErr != nil:
			last = readErr
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("http %s", resp.Status)
		default:
			// A 4xx that is not rate limiting is the answer, not a hiccup:
			// the symbol is wrong, or Yahoo wants something we are not
			// sending. Retrying it just wastes the operator's time.
			return nil, fmt.Errorf("http %s: %s", resp.Status, describe(body))
		}
	}
	return nil, last
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// chart is the slice of Yahoo's response this package reads. Everything else
// in it is ignored on purpose: the fewer fields, the fewer ways a change at
// Yahoo breaks the runtime.
type chart struct {
	Chart struct {
		Result []struct {
			Meta struct {
				GMTOffset int `json:"gmtoffset"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []*float64 `json:"open"`
					High   []*float64 `json:"high"`
					Low    []*float64 `json:"low"`
					Close  []*float64 `json:"close"`
					Volume []*int64   `json:"volume"`
				} `json:"quote"`
				AdjClose []struct {
					AdjClose []*float64 `json:"adjclose"`
				} `json:"adjclose"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// parse turns a chart response into bars, dropping any day Yahoo could not
// price and the current trading day.
func parse(body []byte, now time.Time) ([]bars.Bar, error) {
	var c chart
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("unreadable response: %w", err)
	}
	if e := c.Chart.Error; e != nil {
		return nil, fmt.Errorf("%s: %s", e.Code, e.Description)
	}
	if len(c.Chart.Result) == 0 {
		return nil, errors.New("no result in the response")
	}
	r := c.Chart.Result[0]
	if len(r.Indicators.Quote) == 0 {
		return nil, errors.New("no quote indicators in the response")
	}
	q := r.Indicators.Quote[0]
	var adj []*float64
	if len(r.Indicators.AdjClose) > 0 {
		adj = r.Indicators.AdjClose[0].AdjClose
	}

	// The exchange's local date, not the fetcher's: a bar belongs to the day
	// the session ran, whatever timezone this binary is in.
	offset := time.Duration(r.Meta.GMTOffset) * time.Second
	today := now.Add(offset).UTC().Truncate(24 * time.Hour)

	fetched := now.UTC()
	out := make([]bars.Bar, 0, len(r.Timestamp))
	for i, ts := range r.Timestamp {
		o, okO := at(q.Open, i)
		h, okH := at(q.High, i)
		l, okL := at(q.Low, i)
		cl, okC := at(q.Close, i)
		if !okO || !okH || !okL || !okC {
			continue // A day Yahoo has no prices for is not a bar.
		}
		date := time.Unix(ts, 0).UTC().Add(offset).Truncate(24 * time.Hour)
		if !date.Before(today) {
			continue // Partial while the session is open; quotes cover today.
		}
		b := bars.Bar{
			Date: date, Open: o, High: h, Low: l, Close: cl, AdjClose: cl,
			Source: "yahoo", FetchedAt: fetched,
		}
		if a, ok := at(adj, i); ok {
			b.AdjClose = a
		}
		if v, ok := at(q.Volume, i); ok {
			b.Volume = v
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return dedupe(out), nil
}

// at reads index i of a column Yahoo may have left short or null.
func at[T any](col []*T, i int) (T, bool) {
	var zero T
	if i >= len(col) || col[i] == nil {
		return zero, false
	}
	return *col[i], true
}

// dedupe keeps the last bar for any date that appears twice, which Yahoo does
// around splits. bars.Check refuses a repeated date, so this cannot be left
// to the caller.
func dedupe(series []bars.Bar) []bars.Bar {
	out := series[:0]
	for i, b := range series {
		if i+1 < len(series) && series[i+1].Date.Equal(b.Date) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// describe is a body cut down to something that fits in an error on a phone.
func describe(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "empty response"
	}
	return s
}

// day is t's date at midnight UTC, the instant a Bar's Date carries.
func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
