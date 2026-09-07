package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
)

func TestQuotesComeFromTheNewestBar(t *testing.T) {
	dir := t.TempDir()
	fetched := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	series := []bars.Bar{
		{Date: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), Open: 100, High: 101, Low: 99, Close: 100, AdjClose: 100, Volume: 1, Source: "yahoo", FetchedAt: fetched},
		{Date: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), Open: 100, High: 202, Low: 99, Close: 200, AdjClose: 200, Volume: 1, Source: "yahoo", FetchedAt: fetched},
	}
	if err := bars.Write(filepath.Join(dir, "GLD.parquet"), series); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 19, 50, 0, 0, time.UTC)
	src := &Source{Dir: dir, SpreadBps: 10, Now: func() time.Time { return now }}
	qs, err := src.Quotes(context.Background(), []string{"GLD"})
	if err != nil {
		t.Fatal(err)
	}
	q := qs[0]
	if q.Last != 200 || q.Bid != 199.9 || q.Ask != 200.1 || !q.At.Equal(now) {
		t.Errorf("quote = %+v", q)
	}
	if _, err := src.Quotes(context.Background(), []string{"GDX"}); err == nil {
		t.Error("a symbol with no file must be an error, not a guess")
	}
}
