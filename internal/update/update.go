// Package update replaces the running binary with the latest GitHub release,
// verified against the release's checksum file the same way the install
// scripts verify it.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	defaultBaseURL = "https://github.com/juanhuttemann/deep-research"
	binaryName     = "deep-research"
	checksumsName  = binaryName + "_checksums.txt"
	// Caps on what a download may be, so a broken or hostile response cannot
	// fill memory. The archives are ~10MB and the binary ~30MB today.
	maxArchive  = 128 << 20
	maxChecksum = 1 << 20
	maxBinary   = 256 << 20
)

// State is how the installed version compares with the latest release.
type State int

const (
	UpToDate State = iota + 1
	UpgradeAvailable
	NewerInstalled
)

// Result is what Run found and did.
type Result struct {
	Current string
	Latest  string
	State   State
	Updated bool
	// LeftoverBackup is a backup of the previous executable that could not be
	// deleted yet. The update succeeded; the next update sweeps the file.
	LeftoverBackup string
	// Warning is a problem after the swap, which the update survived.
	Warning string
}

// Platform names a release target.
type Platform struct {
	OS   string
	Arch string
}

// Client fetches and installs releases. Zero fields fall back to the real
// repository, the default limits and running the candidate's --version;
// tests point BaseURL at an httptest server and replace Verify, since the
// downloaded bytes there are not a program.
type Client struct {
	HTTP             *http.Client
	BaseURL          string
	MaxArchiveBytes  int64
	MaxChecksumBytes int64
	MaxBinaryBytes   int64
	Verify           func(candidate, tag string) error
}

// Released reports whether version is an exact release tag. `go build`
// stamps "dev" and `make build` stamps `git describe`, e.g.
// v0.5.0-3-gc8b3b08-dirty: semver reads that as a pre-release of v0.5.0, so
// "updating" it would silently replace a newer source build with an older
// release.
func Released(version string) bool {
	v, err := canonicalVersion(version)
	return err == nil && semver.Prerelease(v) == "" && semver.Build(v) == ""
}

// CompareVersions compares two versions, with or without their leading v.
func CompareVersions(current, latest string) (State, error) {
	cur, err := canonicalVersion(current)
	if err != nil {
		return 0, fmt.Errorf("current version: %w", err)
	}
	newest, err := canonicalVersion(latest)
	if err != nil {
		return 0, fmt.Errorf("latest version: %w", err)
	}
	switch semver.Compare(cur, newest) {
	case -1:
		return UpgradeAvailable, nil
	case 0:
		return UpToDate, nil
	default:
		return NewerInstalled, nil
	}
}

func canonicalVersion(v string) (string, error) {
	v = "v" + strings.TrimPrefix(strings.TrimSpace(v), "v")
	if !semver.IsValid(v) {
		return "", fmt.Errorf("invalid semantic version %q", strings.TrimPrefix(v, "v"))
	}
	return v, nil
}

// AssetName is the release archive for p, as .goreleaser.yaml names it.
func AssetName(p Platform) (string, error) {
	if (p.OS != "linux" && p.OS != "darwin" && p.OS != "windows") ||
		(p.Arch != "amd64" && p.Arch != "arm64") {
		return "", fmt.Errorf("no release is built for %s/%s", p.OS, p.Arch)
	}
	name := binaryName + "_" + p.OS + "_" + p.Arch
	if p.OS == "windows" {
		return name + ".zip", nil
	}
	return name + ".tar.gz", nil
}

