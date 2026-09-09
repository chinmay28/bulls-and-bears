// Package robinhood is the typed edge of the Robinhood Trading MCP: one Go
// function per tool this runtime uses, decoding the server's answers into
// types the rest of the code can hold.
//
// It owns the shapes and nothing else. It does not decide anything, does not
// retry, and does not know what a strategy is: `internal/mcp` carries the
// JSON-RPC, `internal/risk` decides whether an order may go, and the callers
// decide what to ask for. Every field here was read off the live server on
// 2026-09-09 and the fixtures under testdata/ are those answers verbatim
// (docs/DISCOVERY.md).
//
// Two things about the wire format shape everything below. Money and greeks
// arrive as decimal *strings*, so Num decodes either form and an absent
// value is zero. And every tool answers `{"data": {...}, "guide": "..."}`,
// where guide is prose addressed to an agent, not data — it is dropped.
package robinhood

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/mcp"
)

// Tools is the part of *mcp.Client this package needs, which is what lets
// the tests run against recorded answers instead of the network.
type Tools interface {
	CallTool(ctx context.Context, name string, args any) (*mcp.ToolResult, error)
}

// Client is the typed façade over one MCP connection.
type Client struct {
	// Tools is the transport. Required.
	Tools Tools
	// Account is the account number every call is scoped to. The plan's §9
	// asks the runtime to assert it is trading the account it was
	// configured for; CheckAccount is that assertion.
	Account string
}

// Num is a number that may arrive as a JSON number or a decimal string,
// which is how this server sends every price, quantity and greek. An absent
// value, an empty string and null all decode as zero; anything else that is
// not a number is an error rather than a silent zero, because a price this
// package could not read is not a price of zero.
type Num float64

func (n *Num) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` || s == "" {
		*n = 0
		return nil
	}
	s = strings.Trim(s, `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("robinhood: %q is not a number", s)
	}
	*n = Num(f)
	return nil
}

// Float is the value as the rest of the runtime holds it.
func (n Num) Float() float64 { return float64(n) }

// Time is a timestamp that may be RFC3339 with or without a zone, or a bare
// date. Zero when absent.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "null" || s == "" {
		t.Time = time.Time{}
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v.UTC()
			return nil
		}
	}
	return fmt.Errorf("robinhood: %q is not a timestamp", s)
}

// ErrShape is a well-formed answer that is not the answer that was asked
// for: a tool that returned no data, a symbol that came back missing. It is
// separate from a transport or RPC failure because it is not retryable.
var ErrShape = errors.New("robinhood")

// call invokes a tool and decodes its `data` envelope into out.
//
// The server answers in a text content block holding JSON, and may also fill
// structuredContent; structured is preferred where present because it is the
// typed channel, with the text block as the fallback every server fills.
func (c *Client) call(ctx context.Context, tool string, args any, out any) error {
	if c.Tools == nil {
		return fmt.Errorf("%w: no transport configured", ErrShape)
	}
	res, err := c.Tools.CallTool(ctx, tool, args)
	if err != nil {
		return err
	}
	body := res.StructuredContent
	if len(body) == 0 {
		body = json.RawMessage(res.Text())
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("%w: %s returned nothing", ErrShape, tool)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%w: %s answered with something that is not JSON: %v", ErrShape, tool, err)
	}
	if len(env.Data) == 0 {
		return fmt.Errorf("%w: %s answered without a data envelope", ErrShape, tool)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("%w: %s data did not fit %T: %v", ErrShape, tool, out, err)
	}
	return nil
}
