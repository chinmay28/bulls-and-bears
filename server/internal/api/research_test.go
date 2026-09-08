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
	s.Schedule = &research.Schedule{Runner: s.Research, SpecsDir: s.SpecsDir, DataDir: s.DataDir, Log: s.Log}
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

func TestScheduleSwitchAndRevalidateNow(t *testing.T) {
	_, h := researchServer(t)
	info := decode[researchView](t, do(t, h, "GET", "/api/research", ""))
	if info.Schedule == nil || !info.Schedule.Enabled || info.Schedule.Running {
		t.Fatalf("schedule = %+v", info.Schedule)
	}
	if w := do(t, h, "PUT", "/api/research/schedule", `{"enabled":false}`); w.Code != http.StatusOK || decode[research.ScheduleView](t, w).Enabled {
		t.Errorf("turning off = %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PUT", "/api/research/schedule", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty body = %d", w.Code)
	}
	// Install a spec, then re-validate everything now.
	src, err := os.ReadFile("../spec/testdata/gld_gdx_pairs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, h, "POST", "/api/strategies", `{"yaml":`+strconv.Quote(string(src))+`}`); w.Code != http.StatusCreated {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/research/schedule/run", ""); w.Code != http.StatusAccepted {
		t.Fatalf("revalidate now = %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		v := decode[researchView](t, do(t, h, "GET", "/api/research", ""))
		if v.Schedule != nil && !v.Schedule.Running && len(v.Schedule.History) == 1 {
			rec := v.Schedule.History[0]
			if rec.Name != "gld_gdx_pairs" || rec.Study != "gld_gdx_pairs" || rec.Status != research.Succeeded {
				t.Errorf("record = %+v", rec)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("re-validation never recorded: %+v", v.Schedule)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStageIsTheOperatorsAndDefaultsToPaper(t *testing.T) {
	_, h := researchServer(t)
	src, err := os.ReadFile("../spec/testdata/gld_gdx_pairs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, h, "POST", "/api/strategies", `{"yaml":`+strconv.Quote(string(src))+`}`); w.Code != http.StatusCreated {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	if v := decode[strategyView](t, do(t, h, "GET", "/api/strategies/gld_gdx_pairs", "")); v.Stage != "paper" {
		t.Errorf("stage = %q, want paper", v.Stage)
	}
	if w := do(t, h, "PUT", "/api/strategies/gld_gdx_pairs/stage", `{"stage":"live"}`); w.Code != http.StatusOK || decode[strategyView](t, w).Stage != "live" {
		t.Errorf("promote = %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PUT", "/api/strategies/gld_gdx_pairs/stage", `{"stage":"real"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad stage = %d", w.Code)
	}
	if w := do(t, h, "PUT", "/api/strategies/nope/stage", `{"stage":"live"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown spec = %d", w.Code)
	}
}
