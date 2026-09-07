package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/halt"
)

// selfView is what Settings needs to show about the machine it is talking to.
type selfView struct {
	Version string `json:"version"`
	// Ref is the version a self-update would build by default.
	Ref string `json:"ref"`
	// Mode is how orders are handled: "dry-run" until a live broker exists.
	Mode string `json:"mode"`
	// Halted says whether the halt marker is present; Halt says why.
	Halted bool      `json:"halted"`
	Halt   *haltView `json:"halt"`
	Now    time.Time `json:"now"`
}

type haltView struct {
	Reason string    `json:"reason"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

func (s *Server) haltView() (bool, *haltView) {
	m, err := halt.Status(s.DataDir)
	if errors.Is(err, halt.ErrNotHalted) {
		return false, nil
	}
	if err != nil {
		// Fail closed: a data directory that cannot be read is not a green light.
		return true, &haltView{Reason: "cannot read the data directory: " + err.Error(), By: "unknown"}
	}
	return true, &haltView{Reason: m.Reason, By: m.By, At: m.At}
}

func (s *Server) handleGetSelf(w http.ResponseWriter, r *http.Request) {
	halted, hv := s.haltView()
	writeJSON(w, http.StatusOK, selfView{
		Version: s.Version,
		Ref:     s.Ref,
		Mode:    "dry-run",
		Halted:  halted,
		Halt:    hv,
		Now:     time.Now().UTC(),
	})
}

func (s *Server) handleGetHalt(w http.ResponseWriter, r *http.Request) {
	halted, hv := s.haltView()
	writeJSON(w, http.StatusOK, map[string]any{"halted": halted, "halt": hv})
}

// handleHalt writes the marker on the app's behalf. The reason is optional;
// "halted from the app" is what an empty one records.
func (s *Server) handleHalt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "halted from the app"
	}
	if err := halt.Halt(s.DataDir, halt.Marker{Reason: reason, By: "app"}); err != nil {
		s.Log.Error("api: halt", "err", err)
		writeError(w, http.StatusInternalServerError, "could not write the halt marker")
		return
	}
	s.handleGetHalt(w, r)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if err := halt.Resume(s.DataDir); err != nil {
		s.Log.Error("api: resume", "err", err)
		writeError(w, http.StatusInternalServerError, "could not remove the halt marker")
		return
	}
	s.handleGetHalt(w, r)
}
