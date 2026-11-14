package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// serveColor is whether the banner may use colour: on a terminal, unless
// NO_COLOR or a dumb terminal says not to. serve's log is often
// redirected to a file, where escapes are noise.
func serveColor(log io.Writer) bool {
	return isTerminal(log) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// serveBanner says where the page is and what its runs will use. It was
// one bare "serving on" line, which said nothing of which model or search
// a launch from the page would get, or how to stop the server.
func serveBanner(w io.Writer, url, reports string, s agent.Setup, color bool) {
	paint := func(code, text string) string {
		if !color {
			return text
		}
		return code + text + "\x1b[0m"
	}
	link := url
	if color {
		// OSC 8: a terminal that knows it opens the page on click.
		link = "\x1b]8;;" + url + "\x1b\\" + url + "\x1b]8;;\x1b\\"
	}
	if abs, err := filepath.Abs(reports); err == nil {
		reports = abs
	}
	model := s.Model
	if s.Provider != "" {
		model += paint("\x1b[2m", " · "+s.Provider)
	}
	search := s.Search
	if s.Pages != "" {
		search += paint("\x1b[2m", " · pages: "+s.Pages)
	}
	row := func(label, value string) {
		_, _ = fmt.Fprintf(w, "    %s %s\n", paint("\x1b[2m", fmt.Sprintf("%-8s", label)), value)
	}
	_, _ = fmt.Fprintf(w, "\n  %s\n\n", paint("\x1b[1;36m", "deep-research")+paint("\x1b[2m", " · serve"))
	_, _ = fmt.Fprintf(w, "  %s %s\n\n", paint("\x1b[32m", "➜"), paint("\x1b[1m", link))
	row("model", model)
	row("search", search)
	row("reports", reports)
	_, _ = fmt.Fprintf(w, "\n  %s\n\n", paint("\x1b[2m", "ctrl+c to stop"))
}
