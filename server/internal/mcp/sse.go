package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// findInStream reads a text/event-stream until it sees the JSON-RPC response
// with the wanted id, then stops. Other messages on the stream — server
// notifications, progress, requests the server makes of the client — are
// skipped: this client offers no capabilities a server could call on.
func findInStream(r io.Reader, wantID int64) (*response, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var data []string
	flush := func() (*response, bool, error) {
		if len(data) == 0 {
			return nil, false, nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var msg response
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			// Not a single message; could be a batch or noise. Skip it.
			return nil, false, nil
		}
		if idMatches(msg.ID, wantID) && (msg.Result != nil || msg.Error != nil) {
			return &msg, true, nil
		}
		return nil, false, nil
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			msg, found, err := flush()
			if err != nil {
				return nil, err
			}
			if found {
				return msg, nil
			}
		case strings.HasPrefix(line, ":"):
			// comment / keepalive
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:, id:, retry: — none change how the data is read.
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// A stream that ended without a blank line after the last event.
	if msg, found, _ := flush(); found {
		return msg, nil
	}
	return nil, fmt.Errorf("stream ended without a response for id %d", wantID)
}

// idMatches compares a JSON-RPC id, which may arrive as a number or a string,
// with the integer this client sent.
func idMatches(raw json.RawMessage, want int64) bool {
	if len(raw) == 0 {
		return false
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n == want
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == fmt.Sprint(want)
	}
	return false
}