// Run checks the latest release and, unless checkOnly, installs it over the
// running executable when it is newer.
func Run(ctx context.Context, currentVersion string, checkOnly bool) (Result, error) {
	return Client{}.Update(ctx, currentVersion, checkOnly, resolvedExecutable,
		Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
}

// Update is Run with the executable and platform given. exe is a function so
// a check never has to locate the binary it will not touch.
func (c Client) Update(ctx context.Context, currentVersion string, checkOnly bool, exe func() (string, error), p Platform) (Result, error) {
	tag, err := c.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	state, err := CompareVersions(currentVersion, tag)
	if err != nil {
		return Result{}, err
	}
	result := Result{Current: "v" + strings.TrimPrefix(currentVersion, "v"), Latest: tag, State: state}
	if state != UpgradeAvailable || checkOnly {
		return result, nil
	}
	current, err := exe()
	if err != nil {
		return Result{}, err
	}
	binary, err := c.Download(ctx, tag, p)
	if err != nil {
		return Result{}, err
	}
	verify := c.Verify
	if verify == nil {
		verify = verifyVersion
	}
	leftover, err := Install(current, binary, func(candidate string) error { return verify(candidate, tag) })
	if errors.Is(err, errNotSynced) {
		result.Warning, err = err.Error(), nil
	}
	if err != nil {
		return Result{}, err
	}
	result.Updated = true
	result.LeftoverBackup = leftover
	return result, nil
}

// resolvedExecutable follows symlinks so a binary linked into PATH is
// replaced where it lives, not by a regular file in place of the link.
func resolvedExecutable() (string, error) {
	current, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	return current, nil
}

// Latest reads the newest release's tag from the redirect of
// /releases/latest rather than the REST API, whose unauthenticated rate limit
// (60 an hour per IP) shared CI runners and offices exhaust.
func (c Client) Latest(ctx context.Context) (tag string, err error) {
	// A copy, not the caller's client: the redirect is the answer here, and
	// Download must still follow GitHub's redirect to its asset storage.
	noFollow := *c.httpClient()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.baseURL()+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	location := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode >= 400 || location == "" {
		return "", fmt.Errorf("resolve latest release: expected a redirect, got %s", resp.Status)
	}
	u, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("resolve latest release redirect: %w", err)
	}
	return canonicalVersion(path.Base(u.Path))
}

// Download fetches tag's archive for p, checks it against the release's
// checksum file and returns the binary inside it.
func (c Client) Download(ctx context.Context, tag string, p Platform) ([]byte, error) {
	tag, err := canonicalVersion(tag)
	if err != nil {
		return nil, err
	}
	asset, err := AssetName(p)
	if err != nil {
		return nil, err
	}
	base := c.baseURL() + "/releases/download/" + tag + "/"
	sums, err := c.get(ctx, base+checksumsName, limit(c.MaxChecksumBytes, maxChecksum))
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	want, err := parseChecksum(bytes.NewReader(sums), asset)
	if err != nil {
		return nil, err
	}
	archive, err := c.get(ctx, base+asset, limit(c.MaxArchiveBytes, maxArchive))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	if sha256.Sum256(archive) != want {
		return nil, fmt.Errorf("checksum mismatch for %s", asset)
	}
	return extractArchive(archive, asset, limit(c.MaxBinaryBytes, maxBinary))
}

func (c Client) get(ctx context.Context, rawURL string, max int64) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return readLimited(resp.Body, max)
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("download exceeds %d bytes", max)
	}
	return b, nil
}

// parseChecksum wants exactly one line for asset, like install.sh: a missing
// or duplicated entry is a release not safe to install.
func parseChecksum(r io.Reader, asset string) ([sha256.Size]byte, error) {
	var found [sha256.Size]byte
	matches := 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return found, fmt.Errorf("invalid checksum for %s", asset)
		}
		copy(found[:], digest)
		matches++
	}
	if err := scanner.Err(); err != nil {
		return found, err
	}
	if matches != 1 {
		return found, fmt.Errorf("expected exactly one checksum for %s, got %d", asset, matches)
	}
	return found, nil
}

// extractArchive returns the one binary a release archive holds. The
// archives ship the binary alone (.goreleaser.yaml's files: none*), so
// anything else in one means it is not what the release builds.
func extractArchive(archive []byte, asset string, max int64) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		return extractZip(archive, max)
	}
	return extractTarGz(archive, max)
}

