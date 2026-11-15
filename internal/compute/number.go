// Package compute is the run's arithmetic: numbers as a source writes them,
// the operations the analysis may ask for over them, and the charts drawn
// from the results. It is pure and deterministic, and imports nothing from
// the rest of the program, so what a report states as computed is the same
// whichever surface shows it.
package compute

import (
	"math/big"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Number is an exact decimal value and the decimal places it is shown with.
// A rational, not a float: 0.1 + 0.2 is 0.3, and a sum of quarterly figures
// is the figure an auditor gets with a pencil.
type Number struct {
	r   *big.Rat
	dec int
}

// Rat is the exact value.
func (n Number) Rat() *big.Rat {
	if n.r == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(n.r)
}

// Float is the value as a float, for drawing.
func (n Number) Float() float64 {
	f, _ := n.Rat().Float64()
	return f
}

// String is the value rounded to its places, halves away from zero, with
// thousands grouped by commas: "248,000", "-2.46".
func (n Number) String() string { return group(n.Rat().FloatString(n.dec)) }

// Plain is String without grouping, the form a value is stored in.
func (n Number) Plain() string { return n.Rat().FloatString(n.dec) }

func group(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	intPart, frac, hasFrac := strings.Cut(s, ".")
	var sb strings.Builder
	for i, d := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			sb.WriteByte(',')
		}
		sb.WriteRune(d)
	}
	out := sign + sb.String()
	if hasFrac {
		out += "." + frac
	}
	if out == "-0" || strings.TrimLeft(out, "-0.,") == "" {
		return strings.TrimPrefix(out, "-")
	}
	return out
}

// Matches reports whether stated is result as written to stated's own
// places: "248,000" states a sum of 248,000 exactly, and "2.5" states a
// growth of 2.4618 rounded. A figure written with fewer places than the
// result is a rounding of it, not a different figure; one that disagrees at
// its own precision is.
func Matches(stated, result Number) bool {
	return result.Rat().FloatString(stated.dec) == stated.Rat().FloatString(stated.dec)
}

// valueForm is the form the analysis gives a value in: the number as
// digits, commas grouping thousands, a point before the decimals.
var valueForm = regexp.MustCompile(`^(\d{1,3}(,\d{3})+|\d+)(\.\d+)?$`)

// ParseValue reads a value the analysis gave: "61,200", "-3.5", "4.5%".
// Only the one form is read. A decimal comma ("1,5") or a grouping point
// ("1.234") is ambiguous in a value field, where nothing says which
// convention the model followed, so it is refused rather than guessed.
// The same figure on the page may be written either way (see InText).
func ParseValue(s string) (Number, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
	neg := false
	switch {
	case strings.HasPrefix(s, "-"), strings.HasPrefix(s, "+"):
		neg, s = s[0] == '-', s[1:]
	case strings.HasPrefix(s, "−"):
		neg, s = true, strings.TrimPrefix(s, "−")
	}
	if !valueForm.MatchString(s) {
		return Number{}, false
	}
	plain := strings.ReplaceAll(s, ",", "")
	r, ok := new(big.Rat).SetString(plain)
	if !ok {
		return Number{}, false
	}
	if neg {
		r.Neg(r)
	}
	_, frac, _ := strings.Cut(plain, ".")
	return Number{r: r, dec: len(frac)}, true
}

// InText reports whether text writes n as one of its numbers: a whole
// numeric token, never part of one. "61,200" is in "Q1 2025: 61,200" but
// 1,200 is not, nor is the 1 of "Q1". A token is read in every convention it
// is valid in ("1.234" as 1.234 and as 1234), and n matches any reading;
// digits are never dropped or added. Whether the passage means the figure
// the claim gives it is the fact-check's to judge, not this.
func InText(text string, n Number) bool {
	want := n.Rat()
	for _, v := range numbersIn(text) {
		if v.Cmp(want) == 0 {
			return true
		}
	}
	return false
}

// groupSeps are the characters that group digits: comma, point, apostrophe
// and the spaces typography uses between thousands.
func isSep(r rune) bool {
	switch r {
	case ',', '.', '\'', ' ', ' ', ' ', ' ':
		return true
	}
	return false
}

