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
