// Package research runs the Python studies from the app, so a spec can be
// earned without leaving the phone.
//
// It owns one thing: turning "run this study" into the commands the research
// README asks a person to type — find or install uv, sync the project's
// environment, run the script — with the output redirected into the
// runtime's own directories and every line of it kept. It does not decide
// anything about trading: a study promotes a spec only through the same
// spec.Install-equivalent path the script always used, and the runtime judges
// that spec exactly as it judges one that arrived by scp.
//
// Everything a job writes lands under <data>/research: the virtual
// environment, uv's cache, a downloaded uv if the machine has none, the
// rejected specs and goldens, and the logs. The research tree itself is only
// read, which is what lets this run inside a service whose filesystem is
// read-only everywhere but its data directory.
package research

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Study is one script the app can run.
type Study struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Script is the path of the script inside the research tree.
	Script string `json:"script"`
	// TrainTo says the script takes --train-to, and what the app offers by
	// default. Empty when the study has no such option.
	DefaultTrainTo string `json:"defaultTrainTo,omitempty"`
}

var studies = []Study{
	{
		Name:  "etf_gld_ratio",
		Title: "ETF/GLD ratio reversion",
		Description: "SPY, QQQ, VTI and XLK against GLD: hold an ETF while its price ratio to gold is " +
			"stretched below its own trailing mean, hold GLD otherwise. Sweeps on the training window, " +
			"judges once on the year or more after it, and promotes the spec only if the Sharpe clears 1.0.",
		Script:         "scripts/etf_gld_ratio.py",
		DefaultTrainTo: "2019-12-31",
	},
	{
		Name:  "gld_gdx_pairs",
		Title: "GLD/GDX pairs",
		Description: "Chan's gold against gold miners: the cointegrating hedge ratio from the training " +
			"window, then a z-score band on the spread.",
		Script: "scripts/gld_gdx.py",
	},
}

// Studies lists what the app can run, in a fixed order.
func Studies() []Study { return append([]Study(nil), studies...) }

// Find returns the named study.
func Find(name string) (Study, bool) {
	for _, s := range studies {
		if s.Name == name {
			return s, true
		}
	}
	return Study{}, false
}

// Status is where a job is.
type Status string

const (
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
	// Unknown is a job the log directory remembers but this process does not:
	// the server restarted while it ran, or since.
	Unknown Status = "unknown"
)

// Kind is what a job is for.
type Kind string

const (
	// Setup finds or installs uv and syncs the environment, and stops there.
	Setup Kind = "setup"
	// Run does Setup's work if it is needed, then runs a study.
	Run Kind = "run"
)

// Options are the choices a run offers.
type Options struct {
	// TrainTo is the last day of the training window, YYYY-MM-DD; empty
	// takes the script's default.
	TrainTo string `json:"trainTo,omitempty"`
}

// Step is one command of a job.
type Step struct {
	Name    string     `json:"name"`
	Command string     `json:"command"`
	Status  Status     `json:"status"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended,omitempty"`
}

// Outcome is what a study said about its spec, read off its last lines. The
// scripts print "PROMOTED: wrote <path>" or "NOT PROMOTED (<why>): wrote
// <path>"; that line is the contract this package reads.
type Outcome struct {
	Promoted bool   `json:"promoted"`
	Line     string `json:"line"`
}

// View is a job as the API shows it: a snapshot, safe to encode.
type View struct {
	ID      string     `json:"id"`
	Kind    Kind       `json:"kind"`
	Study   string     `json:"study,omitempty"`
	Options Options    `json:"options"`
	Status  Status     `json:"status"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended,omitempty"`
	Error   string     `json:"error,omitempty"`
	Steps   []Step     `json:"steps"`
	Outcome *Outcome   `json:"outcome,omitempty"`
	// LogBytes is how long the whole log is, so a client can ask for the
	// part it has not seen.
	LogBytes int `json:"logBytes"`
}

// Env is what the machine has, for the setup screen.
type Env struct {
	// Dir is the research tree; Tree says whether it is there at all.
	Dir  string `json:"dir"`
	Tree bool   `json:"tree"`
	// UV is the binary a job would use, empty when there is none yet.
	UV string `json:"uv"`
	// Synced says the environment has been built at least once.
	Synced bool `json:"synced"`
	// Venv is where the environment lives.
	Venv string `json:"venv"`
	// Busy says a job is running now.
	Busy bool `json:"busy"`
}

// MinTestDays is how far before today a study's training window must end,
// so its test window holds about a year of bars. The scripts enforce the
// same minimum in bars (MIN_TEST_BARS); this is the fast answer before a
// fetch is paid for.
const MinTestDays = 365

// ErrBusy is returned when a job is asked for while one runs: research is
// one-at-a-time, because two syncs of one environment is a mess and the
// machine is also trading.
var ErrBusy = errors.New("research: a job is already running")

