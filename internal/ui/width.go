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
	for _, rg := range wideRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// wideRanges are the runes a terminal draws two columns wide. The emoji
// stopped at the main block's edges once: 🈁, 🀄 and 🩰 measured one column,
// and a row holding one wrapped and scrolled the frame. Below U+1F000 are
// the symbols a terminal draws as an emoji with no selector after them (⌚,
// ☕, ✅, ⭐).
var wideRanges = [...][2]rune{
	{0x1100, 0x115f}, // Hangul Jamo
	{0x231a, 0x231b}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0}, {0x23f3, 0x23f3}, {0x25fd, 0x25fe},
	{0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693}, {0x26a1, 0x26a1},
	{0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce}, {0x26d4, 0x26d4},
	{0x26ea, 0x26ea}, {0x26f2, 0x26f3}, {0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd},
	{0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728}, {0x274c, 0x274c}, {0x274e, 0x274e},
	{0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797}, {0x27b0, 0x27b0}, {0x27bf, 0x27bf},
	{0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0xa4cf}, // CJK radicals through Yi
	{0xac00, 0xd7a3}, // Hangul syllables
	{0xf900, 0xfaff}, // CJK compatibility ideographs
	{0xfe30, 0xfe6f}, // CJK compatibility forms
	{0xff00, 0xff60}, // fullwidth forms
	{0xffe0, 0xffe6},
	{0x1f004, 0x1f004}, {0x1f0cf, 0x1f0cf}, {0x1f18e, 0x1f18e}, {0x1f191, 0x1f19a}, // 🀄 🃏 🆎 🆑-🆚
	{0x1f200, 0x1f2ff}, // enclosed ideographic supplement (🈁)
	{0x1f300, 0x1f9ff}, // emoji
	{0x1fa70, 0x1faff}, // symbols and pictographs extended-A (🩰)
	{0x20000, 0x3fffd},
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
//
// Tabs are expanded first: a terminal moves a tab to the next stop, up to
// eight columns, while it measures zero here, so a row holding one escaped the
// clamp, wrapped and scrolled the frame. Detail text comes straight from
// model and provider errors, where a tab is ordinary.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\t", "    ")
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
	// A link cut before its close would run on into the rest of the frame.
	if strings.Contains(b.String(), "\x1b]8;;") {
		b.WriteString(linkClose)
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
		w := runeWidth(r)
		// A selector asking for emoji presentation makes a narrow symbol
		// (❤, ☀) the two columns a terminal draws it in.
		if next, _ := utf8.DecodeRuneInString(s[i+size:]); next == 0xfe0f && w == 1 {
			w = 2
		}
		if !fn(r, w) {
			return
		}
		i += size
	}
}

// escapeEnd returns the index just past the ANSI escape sequence starting at
// i, or i+1 when the sequence is malformed. An OSC, a hyperlink's, ends at ST
// or BEL; counted as text, its URL overflowed the row it was on.
func escapeEnd(s string, i int) int {
	j := i + 1
	if j < len(s) && s[j] == ']' {
		for j++; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return j
	}
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
