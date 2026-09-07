package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chinmay28/bulls-and-bears/server/internal/halt"
)

func testServer(t *testing.T, pin string) (*Server, http.Handler) {
	t.Helper()
	s := &Server{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "v2026.9.7",
		DataDir: t.TempDir(),
		Ref:     "main",
		Auth:    NewPinAuth(pin),
	}
	return s, s.Auth.Middleware(s.Routes())
}

func do(t *testing.T, h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

func TestHealthReportsTheVersion(t *testing.T) {
	_, h := testServer(t, "")
	w := do(t, h, "GET", "/api/health", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if got := decode[map[string]string](t, w)["version"]; got != "v2026.9.7" {
		t.Errorf("version = %q", got)
	}
}

func TestSelfAndOverviewStartUnhalted(t *testing.T) {
	_, h := testServer(t, "")
	self := decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if self.Halted || self.Halt != nil || self.Mode != "dry-run" || self.Version != "v2026.9.7" {
		t.Errorf("self = %+v", self)
	}
	w := do(t, h, "GET", "/api/overview", "")
	if !strings.Contains(w.Body.String(), `"strategies":[]`) {
		t.Errorf("overview = %s, want an empty strategies array rather than null", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"book":null`) {
		t.Errorf("overview = %s, want a null book before there is one", w.Body)
	}
}

func TestHaltAndResumeFromTheApp(t *testing.T) {
	s, h := testServer(t, "")
	w := do(t, h, "POST", "/api/halt", `{"reason":"  going on holiday "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("halt status = %d, body %s", w.Code, w.Body)
	}
	m, err := halt.Status(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Reason != "going on holiday" || m.By != "app" {
		t.Errorf("marker = %+v", m)
	}
	self := decode[selfView](t, do(t, h, "GET", "/api/self", ""))
	if !self.Halted || self.Halt == nil || self.Halt.Reason != "going on holiday" {
		t.Errorf("self after halt = %+v", self)
	}

	if w := do(t, h, "DELETE", "/api/halt", ""); w.Code != http.StatusOK {
		t.Fatalf("resume status = %d", w.Code)
	}
	if halt.Halted(s.DataDir) {
		t.Error("still halted after resume")
	}
}

func TestHaltWithNoBodyHasADefaultReason(t *testing.T) {
	s, h := testServer(t, "")
	do(t, h, "POST", "/api/halt", "")
	m, _ := halt.Status(s.DataDir)
	if m.Reason != "halted from the app" {
		t.Errorf("reason = %q", m.Reason)
	}
}

func TestPinGatesTheAPIButNotThePWA(t *testing.T) {
	_, h := testServer(t, "1234")
	if w := do(t, h, "GET", "/api/self", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("without a session: status = %d, want 401", w.Code)
	}
	if w := do(t, h, "GET", "/api/health", ""); w.Code != http.StatusOK {
		t.Errorf("health should stay public, got %d", w.Code)
	}
	status := decode[map[string]bool](t, do(t, h, "GET", "/api/session", ""))
	if !status["required"] || status["authenticated"] {
		t.Errorf("session status = %v", status)
	}

	if w := do(t, h, "POST", "/api/session", `{"pin":"0000"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong PIN: status = %d, want 401", w.Code)
	}
	w := do(t, h, "POST", "/api/session", `{"pin":"1234"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d, body %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %v, want one HttpOnly session cookie", cookies)
	}
	if w := do(t, h, "GET", "/api/self", "", cookies[0]); w.Code != http.StatusOK {
		t.Errorf("with a session: status = %d, want 200", w.Code)
	}
}
