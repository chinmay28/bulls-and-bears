package research

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeUV is a shell script standing in for uv. `sync` builds the venv marker
// the runner looks for; `run` prints a study's verdict. The FAKE_* variables
// reach it through the environment the runner passes on.
const fakeUV = `#!/bin/sh
echo "uv $*"
case "$1" in
  sync)
    mkdir -p "$UV_PROJECT_ENVIRONMENT/bin" && : > "$UV_PROJECT_ENVIRONMENT/bin/python"
    exit "${FAKE_SYNC_EXIT:-0}" ;;
  run)
    echo "cache=$UV_CACHE_DIR"
    if [ -n "$FAKE_SLEEP" ]; then sleep "$FAKE_SLEEP"; fi
    echo "${FAKE_OUTCOME:-NOT PROMOTED (OOS Sharpe 0.26 < 1.0): wrote out/x.rejected.yaml}"
    exit "${FAKE_RUN_EXIT:-0}" ;;
esac
`

// tree lays down a research tree with a pyproject.toml and returns it.
func tree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"tt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newRunner(t *testing.T, withUV bool) *Runner {
	t.Helper()
	r := &Runner{Dir: tree(t), DataDir: t.TempDir(), SpecsDir: "/specs", BarsDir: "/bars"}
	if withUV {
		uv := filepath.Join(t.TempDir(), "uv")
		if err := os.WriteFile(uv, []byte(fakeUV), 0o755); err != nil {
			t.Fatal(err)
		}
		r.UV = uv
	} else {
		// Keep the machine's own uv, if any, out of the picture.
		t.Setenv("PATH", t.TempDir())
	}
	return r
}

