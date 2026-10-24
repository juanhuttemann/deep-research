package ui

import (
	"regexp"
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
	// proseQuote is a double-quoted span on one line, straight or curly. It
	// has no minimum length: with one, a short quote's opening mark was
	// skipped and its closing mark taken as the next quote's opening, and the
	// quote after it went unchecked. Length is checkable's call, once the
	// marks are paired.
	proseQuote = regexp.MustCompile(`"([^"\n]*)"|“([^”\n]*)”`)
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
		`^(?i:dr|mr|mrs|ms|prof|st|jr|sr|vs|no|fig|vol|inc|ltd|co|approx)$`)
	// mdLinkTarget is a Markdown link to a web page, inline or an autolink.
	mdLinkTarget = regexp.MustCompile(`\]\(https?://|<https?://`)
	// typography is what quoting changes in a passage's characters:
	// typographic quotes, dashes and a no-break space made plain.
	typography = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", "\u00a0", " ")
	// quoteMark is any quotation mark once visibleText has made curly ones
	// straight: a quote nested in a quotation may use either.
	quoteMark = strings.NewReplacer("'", `"`)
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
	masked := proseMask(report)
	var sb strings.Builder
	n, at := 0, 0
	for _, m := range proseQuote.FindAllSubmatchIndex(masked, -1) {
		lo, hi := m[2], m[3]
		if lo < 0 {
			lo, hi = m[4], m[5]
		}
		inner := report[lo:hi]
		if !checkable(inner) || !linkedSentence(report, m[0], m[1]) || quoteFound(inner, sources) {
			continue
		}
		start, words := m[0], inner
		if m[2] >= 0 {
			start, words = dropEscapes(report, start, inner)
		}
		sb.WriteString(report[at:start])
		sb.WriteString(words)
		at = m[1]
		n++
	}
	sb.WriteString(report[at:])
	return sb.String(), n
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
// byte so offsets carry over, newlines kept: quotation marks are paired on
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
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(src))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch t := n.(type) {
		case *ast.CodeSpan, *ast.RawHTML, *ast.AutoLink:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if entering {
				copy(masked[t.Segment.Start:t.Segment.Stop], src[t.Segment.Start:t.Segment.Stop])
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
// follows opens with a capital or a digit, past any opening marks, and the
// word before it is not an abbreviation. The stop in "the U.S. agency" ended
// the sentence, and cut a quote off from the link that attributed it.
func endsSentence(s string, i, j int) bool {
	next := strings.TrimLeft(s[j:], "\"“'(*_[")
	r, _ := utf8.DecodeRuneInString(next)
	if next != "" && !unicode.IsUpper(r) && !unicode.IsDigit(r) {
		return false
	}
	return !abbreviation.MatchString(s[strings.LastIndexAny(s[:i], " \t(")+1 : i])
}

// checkable reports whether a quoted span reads as a quotation. A span that
// opens or closes on a space is the close of one quotation paired with the
// opening of the next. Bold and links inside are checked as the words they
// show: refusing a span with either let any quotation with one bold word
// stand unchecked.
func checkable(q string) bool {
	return strings.TrimSpace(q) == q && len(strings.Fields(q)) >= 6
}

// quoteFound reports whether a quote is in some fetched page. It accepts
// what a reader accepts as quoting: an elision, a bracketed edit, a capital
// the sentence needed, trailing punctuation. The words, their order and the
// numbers must still be the page's, and a quote's pieces must all be in one
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
		for _, p := range pages {
			if !literal && p.holds(pieces[0].text) || literal && p.holdsLiteral(pieces[0]) {
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

// holds reports whether one piece is on the page, with either capital.
func (p *pageText) holds(piece string) bool {
	for _, f := range []string{piece, flipFirst(piece)} {
		if p.locate(f) || p.locateLoose(f) {
			return true
		}
	}
	return false
}

// holdsLiteral is holds for a piece of a quote of code.
func (p *pageText) holdsLiteral(pc quotePiece) bool {
	t := p.literalText()
	return pc.loose != "" && (strings.Contains(t, pc.loose) || strings.Contains(t, pc.flipped))
}

// quotePiece is a run of a quote's own words between gaps.
type quotePiece struct {
	text string
	// loose and flipped are text as looseText compares it, flipped with its
	// first letter's case changed; worked out once, not at every try.
	loose, flipped string
	// near marks a piece after a bracketed edit: it starts within
	// maxEditGap bytes of the piece before it.
	near bool
}

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
// data[9]. The code itself is compared as visible text, as the page's is. A
// link is its label first: "[angle](...)" is not a bracketed edit.
func quotePieces(q string, literal bool) []quotePiece {
	q = labelLinks(q)
	masked := maskCode(q)
	var out []quotePiece
	near, at := false, 0
	add := func(text string) {
		if text = strings.Trim(text, " ,.;:!?"); text != "" {
			out = append(out, quotePiece{text: text, loose: comparable(text, literal),
				flipped: comparable(flipFirst(text), literal), near: near && len(out) > 0})
		}
	}
	for _, g := range quoteGap.FindAllIndex(masked, -1) {
		add(q[at:g[0]])
		gap := q[g[0]:g[1]]
		near = !strings.Contains(gap, "…") && !strings.Contains(gap, "...")
		at = g[1]
	}
	add(q[at:])
	return out
}

// inOrder reports whether the pieces are in the page's loose text in order,
// each after the one before it, and a near one close after it. Only the
// first piece may differ in its first letter's case. The hyphen-joined
// forms locate tries for PDF text are not tried here.
//
// A piece with its near ones is a cluster, and clusters are matched each at
// its earliest place after the one before: past an elision any later place
// will do, and the earliest leaves the most page for the rest, so nothing is
// retried. Backtracking over every place of every piece took eight seconds
// on repetitive pages, and the budget that bounded it removed a valid quote.
func (p *pageText) inOrder(pieces []quotePiece, literal bool) bool {
	text, at := p.looseText(), 0
	if literal {
		text = p.literalText()
	}
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
		for off := at; h != ""; {
			j := strings.Index(text[off:], h)
			if j < 0 {
				break
			}
			if end, ok := followNear(text, c[1:], off+j+len(h), failed); ok {
				if best < 0 || end < best {
					best = end
				}
				break
			}
			off += j + 1
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
	window := text[pos:min(len(text), pos+maxEditGap+len(pc.loose))]
	for off := 0; pc.loose != ""; {
		k := strings.Index(window[off:], pc.loose)
		if k < 0 {
			break
		}
		if end, ok := followNear(text, rest[1:], pos+off+k+len(pc.loose), failed); ok {
			return end, true
		}
		off += k + 1
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

// locateLoose is locate on the visible text with the space before
// punctuation removed and every quotation mark made one, on both sides. It is a quote check's fallback, never
// the fact-check's: a supported verdict's quote is held to locate.
func (p *pageText) locateLoose(quote string) bool {
	q := looseForm(quote)
	return q != "" && strings.Contains(p.looseText(), q)
}

// looseText is the page's visible text without the space before
// punctuation, worked out the first time it is needed.
func (p *pageText) looseText() string {
	if !p.hasLoose {
		if !p.hasVisible {
			p.visible, p.hasVisible = visibleText(p.text), true
		}
		p.loose, p.hasLoose = quoteMark.Replace(spaceBeforePunct.ReplaceAllString(p.visible, "$1")), true
	}
	return p.loose
}

// literalText is the page as a quote of code is compared (literalForm).
func (p *pageText) literalText() string {
	if !p.hasLiteral {
		p.literal, p.hasLiteral = literalForm(p.text), true
	}
	return p.literal
}

func looseForm(s string) string { return comparable(s, false) }

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
// spaces collapsed. Quotation marks are not made one, as they are for prose:
// in code print('x') is not print("x").
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
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(src))
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
		sb.Write(util.ResolveNumericReferences(util.ResolveEntityNames(util.UnescapePunctuations(t.Segment.Value(src)))))
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
