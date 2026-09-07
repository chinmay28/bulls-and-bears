package spec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesASpecTheRuntimeWouldRun(t *testing.T) {
	registerPairs(t)
	dir := t.TempDir()
	s, path, err := Install(dir, []byte(example(t)), now, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "gld_gdx_pairs" {
		t.Errorf("name = %q", s.Name)
	}
	if want := filepath.Join(dir, "gld_gdx_pairs.yaml"); path != want {
		t.Errorf("path = %q, want %q: the file is named for the spec", path, want)
	}
	// What lands on disk is what the loader will read back, byte for byte.
	back, err := Load(path, now)
	if err != nil {
		t.Fatalf("the installed spec does not load: %v", err)
	}
	if back.Name != s.Name || back.Provenance.OOSSharpe != s.Provenance.OOSSharpe {
		t.Errorf("reloaded = %+v, want the spec that was installed", back)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temp file was left behind")
	}
}

func TestInstallWritesNothingItWouldRefuse(t *testing.T) {
	registerPairs(t)
	tests := []struct {
		name    string
		src     string
		wantErr any // *Invalid or *Refused
	}{
		{name: "not a spec at all", src: "name: x\n", wantErr: &Invalid{}},
		{name: "not yaml", src: "\t\tnope: [", wantErr: &Invalid{}},
		{name: "empty", src: "", wantErr: &Invalid{}},
		{
			name:    "sharpe under the floor",
			src:     strings.Replace(example(t), "oos_sharpe: 1.32", "oos_sharpe: 0.4", 1),
			wantErr: &Refused{},
		},
		{
			name:    "expired",
			src:     strings.Replace(example(t), "ttl_days: 90", "ttl_days: 1", 1),
			wantErr: &Refused{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			_, _, err := Install(dir, []byte(tt.src), now, false)
			if err == nil {
				t.Fatal("want an error: importing must not install what a run would refuse")
			}
			switch tt.wantErr.(type) {
			case *Invalid:
				var e *Invalid
				if !errors.As(err, &e) {
					t.Errorf("error = %T (%v), want *Invalid", err, err)
				}
			case *Refused:
				var e *Refused
				if !errors.As(err, &e) {
					t.Errorf("error = %T (%v), want *Refused", err, err)
				}
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Errorf("directory holds %d files, want nothing written", len(entries))
			}
		})
	}
}

func TestInstallDoesNotReplaceWithoutBeingAsked(t *testing.T) {
	registerPairs(t)
	dir := t.TempDir()
	src := example(t)
	if _, _, err := Install(dir, []byte(src), now, false); err != nil {
		t.Fatal(err)
	}

	edited := strings.Replace(src, "oos_sharpe: 1.32", "oos_sharpe: 1.90", 1)
	_, path, err := Install(dir, []byte(edited), now, false)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("error = %v, want ErrExists", err)
	}
	if path == "" {
		t.Error("want the path of the spec already installed, so the phone can offer to replace it")
	}
	back, err := Load(filepath.Join(dir, "gld_gdx_pairs.yaml"), now)
	if err != nil {
		t.Fatal(err)
	}
	if back.Provenance.OOSSharpe != 1.32 {
		t.Errorf("sharpe = %v, want the original 1.32 untouched", back.Provenance.OOSSharpe)
	}

	if _, _, err := Install(dir, []byte(edited), now, true); err != nil {
		t.Fatalf("replace = true must overwrite: %v", err)
	}
	back, err = Load(filepath.Join(dir, "gld_gdx_pairs.yaml"), now)
	if err != nil {
		t.Fatal(err)
	}
	if back.Provenance.OOSSharpe != 1.90 {
		t.Errorf("sharpe = %v, want the replacement's 1.90", back.Provenance.OOSSharpe)
	}
}

func TestInstallReplacesAYmlOfTheSameName(t *testing.T) {
	registerPairs(t)
	dir := t.TempDir()
	yml := filepath.Join(dir, "gld_gdx_pairs.yml")
	if err := os.WriteFile(yml, []byte(example(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(dir, []byte(example(t)), now, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(yml); err == nil {
		t.Error("the .yml was left behind: the directory now holds the same spec twice")
	}
}

func TestInstallCreatesTheDirectory(t *testing.T) {
	registerPairs(t)
	dir := filepath.Join(t.TempDir(), "specs")
	if _, _, err := Install(dir, []byte(example(t)), now, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gld_gdx_pairs.yaml")); err != nil {
		t.Error(err)
	}
}

func TestRemove(t *testing.T) {
	registerPairs(t)
	dir := t.TempDir()
	if _, _, err := Install(dir, []byte(example(t)), now, false); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "gld_gdx_pairs"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gld_gdx_pairs.yaml")); err == nil {
		t.Error("the file is still there")
	}
	if err := Remove(dir, "gld_gdx_pairs"); err == nil {
		t.Error("removing what is not installed must say so, not succeed quietly")
	}
}

// TestNamesCannotEscapeTheSpecsDirectory pins the reason the name is
// re-checked here: it becomes a path.
func TestNamesCannotEscapeTheSpecsDirectory(t *testing.T) {
	registerPairs(t)
	for _, name := range []string{"../escape", "a/b", "", ".", "..", "Upper", "with space", "/abs"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := Remove(dir, name); err == nil {
				t.Errorf("Remove(%q) succeeded", name)
			}
			src := strings.Replace(example(t), "name: gld_gdx_pairs", "name: "+name, 1)
			if _, _, err := Install(dir, []byte(src), now, true); err == nil {
				t.Errorf("Install with name %q succeeded", name)
			}
			// Nothing may appear anywhere under the temp root either.
			var found []string
			_ = filepath.Walk(filepath.Dir(dir), func(p string, info os.FileInfo, err error) error {
				if err == nil && info != nil && !info.IsDir() {
					found = append(found, p)
				}
				return nil
			})
			if len(found) != 0 {
				t.Errorf("files written: %v", found)
			}
		})
	}
}
