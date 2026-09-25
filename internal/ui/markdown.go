package ui

import (
	"io"
	"os"
	"strings"

	"charm.land/glamour/v2"
)

// FormatMarkdown styles a report for a terminal, preserving the original
// Markdown for pipes, files, and callers that disable colour.
func FormatMarkdown(w io.Writer, body string, noColor bool) string {
	if noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return body
	}
	f, ok := w.(*os.File)
	if !ok {
		return body
	}
	_, width := termSize(f)
	if width <= 0 {
		return body
	}
	return renderMarkdown(body, width)
}

func renderMarkdown(body string, width int) string {
	// An explicit style avoids terminal background queries competing with
	// the live UI's key reader. Light terminals can set GLAMOUR_STYLE=light.
	style := os.Getenv("GLAMOUR_STYLE")
	if style == "" {
		style = "dark"
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return body
	}
	out, err := r.Render(body)
	if err != nil {
		return body
	}
	return strings.TrimRight(out, "\n")
}
