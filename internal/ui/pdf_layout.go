package ui

import (
	"fmt"
	"html"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The PDF is laid out from the report's Markdown. goldmark parses it into
// blocks of styled runs, and the layout wraps those runs by their measured
// Helvetica widths, so headings, lists, tables and links keep their shape.
// Flattening the Markdown to plain text and wrapping it at a character count
// is what made the earlier export one ragged wall of text.

// Type sizes, leading and spacing, in points.
const (
	pdfBodySize    = 10.5
	pdfBodyLeading = 14.5
	pdfTableSize   = 9
	pdfTableLead   = 11.5
	pdfCodeSize    = 8.5
	pdfCodeLead    = 11.5
	pdfIndentStep  = 18
	pdfCellPad     = 4
	pdfCellPadY    = 3
	pdfNoteScale   = 0.82 // a printed link destination, relative to its text
	pdfTop         = pdfPageHeight - pdfMargin
)

// Colours, as PDF "r g b" operands.
const (
	pdfInk      = "0.13 0.13 0.13"
	pdfHeadInk  = "0.08 0.15 0.27"
	pdfLinkInk  = "0.1 0.33 0.66"
	pdfGrayText = "0.42 0.42 0.42"
)

var pdfHeadingSizes = [...]float64{1: 20, 2: 14.5, 3: 12, 4: 11, 5: 10.5, 6: 10.5}

// pdfRun is a piece of inline text and how it is drawn.
type pdfRun struct {
	text               string
	bold, italic, mono bool
	url                string // the link the text opens
	note               bool   // a printed link destination: smaller and grey
}

func (r pdfRun) font() pdfFont {
	switch {
	case r.mono:
		return fontMono
	case r.bold && r.italic:
		return fontBoldItalic
	case r.bold:
		return fontBold
	case r.italic:
		return fontItalic
	}
	return fontRegular
}

// sameStyle reports whether two runs draw alike, so one string can carry both.
func sameStyle(a, b pdfRun) bool {
	a.text, b.text = "", ""
	return a == b
}

type pdfBlockKind int

const (
	blockPara pdfBlockKind = iota
	blockHeading
	blockCode
	blockRule
	blockTable
)

type pdfBlock struct {
	kind   pdfBlockKind
	level  int    // heading level
	indent int    // list and quote nesting
	marker string // list marker, drawn in the indent of the item's first line
	runs   []pdfRun
	rows   [][][]pdfRun // table rows of cells, header first
}

// pdfParser walks the Markdown AST into blocks. A list item contributes one
// block per paragraph, the first carrying its marker, so nested lists and
// continuation paragraphs keep their indent without a tree of their own.
type pdfParser struct {
	src          []byte
	blocks       []pdfBlock
	cur          int // index of the block receiving inline runs, -1 for none
	indent       int
	marker       string
	bold, italic int
	mono         bool
	url          string
	linkText     strings.Builder
	linksLeft    map[string]int // links to each destination not yet walked
}

func parsePDFBlocks(markdown string) []pdfBlock {
	src := []byte(markdown)
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(src))
	p := &pdfParser{src: src, cur: -1, linksLeft: map[string]int{}}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if l, ok := n.(*ast.Link); ok && entering {
			p.linksLeft[linkDestination(l)]++
		}
		return ast.WalkContinue, nil
	})
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			return p.enter(n), nil
		}
		p.leave(n)
		return ast.WalkContinue, nil
	})
	return p.blocks
}

