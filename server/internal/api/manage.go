package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars/refill"
	"github.com/chinmay28/bulls-and-bears/server/internal/spec"
)

// This file is the phone's write access to what a run reads: the specs it
// arms and the bars it prices them from. Everything here is deliberately
// narrow. Importing a spec cannot install one the runtime would refuse, and
// refilling bars cannot shorten a history — the gates are in the spec and
// refill packages, and these handlers only carry the answers back.

// handleImportStrategy installs an uploaded spec. It is how a strategy
// reaches the runtime without scp: the research side still has to have
// earned it, because the spec is validated against the same schema, the same
// out-of-sample floor and the same TTL a run applies.
func (s *Server) handleImportStrategy(w http.ResponseWriter, r *http.Request) {
	if s.SpecsDir == "" {
		writeError(w, http.StatusConflict, "the server was started without a specs directory")
		return
	}
	var body struct {
		YAML    string `json:"yaml"`
		Replace bool   `json:"replace"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeError(w, http.StatusBadRequest, "no spec: paste the YAML research wrote")
		return
	}
	now := time.Now()
	sp, path, err := spec.Install(s.SpecsDir, []byte(body.YAML), now, body.Replace)
	var invalid *spec.Invalid
	var refused *spec.Refused
	switch {
	case errors.Is(err, spec.ErrExists):
		writeError(w, http.StatusConflict,
			filepath.Base(path)+" is already installed. Replace it to import this one.")
		return
	case errors.As(err, &invalid):
		writeError(w, http.StatusUnprocessableEntity, "not a strategy spec: "+invalid.Reason)
		return
	case errors.As(err, &refused):
		writeError(w, http.StatusUnprocessableEntity, "the runtime would refuse this spec: "+refused.Reason)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("spec imported", "name", sp.Name, "path", path, "replaced", body.Replace)
	for _, v := range s.strategies(now) {
		if v.Name == sp.Name {
			writeJSON(w, http.StatusCreated, v)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, "the spec was written but did not load back")
}

// handleDeleteStrategy removes a spec. A spec imported by mistake, or one
// long past its TTL that only clutters the list, should not need a terminal
// to get rid of. The bars stay: they are expensive to fetch and belong to no
// one spec.
func (s *Server) handleDeleteStrategy(w http.ResponseWriter, r *http.Request) {
	if s.SpecsDir == "" {
		writeError(w, http.StatusConflict, "the server was started without a specs directory")
		return
	}
	name := r.PathValue("name")
	if err := spec.Remove(s.SpecsDir, name); err != nil {
		if os.IsNotExist(err) || strings.HasPrefix(err.Error(), "no spec named") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Info("spec removed", "name", name)
	w.WriteHeader(http.StatusNoContent)
}

// handleRefreshBars refills the bars on disk from the fetcher, so a run that
// halted on stale data can be fixed from the phone. It never trades and is
// allowed while halted: refusing to fetch data would only leave the operator
// with less to decide on.
func (s *Server) handleRefreshBars(w http.ResponseWriter, r *http.Request) {
	if s.BarsDir == "" || s.Fetcher == nil {
		writeError(w, http.StatusConflict, "the server was started without a bars directory to refill")
		return
	}
	var body struct {
		Symbols []string `json:"symbols"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	symbols := body.Symbols
	if len(symbols) == 0 {
		symbols = s.knownSymbols(time.Now())
	}
	if len(symbols) == 0 {
		writeError(w, http.StatusBadRequest,
			"no symbols: import a spec first, or name the symbols to fetch")
		return
	}
	results := refill.Run(r.Context(), s.Fetcher, symbols, refill.Options{Dir: s.BarsDir})
	for _, res := range results {
		if res.Error != "" {
			s.Log.Warn("bars refill failed", "symbol", res.Symbol, "error", res.Error)
			continue
		}
		s.Log.Info("bars refilled", "symbol", res.Symbol, "bars", res.Bars, "added", res.Added, "last", res.Last)
	}
	writeJSON(w, http.StatusOK, results)
}

// knownSymbols is every symbol the machine has a reason to hold bars for:
// the ones specs name, and the ones already on disk. The second half matters
// when there is no spec yet — bars fetched before a spec arrives are not
// orphans, they are the head start.
func (s *Server) knownSymbols(now time.Time) []string {
	seen := map[string]bool{}
	if s.SpecsDir != "" {
		for _, l := range spec.LoadDir(s.SpecsDir, now) {
			if l.Spec == nil {
				continue
			}
			for _, sym := range l.Spec.Universe {
				seen[sym] = true
			}
		}
	}
	for _, sym := range onDisk(s.BarsDir) {
		seen[sym] = true
	}
	out := make([]string, 0, len(seen))
	for sym := range seen {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

// onDisk is the symbols the bars directory holds a file for.
func onDisk(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".parquet" {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".parquet"))
	}
	sort.Strings(out)
	return out
}
