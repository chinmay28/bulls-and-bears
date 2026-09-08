package stage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNothingIsLiveUntilPromoted(t *testing.T) {
	dir := t.TempDir()
	stages, err := Load(dir)
	if err != nil || len(stages) != 0 {
		t.Fatalf("Load on an empty directory = %v, %v", stages, err)
	}
	if Of(stages, "gld_gdx_pairs") != Paper {
		t.Error("an unlisted spec is not paper")
	}
	if err := Set(dir, "gld_gdx_pairs", Live); err != nil {
		t.Fatal(err)
	}
	stages, _ = Load(dir)
	if Of(stages, "gld_gdx_pairs") != Live || len(LiveNames(stages)) != 1 {
		t.Errorf("after promotion: %v", stages)
	}
	if err := Set(dir, "gld_gdx_pairs", Paper); err != nil {
		t.Fatal(err)
	}
	stages, _ = Load(dir)
	if len(stages) != 0 {
		t.Errorf("demotion left %v", stages)
	}
	if st, _ := os.Stat(File(dir)); st.Mode().Perm() != 0o600 {
		t.Errorf("stages file mode %v, want 0600", st.Mode().Perm())
	}
}

func TestParseAndBadInput(t *testing.T) {
	if st, err := Parse(" Live "); err != nil || st != Live {
		t.Errorf("Parse(Live) = %v, %v", st, err)
	}
	if _, err := Parse("real"); err == nil {
		t.Error("Parse accepted 'real'")
	}
	dir := t.TempDir()
	if err := Set(dir, "../x", Live); err == nil {
		t.Error("a path in a name was accepted")
	}
	if err := Set(dir, "x", Stage("real")); err == nil {
		t.Error("an unknown stage was accepted")
	}
}

func TestAnUnreadableFileIsAnErrorNotAnAllPaperGuess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stages.json"), []byte(`{"x": "real"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "real") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stages.json"), []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("garbage was accepted")
	}
}