func (p *pdfParser) enter(n ast.Node) ast.WalkStatus {
	switch n := n.(type) {
	case *ast.Heading:
		p.open(pdfBlock{kind: blockHeading, level: n.Level})
	case *ast.Paragraph, *ast.TextBlock:
		p.open(pdfBlock{kind: blockPara})
	case *ast.List, *ast.Blockquote:
		p.indent++
	case *ast.ListItem:
		p.marker = listMarker(n, p.indent)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		p.code(n)
	case *ast.ThematicBreak:
		p.open(pdfBlock{kind: blockRule})
		p.cur = -1
	case *extast.Table:
		p.open(pdfBlock{kind: blockTable})
	case *extast.TableHeader, *extast.TableRow:
		b := &p.blocks[p.cur]
		b.rows = append(b.rows, nil)
	case *extast.TableCell:
		row := &p.blocks[p.cur].rows[len(p.blocks[p.cur].rows)-1]
		*row = append(*row, nil)
	case *ast.HTMLBlock, *ast.RawHTML:
		return ast.WalkSkipChildren
	default:
		p.enterInline(n)
	}
	return ast.WalkContinue
}

func (p *pdfParser) enterInline(n ast.Node) {
	switch n := n.(type) {
	case *ast.Text:
		p.addText(n)
	case *ast.String:
		p.add(string(n.Value))
	case *ast.Emphasis:
		p.emphasis(n.Level, 1)
	case *ast.CodeSpan:
		p.mono = true
	case *ast.Link:
		p.url = linkDestination(n)
		p.linkText.Reset()
	case *ast.AutoLink:
		p.url = string(n.URL(p.src))
		p.add(string(n.Label(p.src)))
		p.url = ""
	case *extast.TaskCheckBox:
		p.add(map[bool]string{true: "[x] ", false: "[ ] "}[n.IsChecked])
	}
}

func (p *pdfParser) leave(n ast.Node) {
	switch n := n.(type) {
	case *ast.Heading, *ast.Paragraph, *ast.TextBlock, *extast.Table:
		p.cur = -1
	case *ast.List, *ast.Blockquote:
		p.indent--
	case *ast.Emphasis:
		p.emphasis(n.Level, -1)
	case *ast.CodeSpan:
		p.mono = false
	case *ast.Link:
		p.closeLink()
	}
}

// linkDestination is the URL a link spells. goldmark keeps the destination
// as written, Markdown escapes and entity references included; its own
// HTML renderer resolves them the same way.
func linkDestination(l *ast.Link) string {
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(l.Destination))))
}

// closeLink prints the destination after the link text, so a paper copy
// keeps it, once per destination and at its last link. Every source the
// body links to is linked again in the citation list closing the report,
// so the URL is printed there rather than crowding the paragraphs and table
// cells that cite it; a link made nowhere else prints it in place.
func (p *pdfParser) closeLink() {
	u := p.url
	p.url = ""
	p.linksLeft[u]--
	if u != "" && p.linksLeft[u] == 0 && strings.TrimSpace(p.linkText.String()) != u {
		p.addRun(pdfRun{text: " (" + u + ")", url: u, note: true})
	}
}

func (p *pdfParser) open(b pdfBlock) {
	b.indent, b.marker, p.marker = p.indent, p.marker, ""
	p.blocks = append(p.blocks, b)
	p.cur = len(p.blocks) - 1
}

func (p *pdfParser) emphasis(level, delta int) {
	if level >= 2 {
		p.bold += delta
	} else {
		p.italic += delta
	}
}

func (p *pdfParser) addText(n *ast.Text) {
	value := n.Value(p.src)
	s := string(value)
	if !n.IsRaw() {
		s = html.UnescapeString(string(util.UnescapePunctuations(value)))
	}
	switch {
	case n.HardLineBreak():
		s += "\n"
	case n.SoftLineBreak():
		s += " "
	}
	p.add(s)
}

func (p *pdfParser) add(s string) {
	if p.url != "" {
		p.linkText.WriteString(s)
	}
	p.addRun(pdfRun{text: s, bold: p.bold > 0, italic: p.italic > 0, mono: p.mono, url: p.url})
}

// pdfDisplay is parsed text as the PDF can draw it: emoji left out. An emoji
// is decoration (a source titled "Top 20 Raspberry Pi Projects of 2025🤯!");
// drawn as '?' it made the run warn that the PDF lost text. It is done to parsed text, never to the Markdown, whose
// syntax and link targets a rune changed in the source would change.
func pdfDisplay(s string) string {
	return strings.Map(func(r rune) rune {
		if isEmoji(r) {
			return -1
		}
		return r
	}, s)
}

