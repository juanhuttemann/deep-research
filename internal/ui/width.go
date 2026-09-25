package ui

import (
	"strings"
	"unicode/utf8"
)

// Display-width helpers. The live frame must fit the terminal exactly: a line
// that overflows the width wraps onto a second row, which pushes the frame
// past the viewport and makes the terminal scroll. Once it scrolls, the
// in-place repaint no longer lands on the previous frame and every redraw
// leaves a stale copy behind. Measuring in display columns — not bytes and not
// runes — is what keeps that from happening.

// runeWidth returns how many terminal columns r occupies.
func runeWidth(r rune) int {
	switch {
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return 0 // control
	case r >= 0x0300 && r <= 0x036f:
		return 0 // combining marks
	case r == 0x200d, r == 0xfe0e, r == 0xfe0f, r >= 0x200b && r <= 0x200f:
		return 0 // joiners / variation selectors
	case isWideRune(r):
		return 2
	}
	return 1
}

// isWideRune reports whether r is East-Asian wide or emoji-presentation, both
// of which occupy two columns.
func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0xa4cf, // CJK radicals through Yi
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
		r >= 0xfe30 && r <= 0xfe6f, // CJK compatibility forms
		r >= 0xff00 && r <= 0xff60, // fullwidth forms
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f9ff, // emoji
		r >= 0x20000 && r <= 0x3fffd:
		return true
	}
	return false
}

// dispWidth is the column count of s, ignoring ANSI escape sequences.
func dispWidth(s string) int {
	w := 0
	forEachCell(s, func(r rune, cw int) bool {
		w += cw
		return true
	})
	return w
}

// clip shortens s to at most w display columns, keeping ANSI escapes intact
// and closing any open colour so a cut mid-sequence cannot bleed into the rest
// of the frame. A truncated string always ends in an ellipsis: one column is
// reserved for it, so a cut that happens to land exactly on the boundary is
// still visibly a cut rather than a word that looks complete but is not.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if dispWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used, styled := 0, false
	forEachCell(s, func(r rune, cw int) bool {
		// Escapes carry no width, so the scan only ever stops on a visible
		// rune and can never cut a sequence in half.
		if cw > 0 && used+cw > w-1 {
			return false
		}
		if cw == 0 && r == 0x1b {
			styled = true
		}
		b.WriteRune(r)
		used += cw
		return true
	})
	b.WriteRune('…')
	if styled {
		b.WriteString(cReset)
	}
	return b.String()
}

// pad clips s to w columns and right-pads it to exactly w.
func pad(s string, w int) string {
	s = clip(s, w)
	if n := w - dispWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// forEachCell walks s rune by rune, passing each rune and its column width to
// fn; escape-sequence runes are reported with width 0. It stops early when fn
// returns false.
func forEachCell(s string, fn func(r rune, width int) bool) {
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			end := escapeEnd(s, i)
			for _, r := range s[i:end] {
				if !fn(r, 0) {
					return
				}
			}
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !fn(r, runeWidth(r)) {
			return
		}
		i += size
	}
}

// escapeEnd returns the index just past the ANSI escape sequence starting at
// i, or i+1 when the sequence is malformed.
func escapeEnd(s string, i int) int {
	j := i + 1
	if j >= len(s) || s[j] != '[' {
		return j
	}
	for j++; j < len(s); j++ {
		if s[j] >= '@' && s[j] <= '~' {
			return j + 1
		}
	}
	return j
}

// wrapWords breaks plain text onto lines of at most width display columns,
// splitting on spaces. A word too long for any line is hard-broken so it can
// never overflow the frame. Input is assumed free of ANSI escapes: colour is
// applied to the returned lines, not carried through the wrap.
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var out []string
	var line strings.Builder
	lineW := 0
	flush := func() {
		if line.Len() > 0 {
			out = append(out, line.String())
			line.Reset()
			lineW = 0
		}
	}
	for word := range strings.FieldsSeq(s) {
		wordW := 0
		for _, r := range word {
			wordW += runeWidth(r)
		}
		if lineW > 0 && lineW+1+wordW > width {
			flush()
		}
		if wordW > width {
			flush()
			for _, r := range word {
				if lineW+runeWidth(r) > width {
					flush()
				}
				line.WriteRune(r)
				lineW += runeWidth(r)
			}
			continue
		}
		if lineW > 0 {
			line.WriteByte(' ')
			lineW++
		}
		line.WriteString(word)
		lineW += wordW
	}
	flush()
	return out
}
