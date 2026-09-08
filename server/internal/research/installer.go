package research

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultUVReleases is where uv's release archives live. The installer
// fetches the archive and its published sha256 from the same place, so this
// proves the download arrived whole; trust in the publisher is the same the
// installer script extends to Go and Node.
const DefaultUVReleases = "https://github.com/astral-sh/uv/releases/latest/download"

// Installer downloads uv for machines that have none.
type Installer struct {
	// BaseURL is the release directory; empty means DefaultUVReleases.
	BaseURL string
	// Client is the HTTP client; nil means http.DefaultClient.
	Client *http.Client
	// GOOS and GOARCH pick the archive; empty means this binary's.
	GOOS, GOARCH string
}

// asset names uv's archive for a platform, or says it has none.
func asset(goos, goarch string) (string, error) {
	var arch, target string
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("research: no uv build for %s/%s", goos, goarch)
	}
	switch goos {
	case "linux":
		target = "unknown-linux-gnu"
	case "darwin":
		target = "apple-darwin"
	default:
		return "", fmt.Errorf("research: no uv build for %s/%s", goos, goarch)
	}
	return "uv-" + arch + "-" + target, nil
}

// Install downloads uv to dest, verifying it against the published sha256,
// and writes what it did to log. dest is written only once the archive has
// checked out; a failure leaves whatever was there before.
func (in *Installer) Install(ctx context.Context, dest string, log io.Writer) error {
	goos, goarch := in.GOOS, in.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	name, err := asset(goos, goarch)
	if err != nil {
		return err
	}
	base := in.BaseURL
	if base == "" {
		base = DefaultUVReleases
	}
	client := in.Client
	if client == nil {
		client = http.DefaultClient
	}

	fmt.Fprintf(log, "fetching %s/%s.tar.gz\n", base, name)
	sum, err := fetch(ctx, client, base+"/"+name+".tar.gz.sha256")
	if err != nil {
		return fmt.Errorf("checksum: %w", err)
	}
	want := strings.Fields(string(sum))
	if len(want) == 0 || len(want[0]) != 64 {
		return fmt.Errorf("checksum file did not hold a sha256: %q", strings.TrimSpace(string(sum)))
	}
	archive, err := fetch(ctx, client, base+"/"+name+".tar.gz")
	if err != nil {
		return err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != strings.ToLower(want[0]) {
		return errors.New("the uv archive did not match its published sha256")
	}
	fmt.Fprintf(log, "sha256 %s ok (%d bytes)\n", want[0][:12], len(archive))

	bin, err := extractUV(archive)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fmt.Fprintf(log, "installed %s\n", dest)
	return nil
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	// uv is a few tens of megabytes; anything past this is not it.
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// extractUV finds the uv binary in the release archive: the entry named uv
// in any directory.
func extractUV(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("uv archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("uv archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "uv" {
			continue
		}
		return io.ReadAll(tr)
	}
	return nil, errors.New("uv archive held no uv binary")
}
