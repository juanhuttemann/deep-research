package ui

import (
	"bytes"
	"html"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/text/unicode/norm"
)

// The summarizer is told to quote the findings verbatim, and a reader takes
// quotation marks as that promise; nothing held it. On the 53 saved runs
// these rules were worked out on, they remove 15 of 801 quotations, 10 of
// them not the source's words: a paraphrase ("A Seq stops early if yield
// returns false" for the iter package's own sentence), a word added ("a
// team larger than one engineer" for "a team larger than one"), or the
// analyzer's claim wording presented as the source's. On 12 fresh runs they
// were not worked out on, they removed 6 of 184, 5 of them invented, and
// kept no linked quotation the pages do not hold. The rest were scraped
// text that ran two words together, a labelled translation and a quoted
// conclusion. The fact-check's quotes are located in code (pageText.locate);
// these are now too. A quote that is not found keeps its words and loses its
// marks: it still says what the summarizer meant, no longer as the source's
// words, so a wrong call costs a pair of quotation marks and nothing else.

var (
	// proseQuote is a double-quoted span on one line of the mask, straight
	// or curly; a paragraph's soft line breaks are spaces there (proseMask).
	// It has no minimum length: with one, a short quote's opening mark was
	// skipped and its closing mark taken as the next quote's opening, and the
	// quote after it went unchecked. Length is checkable's call, once the
	// marks are paired.
	proseQuote = regexp.MustCompile(`"([^"\n]*)"|“([^”\n]*)”`)
	// lineJoin is a line break inside a paragraph with the next line's
	// indentation or blockquote marker, which a quote that wraps reads as a
	// space.
	lineJoin = regexp.MustCompile(`[ \t]*\n[ \t>]*`)
	// quoteCode is a code span inside a quote: its "..." and brackets are code
	// (INSERT ... ON CONFLICT, data[0]), not an elision or an edit.
	quoteCode = regexp.MustCompile("(?s)``.+?``|`[^`]+`")
	// quoteGap is where a quote leaves the page's words: an elision, words
	// left out, or a bracketed edit, words the quote supplies or changes to
	// fit its sentence ("[t]he", "require[s]", "reduce[]", "[ended] the").
	// Keeping the bracketed letters ("requires") compared them with a page
	// that never had them.
	quoteGap = regexp.MustCompile(`\s*(?:…|\.\.\.|\[…\]|\[\.\.\.\])\s*|\[[^\[\]\n]{0,24}\]`)
	// sentenceMark is where a sentence may end: a full stop, question or
	// exclamation mark, any closing marks, then space (see endsSentence).
	sentenceMark = regexp.MustCompile(`[.!?]["”')\]]*\s+`)
	// abbreviation is a word a full stop ends without ending the sentence:
	// an initial, letters and stops ("U.S.", "e.g."), or a common title or
	// short form ("Dr.", "Inc.", "vs.").
	abbreviation = regexp.MustCompile(`^(?:\p{L}{1,3}\.)+\p{L}{0,3}$|^\p{L}$|` +
		`^(?i:dr|mr|mrs|ms|prof|st|jr|sr|vs|inc|ltd|co|approx)$`)
	// numberAbbreviation is a short form a full stop ends only before a
	// number ("No. 5", "Fig. 3"): taken for one everywhere, "the answer is
	// no." ran into the next sentence and its link attributed a quote there.
	numberAbbreviation = regexp.MustCompile(`^(?i:no|fig|vol|pp?)$`)
	// containerPrefix is what may stand before a block's text on its line:
	// indentation, blockquote and list markers.
	containerPrefix = regexp.MustCompile(`^(?:[ \t]*(?:>|[-+*]|[0-9]{1,9}[.)])?[ \t]*)*$`)
	// blockOpener is text that may open a block where a block starts: a
	// heading, a list item, a blockquote, a thematic break, a table row or a
	// setext underline. Its last character is the one an escape goes before.
	blockOpener = regexp.MustCompile(`^(?:[0-9]{1,9}[.)]|[#\-+*>|=_])`)
	// mdLinkTarget is a Markdown link to a web page, inline or an autolink.
	mdLinkTarget = regexp.MustCompile(`\]\(https?://|<https?://`)
	// typography is what quoting changes in a passage's characters:
	// typographic quotes, dashes and a no-break space made plain.
	typography = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", "\u00a0", " ")
	// quoteMark is any quotation mark once visibleText has made curly ones
	// straight: a quote nested in a quotation may use either.
	quoteMark = strings.NewReplacer("'", `"`)
	// markdownParser reads the report and the pages as the PDF does (GFM).
	// It is built once: proseMask and plainText built one on every call, a
	// page and two pieces a quote, and goldmark keeps each parse's state to
	// the call, as internal/web relies on.
	markdownParser = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()
	// spaceBeforePunct is the space scraped text keeps before a comma
	// ("value for money , as"), which a quote of it writes without.
	spaceBeforePunct = regexp.MustCompile(`\s+([,.;:!?])`)
)