// pdfBMPEmoji are the emoji outside the pictograph blocks that reports and
// page titles use. The blocks they sit in also hold symbols that mean
// something ("C♯", "★"), which still warn: only these are dropped.
var pdfBMPEmoji = map[rune]bool{
	'✅': true, '❌': true, '✨': true, '⭐': true, '⚡': true, '❗': true, '❓': true,
	'⭕': true, '⌛': true, '⏳': true, '☕': true, '⚽': true, '⛔': true, '✋': true,
}

// isEmoji reports whether r is an emoji, or a joiner, selector or keycap
// mark that composes one.
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF, // pictographs, emoticons, transport, skin tones
		r >= 0x1F1E6 && r <= 0x1F1FF,          // regional indicators: flags
		r == 0xFE0F, r == 0x200D, r == 0x20E3: // emoji presentation, joiner, keycap
		return true
	}
	return pdfBMPEmoji[r]
}

func (p *pdfParser) addRun(r pdfRun) {
	r.text = pdfDisplay(r.text)
	if p.cur < 0 {
		p.open(pdfBlock{kind: blockPara})
	}
	b := &p.blocks[p.cur]
	if b.kind == blockTable {
		row := b.rows[len(b.rows)-1]
		row[len(row)-1] = append(row[len(row)-1], r)
		return
	}
	b.runs = append(b.runs, r)
}

// code keeps a code block's lines verbatim, indentation included.
func (p *pdfParser) code(n ast.Node) {
	var sb strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		sb.Write(line.Value(p.src))
	}
	src := strings.ReplaceAll(strings.TrimRight(sb.String(), "\r\n"), "\t", "    ")
	// Any other whitespace prints as a space, and a CRLF file's \r not at all,
	// as the wrapped text does; pdfHasUnsupportedText relies on it.
	src = strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return -1
		case r != '\n' && unicode.IsSpace(r):
			return ' '
		}
		return r
	}, src)
	p.open(pdfBlock{kind: blockCode, runs: []pdfRun{{text: pdfDisplay(src), mono: true}}})
	p.cur = -1
}

// listMarker numbers an ordered item from its list's start, as the Markdown
// does; citations are referred to by those numbers.
func listMarker(item *ast.ListItem, depth int) string {
	list, ok := item.Parent().(*ast.List)
	if !ok || !list.IsOrdered() {
		if depth%2 == 0 {
			return "–"
		}
		return "•"
	}
	n := list.Start
	for s := item.PreviousSibling(); s != nil; s = s.PreviousSibling() {
		n++
	}
	return fmt.Sprintf("%d%c", n, list.Marker)
}

// pdfDropsText reports whether the PDF of markdown prints a '?' for any of
// its text, judged on the same runs the layout draws.
func pdfDropsText(markdown string) bool {
	for _, b := range parsePDFBlocks(markdown) {
		cells := [][]pdfRun{b.runs}
		for _, row := range b.rows {
			cells = append(cells, row...)
		}
		for _, runs := range cells {
			for _, r := range runs {
				if pdfHasUnsupportedText(r.text) {
					return true
				}
			}
		}
	}
	return false
}

// pdfAtom is a word, or the piece of a URL between break points, in one style.
type pdfAtom struct {
	pdfRun
	space bool // whitespace precedes it
	brk   bool // a line may break before it
}

func (a pdfAtom) size(base float64) float64 {
	if a.note {
		return base * pdfNoteScale
	}
	return base
}

func (a pdfAtom) width(base float64) float64 { return pdfWidth(a.text, a.font(), a.size(base)) }

func (a pdfAtom) spaceWidth(base float64) float64 { return pdfWidth(" ", a.font(), a.size(base)) }

func (a pdfAtom) ink(def string) string {
	switch {
	case a.note:
		return pdfGrayText
	case a.url != "":
		return pdfLinkInk
	}
	return def
}

// pdfAtoms splits runs into words. A word that spans runs ("sup**ply**")
// becomes atoms glued together, so a line never breaks inside it.
func pdfAtoms(runs []pdfRun) []pdfAtom {
	var out []pdfAtom
	space := false
	for _, r := range runs {
		var word strings.Builder
		flush := func() {
			if word.Len() > 0 {
				out = appendWord(out, r, word.String(), space)
				space = false
				word.Reset()
			}
		}
		for _, c := range r.text {
			switch {
			case c == '\n':
				flush()
				out = append(out, pdfAtom{pdfRun: pdfRun{text: "\n"}, brk: true})
				space = false
			case unicode.IsSpace(c):
				flush()
				space = true
			default:
				word.WriteRune(c)
			}
		}
		flush()
	}
	return out
}

// appendWord adds a word, breakable at its own separators when it is a URL,
// which is otherwise longer than many a line.
func appendWord(out []pdfAtom, r pdfRun, word string, space bool) []pdfAtom {
	pieces := []string{word}
	if r.url != "" || strings.Contains(word, "://") {
		pieces = splitURL(word)
	}
	for i, piece := range pieces {
		r.text = piece
		out = append(out, pdfAtom{pdfRun: r, space: space && i == 0, brk: space || i > 0})
	}
	return out
}

// splitURL cuts after each path or query separator that a letter or digit
// follows, so never inside "//" or a %HH escape nor before a closing
// parenthesis, and inserts nothing: the pieces rejoin to the exact URL.
func splitURL(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s)-1; i++ {
		if strings.IndexByte("/-_?&=", s[i]) >= 0 && isAlnum(s[i+1]) {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// wrapAtoms breaks atoms greedily into lines no wider than width.
func wrapAtoms(atoms []pdfAtom, width, size float64) [][]pdfAtom {
	var lines [][]pdfAtom
	var line []pdfAtom
	used := 0.0
	for i := 0; i < len(atoms); {
		if atoms[i].text == "\n" {
			lines, line, used = append(lines, line), nil, 0
			i++
			continue
		}
		box := nextBox(atoms[i:])
		w, gap := boxWidth(box, size), 0.0
		if len(line) > 0 && box[0].space {
			gap = box[0].spaceWidth(size)
		}
		switch {
		case len(line) > 0 && used+gap+w > width:
			lines, line, used = append(lines, line), nil, 0
			continue // the box opens the next line
		case len(line) == 0 && w > width:
			cut := cutBox(box, width, size)
			lines, line = append(lines, cut[:len(cut)-1]...), cut[len(cut)-1]
			used = boxWidth(line, size)
		default:
			line, used = append(line, box...), used+gap+w
		}
		i += len(box)
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}
	return lines
}

// nextBox is the leading run of atoms no line may break inside.
func nextBox(atoms []pdfAtom) []pdfAtom {
	j := 1
	for j < len(atoms) && !atoms[j].brk {
		j++
	}
	return atoms[:j]
}

func boxWidth(box []pdfAtom, size float64) float64 {
	w := 0.0
	for _, a := range box {
		w += a.width(size)
	}
	return w
}

// cutBox breaks a box wider than a whole line at characters, the last resort
// for a word with no separator to break at.
func cutBox(box []pdfAtom, width, size float64) [][]pdfAtom {
	var lines [][]pdfAtom
	var line []pdfAtom
	used := 0.0
	for _, a := range box {
		var piece []rune
		for _, c := range a.text {
			cw := pdfWidth(string(c), a.font(), a.size(size))
			if used+cw > width && (len(line) > 0 || len(piece) > 0) {
				lines = append(lines, appendPiece(line, a, piece))
				line, piece, used = nil, nil, 0
			}
			piece, used = append(piece, c), used+cw
		}
		line = appendPiece(line, a, piece)
	}
	return append(lines, line)
}

func appendPiece(line []pdfAtom, a pdfAtom, piece []rune) []pdfAtom {
	if len(piece) == 0 {
		return line
	}
	a.text = string(piece)
	return append(line, a)
}

// pdfLayout places blocks top to bottom, starting a page when one is full.
type pdfLayout struct {
	pages    []*pdfPage
	page     *pdfPage
	y        float64 // top of the free space on the page
	indented bool    // the previous block was a list item or a quote
}

func layoutPDF(blocks []pdfBlock) []*pdfPage {
	l := &pdfLayout{}
	l.newPage()
	for _, b := range blocks {
		l.block(b)
	}
	return l.pages
}

func (l *pdfLayout) newPage() {
	l.page = &pdfPage{}
	l.pages = append(l.pages, l.page)
	l.y = pdfTop
}

// space leaves a vertical gap, except at the top of a page.
func (l *pdfLayout) space(gap float64) {
	if l.y < pdfTop {
		l.y -= gap
	}
}

// room starts a new page unless h points fit above the bottom margin.
func (l *pdfLayout) room(h float64) {
	if l.y-h < pdfMargin && l.y < pdfTop {
		l.newPage()
	}
}

func (l *pdfLayout) block(b pdfBlock) {
	left := pdfMargin + float64(b.indent)*pdfIndentStep
	if l.indented && b.indent == 0 {
		l.space(4) // a list ends with an item's tight gap; open it up
	}
	l.indented = b.indent > 0
	switch b.kind {
	case blockHeading:
		l.heading(b, left)
	case blockCode:
		l.code(b, left)
	case blockRule:
		l.space(6)
		l.room(12)
		l.page.rule(left, pdfPageWidth-pdfMargin, l.y-6, 0.8, 0.5)
		l.y -= 12
		l.space(6)
	case blockTable:
		l.table(b, left)
	default:
		l.paragraph(b, left)
	}
}

func (l *pdfLayout) paragraph(b pdfBlock, left float64) {
	lines := wrapAtoms(pdfAtoms(b.runs), pdfPageWidth-pdfMargin-left, pdfBodySize)
	if len(lines) <= 3 {
		l.room(float64(len(lines)) * pdfBodyLeading) // no lone line atop the next page
	}
	for i, line := range lines {
		l.room(pdfBodyLeading)
		base := l.y - pdfBodyLeading + 0.25*pdfBodySize
		if i == 0 && b.marker != "" {
			x := left - 4 - pdfWidth(b.marker, fontRegular, pdfBodySize)
			l.page.text(x, base, fontRegular, pdfBodySize, pdfInk, b.marker)
		}
		l.drawLine(line, left, base, pdfBodySize, pdfInk)
		l.y -= pdfBodyLeading
	}
	if b.indent > 0 {
		l.space(3)
	} else {
		l.space(7)
	}
}

func (l *pdfLayout) heading(b pdfBlock, left float64) {
	size := pdfHeadingSizes[min(b.level, 6)]
	lead := size * 1.3
	l.space(size * 0.9)
	lines := wrapAtoms(pdfAtoms(bolded(b.runs)), pdfPageWidth-pdfMargin-left, size)
	// Keep the heading with the first lines of what it heads.
	l.room(float64(len(lines))*lead + 3*pdfBodyLeading)
	for _, line := range lines {
		l.room(lead)
		l.drawLine(line, left, l.y-lead+0.25*size, size, pdfHeadInk)
		l.y -= lead
	}
	if b.level <= 2 {
		l.y -= 3
		l.page.rule(left, pdfPageWidth-pdfMargin, l.y, 0.75, 0.6)
	}
	l.space(size * 0.45)
}

func bolded(runs []pdfRun) []pdfRun {
	out := make([]pdfRun, len(runs))
	for i, r := range runs {
		r.bold = true
		out[i] = r
	}
	return out
}

// code draws a block verbatim on a grey band, cutting lines too long for the
// page at the character rather than reflowing them.
func (l *pdfLayout) code(b pdfBlock, left float64) {
	width := pdfPageWidth - pdfMargin - left
	perLine := max(1, int((width-2*pdfCellPad)/(0.6*pdfCodeSize)))
	l.space(2)
	for _, src := range strings.Split(b.runs[0].text, "\n") {
		r := []rune(src)
		for start := 0; start == 0 || start < len(r); start += perLine {
			l.room(pdfCodeLead)
			l.page.fill(left, l.y-pdfCodeLead, width, pdfCodeLead, 0.95)
			part := string(r[start:min(start+perLine, len(r))])
			l.page.text(left+pdfCellPad, l.y-pdfCodeLead+0.3*pdfCodeSize, fontMono, pdfCodeSize, pdfInk, part)
			l.y -= pdfCodeLead
		}
	}
	l.space(9)
}

// drawLine draws one wrapped line, one string per run of same-styled atoms,
// and records a link rectangle over each linked stretch.
func (l *pdfLayout) drawLine(line []pdfAtom, left, base, size float64, ink string) {
	xs := make([]float64, len(line))
	x := left
	for i, a := range line {
		if i > 0 && a.space {
			x += a.spaceWidth(size)
		}
		xs[i] = x
		x += a.width(size)
	}
	for i := 0; i < len(line); {
		var s strings.Builder
		s.WriteString(line[i].text)
		j := i + 1
		for ; j < len(line) && sameStyle(line[j].pdfRun, line[i].pdfRun); j++ {
			if line[j].space {
				s.WriteByte(' ')
			}
			s.WriteString(line[j].text)
		}
		a := line[i]
		l.page.text(xs[i], base, a.font(), a.size(size), a.ink(ink), s.String())
		i = j
	}
	for i := 0; i < len(line); {
		j := i + 1
		for j < len(line) && line[j].url == line[i].url {
			j++
		}
		if u := line[i].url; u != "" {
			end := xs[j-1] + line[j-1].width(size)
			l.page.links = append(l.page.links, pdfLink{xs[i], base - 0.25*size, end, base + 0.85*size, u})
		}
		i = j
	}
}

// pdfTable is a table being drawn: its column edges and its header row,
// which is repeated at the top of every page the table continues on.
type pdfTable struct {
	left   float64
	widths []float64
	header pdfTableRow
}

// pdfTableRow holds each cell's wrapped lines.
type pdfTableRow [][][]pdfAtom

func (r pdfTableRow) lines() int {
	n := 1
	for _, cell := range r {
		n = max(n, len(cell))
	}
	return n
}

func (r pdfTableRow) height() float64 {
	return float64(r.lines())*pdfTableLead + 2*pdfCellPadY
}

func (l *pdfLayout) table(b pdfBlock, left float64) {
	cols := 0
	for _, row := range b.rows {
		cols = max(cols, len(row))
	}
	if cols == 0 {
		return
	}
	cells := make([][][]pdfAtom, len(b.rows))
	for i, row := range b.rows {
		cells[i] = make([][]pdfAtom, cols)
		for c, runs := range row {
			if i == 0 {
				runs = bolded(runs)
			}
			cells[i][c] = pdfAtoms(runs)
		}
	}
	t := &pdfTable{left: left, widths: columnWidths(cells, pdfPageWidth-pdfMargin-left)}
	rows := make([]pdfTableRow, len(cells))
	for i, row := range cells {
		rows[i] = make(pdfTableRow, cols)
		for c, atoms := range row {
			rows[i][c] = wrapAtoms(atoms, t.widths[c]-2*pdfCellPad, pdfTableSize)
		}
	}
	t.header = rows[0]
	l.space(2)
	first := 0.0
	if len(rows) > 1 {
		first = min(rows[1].height(), 3*pdfTableLead)
	}
	l.room(t.header.height() + first)
	for i, r := range rows {
		l.tableRow(t, r, i == 0)
	}
	l.space(10)
}

// columnWidths gives every column its longest word, up to an even share of
// the width, then shares the rest in proportion to how much more text each
// column holds, which keeps the rows from growing tall in one narrow column.
// Capping the word at an even share keeps the needs within the width, so no
// column is squeezed below its padding; a longer word is cut by cutBox.
func columnWidths(cells [][][]pdfAtom, width float64) []float64 {
	cols := len(cells[0])
	share := width/float64(cols) - 2*pdfCellPad
	want, need := make([]float64, cols), make([]float64, cols)
	for _, row := range cells {
		for c, atoms := range row {
			w, word := atomExtent(atoms, pdfTableSize)
			want[c] = max(want[c], min(w, width)+2*pdfCellPad)
			need[c] = max(need[c], min(word, share)+2*pdfCellPad)
		}
	}
	sumWant, sumNeed := 0.0, 0.0
	for c := range want {
		sumWant += want[c]
		sumNeed += need[c]
	}
	out := make([]float64, cols)
	for c := range out {
		switch {
		case sumWant <= width:
			out[c] = want[c] + (width-sumWant)/float64(cols)
		default:
			out[c] = need[c] + (width-sumNeed)*(want[c]-need[c])/(sumWant-sumNeed)
		}
	}
	return out
}

// atomExtent is the width of the atoms set on one line, and of the widest
// box among them, which no column can be narrower than.
func atomExtent(atoms []pdfAtom, size float64) (line, widest float64) {
	for i := 0; i < len(atoms); {
		box := nextBox(atoms[i:])
		w := boxWidth(box, size)
		if box[0].space {
			line += box[0].spaceWidth(size)
		}
		line, widest = line+w, max(widest, w)
		i += len(box)
	}
	return line, widest
}

// tableRow draws a row, moving it whole to a new page when it fits on one
// and splitting it by lines only when it is taller than a page.
func (l *pdfLayout) tableRow(t *pdfTable, r pdfTableRow, header bool) {
	if l.y-r.height() < pdfMargin && r.height() <= pdfTop-pdfMargin-t.header.height() {
		l.tableBreak(t, !header)
	}
	for from, n := 0, r.lines(); from < n; {
		fit := int((l.y - pdfMargin - 2*pdfCellPadY) / pdfTableLead)
		if fit < 1 {
			l.tableBreak(t, !header)
			continue
		}
		to := min(n, from+fit)
		l.rowSlice(t, r, from, to, header)
		from = to
	}
}

// tableBreak continues the table on a new page, under its header again. A
// header taller than a third of a page is not repeated, so the row that
// broke always finds room to make progress.
func (l *pdfLayout) tableBreak(t *pdfTable, repeatHeader bool) {
	l.newPage()
	if repeatHeader && t.header.height() < (pdfTop-pdfMargin)/3 {
		l.rowSlice(t, t.header, 0, t.header.lines(), true)
	}
}

// rowSlice draws lines [from, to) of every cell of a row and rules under it.
func (l *pdfLayout) rowSlice(t *pdfTable, r pdfTableRow, from, to int, header bool) {
	h := float64(to-from)*pdfTableLead + 2*pdfCellPadY
	right := t.left
	for _, w := range t.widths {
		right += w
	}
	if header {
		l.page.fill(t.left, l.y-h, right-t.left, h, 0.93)
	}
	x := t.left
	for c, lines := range r {
		y := l.y - pdfCellPadY
		for _, line := range lines[min(from, len(lines)):min(to, len(lines))] {
			l.drawLine(line, x+pdfCellPad, y-pdfTableLead+0.25*pdfTableSize, pdfTableSize, pdfInk)
			y -= pdfTableLead
		}
		x += t.widths[c]
	}
	l.y -= h
	gray, width := 0.82, 0.5
	if header {
		gray, width = 0.55, 0.8
	}
	l.page.rule(t.left, right, l.y, gray, width)
}