// Runner runs jobs. One at a time; the log of each is kept on disk.
type Runner struct {
	// Dir is the research tree (the checkout's research/).
	Dir string
	// DataDir is the runtime's data directory; everything written goes
	// under <DataDir>/research.
	DataDir string
	// SpecsDir and BarsDir are where a promoted spec and the fetched bars
	// go: the runtime's own, so a run picks them up.
	SpecsDir, BarsDir string
	// UV names the uv binary to use. Empty means: the one on PATH, else the
	// one this package installed, else install one.
	UV string
	// Installer fetches uv when the machine has none. nil disables that step
	// and a job without uv fails saying so.
	Installer *Installer
	Log       *slog.Logger

	mu      sync.Mutex
	current *job
	recent  []*job
}

// job is a View plus what the runner needs to drive it.
type job struct {
	mu      sync.Mutex
	view    View
	log     *logSink
	cancel  context.CancelFunc
	done    chan struct{}
	cmdMu   sync.Mutex
	running *exec.Cmd
}

// root is where this package writes.
func (r *Runner) root() string { return filepath.Join(r.DataDir, "research") }

// venv is the environment uv builds and runs from.
func (r *Runner) venv() string { return filepath.Join(r.root(), "venv") }

func (r *Runner) logsDir() string { return filepath.Join(r.root(), "logs") }

// uvPath finds uv: the configured path, PATH, or the one installed here.
func (r *Runner) uvPath() string {
	if r.UV != "" {
		return r.UV
	}
	if p, err := exec.LookPath("uv"); err == nil {
		return p
	}
	own := filepath.Join(r.root(), "bin", "uv")
	if st, err := os.Stat(own); err == nil && !st.IsDir() {
		return own
	}
	return ""
}

// Env reports what the machine has.
func (r *Runner) Env() Env {
	_, treeErr := os.Stat(filepath.Join(r.Dir, "pyproject.toml"))
	_, venvErr := os.Stat(filepath.Join(r.venv(), "bin", "python"))
	r.mu.Lock()
	busy := r.current != nil && r.current.status() == Running
	r.mu.Unlock()
	return Env{
		Dir: r.Dir, Tree: treeErr == nil,
		UV: r.uvPath(), Synced: venvErr == nil, Venv: r.venv(), Busy: busy,
	}
}

