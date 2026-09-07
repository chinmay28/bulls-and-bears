package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// authServer is a fake authorization server: metadata, registration, an
// authorize endpoint that redirects straight back with a code, and a token
// endpoint that checks PKCE.
type authServer struct {
	srv       *httptest.Server
	mu        sync.Mutex
	challenge string
	code      string
	refreshes int
}

func newAuthServer(t *testing.T) *authServer {
	a := &authServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": a.srv.URL + "/authorize",
			"token_endpoint":         a.srv.URL + "/token",
			"registration_endpoint":  a.srv.URL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["token_endpoint_auth_method"] != "none" {
			http.Error(w, "want a public client", 400)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"client_id": "client-123"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "client-123" || q.Get("resource") == "" {
			http.Error(w, "bad authorize request", 400)
			return
		}
		a.mu.Lock()
		a.challenge = q.Get("code_challenge")
		a.code = "code-xyz"
		a.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=code-xyz&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "code-xyz" || r.Form.Get("code_verifier") == "" {
				http.Error(w, "bad code", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "refresh_token": "rt-1", "token_type": "Bearer", "expires_in": 3600})
		case "refresh_token":
			a.mu.Lock()
			a.refreshes++
			n := a.refreshes
			a.mu.Unlock()
			if r.Form.Get("refresh_token") != fmt.Sprintf("rt-%d", n) {
				http.Error(w, "unknown refresh token "+r.Form.Get("refresh_token"), 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("at-%d", n+1), "refresh_token": fmt.Sprintf("rt-%d", n+1), "expires_in": 3600})
		default:
			http.Error(w, "bad grant", 400)
		}
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

// mcpServer is the resource server: a 401 with the challenge, and the
// protected-resource metadata pointing at the authorization server.
func mcpServer(t *testing.T, as *authServer) *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": srv.URL + "/mcp", "authorization_servers": []string{as.srv.URL}})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(401)
			return
		}
		w.Write([]byte("ok"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverRegisterLoginRefresh(t *testing.T) {
	as := newAuthServer(t)
	rs := mcpServer(t, as)
	ctx := context.Background()
	hc := rs.Client()

	// The challenge, as the mcp client would have received it.
	resp, _ := hc.Get(rs.URL + "/mcp")
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()

	meta, err := Discover(ctx, hc, rs.URL+"/mcp", challenge)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TokenEndpoint != as.srv.URL+"/token" || meta.Resource != rs.URL+"/mcp" || meta.RegistrationEndpoint == "" {
		t.Fatalf("meta = %+v", meta)
	}

	clientID, err := Register(ctx, hc, meta, "http://127.0.0.1:1/callback", "bnb test")
	if err != nil {
		t.Fatal(err)
	}
	if clientID != "client-123" {
		t.Fatalf("client id = %q", clientID)
	}

	// "Open the browser" by following the authorize URL with a client that
	// obeys redirects back to the localhost listener.
	open := func(u string) {
		go func() {
			r, err := http.Get(u)
			if err == nil {
				r.Body.Close()
			}
		}()
	}
	tok, err := Login(ctx, hc, meta, clientID, []string{"trading"}, open)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" || tok.ClientID != clientID || tok.TokenEndpoint != meta.TokenEndpoint || tok.Expiry.IsZero() {
		t.Fatalf("token = %+v", tok)
	}

	// Stored, private, and refreshed when it expires.
	store := Store{Path: filepath.Join(t.TempDir(), "robinhood.json")}
	if err := store.Save(tok); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(store.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v", fi.Mode())
	}
	src := &Source{Store: store, HTTP: hc}
	if got, _ := src.Token(ctx); got != "at-1" {
		t.Errorf("token = %q before expiry", got)
	}
	// Age the stored token past its expiry; a fresh Source must refresh it.
	tok.Expiry = time.Now().Add(-time.Hour)
	store.Save(tok)
	src = &Source{Store: store, HTTP: hc}
	got, err := src.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "at-2" {
		t.Errorf("token = %q after expiry, want a refreshed at-2", got)
	}
	saved, _ := store.Load()
	if saved.RefreshToken != "rt-2" {
		t.Errorf("rotated refresh token was not saved: %+v", saved)
	}
	// A second call inside the new expiry does not refresh again.
	if got, _ := src.Token(ctx); got != "at-2" || as.refreshes != 1 {
		t.Errorf("token = %q, refreshes = %d", got, as.refreshes)
	}
}

func TestDiscoverWithoutAChallengeUsesTheWellKnownPath(t *testing.T) {
	as := newAuthServer(t)
	rs := mcpServer(t, as)
	meta, err := Discover(context.Background(), rs.Client(), rs.URL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.AuthorizationEndpoint == "" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestLoginRefusesABadState(t *testing.T) {
	as := newAuthServer(t)
	meta := Metadata{AuthorizationEndpoint: as.srv.URL + "/authorize", TokenEndpoint: as.srv.URL + "/token", Resource: "r"}
	open := func(u string) {
		go func() {
			// Hit the callback directly with the wrong state.
			parsed, _ := url.Parse(u)
			redirect := parsed.Query().Get("redirect_uri")
			r, err := http.Get(redirect + "?code=x&state=wrong")
			if err == nil {
				r.Body.Close()
			}
		}()
	}
	_, err := Login(context.Background(), as.srv.Client(), meta, "client-123", nil, open)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v, want a state mismatch", err)
	}
}

func TestSourceWithoutATokenFileSaysSo(t *testing.T) {
	src := &Source{Store: Store{Path: filepath.Join(t.TempDir(), "none.json")}}
	if _, err := src.Token(context.Background()); !os.IsNotExist(err) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestChallengeResource(t *testing.T) {
	if got := challengeResource(`Bearer realm="x", resource_metadata="https://a/b", error="invalid_token"`); got != "https://a/b" {
		t.Errorf("got %q", got)
	}
	if got := challengeResource(`Bearer`); got != "" {
		t.Errorf("got %q", got)
	}
}
