package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/rotation"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

func getRotation(t *testing.T, s *Server) rotationView {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/rotation", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var v rotationView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A data directory the rotation has never run in is not an error: the app
// says so and still shows the schedule and the rules.
func TestRotationNotConfigured(t *testing.T) {
	s := &Server{DataDir: t.TempDir()}
	v := getRotation(t, s)
	if v.Configured {
		t.Fatal("want configured false")
	}
	if v.Mode != "flat" || v.Running {
		t.Fatalf("got %+v", v)
	}
	if len(v.Schedule) != 3 || len(v.Rules) == 0 {
		t.Fatal("the schedule and rules should show whether or not it has run")
	}
}

func TestRotationReportsTheOpenLot(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 10, 15, 30, 0, 0, time.UTC)
	st := xlksata.State{
		Mode: xlksata.Recovery, EntryPrice: 187.00, EntryDate: at.Add(-24 * time.Hour),
		OptionPnL: 85, Dividends: 25, Costs: 2,
		ShortCall: &xlksata.ShortCall{
			OptionID: "c1", Strike: 191, Credit: 0.85,
			Expiration: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		},
	}
	book := rotation.Book{
		State: st,
		Marks: rotation.Marks{Day: "2026-09-10", OrdersToday: 2, StartOfDayEquity: 25_000, HighWaterEquity: 26_000},
	}
	if err := rotation.Save(dir, rotation.Book{}, at.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := rotation.Save(dir, book, at); err != nil {
		t.Fatal(err)
	}

	v := getRotation(t, &Server{DataDir: dir})
	if !v.Configured || v.Mode != "recovery" {
		t.Fatalf("got %+v", v)
	}
	if v.Basis != 18_700 {
		t.Errorf("basis %v, want 18700", v.Basis)
	}
	// 85 + 25 - 2 = 108 already banked, against a 187.00 target.
	if v.Banked != 108 {
		t.Errorf("banked %v, want 108", v.Banked)
	}
	if v.RecoveryTargetUSD != 187 {
		t.Errorf("target %v, want 187", v.RecoveryTargetUSD)
	}
	// The shares must make the remaining 79 over 100 shares: 0.79 a share.
	if want := 187.79; v.BreakEvenPrice < want-1e-9 || v.BreakEvenPrice > want+1e-9 {
		t.Errorf("break-even %v, want %v", v.BreakEvenPrice, want)
	}
	if v.ShortCall == nil || v.ShortCall.Strike != 191 {
		t.Fatalf("short call: %+v", v.ShortCall)
	}
	// Called away at 191: 100 x 4 + 108 = 508 on 18,700.
	if want := 508.0 / 18_700; v.ShortCall.AssignedPct < want-1e-9 || v.ShortCall.AssignedPct > want+1e-9 {
		t.Errorf("assigned %v, want %v", v.ShortCall.AssignedPct, want)
	}
	if v.OrdersToday != 2 || v.MarksDay != "2026-09-10" {
		t.Errorf("marks: %+v", v)
	}
	if len(v.History) != 2 || !v.History[0].At.Equal(at) {
		t.Fatalf("history should be newest first: %+v", v.History)
	}
	if !v.UpdatedAt.Equal(at) {
		t.Errorf("updatedAt %v, want %v", v.UpdatedAt, at)
	}
}

func TestRotationReportsAPendingOrder(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC()
	err := rotation.Save(dir, rotation.Book{
		State: xlksata.State{Mode: xlksata.Held, EntryPrice: 187},
		Pending: &rotation.Pending{
			OrderID: "opt-1", Kind: xlksata.SellCallToOpen, Symbol: "XLK", Strike: 191, PlacedAt: at,
		},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	v := getRotation(t, &Server{DataDir: dir})
	if v.Pending == nil || v.Pending.OrderID != "opt-1" || v.Pending.Kind != "sell_call_to_open" {
		t.Fatalf("pending: %+v", v.Pending)
	}
}

// A ledger that cannot be read is reported, not shown as a flat book.
func TestRotationRefusesAnUnreadableLedger(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, `{"version":99,"mode":"flat"}`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&Server{DataDir: dir}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/rotation", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// Reading the status must never contend with the rotation for its lock: a
// status card that could block a cycle would be worse than no card.
func TestRotationStatusDoesNotTakeTheLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := rotation.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	v := getRotation(t, &Server{DataDir: dir})
	if !v.Running || v.HolderPID <= 0 {
		t.Fatalf("a held lock should read as running: %+v", v)
	}
	// And the lock is still held afterwards.
	if _, err := rotation.Acquire(dir); err == nil {
		t.Fatal("the status read released someone else's lock")
	}
}

func writeFile(dir, body string) error {
	return osWriteFile(rotation.LedgerFile(dir), []byte(body+"\n"))
}

func osWriteFile(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }
