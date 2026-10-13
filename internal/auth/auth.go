// Package auth stores the API keys `deep-research login` was given, and the
// provider and model it picked, in the user's config directory.
//
// Keys live in auth.json, created 0600 in a 0700 directory and replaced
// atomically, so a crash mid-write never leaves half a file and nothing but
// the owner can read either. The choice of provider and model lives apart in
// model.json: switching models rewrites that file and never touches a secret.
//
// A key that starts with "!" is a command whose output is the key (pi's
// convention), so a key can stay in a password manager or the OS keychain:
//
//	{"openrouter": {"type": "api_key", "key": "!pass show openrouter"}}
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// File names inside the config directory.
const (
	AuthFile  = "auth.json"
	ModelFile = "model.json"
)

// Credential is one provider's stored key. BaseURL is the endpoint the key
// was entered for: a key is only ever sent there (see Store.KeyFor).
type Credential struct {
	Type    string `json:"type"`
	Key     string `json:"key"`
	BaseURL string `json:"base_url,omitempty"`
}

// Selection is the provider and model `login` or `model` last picked.
// Transport is how deep-research reaches it: "openai" for any
// OpenAI-compatible endpoint, "claude" for the Messages API or Claude Code.
type Selection struct {
	Provider  string `json:"provider"`
	Transport string `json:"transport"`
	BaseURL   string `json:"base_url,omitempty"`
	Model     string `json:"model"`
}

// Store is the auth.json of one config directory.
type Store struct {
	dir   string
	creds map[string]Credential
}

// Load reads dir/auth.json. A missing file is an empty store. warn receives
// a note when the file is readable by others; the store still loads, as a
// refusal would lock the user out of their own key over a chmod.
func Load(dir string, warn io.Writer) (*Store, error) {
	s := &Store{dir: dir, creds: map[string]Credential{}}
	p := filepath.Join(dir, AuthFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	warnPermissive(p, warn)
	if len(bytes.TrimSpace(b)) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.creds); err != nil {
		// The error never quotes the file: its text is the keys.
		return nil, fmt.Errorf("%s is not valid JSON; fix or delete it and run `deep-research login`", p)
	}
	if s.creds == nil { // the file said null
		s.creds = map[string]Credential{}
	}
	return s, nil
}

// Empty is a store with no keys and no file behind it, for a machine with no
// user config directory.
func Empty() *Store { return &Store{creds: map[string]Credential{}} }

// warnPermissive notes a group- or world-readable key file. Windows has no
// such mode bits, so there is nothing to check.
func warnPermissive(p string, warn io.Writer) {
	if runtime.GOOS == "windows" || warn == nil {
		return
	}
	if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 {
		_, _ = fmt.Fprintf(warn, "warning: %s is readable by other users; run: chmod 600 %s\n", p, p)
	}
}

// Path is the auth.json this store reads and writes.
func (s *Store) Path() string { return filepath.Join(s.dir, AuthFile) }

// Get returns the credential stored for provider.
func (s *Store) Get(provider string) (Credential, bool) {
	c, ok := s.creds[provider]
	return c, ok && c.Key != ""
}

// Providers lists the providers with a stored key.
func (s *Store) Providers() []string {
	var ids []string
	for id, c := range s.creds {
		if c.Key != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// Set stores c for provider and saves the file.
func (s *Store) Set(provider string, c Credential) error {
	if c.Type == "" {
		c.Type = "api_key"
	}
	s.creds[provider] = c
	return s.save()
}

// Delete removes provider's key and saves the file. It reports whether there
// was one.
func (s *Store) Delete(provider string) (bool, error) {
	if _, ok := s.creds[provider]; !ok {
		return false, nil
	}
	delete(s.creds, provider)
	return true, s.save()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.creds, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(s.Path(), append(b, '\n'))
}

// KeyFor returns a stored key entered for baseURL, preferring provider's,
// and the provider it belongs to. Keys bound to another endpoint are never
// returned: with OPENAI_BASE_URL pointed somewhere else for a run, the
// OpenRouter key would otherwise be sent to whatever host that is.
func (s *Store) KeyFor(baseURL, provider string) (key, from string, err error) {
	ids := s.Providers()
	slices.Sort(ids)
	if i := slices.Index(ids, provider); i > 0 {
		ids = append([]string{provider}, slices.Delete(ids, i, i+1)...)
	}
	for _, id := range ids {
		if c := s.creds[id]; SameEndpoint(c.BaseURL, baseURL) {
			key, err = Resolve(c.Key)
			return key, id, err
		}
	}
	return "", "", nil
}

// SameEndpoint compares two base URLs, ignoring a trailing slash and case
// in the scheme and host. An empty stored URL matches nothing.
func SameEndpoint(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return norm(a) != "" && norm(a) == norm(b)
}

// LoadSelection reads dir/model.json; ok is false when there is none.
func LoadSelection(dir string) (sel Selection, ok bool, err error) {
	p := filepath.Join(dir, ModelFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Selection{}, false, nil
	}
	if err != nil {
		return Selection{}, false, err
	}
	if err := json.Unmarshal(b, &sel); err != nil {
		return Selection{}, false, fmt.Errorf("read %s: %w", p, err)
	}
	return sel, sel.Model != "" || sel.Transport != "", nil
}

// SaveSelection writes sel to dir/model.json.
func SaveSelection(dir string, sel Selection) error {
	b, err := json.MarshalIndent(sel, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(filepath.Join(dir, ModelFile), append(b, '\n'))
}

// WriteFile replaces p with data, readable by the owner only. It writes a
// temporary file beside p and renames it over p, so a reader sees the old
// file or the new one and never a truncated key. The temporary file is
// created 0600, not chmod-ed after, so the key is never briefly readable.
func WriteFile(p string, data []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// commandTimeout bounds a "!command" key: a password manager waiting on a
// prompt nobody sees should fail the run, not hang it.
const commandTimeout = 10 * time.Second

var (
	resolvedMu sync.Mutex
	resolved   = map[string]string{}
)

// Resolve returns key, or for "!command" the command's trimmed output. A
// command runs once per process; one run makes many model calls. Its errors
// never include the output, which may be the key.
func Resolve(key string) (string, error) {
	key = strings.TrimSpace(key)
	cmd, ok := strings.CutPrefix(key, "!")
	if !ok {
		return key, nil
	}
	resolvedMu.Lock()
	defer resolvedMu.Unlock()
	if v, ok := resolved[cmd]; ok {
		return v, nil
	}
	v, err := runKeyCommand(cmd)
	if err != nil {
		return "", err
	}
	resolved[cmd] = v
	return v, nil
}

func runKeyCommand(command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", command)
	}
	c.Stderr = io.Discard
	out, err := c.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("key command did not finish within %s", commandTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("key command failed: %v", exitStatus(err))
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", errors.New("key command printed nothing")
	}
	return v, nil
}

// exitStatus is err without anything the command printed.
func exitStatus(err error) string {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return ee.String()
	}
	return err.Error()
}