// Start begins a job and returns at once; the job runs in the background.
// study is ignored for Setup. Errors are for what can be judged before
// anything runs: an unknown study, a bad option, a missing tree, a job
// already running.
func (r *Runner) Start(kind Kind, studyName string, opts Options) (View, error) {
	var study Study
	switch kind {
	case Setup:
	case Run:
		s, ok := Find(studyName)
		if !ok {
			return View{}, fmt.Errorf("research: no study named %q", studyName)
		}
		study = s
		if opts.TrainTo != "" {
			if study.DefaultTrainTo == "" {
				return View{}, fmt.Errorf("research: %s takes no train-to date", study.Name)
			}
			trainTo, err := time.Parse("2006-01-02", opts.TrainTo)
			if err != nil {
				return View{}, fmt.Errorf("research: train-to %q is not a YYYY-MM-DD date", opts.TrainTo)
			}
			if latest := time.Now().UTC().AddDate(0, 0, -MinTestDays); trainTo.After(latest) {
				return View{}, fmt.Errorf("research: train-to %s leaves less than a year out of sample; "+
					"it must be %s or earlier", opts.TrainTo, latest.Format("2006-01-02"))
			}
		}
	default:
		return View{}, fmt.Errorf("research: unknown job kind %q", kind)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "pyproject.toml")); err != nil {
		return View{}, fmt.Errorf("research: no research tree at %s (no pyproject.toml)", r.Dir)
	}
	if err := os.MkdirAll(r.logsDir(), 0o700); err != nil {
		return View{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil && r.current.status() == Running {
		return View{}, ErrBusy
	}
	id := time.Now().UTC().Format("20060102-150405")
	if r.current != nil && r.current.view.ID >= id {
		// Two jobs in one second: keep ids unique and ordered.
		id = r.current.view.ID + "b"
	}
	sink, err := newLogSink(filepath.Join(r.logsDir(), id+".log"))
	if err != nil {
		return View{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{
		view:   View{ID: id, Kind: kind, Study: study.Name, Options: opts, Status: Running, Started: time.Now().UTC()},
		log:    sink,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	if r.current != nil {
		r.recent = append([]*job{r.current}, r.recent...)
		if len(r.recent) > 20 {
			r.recent = r.recent[:20]
		}
	}
	r.current = j
	go r.drive(ctx, j, study)
	return j.snapshot(), nil
}

// Current is the job running or last run in this process, if any.
func (r *Runner) Current() (View, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return View{}, false
	}
	return r.current.snapshot(), true
}

// Recent lists the jobs this process ran, newest first, after the current one.
func (r *Runner) Recent() []View {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]View, 0, len(r.recent))
	for _, j := range r.recent {
		out = append(out, j.snapshot())
	}
	return out
}

// LogFiles lists the ids of every log on disk, newest first: the jobs of
// this process and of earlier ones.
func (r *Runner) LogFiles() []string {
	entries, err := os.ReadDir(r.logsDir())
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".log"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids
}

// Get returns a job and its log from byte offset from. A job this process
// does not remember is read back from its file with status Unknown.
func (r *Runner) Get(id string, from int) (View, string, error) {
	if j := r.find(id); j != nil {
		v := j.snapshot()
		text := j.log.Since(from)
		return v, text, nil
	}
	if strings.ContainsAny(id, "/\\") || id == "" {
		return View{}, "", fmt.Errorf("research: no job %q", id)
	}
	raw, err := os.ReadFile(filepath.Join(r.logsDir(), id+".log"))
	if err != nil {
		return View{}, "", fmt.Errorf("research: no job %q", id)
	}
	if from > len(raw) {
		from = len(raw)
	}
	if from < 0 {
		from = 0
	}
	started, _ := time.Parse("20060102-150405", strings.TrimSuffix(id, "b"))
	return View{ID: id, Status: Unknown, Started: started, LogBytes: len(raw), Steps: []Step{}}, string(raw[from:]), nil
}

// Cancel stops a running job. Cancelling a finished one is not an error.
func (r *Runner) Cancel(id string) error {
	j := r.find(id)
	if j == nil {
		return fmt.Errorf("research: no job %q", id)
	}
	if j.status() != Running {
		return nil
	}
	j.mu.Lock()
	j.view.Status = Cancelled
	j.mu.Unlock()
	j.cancel()
	j.killRunning()
	<-j.done
	return nil
}

func (r *Runner) find(id string) *job {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil && r.current.view.ID == id {
		return r.current
	}
	for _, j := range r.recent {
		if j.view.ID == id {
			return j
		}
	}
	return nil
}

// drive runs a job's steps in order and records how it ended.
func (r *Runner) drive(ctx context.Context, j *job, study Study) {
	defer close(j.done)
	defer j.log.Close()
	err := r.steps(ctx, j, study)
	now := time.Now().UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.view.Ended = &now
	switch {
	case j.view.Status == Cancelled:
		j.log.Line("cancelled")
	case err != nil:
		j.view.Status = Failed
		j.view.Error = err.Error()
		j.log.Line("failed: " + err.Error())
	default:
		j.view.Status = Succeeded
		j.log.Line("done")
	}
	j.view.LogBytes = j.log.Len()
	if r.Log != nil {
		r.Log.Info("research job ended", "id", j.view.ID, "kind", j.view.Kind, "study", j.view.Study, "status", j.view.Status, "err", err)
	}
}

// steps is the job: uv, sync, then the study.
func (r *Runner) steps(ctx context.Context, j *job, study Study) error {
	uv := r.uvPath()
	if uv == "" {
		if r.Installer == nil {
			return errors.New("uv is not installed and this server cannot install it; put uv on PATH")
		}
		dest := filepath.Join(r.root(), "bin", "uv")
		if err := j.step(ctx, "install uv", "download uv into "+dest, func(ctx context.Context) error {
			return r.Installer.Install(ctx, dest, j.log)
		}); err != nil {
			return err
		}
		uv = dest
	}
	env := r.childEnv(uv)
	if err := j.step(ctx, "sync", uv+" sync --frozen", func(ctx context.Context) error {
		return j.exec(ctx, r.Dir, env, uv, "sync", "--frozen")
	}); err != nil {
		return err
	}
	if j.view.Kind == Setup {
		return nil
	}
	args := []string{"run", "--frozen", "python", study.Script,
		"--specs-dir", r.SpecsDir,
		"--bars-dir", r.BarsDir,
		"--out-dir", filepath.Join(r.root(), "out"),
		"--golden", filepath.Join(r.root(), "golden", study.Name),
	}
	if j.view.Options.TrainTo != "" {
		args = append(args, "--train-to", j.view.Options.TrainTo)
	}
	err := j.step(ctx, "study", uv+" "+strings.Join(args, " "), func(ctx context.Context) error {
		return j.exec(ctx, r.Dir, env, uv, args...)
	})
	if out := j.log.Outcome(); out != nil {
		j.mu.Lock()
		j.view.Outcome = out
		j.mu.Unlock()
	}
	return err
}

// childEnv is the process environment a step runs with: the parent's, plus
// uv pointed at the data directory for everything it writes, and Python told
// not to litter a read-only tree with bytecode.
func (r *Runner) childEnv(uv string) []string {
	env := os.Environ()
	set := func(k, v string) {
		prefix := k + "="
		for i, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				env[i] = prefix + v
				return
			}
		}
		env = append(env, prefix+v)
	}
	set("UV_PROJECT_ENVIRONMENT", r.venv())
	set("UV_CACHE_DIR", filepath.Join(r.root(), "cache"))
	set("UV_PYTHON_INSTALL_DIR", filepath.Join(r.root(), "python"))
	set("UV_NO_PROGRESS", "1")
	set("PYTHONDONTWRITEBYTECODE", "1")
	set("PYTHONUNBUFFERED", "1")
	if home := os.Getenv("HOME"); home == "" {
		set("HOME", r.DataDir)
	}
	// The uv this package installed is not on PATH; `uv run` finds itself.
	set("PATH", filepath.Dir(uv)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return env
}

