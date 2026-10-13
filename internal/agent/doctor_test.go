package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func checkNamed(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, checks)
	return Check{}
}

// A run that produced nothing useful gave no way to see which dependency was
// the cause. Diagnose answers that with one cheap check per dependency.
func TestDiagnoseReportsEachDependency(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer provider.Close()
	sx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"a","url":"https://a.example","content":"x"}]}`)
	}))
	defer sx.Close()

	checks := Diagnose(context.Background(), Config{
		OpenAIAPIKey: "k", OpenAIBaseURL: provider.URL, OpenAIModel: "m",
		SearXNGURL: sx.URL, ModelCallTimeout: time.Second,
	})
	if c := checkNamed(t, checks, "model"); !c.OK || !strings.Contains(c.Detail, "m") {
		t.Errorf("model check = %+v", c)
	}
	if c := checkNamed(t, checks, "search"); !c.OK || !strings.Contains(c.Detail, sx.URL) || !strings.Contains(c.Detail, "1 result") {
		t.Errorf("search check = %+v", c)
	}
	if c := checkNamed(t, checks, "scraper"); !c.OK || !strings.Contains(c.Detail, "snippet") {
		t.Errorf("scraper check = %+v", c)
	}
}

func TestDiagnoseFlagsWhatIsBroken(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer provider.Close()
	sx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer sx.Close()

	checks := Diagnose(context.Background(), Config{
		OpenAIAPIKey: "bad", OpenAIBaseURL: provider.URL, OpenAIModel: "m",
		SearXNGURL: sx.URL, ModelCallTimeout: time.Second,
	})
	if c := checkNamed(t, checks, "model"); c.OK || !strings.Contains(c.Detail, "401") {
		t.Errorf("a rejected key passed: %+v", c)
	}
	if c := checkNamed(t, checks, "search"); c.OK || !strings.Contains(c.Detail, "rate-limited") {
		t.Errorf("a rate-limited search passed: %+v", c)
	}
	if c := checkNamed(t, Diagnose(context.Background(), Config{}), "model"); c.OK || !strings.Contains(c.Detail, "openrouter.ai/keys") {
		t.Errorf("a missing key passed: %+v", c)
	}
	if c := checkNamed(t, Diagnose(context.Background(), Config{OpenAIAPIKey: "k", OpenAIBaseURL: provider.URL}), "search"); !c.OK || !strings.Contains(c.Detail, "unverified") {
		t.Errorf("search off should pass and say what it means: %+v", c)
	}
}

// The free-model daily cap is counted in requests, and the key endpoint
// reports it; doctor showed usage in dollars only, which says nothing about
// how many free-model runs are left today.
func TestKeyTierShowsFreeModelDailyRequests(t *testing.T) {
	got := keyTier([]byte(`{"data":{"usage":0,"limit":null,"is_free_tier":true,
		"free_model_daily_requests":{"limit":50,"remaining":38,"used":12}}}`))
	if !strings.Contains(got, "free-model requests today: 12 of 50 used, 38 left") {
		t.Errorf("keyTier = %q", got)
	}
	if got := keyTier([]byte(`{"data":{"usage":1.5}}`)); strings.Contains(got, "free-model requests") {
		t.Errorf("a key record without the counter invented one: %q", got)
	}
}

// Doctor says which way provider claude would reach the model, and checks the
// key only when the key is what a run would use.
func TestDoctorReportsTheClaudeTransport(t *testing.T) {
	fakeClaudeCode(t, subscribed, "")
	c := checkModel(context.Background(), Config{Provider: "claude", ClaudeModel: "claude-opus-5-5"})
	if !c.OK || !strings.Contains(c.Detail, "Claude Code (pro subscription)") {
		t.Errorf("subscribed: %+v", c)
	}

	noClaudeCode(t)
	api := newAnthropicServer(t, func(int) anthropicReply { return anthropicReply{} })
	c = checkModel(context.Background(), Config{Provider: "claude", ClaudeModel: "m",
		AnthropicAPIKey: "sk", AnthropicBaseURL: api.URL})
	if !c.OK || api.requests() != 1 {
		t.Errorf("key: %+v after %d requests", c, api.requests())
	}

	bad := newAnthropicServer(t, func(int) anthropicReply { return anthropicReply{status: http.StatusUnauthorized} })
	c = checkModel(context.Background(), Config{Provider: "claude", ClaudeModel: "m",
		AnthropicAPIKey: "sk", AnthropicBaseURL: bad.URL})
	if c.OK || !strings.Contains(c.Detail, "401") {
		t.Errorf("rejected key: %+v", c)
	}

	c = checkModel(context.Background(), Config{Provider: "claude", ClaudeModel: "m"})
	if c.OK || !strings.Contains(c.Detail, "claude auth login") {
		t.Errorf("neither: %+v", c)
	}
}

// TestCheckModelNamesTheKeySourceNotTheKey: doctor says where the key came
// from (a stale one in auth.json beats a fresh one in .env), never the key.
func TestCheckModelNamesTheKeySourceNotTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-stored-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
	}))
	defer srv.Close()
	c := CheckModel(context.Background(), Config{OpenAIBaseURL: srv.URL, OpenAIAPIKey: "sk-stored-secret",
		OpenAIModel: "m1", KeySource: KeyFromLogin})
	if !c.OK || !strings.Contains(c.Detail, "key from auth.json") || strings.Contains(c.Detail, "sk-stored") {
		t.Errorf("check = %+v", c)
	}
	ids, err := ListModels(context.Background(), srv.URL, "sk-stored-secret")
	if err != nil || strings.Join(ids, ",") != "m1,m2" {
		t.Errorf("ListModels = %v, %v", ids, err)
	}
	if _, err := ListModels(context.Background(), srv.URL, "wrong"); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("rejected key: err = %v", err)
	}
}

// TestClaudeCheckFallsBackToTheModelList: Kimi's Anthropic-compatible API
// answers 404 for /v1/models/{id} but serves the list and the Messages
// endpoint a run uses; the check refused to save a working provider.
func TestClaudeCheckFallsBackToTheModelList(t *testing.T) {
	noClaudeCode(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != "sk" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"resource_not_found_error","message":"no route"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[],"has_more":false}`)
	}))
	defer srv.Close()
	cfg := Config{Provider: "claude", ClaudeModel: "k3", ClaudeAPIOnly: true, AnthropicAPIKey: "sk", AnthropicBaseURL: srv.URL}
	if c := CheckModel(context.Background(), cfg); !c.OK {
		t.Errorf("check = %+v after %v", c, paths)
	}
	cfg.AnthropicAPIKey = "wrong"
	if c := CheckModel(context.Background(), cfg); c.OK || !strings.Contains(c.Detail, "401") {
		t.Errorf("rejected key passed: %+v", c)
	}
}
