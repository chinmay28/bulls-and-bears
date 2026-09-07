// Package mcp is a minimal client for the Model Context Protocol over
// streamable HTTP: initialize, tools/list, tools/call, and nothing else.
//
// It owns the JSON-RPC framing, the session header, the protocol-version
// header, and the two shapes a server may answer in (a JSON body or an SSE
// stream carrying the response). It does not own authentication — a
// TokenSource supplies the bearer token, and oauth/ is one implementation —
// and it does not know what any tool means: the Robinhood adapters do.
//
// Retries are the caller's choice per call. A tools/list can be retried
// freely; a tools/call that places an order must not be, because the first
// attempt may have gone through. Nothing here retries a call on its own.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the MCP revision this client speaks.
const ProtocolVersion = "2025-06-18"

// TokenSource hands out the bearer token for each request. Returning an
// empty token means no Authorization header.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a TokenSource for a token that never changes (tests, or a
// server that needs none).
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// Client talks to one MCP server.
type Client struct {
	// Endpoint is the server's MCP URL, e.g. https://agent.robinhood.com/mcp/trading.
	Endpoint string
	// HTTP is the transport; nil means a client with a 30 s timeout.
	HTTP *http.Client
	// Token supplies the bearer token; nil means none.
	Token TokenSource
	// Name and Version identify this client to the server.
	Name, Version string

	mu        sync.Mutex
	sessionID string
	nextID    atomic.Int64
	server    *ServerInfo
}

// ServerInfo is what initialize returns about the other side.
type ServerInfo struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	Instructions    string          `json:"instructions,omitempty"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// Tool is one entry of tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolResult is what tools/call returns: content blocks, an optional
// structured payload, and whether the tool itself reported an error.
type ToolResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// Content is one block of a tool result; only text is interpreted here.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Text joins the result's text blocks, which is usually the whole answer.
func (r *ToolResult) Text() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// RPCError is a JSON-RPC error the server returned.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp: rpc error %d: %s", e.Code, e.Message) }

// AuthError is a 401 from the server. Challenge is the WWW-Authenticate
// header, which oauth/ knows how to follow.
type AuthError struct {
	Challenge string
}

func (e *AuthError) Error() string { return "mcp: unauthorized (" + e.Challenge + ")" }

// HTTPError is any other non-2xx answer.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("mcp: http %d: %s", e.Status, e.Body) }

// Retryable reports whether an error is worth one more try: transport
// failures and 5xx answers, never a 4xx and never an RPC error.
func Retryable(err error) bool {
	var h *HTTPError
	if errors.As(err, &h) {
		return h.Status >= 500
	}
	var a *AuthError
	var r *RPCError
	var t *ToolError
	if errors.As(err, &a) || errors.As(err, &r) || errors.As(err, &t) {
		return false
	}
	return err != nil && !errors.Is(err, context.Canceled)
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Initialize performs the handshake: initialize, then the initialized
// notification. It records the session id the server assigns, which every
// later request carries. Calling it again starts a new session.
func (c *Client) Initialize(ctx context.Context) (*ServerInfo, error) {
	c.mu.Lock()
	c.sessionID = ""
	c.server = nil
	c.mu.Unlock()

	name, version := c.Name, c.Version
	if name == "" {
		name = "bnb"
	}
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": name, "version": version},
	}
	var info ServerInfo
	if err := c.Call(ctx, "initialize", params, &info); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.server = &info
	c.mu.Unlock()
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return nil, fmt.Errorf("mcp: initialized notification: %w", err)
	}
	return &info, nil
}

// ListTools returns every tool the server offers, following pagination.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.Call(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// CallTool invokes one tool. args must marshal to a JSON object. A result
// with IsError set is returned as a *ToolError so callers cannot mistake a
// tool's refusal for its answer.
func (c *Client) CallTool(ctx context.Context, name string, args any) (*ToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	var result ToolResult
	if err := c.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
		return nil, err
	}
	if result.IsError {
		return &result, &ToolError{Tool: name, Text: result.Text()}
	}
	return &result, nil
}

// ToolError is a tool reporting failure in its result rather than as an RPC
// error — the MCP way of saying "the order was rejected".
type ToolError struct {
	Tool string
	Text string
}

func (e *ToolError) Error() string { return "mcp: " + e.Tool + ": " + e.Text }

// Call sends one JSON-RPC request and decodes its result into out (which may
// be nil to discard it).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	if method == "" {
		return errors.New("mcp: empty method")
	}
	id := c.nextID.Add(1)
	body, err := c.post(ctx, request{JSONRPC: "2.0", ID: id, Method: method, Params: params}, id)
	if err != nil {
		return err
	}
	if body.Error != nil {
		return body.Error
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body.Result, out); err != nil {
		return fmt.Errorf("mcp: %s: decoding result: %w", method, err)
	}
	return nil
}

// notify sends a JSON-RPC notification (no id, no answer expected).
func (c *Client) notify(ctx context.Context, method string, params any) error {
	_, err := c.post(ctx, request{JSONRPC: "2.0", Method: method, Params: params}, 0)
	return err
}

// post does one HTTP round trip and, for a request with an id, finds its
// response in whatever shape the server answered.
func (c *Client) post(ctx context.Context, req request, wantID int64) (*response, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	c.mu.Lock()
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	c.mu.Unlock()
	if c.Token != nil {
		tok, err := c.Token.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("mcp: token: %w", err)
		}
		if tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	resp, err := c.http().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: %w", req.Method, err)
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, &AuthError{Challenge: resp.Header.Get("WWW-Authenticate")}
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent:
		// A notification's answer, or a server that had nothing to say.
		if wantID != 0 {
			return nil, &HTTPError{Status: resp.StatusCode, Body: "no response body for a request"}
		}
		return nil, nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if wantID == 0 {
		// A notification answered with 200 and a body: drain and ignore.
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mediaType {
	case "application/json":
		var body response
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); err != nil {
			return nil, fmt.Errorf("mcp: %s: decoding response: %w", req.Method, err)
		}
		return &body, nil
	case "text/event-stream":
		body, err := findInStream(resp.Body, wantID)
		if err != nil {
			return nil, fmt.Errorf("mcp: %s: %w", req.Method, err)
		}
		return body, nil
	default:
		return nil, fmt.Errorf("mcp: %s: unexpected content type %q", req.Method, resp.Header.Get("Content-Type"))
	}
}

// SessionID is the server-assigned session, empty before Initialize or for a
// server that does not use one.
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// Server is what Initialize learned, nil before it ran.
func (c *Client) Server() *ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.server
}
