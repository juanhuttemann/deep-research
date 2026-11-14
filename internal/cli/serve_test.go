package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// serve said only "serving on <url>": nothing of the model or search a
// launch from the page would use, or how to stop it.
func TestServeBannerSaysWhatRunsUse(t *testing.T) {
	var b bytes.Buffer
	serveBanner(&b, "http://localhost:7777", "reports",
		agent.Setup{Model: "glm-5", Provider: "openrouter.ai", Search: "searxng", Pages: "firecrawl"}, false)
	got := b.String()
	for _, want := range []string{"http://localhost:7777", "glm-5", "openrouter.ai", "searxng", "firecrawl", "reports", "ctrl+c"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("escapes in a banner without colour:\n%q", got)
	}
}
