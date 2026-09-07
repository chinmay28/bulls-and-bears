package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeServer answers like a streamable-HTTP MCP server, in JSON or SSE.
type fakeServer struct {
	t        *testing.T
	sse      bool
	token    string
	mu       sync.Mutex
	requests []request
	calls    int
	failNext int // 5xx answers to give before behaving
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://example.test/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Header.Get("MCP-Protocol-Version") != ProtocolVersion {
		f.t.Errorf("missing protocol version header: %q", r.Header.Get("MCP-Protocol-Version"))
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		f.t.Errorf("Accept = %q, want both json and event-stream", r.Header.Get("Accept"))
	}
	var req request
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Errorf("bad request body %s: %v", body, err)
	}
	f.requests = append(f.requests, req)
	if f.failNext > 0 {
		f.failNext--
		http.Error(w, "flaky", http.StatusBadGateway)
		return
	}

	if req.ID == 0 {
		// A notification.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "sess-1" {
		f.t.Errorf("%s: session header = %q, want sess-1", req.Method, r.Header.Get("Mcp-Session-Id"))
	}

	var result any
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake", "version": "1"},
		}
	case "tools/list":
		params, _ := json.Marshal(req.Params)
		if strings.Contains(string(params), "page2") {
			result = map[string]any{"tools": []map[string]any{{"name": "b", "inputSchema": map[string]any{}}}}
		} else {
			result = map[string]any{
				"tools":      []map[string]any{{"name": "a", "description": "A", "inputSchema": map[string]any{"type": "object"}}},
				"nextCursor": "page2",
			}
		}
	case "tools/call":
		f.calls++
		params, _ := json.Marshal(req.Params)
		if strings.Contains(string(params), `"boom"`) {
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "rejected: no such symbol"}}, "isError": true}
		} else {
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "hello"}, {"type": "text", "text": "world"}}, "structuredContent": map[string]any{"x": 1}}
		}
	case "explode":
		f.write(w, req.ID, nil, &RPCError{Code: -32601, Message: "method not found"})
		return
	default:
		f.write(w, req.ID, nil, &RPCError{Code: -32601, Message: "unknown " + req.Method})
		return
	}
	f.write(w, req.ID, result, nil)
}

func (f *fakeServer) write(w http.ResponseWriter, id int64, result any, rpcErr *RPCError) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	b, _ := json.Marshal(msg)
	if f.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		// A notification first, then the answer, then a keepalive comment.
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "data: %s\n\n: keepalive\n", b)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func newClient(t *testing.T, f *fakeServer) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Client{Endpoint: srv.URL, Token: StaticToken(f.token), Name: "test", Version: "0"}
}

func TestHandshakeListAndCall(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			f := &fakeServer{t: t, sse: sse, token: "tok"}
			c := newClient(t, f)
			ctx := context.Background()

			info, err := c.Initialize(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if info.ProtocolVersion != ProtocolVersion || c.SessionID() != "sess-1" {
				t.Errorf("info = %+v, session = %q", info, c.SessionID())
			}
			// initialize, then the initialized notification.
			if len(f.requests) != 2 || f.requests[1].Method != "notifications/initialized" || f.requests[1].ID != 0 {
				t.Errorf("requests = %+v", f.requests)
			}

			tools, err := c.ListTools(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools) != 2 || tools[0].Name != "a" || tools[1].Name != "b" {
				t.Errorf("tools = %+v, want both pages", tools)
			}

			res, err := c.CallTool(ctx, "greet", map[string]any{"who": "x"})
			if err != nil {
				t.Fatal(err)
			}
			if res.Text() != "hello\nworld" || string(res.StructuredContent) != `{"x":1}` {
				t.Errorf("result = %+v", res)
			}

			_, err = c.CallTool(ctx, "greet", map[string]any{"who": "boom"})
			var te *ToolError
			if !errors.As(err, &te) || !strings.Contains(te.Text, "no such symbol") {
				t.Errorf("tool error = %v, want *ToolError with the text", err)
			}

			var rpc *RPCError
			if err := c.Call(ctx, "explode", nil, nil); !errors.As(err, &rpc) || rpc.Code != -32601 {
				t.Errorf("rpc error = %v", err)
			}
		})
	}
}

func TestUnauthorizedIsAnAuthError(t *testing.T) {
	f := &fakeServer{t: t, token: "right"}
	c := newClient(t, f)
	c.Token = StaticToken("wrong")
	_, err := c.Initialize(context.Background())
	var ae *AuthError
	if !errors.As(err, &ae) || !strings.Contains(ae.Challenge, "resource_metadata") {
		t.Fatalf("err = %v, want *AuthError carrying the challenge", err)
	}
	if Retryable(err) {
		t.Error("a 401 must not be retryable")
	}
}

func TestRetryableClassifiesErrors(t *testing.T) {
	f := &fakeServer{t: t, failNext: 1}
	c := newClient(t, f)
	_, err := c.Initialize(context.Background())
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 502 {
		t.Fatalf("err = %v, want a 502", err)
	}
	if !Retryable(err) {
		t.Error("a 502 should be retryable")
	}
	if Retryable(&RPCError{Code: 1}) || Retryable(&ToolError{}) || Retryable(nil) {
		t.Error("rpc/tool errors and nil are not retryable")
	}
	// The client does not retry on its own: exactly one request went out.
	if len(f.requests) != 1 {
		t.Errorf("requests = %d, want 1 (no automatic retry)", len(f.requests))
	}
}

func TestNoTokenMeansNoHeader(t *testing.T) {
	seen := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL}
	if err := c.Call(context.Background(), "x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if seen != "" {
		t.Errorf("Authorization = %q, want none", seen)
	}
}

func TestStreamWithoutTheAnswerIsAnError(t *testing.T) {
	r := strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/x\"}\n\n")
	if _, err := findInStream(r, 7); err == nil {
		t.Fatal("want an error for a stream that never answers")
	}
	// A string id is matched too, and a final event without a trailing blank line is read.
	r = strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"id\":\"7\",\"result\":{\"ok\":true}}")
	msg, err := findInStream(r, 7)
	if err != nil || string(msg.Result) != `{"ok":true}` {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
}