// wait blocks until the job is no longer Running, or fails the test.
func wait(t *testing.T, r *Runner, id string) View {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		v, _, err := r.Get(id, 0)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != Running {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s still running", id)
	return View{}
}

func TestStudiesAreKnown(t *testing.T) {
	if len(Studies()) < 2 {
		t.Fatalf("studies = %v", Studies())
	}
	s, ok := Find("etf_gld_ratio")
	if !ok || s.Script != "scripts/etf_gld_ratio.py" || s.DefaultTrainTo == "" {
		t.Errorf("etf_gld_ratio = %+v, %v", s, ok)
	}
	if _, ok := Find("momentum"); ok {
		t.Error("found a study that does not exist")
	}
}

func TestEnvBeforeAndAfterSetup(t *testing.T) {
	r := newRunner(t, true)
	e := r.Env()
	if !e.Tree || e.UV != r.UV || e.Synced || e.Busy {
		t.Fatalf("env before = %+v", e)
	}
	v, err := r.Start(Setup, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	v = wait(t, r, v.ID)
	if v.Status != Succeeded || len(v.Steps) != 1 || v.Steps[0].Name != "sync" || v.Steps[0].Status != Succeeded {
		t.Fatalf("setup = %+v", v)
	}
	if e := r.Env(); !e.Synced {
		t.Errorf("env after = %+v, want synced", e)
	}
	_, text, _ := r.Get(v.ID, 0)
	for _, want := range []string{"$ " + r.UV + " sync --frozen", "uv sync --frozen", "done"} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(filepath.Join(r.DataDir, "research", "logs", v.ID+".log")); err != nil {
		t.Errorf("no log file: %v", err)
	}
}

func TestRunSyncsThenRunsTheStudyWithTheRuntimesDirectories(t *testing.T) {
	r := newRunner(t, true)
	v, err := r.Start(Run, "etf_gld_ratio", Options{TrainTo: "2019-12-31"})
	if err != nil {
		t.Fatal(err)
	}
	v = wait(t, r, v.ID)
	if v.Status != Succeeded {
		t.Fatalf("run = %+v", v)
	}
	if len(v.Steps) != 2 || v.Steps[0].Name != "sync" || v.Steps[1].Name != "study" {
		t.Fatalf("steps = %+v", v.Steps)
	}
	if v.Outcome == nil || v.Outcome.Promoted || !strings.HasPrefix(v.Outcome.Line, "NOT PROMOTED") {
		t.Errorf("outcome = %+v", v.Outcome)
	}
	_, text, _ := r.Get(v.ID, 0)
	root := filepath.Join(r.DataDir, "research")
	for _, want := range []string{
		"run --frozen python scripts/etf_gld_ratio.py",
		"--specs-dir /specs", "--bars-dir /bars",
		"--out-dir " + filepath.Join(root, "out"),
		"--golden " + filepath.Join(root, "golden", "etf_gld_ratio"),
		"--train-to 2019-12-31",
		"cache=" + filepath.Join(root, "cache"),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	if v.LogBytes != len(text) {
		t.Errorf("LogBytes = %d, log is %d bytes", v.LogBytes, len(text))
	}
	// Paging: from the end there is nothing more.
	if _, more, _ := r.Get(v.ID, v.LogBytes); more != "" {
		t.Errorf("log from the end = %q", more)
	}
}

func TestPromotedOutcome(t *testing.T) {
	r := newRunner(t, true)
	t.Setenv("FAKE_OUTCOME", "PROMOTED: wrote /specs/etf_gld_ratio.yaml")
	v, err := r.Start(Run, "gld_gdx_pairs", Options{})
	if err != nil {
		t.Fatal(err)
	}
	v = wait(t, r, v.ID)
	if v.Outcome == nil || !v.Outcome.Promoted {
		t.Errorf("outcome = %+v", v.Outcome)
	}
}

func TestAFailingStepFailsTheJobAndStops(t *testing.T) {
	r := newRunner(t, true)
	t.Setenv("FAKE_SYNC_EXIT", "3")
	v, err := r.Start(Run, "etf_gld_ratio", Options{})
	if err != nil {
		t.Fatal(err)
	}
	v = wait(t, r, v.ID)
	if v.Status != Failed || !strings.HasPrefix(v.Error, "sync:") {
		t.Fatalf("job = %+v", v)
	}
	if len(v.Steps) != 1 || v.Steps[0].Status != Failed {
		t.Errorf("steps = %+v, want the sync failed and no study step", v.Steps)
	}
}

func TestStartRefusesWhatItCanJudgeUpFront(t *testing.T) {
	r := newRunner(t, true)
	cases := []struct {
		name  string
		kind  Kind
		study string
		opts  Options
		want  string
	}{
		{"unknown study", Run, "momentum", Options{}, "no study named"},
		{"bad date", Run, "etf_gld_ratio", Options{TrainTo: "yesterday"}, "not a YYYY-MM-DD date"},
		{"date for a study without one", Run, "gld_gdx_pairs", Options{TrainTo: "2019-12-31"}, "takes no train-to"},
		{"train-to too recent", Run, "etf_gld_ratio", Options{TrainTo: time.Now().AddDate(0, -6, 0).Format("2006-01-02")}, "less than a year out of sample"},
		{"unknown kind", Kind("dance"), "", Options{}, "unknown job kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Start(tc.kind, tc.study, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("no tree", func(t *testing.T) {
		r := &Runner{Dir: t.TempDir(), DataDir: t.TempDir(), UV: r.UV}
		if _, err := r.Start(Setup, "", Options{}); err == nil || !strings.Contains(err.Error(), "no research tree") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestOneJobAtATimeAndCancel(t *testing.T) {
	r := newRunner(t, true)
	t.Setenv("FAKE_SLEEP", "30")
	v, err := r.Start(Run, "etf_gld_ratio", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(Setup, "", Options{}); !errors.Is(err, ErrBusy) {
		t.Errorf("second start: err = %v, want ErrBusy", err)
	}
	if !r.Env().Busy {
		t.Error("env not busy while a job runs")
	}
	// Wait for the study step to be under way, then pull the plug.
	deadline := time.Now().Add(10 * time.Second)
	for {
		cur, _ := r.Current()
		if len(cur.Steps) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("study step never started: %+v", cur)
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	if err := r.Cancel(v.ID); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("cancel took %s; the sleeping child was not killed with its group", time.Since(start))
	}
	got := wait(t, r, v.ID)
	if got.Status != Cancelled || got.Steps[1].Status != Cancelled {
		t.Errorf("after cancel = %+v", got)
	}
	if err := r.Cancel(v.ID); err != nil {
		t.Errorf("cancelling a finished job: %v", err)
	}
	// The line is free again.
	if _, err := r.Start(Setup, "", Options{}); err != nil {
		t.Errorf("start after cancel: %v", err)
	}
	if len(r.Recent()) != 1 || r.Recent()[0].ID != v.ID {
		t.Errorf("recent = %+v", r.Recent())
	}
}

func TestMissingUVWithoutAnInstallerFails(t *testing.T) {
	r := newRunner(t, false)
	v, err := r.Start(Setup, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	v = wait(t, r, v.ID)
	if v.Status != Failed || !strings.Contains(v.Error, "uv is not installed") {
		t.Errorf("job = %+v", v)
	}
	if e := r.Env(); e.UV != "" {
		t.Errorf("env.UV = %q, want none", e.UV)
	}
}

func TestLogsOutliveTheProcess(t *testing.T) {
	r := newRunner(t, true)
	v, err := r.Start(Setup, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, r, v.ID)
	// A fresh runner over the same data directory: the job is a file now.
	r2 := &Runner{Dir: r.Dir, DataDir: r.DataDir, UV: r.UV}
	if ids := r2.LogFiles(); len(ids) != 1 || ids[0] != v.ID {
		t.Fatalf("LogFiles = %v", ids)
	}
	got, text, err := r2.Get(v.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Unknown || !strings.Contains(text, "uv sync --frozen") || got.LogBytes != len(text) {
		t.Errorf("from disk = %+v, %q", got, text)
	}
	if _, _, err := r2.Get("../etc/passwd", 0); err == nil {
		t.Error("a path in an id was accepted")
	}
	if _, _, err := r2.Get("nope", 0); err == nil {
		t.Error("an unknown id was accepted")
	}
}

func TestLogSinkPagesAndCaps(t *testing.T) {
	l, err := newLogSink(filepath.Join(t.TempDir(), "x.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Line("one")
	l.Line("two")
	if l.Len() != 8 || l.Since(0) != "one\ntwo\n" || l.Since(4) != "two\n" || l.Since(99) != "" {
		t.Errorf("len %d, since0 %q, since4 %q", l.Len(), l.Since(0), l.Since(4))
	}
	big := strings.Repeat("x", memCap)
	l.Line(big)
	if l.Len() != 8+memCap+1 {
		t.Errorf("Len = %d", l.Len())
	}
	// The front fell out of memory; asking for it gives the oldest kept.
	if got := l.Since(0); len(got) != memCap || strings.HasPrefix(got, "one") {
		t.Errorf("Since(0) after the cap: %d bytes, starts %q", len(got), got[:4])
	}
	if l.Outcome() != nil {
		t.Error("outcome without a verdict line")
	}
	l.Line("PROMOTED: wrote x")
	if o := l.Outcome(); o == nil || !o.Promoted {
		t.Errorf("outcome = %+v", o)
	}
}
