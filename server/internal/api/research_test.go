package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/research"
)

const fakeUV = `#!/bin/sh
echo "uv $*"
case "$1" in
  sync) mkdir -p "$UV_PROJECT_ENVIRONMENT/bin" && : > "$UV_PROJECT_ENVIRONMENT/bin/python" ;;
  run) echo "NOT PROMOTED (OOS Sharpe 0.26 < 1.0): wrote out/x.rejected.yaml" ;;
esac
`

func researchServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s, h := testServer(t, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uv := filepath.Join(t.TempDir(), "uv")
	if err := os.WriteFile(uv, []byte(fakeUV), 0o755); err != nil {
		t.Fatal(err)
	}
	s.SpecsDir = t.TempDir()
	s.BarsDir = t.TempDir()
	s.Research = &research.Runner{Dir: dir, DataDir: s.DataDir, SpecsDir: s.SpecsDir, BarsDir: s.BarsDir, UV: uv, Log: s.Log}
	return s, h
}

func waitJob(t *testing.T, h http.Handler, id string) jobLogView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		w := do(t, h, "GET", "/api/research/jobs/"+id, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET job: %d %s", w.Code, w.Body)
		}
		v := decode[jobLogView](t, w)
		if v.Job.Status != research.Running {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return jobLogView{}
}

func TestResearchWithoutATree(t *testing.T) {
	_, h := testServer(t, "")
	for _, req := range [][2]string{{"GET", "/api/research"}, {"POST", "/api/research/setup"}} {
		if w := do(t, h, req[0], req[1], ""); w.Code != http.StatusConflict {
			t.Errorf("%s %s = %d, want 409", req[0], req[1], w.Code)
		}
	}
}

func TestResearchSetupAndRunFromTheApp(t *testing.T) {
	s, h := researchServer(t)

	w := do(t, h, "GET", "/api/research", "")
	info := decode[researchView](t, w)
	if !info.Env.Tree || info.Env.Synced || info.Current != nil || len(info.Studies) == 0 {
		t.Fatalf("research = %+v", info)
	}

	w = do(t, h, "POST", "/api/research/setup", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	setup := decode[research.View](t, w)
	done := waitJob(t, h, setup.ID)
	if done.Job.Status != research.Succeeded || !strings.Contains(done.Log, "uv sync --frozen") || done.Next != done.Job.LogBytes {
		t.Fatalf("setup job = %+v log %q", done.Job, done.Log)
	}
	if !decode[researchView](t, do(t, h, "GET", "/api/research", "")).Env.Synced {
		t.Error("env not synced after setup")
	}

	w = do(t, h, "POST", "/api/research/runs", `{"study":"etf_gld_ratio","trainTo":"2019-12-31"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("run = %d %s", w.Code, w.Body)
	}
	run := decode[research.View](t, w)
	got := waitJob(t, h, run.ID)
	if got.Job.Status != research.Succeeded || got.Job.Outcome == nil || got.Job.Outcome.Promoted {
		t.Fatalf("run job = %+v", got.Job)
	}
	if !strings.Contains(got.Log, "--specs-dir "+s.SpecsDir) || !strings.Contains(got.Log, "--bars-dir "+s.BarsDir) {
		t.Errorf("the study was not pointed at the runtime's directories:\n%s", got.Log)
	}
	// Paging from the end returns nothing new.
	tail := decode[jobLogView](t, do(t, h, "GET", "/api/research/jobs/"+run.ID+"?from="+strconv.Itoa(got.Next), ""))
	if tail.Log != "" || tail.Next != got.Next {
		t.Errorf("tail = %+v", tail)
	}

	info = decode[researchView](t, do(t, h, "GET", "/api/research", ""))
	if info.Current == nil || info.Current.ID != run.ID || len(info.Recent) != 1 || len(info.Logs) != 2 {
		t.Errorf("research after two jobs = %+v", info)
	}
}

func TestResearchRunValidation(t *testing.T) {
	_, h := researchServer(t)
	cases := []struct {
		body string
		want int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"study":"momentum"}`, http.StatusBadRequest},
		{`{"study":"etf_gld_ratio","trainTo":"soon"}`, http.StatusBadRequest},
		{`{"study":"etf_gld_ratio","typo":1}`, http.StatusBadRequest},
		{`{"study":"etf_gld_ratio","universe":["SPY"]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if w := do(t, h, "POST", "/api/research/runs", tc.body); w.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", tc.body, w.Code, w.Body, tc.want)
		}
	}
	if w := do(t, h, "GET", "/api/research/jobs/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown job = %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/research/jobs/x?from=-1", ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad offset = %d", w.Code)
	}
	if w := do(t, h, "DELETE", "/api/research/jobs/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("cancel unknown = %d", w.Code)
	}
}