// unquoteUnfound removes the quotation marks from each quotation of a source
// in the report's prose that is in none of sources, and says how many it
// changed. Only prose is read (proseMask). A quotation of a
// source is a quoted span of six words or more with a link in its sentence:
// the summarizer is told to give its quotes with their links. Quotation
// marks around a phrase being named ("start with SQLite and migrate later",
// "how long before the first probe") carry no link and claim no source's
// words; in the runs measured, 10 of the 11 invented quotations had one,
// and 8 of the 17 spans wrongly unquoted without the rule had none.
func unquoteUnfound(report string, sources map[string]*pageText) (string, int) {
	var sb strings.Builder
	n, at := 0, 0
	for _, m := range quoteSpans(proseMask(report)) {
		lo, hi := m[2], m[3]
		if lo < 0 {
			lo, hi = m[4], m[5]
		}
		// The escapes go before the lookup: looked up with the backslash
		// that escaped its closing mark, a genuine \"...\" quote was never
		// found and always lost its marks.
		start, words := m[0], report[lo:hi]
		if m[2] >= 0 {
			start, words = dropEscapes(report, start, words)
		}
		q := lineJoin.ReplaceAllString(words, " ")
		if !checkable(q) || !linkedSentence(report, m[0], m[1]) || quoteFound(q, sources) {
			continue
		}
		sb.WriteString(report[at:start])
		sb.WriteString(keepInline(report[strings.LastIndexByte(report[:start], '\n')+1:start], words))
		at = m[1]
		n++
	}
	sb.WriteString(report[at:])
	return sb.String(), n
}

// quoteSpans pairs the quotation marks in the masked report, as proseQuote
// matches. A quotation that holds another, straight marks in straight ones
// or curly in curly, is one span: paired left to right, its marks were cut
// into the pieces around the inner quotation, each opening or closing on a
// space, which checkable refuses, and words invented around a genuine inner
// quotation kept their marks unchecked.
func quoteSpans(mask []byte) [][]int {
	ms := proseQuote.FindAllSubmatchIndex(mask, -1)
	var out [][]int
	for i := 0; i < len(ms); i++ {
		m := ms[i]
		if m[2] >= 0 {
			if outer, used := nestedStraight(mask, ms[i:]); used > 0 {
				out = append(out, outer)
				i += used
				continue
			}
		}
		if m[4] >= 0 {
			m = nestedCurly(mask, m)
		}
		out = append(out, m)
	}
	return out
}

// nestedStraight joins ms[0] and the straight-quoted spans after it into
// the quotation around inner ones, and says how many it joined after the
// first. Paired left to right, "a "b" c "d" e" is "a ", " c " and " e":
// the first opens on a word and ends on the space before an inner opening
// mark, each middle one runs from an inner closing mark to the next inner
// opening one, and the last closes the quotation on a word. Between each two
// is an inner quotation, on one line, opening and closing on a word.
func nestedStraight(mask []byte, ms [][]int) ([]int, int) {
	first := mask[ms[0][2]:ms[0][3]]
	if len(first) == 0 || isSpace(first[0]) || !isSpace(first[len(first)-1]) {
		return nil, 0
	}
	for j := 1; j < len(ms) && ms[j][2] >= 0; j++ {
		nested, span := mask[ms[j-1][1]:ms[j][0]], mask[ms[j][2]:ms[j][3]]
		if len(nested) == 0 || isSpace(nested[0]) || isSpace(nested[len(nested)-1]) ||
			bytes.IndexByte(nested, '\n') >= 0 || len(span) == 0 || startsWord(span) {
			return nil, 0
		}
		if !isSpace(span[len(span)-1]) {
			return []int{ms[0][0], ms[j][1], ms[0][2], ms[j][3], -1, -1}, j
		}
	}
	return nil, 0
}

