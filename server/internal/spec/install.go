package spec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ErrExists says a spec of that name is already installed. Replacing one is
// a deliberate act, not a side effect of importing.
var ErrExists = errors.New("a spec with that name is already installed")

// safeName is the schema's own name pattern, re-checked here because the
// name becomes a filename: nothing that could climb out of the specs
// directory may reach filepath.Join, whatever the schema is edited to say
// later.
var safeName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Install validates src and writes it to dir/<name>.yaml, returning the spec
// and the path written.
//
// It writes only a spec the runtime would actually run: an *Invalid or a
// *Refused comes back with nothing written, so importing cannot put a spec
// on disk that the next run will only refuse again. Replacing an installed
// spec needs replace, and the file is written whole and renamed into place.
//
// Installing is not a shortcut past provenance. The spec still carries its
// backtest windows, its out-of-sample Sharpe and its TTL, and is still judged
// against the same floor — this only means the file need not arrive by scp.
func Install(dir string, src []byte, now time.Time, replace bool) (*Spec, string, error) {
	s, err := Check(src, "uploaded spec", now)
	if err != nil {
		return nil, "", err
	}
	if !safeName.MatchString(s.Name) {
		return nil, "", fmt.Errorf("name %q is not a spec name", s.Name)
	}
	path := filepath.Join(dir, s.Name+".yaml")
	if !replace {
		if existing := installedPath(dir, s.Name); existing != "" {
			return nil, existing, ErrExists
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, src, 0o600); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, "", err
	}
	// A .yml of the same name would now shadow or duplicate the .yaml; the
	// import replaced it, so it goes.
	if replace {
		if old := filepath.Join(dir, s.Name+".yml"); old != path {
			os.Remove(old)
		}
	}
	return s, path, nil
}

// Remove deletes the named spec from dir. It is how a spec that should not
// be there — imported by mistake, or long expired — leaves without a
// terminal. A name that is not installed is an error, not a silent success:
// the operator asked for something that did not happen.
func Remove(dir, name string) error {
	if !safeName.MatchString(name) {
		return fmt.Errorf("name %q is not a spec name", name)
	}
	path := installedPath(dir, name)
	if path == "" {
		return fmt.Errorf("no spec named %q in %s", name, dir)
	}
	return os.Remove(path)
}

// installedPath is the file backing name in dir, or "" when there is none.
func installedPath(dir, name string) string {
	for _, ext := range []string{".yaml", ".yml"} {
		path := filepath.Join(dir, name+ext)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
