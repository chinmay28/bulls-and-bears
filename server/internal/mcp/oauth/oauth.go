// Package oauth gets and keeps the bearer token an MCP server wants.
//
// It follows the MCP authorization flow: a 401 with a resource_metadata
// challenge, the protected-resource metadata, the authorization server's
// metadata, dynamic client registration when the server offers it, and the
// authorization-code grant with PKCE and a localhost redirect. Robinhood's
// documentation says the redirect lands on localhost and that onboarding
// completes only in a desktop browser (docs/PLAN.md §2), which is why Login
// is a thing you run on a laptop and Store is a file you then copy to the
// machine that trades. The refresh happens headlessly wherever the file is.
//
// The token file is 0600 in the data directory and never in the database.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Metadata is what discovery learns about the server's authorization.
type Metadata struct {
	Resource              string   `json:"resource"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
}

// Token is what the token endpoint issued.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	Expiry       time.Time `json:"expiry"`
	// Everything a refresh needs, kept beside the token so the file is
	// self-contained.
	ClientID      string `json:"client_id"`
	TokenEndpoint string `json:"token_endpoint"`
	Resource      string `json:"resource"`
}

// Expired reports whether the access token is past its expiry, with a
// minute's grace so a token about to lapse is refreshed rather than sent.
func (t *Token) Expired(now time.Time) bool {
	return !t.Expiry.IsZero() && now.Add(time.Minute).After(t.Expiry)
}

// challengeResource pulls resource_metadata="…" out of a WWW-Authenticate
// value. Empty when the server did not say.
func challengeResource(challenge string) string {
	const key = `resource_metadata="`
	i := strings.Index(challenge, key)
	if i < 0 {
		return ""
	}
	rest := challenge[i+len(key):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return ""
}

// Discover learns where to authorize against for an MCP endpoint: from the
// challenge's resource metadata URL when there is one, else from the
// endpoint's own /.well-known/oauth-protected-resource, then the named
// authorization server's metadata.
func Discover(ctx context.Context, hc *http.Client, mcpURL, challenge string) (Metadata, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	prm := challengeResource(challenge)
	if prm == "" {
		u, err := url.Parse(mcpURL)
		if err != nil {
			return Metadata{}, err
		}
		prm = u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path
	}
	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := getJSON(ctx, hc, prm, &resource); err != nil {
		return Metadata{}, fmt.Errorf("oauth: protected resource metadata: %w", err)
	}
	if len(resource.AuthorizationServers) == 0 {
		return Metadata{}, errors.New("oauth: protected resource metadata names no authorization server")
	}
	as := strings.TrimSuffix(resource.AuthorizationServers[0], "/")
	asURL, err := url.Parse(as)
	if err != nil {
		return Metadata{}, err
	}
	var meta Metadata
	var lastErr error
	for _, candidate := range []string{
		asURL.Scheme + "://" + asURL.Host + "/.well-known/oauth-authorization-server" + asURL.Path,
		as + "/.well-known/oauth-authorization-server",
		as + "/.well-known/openid-configuration",
	} {
		if lastErr = getJSON(ctx, hc, candidate, &meta); lastErr == nil && meta.TokenEndpoint != "" {
			break
		}
	}
	if meta.TokenEndpoint == "" {
		return Metadata{}, fmt.Errorf("oauth: authorization server metadata: %v", lastErr)
	}
	meta.Resource = resource.Resource
	if meta.Resource == "" {
		meta.Resource = mcpURL
	}
	return meta, nil
}

// Register performs dynamic client registration for a public client with the
// localhost redirect, returning the client id. An authorization server
// without a registration endpoint needs a pre-registered id instead.
func Register(ctx context.Context, hc *http.Client, meta Metadata, redirectURI, name string) (string, error) {
	if meta.RegistrationEndpoint == "" {
		return "", errors.New("oauth: the authorization server offers no dynamic registration; pass a client id")
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                name,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("oauth: registration: http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ClientID == "" {
		return "", errors.New("oauth: registration answered without a client_id")
	}
	return out.ClientID, nil
}

// Login runs the authorization-code flow: a localhost listener for the
// redirect, a URL the person opens in a desktop browser (handed to open,
// which may print it), and the code exchanged with PKCE. It returns the
// token with everything a refresh needs.
func Login(ctx context.Context, hc *http.Client, meta Metadata, clientID string, scopes []string, open func(url string)) (*Token, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	redirect := "http://" + ln.Addr().String() + "/callback"

	verifier := random(64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := random(24)

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {meta.Resource},
	}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	sep := "?"
	if strings.Contains(meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	authURL := meta.AuthorizationEndpoint + sep + q.Encode()

	type answer struct {
		code string
		err  error
	}
	got := make(chan answer, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			got <- answer{err: errors.New("oauth: state mismatch on the redirect")}
			return
		}
		if e := r.URL.Query().Get("error"); e != "" {
			http.Error(w, "authorization refused: "+e, http.StatusBadRequest)
			got <- answer{err: errors.New("oauth: authorization refused: " + e + " " + r.URL.Query().Get("error_description"))}
			return
		}
		fmt.Fprint(w, "<!doctype html><title>Bulls and Bears</title><p>Signed in. You can close this tab and go back to the terminal.")
		got <- answer{code: r.URL.Query().Get("code")}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	open(authURL)
	var a answer
	select {
	case a = <-got:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if a.err != nil {
		return nil, a.err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {a.code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {meta.Resource},
	}
	tok, err := exchange(ctx, hc, meta.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	tok.ClientID, tok.TokenEndpoint, tok.Resource = clientID, meta.TokenEndpoint, meta.Resource
	return tok, nil
}

// Refresh trades a refresh token for a new access token. A server that
// rotates refresh tokens hands back a new one, which replaces the old.
func Refresh(ctx context.Context, hc *http.Client, t *Token) (*Token, error) {
	if t.RefreshToken == "" {
		return nil, errors.New("oauth: no refresh token: sign in again on a desktop")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {t.ClientID},
		"resource":      {t.Resource},
	}
	nt, err := exchange(ctx, hc, t.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = t.RefreshToken
	}
	nt.ClientID, nt.TokenEndpoint, nt.Resource = t.ClientID, t.TokenEndpoint, t.Resource
	return nt, nil
}

func exchange(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (*Token, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth: token endpoint: http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.AccessToken == "" {
		return nil, errors.New("oauth: token endpoint answered without an access_token")
	}
	t := &Token{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, TokenType: raw.TokenType, Scope: raw.Scope}
	if raw.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}

// Store keeps the token in a 0600 file.
type Store struct {
	Path string
}

// Load reads the token; os.ErrNotExist when nobody has signed in.
func (s Store) Load() (*Token, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("oauth: %s: %w", s.Path, err)
	}
	return &t, nil
}

// Save writes the token whole and renames it into place, 0600.
func (s Store) Save(t *Token) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Source is an mcp.TokenSource over a Store: it hands out the access token,
// refreshing and re-saving it when it has expired.
type Source struct {
	Store Store
	HTTP  *http.Client
	Now   func() time.Time

	mu    sync.Mutex
	token *Token
}

// Token implements mcp.TokenSource.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if s.token == nil {
		t, err := s.Store.Load()
		if err != nil {
			return "", err
		}
		s.token = t
	}
	if s.token.Expired(now()) {
		nt, err := Refresh(ctx, s.HTTP, s.token)
		if err != nil {
			return "", err
		}
		if err := s.Store.Save(nt); err != nil {
			return "", err
		}
		s.token = nt
	}
	return s.token.AccessToken, nil
}

func getJSON(ctx context.Context, hc *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: http %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}