// nestedCurly extends a curly-quoted span whose words open another curly
// quotation to the closing mark after the inner one's, on the same line.
func nestedCurly(mask []byte, m []int) []int {
	if !bytes.Contains(mask[m[4]:m[5]], []byte("“")) {
		return m
	}
	line := mask[m[1]:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	c := bytes.Index(line, []byte("”"))
	if c < 0 || bytes.Contains(line[:c], []byte("“")) {
		return m
	}
	end := m[1] + c
	return []int{m[0], end + len("”"), -1, -1, m[4], end}
}

func startsWord(b []byte) bool {
	r, _ := utf8.DecodeRune(b)
	return wordRune(r)
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

// keepInline is words, taken out of their quotation marks, escaped where
// they would otherwise open a block: at the start of a line, past only
// list and blockquote markers (before), "1. Install the package" became a
// numbered list item and "# of users doubled" a heading, losing the "1." or
// the "#" the words said.
func keepInline(before, words string) string {
	if !containerPrefix.MatchString(before) {
		return words
	}
	m := blockOpener.FindStringIndex(words)
	if m == nil {
		return words
	}
	// Any ASCII punctuation may be escaped, so an escape where none was
	// needed ("\#hashtag") reads the same.
	return words[:m[1]-1] + `\` + words[m[1]-1:]
}

// dropEscapes returns where straight quotation marks' removal starts and the
// words left, without the backslashes escaping the marks (\"...\"), which
// shown alone are noise. Only a backslash ending an odd run escapes: in
// "C:\\" the mark is not escaped and the path keeps its backslash. Curly
// marks are not Markdown-escapable, so a backslash before one is text and
// the caller keeps it.
func dropEscapes(report string, start int, inner string) (int, string) {
	if escapes(report[:start]) {
		start--
	}
	if escapes(inner) {
		inner = inner[:len(inner)-1]
	}
	return start, inner
}

// escapes says whether s ends in a backslash that escapes what follows.
func escapes(s string) bool {
	return (len(s)-len(strings.TrimRight(s, `\`)))%2 == 1
}

// proseMask is the report with all but its prose text blanked out, byte for
// byte so offsets carry over, newlines kept but for a paragraph's own: quotation marks are paired on
// it and edited in the report. What is blanked is Markdown syntax as goldmark
// parses it, the parser the PDF is laid out with: code spans and blocks, a
// link's target and title, emphasis markers, raw HTML. Matched by hand, every
// form the patterns missed (a double-backtick span, a tilde fence, an escaped
// backtick, a four-backtick fence around a three-backtick one) lost the
// quotation marks in its code.
func proseMask(report string) []byte {
	src := []byte(report)
	masked := make([]byte, len(src))
	for i, c := range src {
		masked[i] = 'x'
		if c == '\n' {
			masked[i] = c
		}
	}
	doc := markdownParser.Parse(text.NewReader(src))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch t := n.(type) {
		case *ast.CodeSpan, *ast.RawHTML, *ast.AutoLink:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if entering {
				copy(masked[t.Segment.Start:t.Segment.Stop], src[t.Segment.Start:t.Segment.Stop])
				// A line break inside a paragraph is a space, so a quotation
				// Markdown wrapped over two lines is still one span.
				if t.SoftLineBreak() || t.HardLineBreak() {
					if i := bytes.IndexByte(src[t.Segment.Stop:], '\n'); i >= 0 {
						masked[t.Segment.Stop+i] = ' '
					}
				}
			}
		}
		return ast.WalkContinue, nil
	})
	return masked
}

// linkedSentence reports whether the sentence around report[lo:hi] holds a
// link: the line it is on, from the previous sentence's end to the next's. A
// table row is one line, and a quote in one cell is attributed by the link
// in the next.
func linkedSentence(report string, lo, hi int) bool {
	start := strings.LastIndexByte(report[:lo], '\n') + 1
	end := len(report)
	if i := strings.IndexByte(report[hi:], '\n'); i >= 0 {
		end = hi + i
	}
	from, to := start, end
	for _, m := range sentenceMark.FindAllStringIndex(report[start:lo], -1) {
		if endsSentence(report, start+m[0], start+m[1]) {
			from = start + m[1]
		}
	}
	for _, m := range sentenceMark.FindAllStringIndex(report[hi:end], -1) {
		if endsSentence(report, hi+m[0], hi+m[1]) {
			to = hi + m[0] + 1
			break
		}
	}
	// A link inside the quote attributes it too.
	return mdLinkTarget.MatchString(report[from:to])
}

// endsSentence reports whether the mark at s[i:j] ends a sentence: what
// follows opens with a capital, a digit or a quotation mark, past any other
// opening marks, and the word before it is not an abbreviation. The stop in
// "the U.S. agency" ended the sentence, and cut a quote off from the link
// that attributed it. A quotation may open a sentence in lower case
// ("always start with SQLite" is the rule); read as the sentence before
// going on, it took that sentence's link as its own.
func endsSentence(s string, i, j int) bool {
	next := strings.TrimLeft(s[j:], "(*_[")
	r, _ := utf8.DecodeRuneInString(next)
	if next != "" && !unicode.IsUpper(r) && !unicode.IsDigit(r) && !strings.ContainsRune("\"“'", r) {
		return false
	}
	word := s[strings.LastIndexAny(s[:i], " \t(")+1 : i]
	if numberAbbreviation.MatchString(word) {
		return !unicode.IsDigit(r)
	}
	return !abbreviation.MatchString(word)
}

// checkable reports whether a quoted span reads as a quotation. A span that
// opens or closes on a space is the close of one quotation paired with the
// opening of the next. Bold and links inside are checked as the words they
// show: refusing a span with either let any quotation with one bold word
// stand unchecked. A script written without spaces (Chinese, Japanese,
// Thai, ...) has no words to count: a whole quotation was one field and none
// was ever checked, so there two characters count as a word.
func checkable(q string) bool {
	if strings.TrimSpace(q) != q {
		return false
	}
	words := 0
	for _, f := range strings.Fields(q) {
		n := 0
		for _, r := range f {
			if spaceless(r) {
				n++
			}
		}
		words += max(1, (n+1)/2)
	}
	return words >= 6
}

// spaceless is a letter of a script written without spaces between words,
// where any two letters may be a word's edge.
func spaceless(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar)
}

// quoteFound reports whether a quote is in some fetched page. It accepts
// what a reader accepts as quoting: an elision, a bracketed edit, a capital
// the sentence needed, trailing punctuation. The words, their order and the
// numbers must still be the page's, a quote must begin and end where the
// page's words do (keepsEdges), and a quote's pieces must all be in one
// page, in the quote's order: pieces found on different pages, or out of
// order, vouched for a sentence no source wrote ("The default timeout for
// this service is … 75 seconds" from a page with 30 seconds and another with
// a 75-second watchdog). The piece after a bracketed edit follows the one
// before it closely, as the edit replaced a word or two, not a passage.
func quoteFound(q string, pages map[string]*pageText) bool {
	// A quote of code is compared literally (literalForm): as visible text,
	// a quoted data[0] matched a page's data[9], and requiring the code
	// elsewhere on the page let data[0] in another passage vouch for it.
	literal := quoteCode.MatchString(q)
	pieces := quotePieces(q, literal)
	switch len(pieces) {
	case 0:
		return true
	case 1:
		pc := pieces[0]
		if !literal {
			pc = pc.withForms()
		}
		for _, p := range pages {
			if !literal && p.holds(pc) || literal && p.holdsLiteral(pc) {
				return true
			}
		}
		return false
	}
	for _, p := range pages {
		if p.inOrder(pieces, literal) {
			return true
		}
	}
	return false
}

// holds reports whether one piece is on the page, with either capital: as
// locate finds it, or failing that in the loose text, the quote check's
// fallback and never the fact-check's, whose quotes are held to locate.
func (p *pageText) holds(pc quotePiece) bool {
	for k, q := range pc.nfc {
		if q != "" && (pc.within(p.text, q) || p.locateVisible(pc.visible[k], pc.within)) {
			return true
		}
	}
	return slices.ContainsFunc(p.looseTexts(), pc.inText)
}

// holdsLiteral is holds for a piece of a quote of code.
func (p *pageText) holdsLiteral(pc quotePiece) bool { return pc.inText(p.literalText()) }

// quotePiece is a run of a quote's own words between gaps.
type quotePiece struct {
	text string
	// loose and flipped are text as looseText compares it, flipped with its
	// first letter's case changed; worked out once, not at every try.
	loose, flipped string
	// near marks a piece after a bracketed edit: it starts within
	// maxEditGap bytes of the piece before it.
	near bool
	// open and close say whether the piece starts and ends where a word of
	// the page does (keepsEdges).
	open, close bool
	// nfc and visible are text and its other capital as locate compares
	// them (withForms), for a quote of one piece only.
	nfc, visible [2]string
}

// withForms is pc with the forms locate compares worked out once, rather
// than again for every page.
func (pc quotePiece) withForms() quotePiece {
	for k, s := range []string{pc.text, flipFirst(pc.text)} {
		pc.nfc[k] = strings.TrimSpace(norm.NFC.String(s))
		pc.visible[k] = visibleText(pc.nfc[k])
	}
	return pc
}

// inText reports whether t holds the piece, with either capital.
func (pc quotePiece) inText(t string) bool { return pc.within(t, pc.loose) || pc.within(t, pc.flipped) }

// within reports whether s holds sub at the word edges pc keeps.
func (pc quotePiece) within(s, sub string) bool { return pc.index(s, sub, 0, len(s)) >= 0 }

// index is the first place in s[from:to] that holds sub at the word edges
// pc keeps, or -1. The edges are read in all of s, not in the window: a
// window that ended at "10" of "100" saw a number end.
func (pc quotePiece) index(s, sub string, from, to int) int {
	for sub != "" && from < to {
		j := strings.Index(s[from:to], sub)
		if j < 0 {
			return -1
		}
		if pc.keepsEdges(s, from+j, from+j+len(sub)) {
			return from + j
		}
		from += j + 1
	}
	return -1
}

// keepsEdges reports whether s[i:j] begins and ends at a word's edge where
// pc must. Matched anywhere in the page's text, a quote of "the maximum
// request size is 10" stood on a page's 100 MB, and "secure for production"
// on its "insecure for production". A word runs on only from a letter to a
// letter or a digit to a digit: a footnote marker after a word ("risks¹",
// made "risks1" by visibleText, or "risksᵃ") is not part of it. A comma
// between digits parts a list's items as often as a number's
// thousands ("200,201" is either), and read as a number's it unquoted a
// list's valid excerpt, so "costs 1" still stands on "costs 1,000". A hyphen
// does not either, since the page's dashes are made one ("result—secure" is
// read as "result-secure").
func (pc quotePiece) keepsEdges(s string, i, j int) bool {
	return (!pc.open || !joinedAt(s, i)) && (!pc.close || !joinedAt(s, j))
}

// joinedAt reports whether one word of s runs on across position k.
func joinedAt(s string, k int) bool {
	p, n := utf8.DecodeLastRuneInString(s[:k])
	r, _ := utf8.DecodeRuneInString(s[k:])
	if n == 0 || k == len(s) {
		return false
	}
	// Between two letters of a space-less script is a word's edge as often
	// as not: read as one word, a quote of such a page was never found in it.
	if spaceless(p) || spaceless(r) {
		return false
	}
	return letter(p) && letter(r) || unicode.IsDigit(p) && unicode.IsDigit(r)
}

// letter is a letter of a word: not a modifier letter, which a superscript
// footnote marker is (ᵃ).
func letter(r rune) bool {
	return unicode.IsLetter(r) && !unicode.Is(unicode.Lm, r) || unicode.IsMark(r)
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }

// maxEditGap is how far on the page a bracketed edit may move the next
// piece: the word or two the edit stands for.
const maxEditGap = 24

// maskCode is q with its code spans blanked out, byte for byte.
func maskCode(q string) []byte {
	masked := []byte(q)
	for _, m := range quoteCode.FindAllStringIndex(q, -1) {
		for i := m[0]; i < m[1]; i++ {
			masked[i] = 'x'
		}
	}
	return masked
}

// labelLinks replaces each link outside q's code spans with its label. A
// link inside code is code: reduced too, a quoted `[x](y)` matched a page
// that said x.
func labelLinks(q string) string {
	var sb strings.Builder
	at := 0
	for _, m := range mdLink.FindAllSubmatchIndex(maskCode(q), -1) {
		sb.WriteString(q[at:m[0]])
		sb.WriteString(q[m[2]:m[3]])
		at = m[1]
	}
	sb.WriteString(q[at:])
	return sb.String()
}

// quotePieces splits a quote at its gaps (quoteGap) outside its code spans:
// a gap is looked for on a copy whose code is blanked out. Stripping the
// backticks first made code a pattern, and a quoted data[0] matched a page's
// data[9]. The code itself is compared as written (literalForm). A link is
// its label first: "[angle](...)" is not a bracketed edit.
//
// A piece is near the one before it only when nothing but bracketed edits
// lies between them. An elision followed by an edit ("… [t]he") left an
// empty piece between them, and the edit alone decided: the piece after it
// had to sit within maxEditGap of the words before the elision. A piece
// keeps the word edges of the quote, except where an edit touches it with
// no space between: "[t]he" begins inside the page's "The", and "reduce[]"
// ends inside its "reduced".
func quotePieces(q string, literal bool) []quotePiece {
	q = labelLinks(q)
	masked := maskCode(q)
	var out []quotePiece
	elided, open, at := false, true, 0
	add := func(text string, closed bool) {
		if text = strings.Trim(text, " ,.;:!?。，、；：！？"); text == "" {
			return
		}
		pc := quotePiece{text: text, loose: comparable(text, literal), near: !elided && len(out) > 0, open: open, close: closed}
		// Only the first piece may take the other capital (inOrder).
		if len(out) == 0 {
			pc.flipped = comparable(flipFirst(text), literal)
		}
		out = append(out, pc)
		elided = false
	}
	for _, g := range quoteGap.FindAllIndex(masked, -1) {
		gap := q[g[0]:g[1]]
		edit := !strings.Contains(gap, "…") && !strings.Contains(gap, "...")
		// Emphasis does not part an edit from its word ("[t]**he**"), and an
		// entity is the letter it shows ("[t]&#104;e").
		last, _ := utf8.DecodeLastRuneInString(html.UnescapeString(strings.TrimRight(q[:g[0]], "*_~")))
		add(q[at:g[0]], !edit || !wordRune(last))
		next, _ := utf8.DecodeRuneInString(html.UnescapeString(strings.TrimLeft(q[g[1]:], "*_~")))
		elided, open, at = elided || !edit, !edit || !wordRune(next), g[1]
	}
	add(q[at:], true)
	return out
}

// inOrder reports whether the pieces are in one of the page's loose texts
// in order, each after the one before it, and a near one close after it.
// Only the first piece may differ in its first letter's case. The
// hyphen-joined texts are tried too: without them a PDF passage quoted whole
// was found and the same passage elided was not.
//
// A piece with its near ones is a cluster, and clusters are matched each at
// its earliest place after the one before: past an elision any later place
// will do, and the earliest leaves the most page for the rest, so nothing is
// retried. Backtracking over every place of every piece took eight seconds
// on repetitive pages, and the budget that bounded it removed a valid quote.
func (p *pageText) inOrder(pieces []quotePiece, literal bool) bool {
	if literal {
		return inOrderIn(p.literalText(), pieces)
	}
	return slices.ContainsFunc(p.looseTexts(), func(t string) bool { return inOrderIn(t, pieces) })
}

// inOrderIn is inOrder in one text.
func inOrderIn(text string, pieces []quotePiece) bool {
	at := 0
	for i := 0; i < len(pieces); {
		j := i + 1
		for j < len(pieces) && pieces[j].near {
			j++
		}
		end, ok := matchCluster(text, pieces[i:j], at, i == 0)
		if !ok {
			return false
		}
		at, i = end, j
	}
	return true
}

// matchCluster finds the earliest place from at where c's first piece is
// followed, each within maxEditGap, by the rest, and returns where it ends.
// first allows the first piece's other capital.
func matchCluster(text string, c []quotePiece, at int, first bool) (int, bool) {
	heads := []string{c[0].loose}
	if first {
		heads = append(heads, c[0].flipped)
	}
	best, failed := -1, map[[2]int]bool{}
	for _, h := range heads {
		for off := at; ; {
			j := c[0].index(text, h, off, len(text))
			if j < 0 {
				break
			}
			if end, ok := followNear(text, c[1:], j+len(h), failed); ok {
				if best < 0 || end < best {
					best = end
				}
				break
			}
			off = j + 1
		}
	}
	return best, best >= 0
}

// followNear matches each piece within maxEditGap of the end of the one
// before it, from pos, and returns where the last ends. Every place in the
// window is tried: the first "uses" after "The system [now]" can be too far
// from "reliable" while a second, also in reach, is not. failed holds the
// (pieces left, position) states already ruled out: tried again from every
// path that reaches them, seven edits over a page of "a a a" took seconds.
func followNear(text string, rest []quotePiece, pos int, failed map[[2]int]bool) (int, bool) {
	if len(rest) == 0 {
		return pos, true
	}
	state := [2]int{len(rest), pos}
	if failed[state] {
		return 0, false
	}
	pc := rest[0]
	to := min(len(text), pos+maxEditGap+len(pc.loose))
	for off := pos; ; {
		k := pc.index(text, pc.loose, off, to)
		if k < 0 {
			break
		}
		if end, ok := followNear(text, rest[1:], k+len(pc.loose), failed); ok {
			return end, true
		}
		off = k + 1
	}
	failed[state] = true
	return 0, false
}

// flipFirst changes the case of a quote's first letter: a quote that opens
// a sentence capitalises the page's lower-case word, and one set inside a
// sentence lowers the page's capital.
func flipFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if unicode.IsUpper(r) {
		return string(unicode.ToLower(r)) + s[size:]
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// looseTexts are the page's visible text and its hyphen-joined forms
// (joinedForms) without the space before punctuation, with one quotation
// mark and footnote markers read as spaces, worked out the first time they
// are needed. A page with no footnote marker reuses the forms locate keeps.
func (p *pageText) looseTexts() []string {
	if p.hasLoose {
		return p.loose
	}
	var forms []string
	if strings.IndexFunc(p.text, footnoteMark) >= 0 {
		clean := strings.Map(func(r rune) rune {
			if footnoteMark(r) {
				return ' '
			}
			return r
		}, p.text)
		for _, t := range append([]string{clean}, hyphenJoins(clean)...) {
			forms = append(forms, visibleText(t))
		}
	} else {
		forms = append([]string{p.visibleForm()}, p.joinedForms()...)
	}
	for i, f := range forms {
		forms[i] = quoteMark.Replace(spaceBeforePunct.ReplaceAllString(f, "$1"))
	}
	p.loose, p.hasLoose = forms, true
	return forms
}

// footnoteMark is a superscript or modifier letter, as a footnote marker is
// written. visibleText's NFKC makes one the plain letter or digit ("risksᵃ"
// read "risksa", "10¹" read "101"), joining it to the word before; the loose
// texts, which only the report's quotes are held to, read one as a space.
func footnoteMark(r rune) bool {
	return r == '¹' || r == '²' || r == '³' || r >= 0x2070 && r <= 0x209f || r >= 0x1d2c && r <= 0x1d6a
}

// literalText is the page as a quote of code is compared (literalForm).
func (p *pageText) literalText() string {
	if !p.hasLiteral {
		p.literal, p.hasLiteral = literalForm(p.text), true
	}
	return p.literal
}

// comparable is s as a quote is compared: visible text without the space
// before punctuation and with one quotation mark, or literalForm for a
// quote of code.
func comparable(s string, literal bool) string {
	if literal {
		return literalPiece(s)
	}
	return quoteMark.Replace(spaceBeforePunct.ReplaceAllString(visibleText(s), "$1"))
}

// literalForm is a page as a quote of code is compared with it, and
// literalPiece a piece of the quote: the text a reader sees rendered
// (plainText), its code exactly as written, typography made plain and
// spaces collapsed. Quotation marks are not made one, as they are for prose,
// in the quote's prose either: in code print('x') is not print("x"), and a
// page that lost its backticks shows its code as prose.
//
// Both sides are read by goldmark, the parser the PDF is laid out with,
// because every approximation failed one way or the other. Read as visible
// text, | became a table bar, *x* emphasis, \* an escape and [0] a citation,
// and code in which they were characters matched a page without them.
// Dropping links and backticks on the page let `echo whoami` match a page's
// echo `whoami`, and `x` a page's `[x](y)`. Marking code off by pattern
// paired a stray backtick in one paragraph with one in the next, and split
// links written around code ([`MERGE`](...)). A citation marker or emphasis
// inside the quoted passage on the page still keeps a quote of code from
// matching: a pair of quotation marks lost, not a quote let stand.
func literalForm(s string) string { return literalFinish(plainText(s, false)) }

// literalPiece is literalForm for a piece of a quote, read as a fragment of
// a line (plainText).
func literalPiece(s string) string { return literalFinish(plainText(s, true)) }

func literalFinish(s string) string {
	s = typography.Replace(norm.NFC.String(s))
	return spaceBeforePunct.ReplaceAllString(strings.Join(strings.Fields(s), " "), "$1")
}

// plainText is Markdown as a reader sees it rendered: prose with its links
// read as their labels, its entities decoded and its emphasis, escapes and
// HTML dropped; code as written, entities and all, as Markdown shows it
// (printf '&amp;' is not printf '&'). A fragment, a piece of a quote, gets a
// leading word so that it is never read as a list item or a heading ("1. ",
// "# "), removed after; a page is read as it is, since the word would turn
// an opening code block into prose.
func plainText(md string, fragment bool) string {
	if fragment {
		md = "x " + md
	}
	src := []byte(md)
	doc := markdownParser.Parse(text.NewReader(src))
	var sb strings.Builder
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if n.Type() == ast.TypeBlock {
				sb.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		}
		return plainNode(&sb, n, src), nil
	})
	out := strings.TrimSpace(sb.String())
	if fragment {
		out = strings.TrimPrefix(out, "x ")
	}
	return out
}

// plainNode writes what n shows and says whether to read its children.
func plainNode(sb *strings.Builder, n ast.Node, src []byte) ast.WalkStatus {
	switch t := n.(type) {
	case *ast.Text:
		// Prose is NFKC, as visibleText reads it: a PDF's "ﬁle" is "file".
		// Code is left as written.
		sb.WriteString(norm.NFKC.String(string(util.ResolveNumericReferences(util.ResolveEntityNames(util.UnescapePunctuations(t.Segment.Value(src)))))))
		if t.SoftLineBreak() || t.HardLineBreak() {
			sb.WriteByte(' ')
		}
	case *ast.String:
		sb.Write(t.Value)
	case *ast.CodeSpan:
		for c := t.FirstChild(); c != nil; c = c.NextSibling() {
			if tx, ok := c.(*ast.Text); ok {
				sb.Write(tx.Segment.Value(src))
			}
		}
		return ast.WalkSkipChildren
	case *ast.AutoLink:
		sb.Write(t.URL(src))
		return ast.WalkSkipChildren
	case *ast.RawHTML:
		return ast.WalkSkipChildren
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		for i := range n.Lines().Len() {
			seg := n.Lines().At(i)
			sb.Write(seg.Value(src))
		}
		return ast.WalkSkipChildren
	}
	return ast.WalkContinue
}
