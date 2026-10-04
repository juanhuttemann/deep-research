package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helperVersionEnv makes the test binary act as a released deep-research
// whose --version prints the variable's value, so verifyVersion runs a real
// process on every platform without building one.
const helperVersionEnv = "DEEP_RESEARCH_UPDATE_HELPER_VERSION"

func TestMain(m *testing.M) {
	if out, ok := os.LookupEnv(helperVersionEnv); ok {
		fmt.Println(out)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestVerifyVersionReadsTheCandidatesVersion(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperVersionEnv, "deep-research version 0.6.0")
	if err := verifyVersion(self, "v0.6.0"); err != nil {
		t.Errorf("a binary reporting 0.6.0 failed verification for v0.6.0: %v", err)
	}
	if err := verifyVersion(self, "v0.7.0"); err == nil {
		t.Error("a binary reporting 0.6.0 passed verification for v0.7.0")
	}
	if err := verifyVersion(filepath.Join(t.TempDir(), "missing"), "v0.6.0"); err == nil {
		t.Error("a binary that does not run passed verification")
	}
}

// newFullReleaseServer is a repository whose latest release is tag, with the
// linux/amd64 archive holding body, and records how many archives it served.
func newFullReleaseServer(t *testing.T, tag, body string) (*httptest.Server, *int) {
	t.Helper()
	archive := tarGz(t, "deep-research", body)
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			w.Header().Set("Location", "/releases/tag/"+tag)
			w.WriteHeader(http.StatusFound)
		case path.Base(r.URL.Path) == checksumsName:
			fmt.Fprintf(w, "%x  deep-research_linux_amd64.tar.gz\n", sha256.Sum256(archive))
		default:
			downloads++
			_, _ = w.Write(archive)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &downloads
}

func updateWith(t *testing.T, srv *httptest.Server, current, version string, checkOnly bool) (Result, error) {
	t.Helper()
	c := Client{HTTP: srv.Client(), BaseURL: srv.URL, Verify: func(string, string) error { return nil }}
	exe := func() (string, error) { return current, nil }
	return c.Update(context.Background(), version, checkOnly, exe, Platform{OS: "linux", Arch: "amd64"})
}

func TestUpdateInstallsANewerRelease(t *testing.T) {
	srv, _ := newFullReleaseServer(t, "v0.6.0", "new")
	current := filepath.Join(t.TempDir(), "deep-research")
	writeFile(t, current, "old", 0o755)
	got, err := updateWith(t, srv, current, "0.5.0", false)
	if err != nil {
		t.Fatal(err)
	}
	want := Result{Current: "v0.5.0", Latest: "v0.6.0", State: UpgradeAvailable, Updated: true}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if b, _ := os.ReadFile(current); string(b) != "new" {
		t.Errorf("executable is %q, want the release", b)
	}
}

func TestUpdateCheckChangesNothing(t *testing.T) {
	srv, downloads := newFullReleaseServer(t, "v0.6.0", "new")
	got, err := updateWith(t, srv, "", "0.5.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != UpgradeAvailable || got.Updated || got.Latest != "v0.6.0" {
		t.Errorf("got %+v, want an available, uninstalled v0.6.0", got)
	}
	if *downloads != 0 {
		t.Errorf("--check downloaded %d archive(s)", *downloads)
	}
}

func TestUpdateNeverDowngrades(t *testing.T) {
	for version, state := range map[string]State{"0.6.0": UpToDate, "0.7.0": NewerInstalled} {
		srv, downloads := newFullReleaseServer(t, "v0.6.0", "new")
		got, err := updateWith(t, srv, "", version, false)
		if err != nil || got.State != state || got.Updated || *downloads != 0 {
			t.Errorf("%s: got %+v, %v, %d download(s); want %v and nothing installed",
				version, got, err, *downloads, state)
		}
	}
}

// The directory fsync comes after the swap. Failing the command there told
// the user an update had failed while the new binary was already in place.
func TestUpdateSurvivesAFailedDirectorySync(t *testing.T) {
	orig := syncDirectory
	syncDirectory = func(string) error { return errors.New("input/output error") }
	t.Cleanup(func() { syncDirectory = orig })
	srv, _ := newFullReleaseServer(t, "v0.6.0", "new")
	current := filepath.Join(t.TempDir(), "deep-research")
	writeFile(t, current, "old", 0o755)
	got, err := updateWith(t, srv, current, "0.5.0", false)
	if err != nil {
		t.Fatalf("a swap that happened was reported as failed: %v", err)
	}
	if !got.Updated || !strings.Contains(got.Warning, "input/output error") {
		t.Errorf("got %+v, want Updated with the sync error as a warning", got)
	}
}

func TestInstallSweepsOnlyStaleCandidates(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "deep-research")
	writeFile(t, current, "old", 0o755)
	stale := filepath.Join(dir, candidatePrefix+"1")
	fresh := filepath.Join(dir, candidatePrefix+"2")
	writeFile(t, stale, "killed mid-install", 0o755)
	writeFile(t, fresh, "another update, running now", 0o755)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(current, []byte("new"), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a candidate left by a killed update is still there: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a concurrent update's candidate was deleted: %v", err)
	}
}