func isSpaceSep(r rune) bool {
	return r == ' ' || r == ' ' || r == ' ' || r == ' '
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// numbersIn is every reading of every numeric token in text.
func numbersIn(text string) []*big.Rat {
	var out []*big.Rat
	for _, t := range tokens(text) {
		out = append(out, t.readings...)
	}
	return out
}

// token is a number as text writes it, and the values it can be read as.
type token struct {
	text     string
	readings []*big.Rat
}

// Unstated are the numbers text writes that none of sources writes, in any
// reading: the figures a statement adds to the claims it rests on.
func Unstated(text string, sources ...string) []string {
	var known []*big.Rat
	for _, s := range sources {
		known = append(known, numbersIn(s)...)
	}
	var out []string
	for _, t := range tokens(text) {
		// A token that reads as no number ("1.2.3") is an identifier, stated
		// when a source writes it as it is.
		stated := slices.ContainsFunc(t.readings, func(v *big.Rat) bool { return containsRat(known, v) }) ||
			len(t.readings) == 0 && slices.ContainsFunc(sources, func(s string) bool { return strings.Contains(s, t.text) })
		if !stated {
			out = append(out, t.text)
		}
	}
	return out
}

// tokens are text's numeric tokens with their readings (see InText).
func tokens(text string) []token {
	rs := []rune(text)
	var out []token
	for i := 0; i < len(rs); {
		if !isDigit(rs[i]) {
			i++
			continue
		}
		start := i
		end := tokenEnd(rs, i)
		i = end
		// A digit run inside a word ("Q1", "H2O", "v3") is part of a name,
		// and one before an exponent ("3e6") is not the number it writes.
		if e := exponentEnd(rs, end); e > end {
			i = e // the exponent's digits are not a number either
			continue
		}
		if start > 0 && (isWordRune(rs[start-1]) || rs[start-1] == '.' || rs[start-1] == ',') || inName(rs, start, end) {
			continue
		}
		t := token{text: string(rs[start:end]), readings: readings(string(rs[start:end]))}
		if negative(rs, start) {
			t.text = "-" + t.text
			for _, v := range t.readings {
				v.Neg(v)
			}
		}
		out = append(out, t)
	}
	return out
}

// negative reports whether the token at start is signed: a minus right
// before it, itself after a space, an opening bracket, a colon or equals
// sign, or a currency symbol ("$-3.5"). A hyphen between numbers ("10-20")
// is no sign.
func negative(rs []rune, start int) bool {
	if start == 0 || rs[start-1] != '-' && rs[start-1] != '−' {
		return false
	}
	if start == 1 {
		return true
	}
	p := rs[start-2]
	return unicode.IsSpace(p) || strings.ContainsRune("([:=", p) || unicode.Is(unicode.Sc, p)
}

// inName reports whether the token rs[start:end] is part of a name rather
// than a quantity: after a letter and a hyphen ("GPT-4", "COVID-19"), or
// before an ordinal or decade suffix ("1st", "4th", "1990s").
func inName(rs []rune, start, end int) bool {
	if start >= 2 && rs[start-1] == '-' && unicode.IsLetter(rs[start-2]) {
		return true
	}
	j := end
	for j < len(rs) && unicode.IsLetter(rs[j]) {
		j++
	}
	switch strings.ToLower(string(rs[end:j])) {
	case "st", "nd", "rd", "th", "s":
		return true
	}
	return false
}

// exponentEnd is the end of an exponent following a token that ends at i
// ("e6", "E-3"), or i when none does.
func exponentEnd(rs []rune, i int) int {
	if i+1 >= len(rs) || rs[i] != 'e' && rs[i] != 'E' {
		return i
	}
	j := i + 1
	if (rs[j] == '-' || rs[j] == '+') && j+1 < len(rs) {
		j++
	}
	if !isDigit(rs[j]) {
		return i
	}
	for j < len(rs) && isDigit(rs[j]) {
		j++
	}
	return j
}

// tokenEnd is where the numeric token starting at i ends: digits, and single
// separators between digits.
//
// A space groups thousands only between a group of one to three digits and
// one of exactly three: "61 200" is one number, "Q1 2025" two, and the year
// is not lost into the quarter's name.
func tokenEnd(rs []rune, i int) int {
	j, group := i, 0
	for j < len(rs) {
		switch {
		case isDigit(rs[j]):
			j++
			group++
		case isSpaceSep(rs[j]) && (group > 3 || digitRun(rs, j+1) != 3):
			return j
		case isSep(rs[j]) && j+1 < len(rs) && isDigit(rs[j+1]):
			j++
			group = 0
		default:
			return j
		}
	}
	return j
}

// digitRun is how many digits start at i.
func digitRun(rs []rune, i int) int {
	n := 0
	for i+n < len(rs) && isDigit(rs[i+n]) {
		n++
	}
	return n
}

// readings are the values a token can be read as. Thousands grouped by a
// comma with a decimal point, or by a point with a decimal comma; spaces and
// apostrophes group in both. A token valid in neither, with spaces in it,
// is numbers side by side ("2025 61,200"), each read on its own.
func readings(tok string) []*big.Rat {
	var out []*big.Rat
	for _, conv := range [][2]rune{{',', '.'}, {'.', ','}} {
		if v, ok := readAs(tok, conv[0], conv[1]); ok && !containsRat(out, v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = glued(tok)
	}
	// Spaces also part table cells ("123 456" in a row of counts): such a
	// token is read as the cells too, whichever it was.
	if !strings.ContainsFunc(tok, isSpaceSep) {
		return out
	}
	for _, part := range strings.FieldsFunc(tok, isSpaceSep) {
		for _, v := range readings(part) {
			if !containsRat(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

func containsRat(rs []*big.Rat, v *big.Rat) bool {
	for _, r := range rs {
		if r.Cmp(v) == 0 {
			return true
		}
	}
	return false
}

// glued reads a token that is table cells run together, as text extracted
// from a PDF often is: "2,866,1182,834,4721" is 2,866,118 then 2,834,472.
// Every group after the first of a grouped number has three digits, so a
// longer group ends one number after its third digit and starts the next
// with the rest; the split is forced, never guessed. It is read so only on
// evidence of a row: two or more whole comma-grouped numbers come out of it.
// A typo ("1,000,0001") gives one and reads as nothing; digits left over
// with no grouping (the 1 above, the start of a "1%" cell) are not offered;
// and point-grouped runs are left alone, being as often identifiers or
// coordinates ("12.3456.7890").
func glued(tok string) []*big.Rat {
	if strings.ContainsFunc(tok, func(r rune) bool { return isSep(r) && r != ',' }) {
		return nil
	}
	groups := strings.Split(tok, ",")
	if len(groups) < 2 || len(groups[0]) > 3 {
		return nil
	}
	var out []*big.Rat
	cur, n := groups[0], 1
	for _, g := range groups[1:] {
		switch {
		case len(g) == 3:
			cur, n = cur+g, n+1
		case len(g) > 3 && len(g) <= 6:
			// The number this group closes has at least two groups.
			out = append(out, ratOf(cur+g[:3]))
			cur, n = g[3:], 1
		default:
			return nil
		}
	}
	if n >= 2 {
		out = append(out, ratOf(cur))
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func ratOf(digits string) *big.Rat {
	r, _ := new(big.Rat).SetString(digits)
	return r
}

// readAs reads tok with groupSep grouping thousands and decimal before the
// decimals: every group after the first is three digits, the first one to
// three, and the decimal mark comes once, last.
func readAs(tok string, groupSep, decimal rune) (*big.Rat, bool) {
	intPart, frac, hasFrac := strings.Cut(tok, string(decimal))
	if hasFrac && strings.ContainsFunc(frac, isSep) {
		return nil, false
	}
	groups := strings.FieldsFunc(intPart, func(r rune) bool { return r == groupSep || r == '\'' || isSpaceSep(r) })
	if strings.ContainsFunc(intPart, func(r rune) bool { return isSep(r) && r != groupSep && r != '\'' && !isSpaceSep(r) }) {
		return nil, false
	}
	if len(groups) > 1 {
		if len(groups[0]) < 1 || len(groups[0]) > 3 {
			return nil, false
		}
		for _, g := range groups[1:] {
			if len(g) != 3 {
				return nil, false
			}
		}
	}
	plain := strings.Join(groups, "")
	if hasFrac {
		plain += "." + frac
	}
	r, ok := new(big.Rat).SetString(plain)
	return r, ok
}
