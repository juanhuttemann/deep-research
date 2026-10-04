package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestReleased(t *testing.T) {
	for v, want := range map[string]bool{
		"0.5.0":                   true,
		"v0.5.0":                  true,
		"dev":                     false,
		"":                        false,
		"v0.5.0-3-gc8b3b08":       false,
		"v0.5.0-3-gc8b3b08-dirty": false,
		"c8b3b08":                 false,
		"0.5.0+local":             false,
	} {
		if got := Released(v); got != want {
			t.Errorf("Released(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		name, current, latest string
		want                  State
		wantErr               bool
	}{
		{"equal", "0.5.0", "v0.5.0", UpToDate, false},
		{"upgrade", "v0.5.0", "v0.6.0", UpgradeAvailable, false},
		{"numeric not lexical", "0.9.0", "v0.10.0", UpgradeAvailable, false},
		{"no downgrade", "0.10.0", "v0.9.0", NewerInstalled, false},
		{"development build", "dev", "v0.6.0", 0, true},
		{"bad latest", "0.5.0", "latest", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CompareVersions(tc.current, tc.latest)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAssetNameMatchesGoreleaser(t *testing.T) {
	for p, want := range map[Platform]string{
		{OS: "linux", Arch: "arm64"}:   "deep-research_linux_arm64.tar.gz",
		{OS: "darwin", Arch: "amd64"}:  "deep-research_darwin_amd64.tar.gz",
		{OS: "windows", Arch: "amd64"}: "deep-research_windows_amd64.zip",
	} {
		got, err := AssetName(p)
		if err != nil || got != want {
			t.Errorf("AssetName(%v) = %q, %v; want %q", p, got, err, want)
		}
	}
	if _, err := AssetName(Platform{OS: "freebsd", Arch: "amd64"}); err == nil {
		t.Error("freebsd has no release, want an error")
	}
}

func TestParseChecksumRequiresOneExactValidEntry(t *testing.T) {
	const asset = "deep-research_linux_amd64.tar.gz"
	digest := sha256.Sum256([]byte("archive"))
	got, err := parseChecksum(strings.NewReader(fmt.Sprintf("%x  %s\n", digest, asset)), asset)
	if err != nil || got != digest {
		t.Fatalf("got %x, %v; want %x", got, err, digest)
	}
	for name, body := range map[string]string{
		"missing":   fmt.Sprintf("%x  other.tar.gz\n", digest),
		"prefix":    fmt.Sprintf("%x  %s.sig\n", digest, asset),
		"duplicate": fmt.Sprintf("%x  %s\n%x  %s\n", digest, asset, digest, asset),
		"malformed": "not-a-digest  " + asset + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseChecksum(strings.NewReader(body), asset); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestExtractArchiveRejectsUnsafeEntries(t *testing.T) {
	const asset = "deep-research_linux_amd64.tar.gz"
	got, err := extractArchive(tarGz(t, "deep-research", "binary"), asset, 1024)
	if err != nil || string(got) != "binary" {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, archive := range map[string][]byte{
		"nested": tarGz(t, "bin/deep-research", "binary"),
		"extra":  tarGz(t, "deep-research", "binary", "README", "x"),
		"large":  tarGz(t, "deep-research", strings.Repeat("x", 1025)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := extractArchive(archive, asset, 1024); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// tar writers may open an archive with a PAX global header; it is metadata,
// not an extra entry.
func TestExtractArchiveSkipsAPAXGlobalHeader(t *testing.T) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": "release"}}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "deep-research", Mode: 0o755, Size: 6, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := extractArchive(out.Bytes(), "deep-research_linux_amd64.tar.gz", 1024)
	if err != nil || string(got) != "binary" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExtractWindowsArchive(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.Create("deep-research.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := extractArchive(archive.Bytes(), "deep-research_windows_amd64.zip", 1024)
	if err != nil || string(got) != "binary" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// newReleaseServer serves one release the way github.com does: the checksum
// file and the archive under /releases/download/<tag>/.
func newReleaseServer(t *testing.T, archive []byte, sums string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if path.Base(r.URL.Path) == checksumsName {
			fmt.Fprint(w, sums)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestDownloadVerifiesTheArchiveOfTheRequestedTag(t *testing.T) {
	archive := tarGz(t, "deep-research", "binary")
	srv, paths := newReleaseServer(t, archive,
		fmt.Sprintf("%x  deep-research_linux_amd64.tar.gz\n", sha256.Sum256(archive)))
	c := Client{HTTP: srv.Client(), BaseURL: srv.URL}
	got, err := c.Download(context.Background(), "0.6.0", Platform{OS: "linux", Arch: "amd64"})
	if err != nil || string(got) != "binary" {
		t.Fatalf("got %q, %v", got, err)
	}
	want := []string{
		"/releases/download/v0.6.0/deep-research_checksums.txt",
		"/releases/download/v0.6.0/deep-research_linux_amd64.tar.gz",
	}
	if !slices.Equal(*paths, want) {
		t.Errorf("fetched %v, want %v", *paths, want)
	}
}

func TestDownloadRefusesAChecksumMismatch(t *testing.T) {
	archive := tarGz(t, "deep-research", "binary")
	srv, _ := newReleaseServer(t, archive,
		fmt.Sprintf("%x  deep-research_linux_amd64.tar.gz\n", sha256.Sum256([]byte("other"))))
	c := Client{HTTP: srv.Client(), BaseURL: srv.URL}
	_, err := c.Download(context.Background(), "v0.6.0", Platform{OS: "linux", Arch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want a checksum mismatch, got %v", err)
	}
}

func TestDownloadFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"http error", http.StatusNotFound, ""},
		{"oversized checksum", http.StatusOK, strings.Repeat("x", 1025)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			c := Client{HTTP: srv.Client(), BaseURL: srv.URL, MaxChecksumBytes: 1024}
			if _, err := c.Download(context.Background(), "v0.6.0", Platform{OS: "linux", Arch: "amd64"}); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestLatestReadsTheRedirectTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			t.Errorf("requested %s", r.URL.Path)
		}
		w.Header().Set("Location", "/juanhuttemann/deep-research/releases/tag/v0.6.0")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	tag, err := Client{HTTP: srv.Client(), BaseURL: srv.URL + "/"}.Latest(context.Background())
	if err != nil || tag != "v0.6.0" {
		t.Fatalf("got %q, %v; want v0.6.0", tag, err)
	}
}

// A repository with no release answers /releases/latest with 404, not a
// redirect; that must be an error, not an empty tag compared as a version.
func TestLatestWithoutARedirectIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	if tag, err := (Client{HTTP: srv.Client(), BaseURL: srv.URL}).Latest(context.Background()); err == nil {
		t.Fatalf("want an error, got tag %q", tag)
	}
}

func TestInstallVerifiesBeforeReplacing(t *testing.T) {
	current := filepath.Join(t.TempDir(), "deep-research")
	writeFile(t, current, "old", 0o755)
	broken := errors.New("exec format error")
	_, err := Install(current, []byte("new"), func(candidate string) error {
		if b, _ := os.ReadFile(candidate); string(b) != "new" {
			t.Errorf("verify saw %q, want the downloaded binary", b)
		}
		return broken
	})
	if !errors.Is(err, broken) {
		t.Fatalf("got %v, want the verify error", err)
	}
	if b, _ := os.ReadFile(current); string(b) != "old" {
		t.Errorf("executable is %q; a binary that failed verification replaced it", b)
	}
	assertOnlyFile(t, current)
}

func TestInstallReplacesAndKeepsTheMode(t *testing.T) {
	current := filepath.Join(t.TempDir(), "deep-research")
	writeFile(t, current, "old", 0o751)
	leftover, err := Install(current, []byte("new"), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Only Windows can leave one: it locks a running executable. The test's
	// file is not running, so the removal normally succeeds there too.
	if leftover != "" && runtime.GOOS != "windows" {
		t.Errorf("left %s behind; only Windows makes a backup", leftover)
	}
	if b, _ := os.ReadFile(current); string(b) != "new" {
		t.Errorf("executable is %q, want the new binary", b)
	}
	if runtime.GOOS != "windows" { // no Unix permission bits there
		if info, err := os.Stat(current); err != nil || info.Mode().Perm() != 0o751 {
			t.Errorf("mode %v, %v; want 0751", info.Mode().Perm(), err)
		}
	}
}

// A copy the user kept beside the binary matches <exe>.old-*, but only a
// Windows update makes such files; deleting it on Unix loses the user's file.
func TestInstallKeepsUserRollbackCopiesOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows sweeps its own .old-* backups")
	}
	current := filepath.Join(t.TempDir(), "deep-research")
	writeFile(t, current, "old", 0o755)
	manual := current + ".old-manual"
	writeFile(t, manual, "keep me", 0o644)
	if _, err := Install(current, []byte("new"), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(manual); err != nil || string(b) != "keep me" {
		t.Errorf("rollback copy is %q, %v; it is not the updater's to delete", b, err)
	}
}

func writeFile(t *testing.T, name, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// assertOnlyFile checks Install cleaned up its candidate.
func assertOnlyFile(t *testing.T, current string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(current))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(current) {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want only %s", names, filepath.Base(current))
	}
}

// tarGz builds a release archive from name, body pairs, in order.
func tarGz(t *testing.T, pairs ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for i := 0; i < len(pairs); i += 2 {
		name, body := pairs[i], pairs[i+1]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
