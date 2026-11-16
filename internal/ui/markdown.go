package ui

import (
	"io"
	"os"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
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
	r, err := glamour.NewTermRenderer(markdownStyle(), glamour.WithWordWrap(width))
	if err != nil {
		return body
	}
	out, err := r.Render(body)
	if err != nil {
		return body
	}
	return strings.TrimRight(out, "\n")
}

// markdownStyle picks the renderer's style. An explicit style avoids
// terminal background queries competing with the live UI's key reader.
// Light terminals can set GLAMOUR_STYLE=light; any other value is passed to
// Glamour as a style name or a JSON stylesheet path.
func markdownStyle() glamour.TermRendererOption {
	style := os.Getenv("GLAMOUR_STYLE")
	if style == "" || style == styles.DarkStyle {
		return glamour.WithStyles(readableDarkStyle())
	}
	return glamour.WithStylePath(style)
}

// readableDarkStyle is Glamour's dark style without its fixed greys. Body
// text in 252, rules and image captions in 240/243 and plain code in 244
// and 251 assume xterm's grey ramp, but themes repaint those indexes: one
// desktop theme sets 252 to #46464f, leaving a report's prose dark grey on
// a dark background, while the live frame, which draws with the terminal's
// own foreground and the dim attribute, stayed readable. Making the same
// choice here keeps reports legible under whatever palette the terminal
// carries. Accents (headings, links, inline code) keep their colours.
func readableDarkStyle() ansi.StyleConfig {
	s := styles.DarkStyleConfig
	faint := true
	s.Document.Color = nil
	s.HorizontalRule.Color, s.HorizontalRule.Faint = nil, &faint
	s.ImageText.Color, s.ImageText.Faint = nil, &faint
	s.CodeBlock.Color = nil
	// Chroma's plain text (#C4C4C4) is downsampled onto the same ramp, so
	// an unhighlighted block would still be grey; copy before clearing, as
	// the pointer is shared with Glamour's own style.
	chroma := *s.CodeBlock.Chroma
	chroma.Text.Color, chroma.Name.Color = nil, nil
	s.CodeBlock.Chroma = &chroma
	return s
}