func (j *job) status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.view.Status
}

func (j *job) snapshot() View {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := j.view
	v.Steps = append([]Step(nil), j.view.Steps...)
	if v.Steps == nil {
		v.Steps = []Step{}
	}
	if j.view.Outcome != nil {
		o := *j.view.Outcome
		v.Outcome = &o
	}
	v.LogBytes = j.log.Len()
	return v
}

// step records one command around running it. A cancelled job's step ends
// Cancelled whatever the command returned.
func (j *job) step(ctx context.Context, name, command string, fn func(context.Context) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	j.mu.Lock()
	j.view.Steps = append(j.view.Steps, Step{Name: name, Command: command, Status: Running, Started: time.Now().UTC()})
	idx := len(j.view.Steps) - 1
	j.mu.Unlock()
	j.log.Line("$ " + command)

	err := fn(ctx)

	now := time.Now().UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	st := &j.view.Steps[idx]
	st.Ended = &now
	switch {
	case j.view.Status == Cancelled:
		st.Status = Cancelled
		if err == nil {
			err = context.Canceled
		}
	case err != nil:
		st.Status = Failed
	default:
		st.Status = Succeeded
	}
	if err != nil && j.view.Status == Cancelled {
		return context.Canceled
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// exec runs one command with its output going to the log.
func (j *job) exec(ctx context.Context, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = j.log
	cmd.Stderr = j.log
	cmd.Stdin = nil
	// Its own process group, so a cancel takes the children (uv starts
	// python) and not just uv.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	j.cmdMu.Lock()
	j.running = cmd
	err := cmd.Start()
	j.cmdMu.Unlock()
	if err != nil {
		return err
	}
	err = cmd.Wait()
	j.cmdMu.Lock()
	j.running = nil
	j.cmdMu.Unlock()
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (j *job) killRunning() {
	j.cmdMu.Lock()
	defer j.cmdMu.Unlock()
	if j.running != nil && j.running.Process != nil {
		_ = syscall.Kill(-j.running.Process.Pid, syscall.SIGKILL)
	}
}

// logSink is a job's log: appended to a file as it happens, and kept in
// memory so the app can read it back without touching the disk on every
// poll. The memory copy is capped; the file has everything.
type logSink struct {
	mu   sync.Mutex
	f    *os.File
	buf  bytes.Buffer
	drop int // bytes dropped from the front of buf
	tail []byte
}

const (
	memCap  = 2 << 20 // what the app can page through from memory
	tailCap = 64 << 10
)

func newLogSink(path string) (*logSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &logSink{f: f}, nil
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_, _ = l.f.Write(p)
	}
	l.buf.Write(p)
	if l.buf.Len() > memCap {
		over := l.buf.Len() - memCap
		l.buf.Next(over)
		l.drop += over
	}
	l.tail = append(l.tail, p...)
	if len(l.tail) > tailCap {
		l.tail = l.tail[len(l.tail)-tailCap:]
	}
	return len(p), nil
}

// Line writes one line of the runner's own: the command it is about to run
// (prefixed "$ ", which the app colours), or how the job ended.
func (l *logSink) Line(text string) {
	_, _ = io.WriteString(l, text+"\n")
}

// Len is how many bytes the whole log holds.
func (l *logSink) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.drop + l.buf.Len()
}

// Since returns the log from byte offset from. An offset older than what
// memory still holds returns from the oldest byte kept.
func (l *logSink) Since(from int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buf.Bytes()
	i := from - l.drop
	if i < 0 {
		i = 0
	}
	if i > len(b) {
		i = len(b)
	}
	return string(b[i:])
}

// Outcome reads the study's verdict off the tail of the log.
func (l *logSink) Outcome() *Outcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(string(l.tail), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "PROMOTED:"):
			return &Outcome{Promoted: true, Line: line}
		case strings.HasPrefix(line, "NOT PROMOTED"):
			return &Outcome{Promoted: false, Line: line}
		}
	}
	return nil
}

func (l *logSink) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}
