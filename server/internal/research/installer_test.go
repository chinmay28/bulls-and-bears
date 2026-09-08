package research

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release serves a fake uv release: the archive and its sha256, under the
// names the real one uses.
func release(t *testing.T, sumOverride string) (*httptest.Server, []byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho fake uv\n")
	for _, f := range []struct {
		name string
		body []byte
	}{
		{"uv-x86_64-unknown-linux-gnu/uvx", []byte("not this one")},
		{"uv-x86_64-unknown-linux-gnu/uv", body},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	sumLine := hex.EncodeToString(sum[:]) + "  uv-x86_64-unknown-linux-gnu.tar.gz\n"
	if sumOverride != "" {
		sumLine = sumOverride
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uv-x86_64-unknown-linux-gnu.tar.gz":
			w.Write(archive)
		case "/uv-x86_64-unknown-linux-gnu.tar.gz.sha256":
			w.Write([]byte(sumLine))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, body
}

func TestInstallerDownloadsVerifiesAndExtracts(t *testing.T) {
	srv, body := release(t, "")
	in := &Installer{BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64"}
	dest := filepath.Join(t.TempDir(), "bin", "uv")
	var log bytes.Buffer
	if err := in.Install(context.Background(), dest, &log); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("installed %q, want the uv entry", got)
	}
	if st, _ := os.Stat(dest); st.Mode()&0o100 == 0 {
		t.Errorf("uv is not executable: %v", st.Mode())
	}
	for _, want := range []string{"fetching", "sha256", "installed " + dest} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q: %s", want, log.String())
		}
	}
}

func TestInstallerRefusesABadChecksum(t *testing.T) {
	srv, _ := release(t, strings.Repeat("0", 64)+"  uv-x86_64-unknown-linux-gnu.tar.gz\n")
	in := &Installer{BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64"}
	dest := filepath.Join(t.TempDir(), "uv")
	err := in.Install(context.Background(), dest, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a file was written despite the bad checksum")
	}
}

func TestInstallerKnowsItsPlatforms(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "uv-x86_64-unknown-linux-gnu"},
		{"linux", "arm64", "uv-aarch64-unknown-linux-gnu"},
		{"darwin", "arm64", "uv-aarch64-apple-darwin"},
		{"windows", "amd64", ""},
		{"linux", "386", ""},
	}
	for _, tc := range cases {
		got, err := asset(tc.goos, tc.goarch)
		if (err == nil) != (tc.want != "") || got != tc.want {
			t.Errorf("asset(%s, %s) = %q, %v; want %q", tc.goos, tc.goarch, got, err, tc.want)
		}
	}
}

func TestInstallerReportsAMissingRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	in := &Installer{BaseURL: srv.URL, GOOS: "linux", GOARCH: "arm64"}
	err := in.Install(context.Background(), filepath.Join(t.TempDir(), "uv"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}
