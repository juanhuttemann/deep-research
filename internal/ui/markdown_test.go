package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownRendering(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	body := "# Research Report\n\n**Important** and *emphasized* text.\n\n" +
		"> Quoted evidence\n\n- First finding\n\n" +
		"[Source](https://example.com)\n\n" +
		"| Source | Status |\n| --- | --- |\n| Paper | Verified |\n\n" +
		"```go\nfmt.Println(42)\n```\n\n" + strings.Repeat("Readable prose with wrapping. ", 10)
	out := renderMarkdown(body, 60)
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("terminal report has no styling")
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"Research Report", "Important", "emphasized", "Quoted evidence", "First finding", "https://example.com", "Paper", "Verified", "fmt.Println(42)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered report lost %q: %s", want, plain)
		}
	}
	for _, markup := range []string{"**Important**", "*emphasized*", "```go", "| --- |"} {
		if strings.Contains(plain, markup) {
			t.Errorf("raw Markdown remains: %q", markup)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if width := ansi.StringWidth(line); width > 60 {
			t.Errorf("line exceeds terminal width (%d): %q", width, line)
		}
	}
}

func TestMarkdownPreservesNonTerminalOutput(t *testing.T) {
	body := "# Report\n\n**Finding**\n"
	var buf bytes.Buffer
	if got := FormatMarkdown(&buf, body, false); got != body {
		t.Errorf("buffer output changed: %q", got)
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := FormatMarkdown(f, body, false); got != body {
		t.Errorf("redirected output changed: %q", got)
	}
}

func TestMarkdownFallsBackWhenStyleCannotLoad(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", filepath.Join(t.TempDir(), "missing.json"))
	body := "# Report\n\nKeep the entire report.\n"
	if got := renderMarkdown(body, 80); got != body {
		t.Errorf("fallback lost original Markdown: %q", got)
	}
}
