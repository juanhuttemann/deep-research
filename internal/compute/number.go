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

// PlusOne is n increased by one, shown as n is.
func (n Number) PlusOne() Number {
	return Number{r: new(big.Rat).Add(n.Rat(), big.NewRat(1, 1)), dec: n.dec}
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
		switch neg, either := negative(rs, start); {
		case either:
			// Both signs: the reading the claim means is the checker's call.
			for _, v := range t.readings {
				t.readings = append(t.readings, new(big.Rat).Neg(v))
			}
		case neg:
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
// before it, Markdown-escaped or not ("\-0.9"), that does not follow a
// letter, a digit or a decimal mark. A hyphen between numbers ("10-20",
// "2019-2024") is no sign; one after a space, a bracket, a currency symbol
// ("$-3.5") or the end of another cell ("1.8%\-0.9%", as scraped chart
// labels run together) is. either is set for an escaped minus right after a
// letter: a label run into its value ("rate\-4.1") and a name ("COVID\-19")
// are written alike, so the token is read with both signs.
func negative(rs []rune, start int) (neg, either bool) {
	i := start - 1
	if i < 0 || rs[i] != '-' && rs[i] != '−' {
		return false, false
	}
	escaped := i > 0 && rs[i-1] == '\\'
	if escaped {
		i--
	}
	if i == 0 {
		return true, false
	}
	p := rs[i-1]
	if escaped && unicode.IsLetter(p) {
		return false, true
	}
	return !isWordRune(p) && p != '.' && p != ',', false
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

// SoleFigure is the one number text states, when it states exactly one that
// is not a year (a whole number from 1800 to 2100 written without grouping)
// and not a number context writes: "The United States won 40 gold medals at
// Paris 2024" is 40. Numbers are compared whole, so the 20 of "Japan won 20"
// is not taken for part of a context's "2024". A token read more than one
// way ("1.234") or two candidate numbers give nothing, rather than a guess.
func SoleFigure(text, context string) (Number, bool) {
	known := numbersIn(context)
	var found []token
	for _, t := range tokens(text) {
		if len(t.readings) == 0 || isYear(t) || slices.ContainsFunc(t.readings, func(v *big.Rat) bool { return containsRat(known, v) }) {
			continue
		}
		found = append(found, t)
	}
	if len(found) != 1 {
		return Number{}, false
	}
	// A claim written with commas grouping thousands ("367,500"), the
	// program's value form, is one number, not also 367.5. Any other token
	// read more than one way ("1.234") gives nothing.
	if n, ok := ParseValue(found[0].text); ok && strings.Contains(found[0].text, ",") {
		return n, true
	}
	if len(found[0].readings) != 1 {
		return Number{}, false
	}
	r := found[0].readings[0]
	return Number{r: r, dec: exactDecimals(r)}, true
}

// exactDecimals are the places that write r exactly, at most twelve.
func exactDecimals(r *big.Rat) int {
	x := new(big.Rat).Set(r)
	ten := big.NewRat(10, 1)
	for d := 0; d < 12; d++ {
		if x.IsInt() {
			return d
		}
		x.Mul(x, ten)
	}
	return 12
}

func isYear(t token) bool {
	if len(t.readings) != 1 || len(t.text) != 4 || !t.readings[0].IsInt() {
		return false
	}
	v := t.readings[0].Num().Int64()
	return v >= 1800 && v <= 2100
}

// SoleYear is the one year text writes, "" when it writes none or several.
func SoleYear(text string) string {
	var years []string
	for _, t := range tokens(text) {
		if isYear(t) && !slices.Contains(years, t.text) {
			years = append(years, t.text)
		}
	}
	if len(years) != 1 {
		return ""
	}
	return years[0]
}

// units are the measurement words a figure may be followed by, as the unit
// they name. A noun that is not a unit ("40 gold medals") names none: the
// claim's criterion says what is counted.
var units = map[string]string{
	"%": "%", "percent": "%", "per cent": "%",
	"m": "m", "metre": "m", "metres": "m", "meter": "m", "meters": "m",
	"km": "km", "kilometre": "km", "kilometres": "km", "kilometer": "km", "kilometers": "km",
	"cm": "cm", "mm": "mm", "ft": "ft", "feet": "ft", "mi": "mi", "miles": "mi",
	"kg": "kg", "kilograms": "kg", "g": "g", "grams": "g", "t": "t", "tonnes": "t", "tons": "tons", "lb": "lb", "pounds": "lb",
	"kw": "kW", "mw": "MW", "gw": "GW", "tw": "TW", "kwh": "kWh", "mwh": "MWh", "gwh": "GWh", "twh": "TWh",
	"°c": "°C", "°f": "°F", "teu": "TEU", "km/h": "km/h", "mph": "mph",
}

// currencies are the symbols written before a figure.
var currencies = map[rune]string{'$': "USD", '€': "EUR", '£': "GBP", '¥': "JPY"}

// scales are the words that multiply a figure, kept in its unit: "$12.4
// million" is 12.4 in USD million, never 12,400,000 by conversion.
var scales = map[string]string{"thousand": "thousand", "million": "million", "billion": "billion", "trillion": "trillion"}

// UnitOf is the unit text writes n in: a currency symbol before it, a
// scale word and a measurement word after it ("$12.4 million" is "USD
// million", "828 metres" is "m", "4.5%" is "%"). It is "" when n is not
// in text or is followed by no unit word.
func UnitOf(text string, n Number) string {
	rs := []rune(text)
	want := n.Rat()
	unit, found := "", false
	for _, t := range tokensAt(rs) {
		if !containsRat(t.readings, want) {
			continue
		}
		u := unitAt(rs, t)
		// The same number written twice in different units ("5 km away,
		// 5 GW") is no unit of the figure's.
		if found && u != unit {
			return ""
		}
		unit, found = u, true
	}
	return unit
}

// unitAt is the unit written around the token t: a currency symbol before
// it, a scale word and a measurement word after it.
func unitAt(rs []rune, t positioned) string {
	var parts []string
	if t.start > 0 {
		if cur, ok := currencies[rs[t.start-1]]; ok {
			parts = append(parts, cur)
		}
	}
	words := wordsAfter(rs, t.end, 3)
	scale := scales[firstOf(words)]
	if len(parts) > 0 && scale == "" {
		// After a currency, "m", "bn" and "k" are scales: "$12.4m".
		scale = map[string]string{"m": "million", "bn": "billion", "b": "billion", "k": "thousand"}[firstOf(words)]
	}
	if scale != "" {
		parts = append(parts, scale)
		words = words[1:]
	}
	// A one-letter unit is taken only set apart from the number and with no
	// word after it: "828 m" is metres; "33.3m" and "33.3m passengers" may
	// be millions.
	spaced := t.end < len(rs) && rs[t.end] == ' '
	if u := units[firstOf(words)]; u != "" && (!oneLetter(words[0]) || len(words) == 1 && spaced && scale == "") {
		parts = append(parts, u)
	}
	return strings.Join(parts, " ")
}

// wordsAfter are up to n lower-cased words right after i: a "%" or "°C"
// written against the number counts as one.
func wordsAfter(rs []rune, i, n int) []string {
	var out []string
	for len(out) < n && i < len(rs) {
		for i < len(rs) && rs[i] == ' ' {
			i++
		}
		j := i
		for j < len(rs) && (unicode.IsLetter(rs[j]) || strings.ContainsRune("%°/", rs[j])) {
			j++
			if rs[j-1] == '%' {
				break
			}
		}
		if j == i {
			break
		}
		out = append(out, strings.ToLower(string(rs[i:j])))
		i = j
	}
	return out
}

// positioned is a token and where it is in the text.
type positioned struct {
	token
	start, end int
}

// tokensAt are tokens with their places, for reading what is written
// around a figure.
func tokensAt(rs []rune) []positioned {
	var out []positioned
	text := string(rs)
	toks := tokens(text)
	// tokens() does not keep places; find each token's text in order.
	at := 0
	for _, t := range toks {
		raw := strings.TrimPrefix(t.text, "-")
		k := indexRunes(rs, []rune(raw), at)
		if k < 0 {
			continue
		}
		out = append(out, positioned{t, k, k + len([]rune(raw))})
		at = k + len([]rune(raw))
	}
	return out
}

func indexRunes(rs, sub []rune, from int) int {
	for i := from; i+len(sub) <= len(rs); i++ {
		if string(rs[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}

func firstOf(words []string) string {
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

func oneLetter(w string) bool {
	r := []rune(w)
	return len(r) == 1 && unicode.IsLetter(r[0])
}