func extractZip(archive []byte, max int64) (binary []byte, err error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != binaryName+".exe" || !zr.File[0].Mode().IsRegular() {
		return nil, fmt.Errorf("archive must contain only the %s.exe binary", binaryName)
	}
	f, err := zr.File[0].Open()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return readLimited(f, max)
}

func extractTarGz(archive []byte, max int64) (binary []byte, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	tr := tar.NewReader(gz)
	header, err := nextEntry(tr)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if header.Name != binaryName || header.Typeflag != tar.TypeReg || header.Size > max {
		return nil, fmt.Errorf("archive must contain only the %s binary", binaryName)
	}
	binary, err = readLimited(tr, max)
	if err != nil {
		return nil, err
	}
	if _, err := nextEntry(tr); !errors.Is(err, io.EOF) {
		return nil, errors.New("archive contains unexpected entries")
	}
	return binary, nil
}

// nextEntry skips PAX global headers: metadata for the whole archive that
// tar writers may emit, not a file.
func nextEntry(tr *tar.Reader) (*tar.Header, error) {
	for {
		h, err := tr.Next()
		if err != nil || h.Typeflag != tar.TypeXGlobalHeader {
			return h, err
		}
	}
}

// errNotSynced marks an Install whose swap is done but whose directory fsync
// failed: the new binary is in place, only a crash right now could undo it.
var errNotSynced = errors.New("the new binary is in place, but its directory could not be synced")

// syncDirectory is syncDir, replaced by the test of a failing sync.
var syncDirectory = syncDir

// candidatePrefix names Install's temporary files beside the executable.
const candidatePrefix = "." + binaryName + "-update-"

// Install writes binary beside current, runs verify on it, and only then
// swaps it in, so a binary that cannot run on this machine never replaces
// one that can. It returns any backup left behind (see replaceExecutable).
func Install(current string, binary []byte, verify func(string) error) (leftover string, err error) {
	sweepBackups(current)
	sweepCandidates(filepath.Dir(current))
	info, err := os.Stat(current)
	if err != nil {
		return "", fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable is not a regular file: %s", current)
	}
	// Beside the executable, not in the temp dir: a rename is only atomic
	// within one filesystem.
	pattern := candidatePrefix + "*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	f, err := os.CreateTemp(filepath.Dir(current), pattern)
	if err != nil {
		return "", fmt.Errorf("create update beside executable: %w", err)
	}
	candidate := f.Name()
	defer func() {
		if rmErr := os.Remove(candidate); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
	}()
	if err := writeCandidate(f, binary, info.Mode().Perm()); err != nil {
		return "", err
	}
	if err := verify(candidate); err != nil {
		return "", fmt.Errorf("verify downloaded binary: %w", err)
	}
	leftover, err = replaceExecutable(current, candidate)
	if err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(current)); err != nil {
		return leftover, fmt.Errorf("%w: %w", errNotSynced, err)
	}
	return leftover, nil
}

// sweepCandidates removes candidates an earlier Install left when it was
// killed between writing and swapping. The name is the updater's own, so
// unlike <exe>.old-* nothing of the user's can match it. The age floor keeps
// two updates run at once from deleting each other's candidate.
func sweepCandidates(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), candidatePrefix) {
			continue
		}
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func writeCandidate(f *os.File, binary []byte, perm os.FileMode) error {
	if err := f.Chmod(perm); err != nil {
		return errors.Join(err, f.Close())
	}
	if _, err := f.Write(binary); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// verifyVersion runs the candidate's --version: it proves the binary starts
// on this OS and architecture and is the release that was asked for. It
// reads the last word of cobra's "deep-research version 0.6.0", which
// TestVersionOutputEndsWithTheVersion in internal/cli pins.
func verifyVersion(candidate, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, candidate, "--version").Output()
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || "v"+strings.TrimPrefix(fields[len(fields)-1], "v") != want {
		return fmt.Errorf("expected %s, got %q", want, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (c Client) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return defaultBaseURL
}

func limit(set, fallback int64) int64 {
	if set > 0 {
		return set
	}
	return fallback
}
