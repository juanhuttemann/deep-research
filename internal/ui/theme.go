package ui

// Theme holds the colours and styles used by the live UI and completion card.
// All colour is emitted as ANSI escape sequences.
type Theme struct {
	// Enabled controls whether ANSI codes are emitted at all. When false the
	// theme degrades to plain text, which keeps --jsonl / pipe output clean.
	Enabled bool
}

// Palette of named colours. Values are SGR escape strings.
const (
	cReset = "\x1b[0m"
	cBold  = "\x1b[1m"
	cDim   = "\x1b[2m"

	cRed     = "\x1b[31m"
	cGreen   = "\x1b[32m"
	cYellow  = "\x1b[33m"
	cMagenta = "\x1b[35m"
	cCyan    = "\x1b[36m"
	cGray    = "\x1b[90m"
)

// wrap wraps s in the given SGR codes.
func wrap(code, s string) string {
	if code == "" {
		return s
	}
	return code + s + cReset
}

// paint applies an SGR code only when colour is enabled; otherwise it returns
// the string unchanged so headless / pipe output stays clean.
func (t Theme) paint(code, s string) string {
	if !t.Enabled {
		return s
	}
	return wrap(code, s)
}

func (t Theme) bold(s string) string { return t.paint(cBold, s) }
func (t Theme) dim(s string) string  { return t.paint(cDim, s) }

func (t Theme) red(s string) string     { return t.paint(cRed, s) }
func (t Theme) green(s string) string   { return t.paint(cGreen, s) }
func (t Theme) yellow(s string) string  { return t.paint(cYellow, s) }
func (t Theme) magenta(s string) string { return t.paint(cMagenta, s) }
func (t Theme) cyan(s string) string    { return t.paint(cCyan, s) }
func (t Theme) gray(s string) string    { return t.paint(cGray, s) }

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
