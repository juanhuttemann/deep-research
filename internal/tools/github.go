package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHubClient asks GitHub about a repository: whether its owner archived it
// and when it last received a push. A snippet rarely says either, and the
// planner, shown guides that still recommend a project, planned research on
// one its owner had archived.
type GitHubClient struct {
	HTTPClient *http.Client
	// BaseURL is the REST API's root; tests point it at a double.
	BaseURL string
}

// NewGitHubClient is a client of the public API. It sends no token: the
// unauthenticated limit (60 requests an hour) covers the few repositories a
// plan's search turns up, and a refusal only leaves a repository unmarked.
func NewGitHubClient(timeout time.Duration) *GitHubClient {
	return &GitHubClient{HTTPClient: &http.Client{Timeout: timeout}, BaseURL: "https://api.github.com"}
}

// reservedGitHubPaths are first path segments that are GitHub's own pages,
// not owners: github.com/topics/llm names no repository.
var reservedGitHubPaths = map[string]bool{
	"topics": true, "search": true, "features": true, "marketplace": true, "sponsors": true,
	"collections": true, "trending": true, "explore": true, "settings": true, "about": true,
	"pricing": true, "enterprise": true, "login": true, "apps": true, "orgs": true, "users": true,
	"site": true, "security": true, "customer-stories": true, "readme": true, "events": true,
}

// GitHubRepo is the owner and name of the repository a github.com URL is
// on, and whether it is on one.
func GitHubRepo(raw string) (owner, repo string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	if host := strings.ToLower(u.Hostname()); host != "github.com" && host != "www.github.com" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" || reservedGitHubPaths[strings.ToLower(parts[0])] {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}

// RepoStatus is what GitHub says of a repository's upkeep.
type RepoStatus struct {
	Archived bool `json:"archived"`
	// PushedAt is the last push to any of its branches, the nearest the
	// API comes to its last commit.
	PushedAt time.Time `json:"pushed_at"`
}

// Repo is owner/repo's status.
func (c *GitHubClient) Repo(ctx context.Context, owner, repo string) (RepoStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), nil)
	if err != nil {
		return RepoStatus{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "deep-research-agent/1.0")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return RepoStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RepoStatus{}, fmt.Errorf("github %s/%s: %s", owner, repo, resp.Status)
	}
	var r RepoStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return RepoStatus{}, err
	}
	return r, nil
}
