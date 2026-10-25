package ui

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Minimal, dependency-free PDF writer. The report is laid out by
// pdf_layout.go into pages of positioned text, rules and link rectangles;
// this file turns those pages into a valid PDF 1.4 document: every object is
// written, the xref lists them all as in-use, and each content stream
// declares its exact byte length. The fonts are the standard Helvetica,
// Courier and Symbol faces, which every viewer has, so nothing is embedded.

// US Letter layout, in PDF user-space units (1/72").
const (
	pdfPageWidth  = 612
	pdfPageHeight = 792
	pdfMargin     = 54
)

type pdfFont int

const (
	fontRegular pdfFont = iota
	fontBold
	fontItalic
	fontBoldItalic
	fontMono
	// fontSymbol is no style of its own: text draws in it the runes its
	// style's font has no glyph for and Symbol does (see pdfGlyph).
	fontSymbol
)

// pdfFontNames are the resource order: fontRegular is /F1.
var pdfFontNames = [...]string{"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique", "Courier", "Symbol"}

// pdfLink is a clickable rectangle on a page that opens uri.
type pdfLink struct {
	x0, y0, x1, y1 float64
	uri            string
}

// pdfPage accumulates one page's content-stream operators and links.
type pdfPage struct {
	ops   strings.Builder
	links []pdfLink
}

// text draws s with its baseline starting at (x, y). Each string sets its own
// font and colour so no graphics state leaks from one to the next. A run of
// runes drawn from the Symbol font is a string of its own, placed where the
// layout measured it (pdfWidth measures each rune in the font it is drawn in).
func (p *pdfPage) text(x, y float64, f pdfFont, size float64, rgb, s string) {
	for s != "" {
		run, rest, symbol := nextFontRun(s)
		font := f
		if symbol {
			font = fontSymbol
		}
		fmt.Fprintf(&p.ops, "BT /F%d %s Tf %s rg 1 0 0 1 %s %s Tm (%s) Tj ET\n",
			font+1, pdfNum(size), rgb, pdfNum(x), pdfNum(y), escapePDF(run))
		x += pdfWidth(run, font, size)
		s = rest
	}
}

// nextFontRun splits off s's leading runes that draw in one font: the
// Symbol font when symbol is true, the text's own otherwise.
func nextFontRun(s string) (run, rest string, symbol bool) {
	for i, r := range s {
		_, sym, _ := pdfGlyph(r)
		if i == 0 {
			symbol = sym
		} else if sym != symbol {
			return s[:i], s[i:], symbol
		}
	}
	return s, "", symbol
}

// fill paints a grey rectangle whose lower-left corner is (x, y).
func (p *pdfPage) fill(x, y, w, h, gray float64) {
	fmt.Fprintf(&p.ops, "q %s g %s %s %s %s re f Q\n", pdfNum(gray), pdfNum(x), pdfNum(y), pdfNum(w), pdfNum(h))
}

// rule strokes a horizontal line at y from x0 to x1.
func (p *pdfPage) rule(x0, x1, y, gray, width float64) {
	fmt.Fprintf(&p.ops, "q %s G %s w %s %s m %s %s l S Q\n",
		pdfNum(gray), pdfNum(width), pdfNum(x0), pdfNum(y), pdfNum(x1), pdfNum(y))
}

// pdfNum formats a coordinate with at most two decimals and no exponent.
func pdfNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// BuildPDF lays out a Markdown report and returns the PDF bytes.
func BuildPDF(markdown string) []byte {
	pages := layoutPDF(parsePDFBlocks(markdown))
	for i, p := range pages {
		label := fmt.Sprintf("%d / %d", i+1, len(pages))
		p.text((pdfPageWidth-pdfWidth(label, fontRegular, 8))/2, pdfMargin/2, fontRegular, 8, pdfGrayText, label)
	}
	return writePDF(pages)
}

// writePDF serializes laid-out pages. Object numbers are allocated as the
// objects are added; the catalog and page tree are reserved first because
// they must point at objects that do not exist yet.
func writePDF(pages []*pdfPage) []byte {
	var objects []string
	add := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}
	catalog, tree := add(""), add("")
	var fonts []string
	for i, name := range pdfFontNames {
		// Symbol's glyphs are reachable only through its built-in encoding:
		// WinAnsi names Latin glyphs it does not have.
		encoding := " /Encoding /WinAnsiEncoding"
		if pdfFont(i) == fontSymbol {
			encoding = ""
		}
		n := add("<< /Type /Font /Subtype /Type1 /BaseFont /" + name + encoding + " >>")
		fonts = append(fonts, fmt.Sprintf("/F%d %d 0 R", i+1, n))
	}
	resources := "<< /Font << " + strings.Join(fonts, " ") + " >> >>"
	kids := make([]string, len(pages))
	for i, p := range pages {
		contents := add(contentStream(p.ops.String()))
		var annots []string
		for _, l := range p.links {
			annots = append(annots, fmt.Sprintf("%d 0 R", add(linkAnnotation(l))))
		}
		extra := ""
		if len(annots) > 0 {
			extra = " /Annots [" + strings.Join(annots, " ") + "]"
		}
		kids[i] = fmt.Sprintf("%d 0 R", add(fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d] /Resources %s /Contents %d 0 R%s >>",
			tree, pdfPageWidth, pdfPageHeight, resources, contents, extra)))
	}
	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", tree)
	objects[tree-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	xrefOff := buf.Len()
	size := len(objects) + 1 // object 0 is the head of the free list
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", size)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\n", size, catalog)
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// contentStream wraps a page's operators as a stream object body.
func contentStream(body string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body)
}

func linkAnnotation(l pdfLink) string {
	return fmt.Sprintf("<< /Type /Annot /Subtype /Link /Rect [%s %s %s %s] /Border [0 0 0] /A << /S /URI /URI (%s) >> >>",
		pdfNum(l.x0), pdfNum(l.y0), pdfNum(l.x1), pdfNum(l.y1), escapeURI(l.uri))
}

// escapeURI writes a URI action's string, which must be 7-bit ASCII:
// anything else is percent-encoded byte by byte, as a browser would send it.
func escapeURI(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '(' || c == ')' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c > 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

// winAnsi maps the typographic runes the report text actually uses onto their
// WinAnsiEncoding slots. WinAnsi is Latin-1 except for 0x80-0x9F, so anything
// else in Latin-1 passes through as its own byte; the rest is drawn from the
// Symbol font when it has the glyph (symbolCodes), and as '?' when not.
// The fact-check marks have no glyph in either and get the nearest that
// prints, rather than a '?' on every unverified claim.
var winAnsi = map[rune]byte{
	'€': 0x80, '‚': 0x82, '„': 0x84, '…': 0x85,
	'†': 0x86, '‡': 0x87, '‰': 0x89, '‹': 0x8b,
	'‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94,
	'•': 0x95, '–': 0x96, '—': 0x97, '™': 0x99,
	'›': 0x9b, '✗': 0xd7, '⚠': '!',
}

// symbolCodes maps the Greek letters and mathematical signs WinAnsi lacks
// onto their slots in the Symbol font's built-in encoding, by glyph name as
// the Adobe Glyph List gives each. A report on π printed "How ?'s decimal
// digits are produced" with a '?' for each of its thirty π. The arrows and
// comparison signs were spelled out ("->", "<=") before, and ≈ became a "~"
// a reader cannot tell from a tilde.
var symbolCodes = map[rune]byte{
	'Α': 0x41, 'Β': 0x42, 'Χ': 0x43, 'Δ': 0x44, 'Ε': 0x45, 'Φ': 0x46, 'Γ': 0x47, 'Η': 0x48,
	'Ι': 0x49, 'Κ': 0x4b, 'Λ': 0x4c, 'Μ': 0x4d, 'Ν': 0x4e, 'Ο': 0x4f, 'Π': 0x50, 'Θ': 0x51,
	'Ρ': 0x52, 'Σ': 0x53, 'Τ': 0x54, 'Υ': 0x55, 'Ω': 0x57, 'Ξ': 0x58, 'Ψ': 0x59, 'Ζ': 0x5a,
	'α': 0x61, 'β': 0x62, 'χ': 0x63, 'δ': 0x64, 'ε': 0x65, 'φ': 0x66, 'γ': 0x67, 'η': 0x68,
	'ι': 0x69, 'ϕ': 0x6a, 'κ': 0x6b, 'λ': 0x6c, 'μ': 0x6d, 'ν': 0x6e, 'ο': 0x6f, 'π': 0x70,
	'θ': 0x71, 'ρ': 0x72, 'σ': 0x73, 'τ': 0x74, 'υ': 0x75, 'ϖ': 0x76, 'ω': 0x77, 'ξ': 0x78,
	'ψ': 0x79, 'ζ': 0x7a, 'ς': 0x56, 'ϑ': 0x4a, 'ϒ': 0xa1,
	'∀': 0x22, '∃': 0x24, '∋': 0x27, '∗': 0x2a, '−': 0x2d, '≅': 0x40, '∴': 0x5c, '⊥': 0x5e,
	'∼': 0x7e, '′': 0xa2, '≤': 0xa3, '⁄': 0xa4, '∕': 0xa4, '∞': 0xa5, '♣': 0xa7, '♦': 0xa8,
	'♥': 0xa9, '♠': 0xaa, '↔': 0xab, '←': 0xac, '↑': 0xad, '→': 0xae, '↓': 0xaf, '″': 0xb2,
	'≥': 0xb3, '∝': 0xb5, '∂': 0xb6, '≠': 0xb9, '≡': 0xba, '≈': 0xbb, '↵': 0xbf, 'ℵ': 0xc0,
	'ℑ': 0xc1, 'ℜ': 0xc2, '℘': 0xc3, '⊗': 0xc4, '⊕': 0xc5, '∅': 0xc6, '∩': 0xc7, '∪': 0xc8,
	'⊃': 0xc9, '⊇': 0xca, '⊄': 0xcb, '⊂': 0xcc, '⊆': 0xcd, '∈': 0xce, '∉': 0xcf, '∠': 0xd0,
	'∇': 0xd1, '∆': 0x44, '∏': 0xd5, '√': 0xd6, '⋅': 0xd7, '∧': 0xd9, '∨': 0xda, '⇔': 0xdb,
	'⇐': 0xdc, '⇑': 0xdd, '⇒': 0xde, '⇓': 0xdf, '◊': 0xe0, '〈': 0xe1, '⟨': 0xe1, '∑': 0xe5,
	'〉': 0xf1, '⟩': 0xf1, '∫': 0xf2,
}

// escapePDF encodes a string as a PDF literal string, each rune as its byte
// in the font it draws in (pdfGlyph): text calls it on one font's run.
// Non-ASCII bytes are written as octal escapes rather than raw: a raw UTF-8
// sequence is read byte-per-glyph by the viewer and renders as mojibake.
func escapePDF(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r >= 0x20 && r < 0x7f:
			sb.WriteRune(r)
		default:
			b, _, _ := pdfGlyph(r)
			fmt.Fprintf(&sb, "\\%03o", b)
		}
	}
	return sb.String()
}

// pdfByte is r's byte in WinAnsiEncoding, and false when it has none.
func pdfByte(r rune) (byte, bool) {
	if b, ok := winAnsi[r]; ok {
		return b, true
	}
	if r >= 0x20 && r <= 0xff {
		return byte(r), true
	}
	return 0, false
}

// pdfGlyph is the byte the PDF draws for r, and whether that byte is in the
// Symbol font rather than the text's own; ok is false when neither has r,
// which then draws as the text font's '?'. A letter or digit styled as
// mathematics ("𝜋", "𝐱") is drawn as itself: a model writing formulas
// uses them, and they mean the plain letter set in italic or bold.
func pdfGlyph(r rune) (b byte, symbol, ok bool) {
	if r >= 0x1d400 && r <= 0x1d7ff { // Mathematical Alphanumeric Symbols
		if plain := []rune(norm.NFKC.String(string(r))); len(plain) == 1 {
			r = plain[0]
		}
	}
	if b, ok := pdfByte(r); ok {
		return b, false, true
	}
	if b, ok := symbolCodes[r]; ok {
		return b, true, true
	}
	return '?', false, false
}

// pdfHasUnsupportedText reports whether escapePDF would write a '?' for any of
// text. It asks pdfGlyph, as escapePDF does, so accented Latin, the mapped
// punctuation and the Symbol font's glyphs never warn. Whitespace is skipped:
// layout draws every kind as a plain space, or as a line break, before a
// line is encoded.
func pdfHasUnsupportedText(text string) bool {
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if _, _, ok := pdfGlyph(r); !ok {
			return true
		}
	}
	return false
}

// pdfWidth is the advance width of s in points, measured glyph by glyph as
// the viewer will draw it: a rune drawn from the Symbol font has Symbol's
// width whatever f is.
func pdfWidth(s string, f pdfFont, size float64) float64 {
	units := 0
	for _, r := range s {
		b, symbol, _ := pdfGlyph(r)
		switch {
		case symbol:
			units += int(symbolWidths[b-0x20])
		case f == fontMono:
			units += 600
		case f == fontBold, f == fontBoldItalic:
			units += int(helveticaBoldWidths[b-0x20])
		default:
			units += int(helveticaWidths[b-0x20])
		}
	}
	return float64(units) * size / 1000
}

// Advance widths in 1/1000 em for WinAnsi codes 0x20-0xFF, taken by glyph
// name from the URW Nimbus Sans AFMs (urw-base35), which are metric-
// compatible with Helvetica. The obliques share their upright widths.
var helveticaWidths = [224]uint16{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, 0,
	556, 0, 222, 556, 333, 1000, 556, 556, 333, 1000, 667, 333, 1000, 0, 611, 0,
	0, 222, 222, 333, 333, 350, 556, 1000, 333, 1000, 500, 333, 944, 0, 500, 667,
	278, 333, 556, 556, 556, 556, 260, 556, 333, 737, 370, 556, 584, 333, 737, 333,
	400, 584, 333, 333, 333, 556, 537, 278, 333, 333, 365, 556, 834, 834, 834, 611,
	667, 667, 667, 667, 667, 667, 1000, 722, 667, 667, 667, 667, 278, 278, 278, 278,
	722, 722, 778, 778, 778, 778, 778, 584, 778, 722, 722, 722, 722, 667, 667, 611,
	556, 556, 556, 556, 556, 556, 889, 500, 556, 556, 556, 556, 278, 278, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 584, 611, 556, 556, 556, 556, 500, 556, 500,
}

var helveticaBoldWidths = [224]uint16{
	278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584, 0,
	556, 0, 278, 556, 500, 1000, 556, 556, 333, 1000, 667, 333, 1000, 0, 611, 0,
	0, 278, 278, 500, 500, 350, 556, 1000, 333, 1000, 556, 333, 944, 0, 500, 667,
	278, 333, 556, 556, 556, 556, 280, 556, 333, 737, 370, 556, 584, 333, 737, 333,
	400, 584, 333, 333, 333, 611, 556, 278, 333, 333, 365, 556, 834, 834, 834, 611,
	722, 722, 722, 722, 722, 722, 1000, 722, 667, 667, 667, 667, 278, 278, 278, 278,
	722, 722, 778, 778, 778, 778, 778, 584, 778, 722, 722, 722, 722, 667, 667, 611,
	556, 556, 556, 556, 556, 556, 889, 556, 556, 556, 556, 556, 278, 278, 278, 278,
	611, 611, 611, 611, 611, 611, 611, 584, 611, 611, 611, 611, 611, 556, 611, 556,
}

// symbolWidths are the Symbol font's advance widths in 1/1000 em for codes
// 0x20-0xFF of its built-in encoding, from the URW Standard Symbols PS AFM
// (urw-base35), which is metric-compatible with Symbol. Codes with no glyph
// are 0; symbolCodes maps no rune onto them.
var symbolWidths = [224]uint16{
	250, 333, 713, 500, 549, 833, 778, 439, 333, 333, 500, 549, 250, 549, 250, 278,
	500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 278, 278, 549, 549, 549, 444,
	549, 722, 667, 722, 612, 611, 763, 603, 722, 333, 631, 722, 686, 889, 722, 722,
	768, 741, 556, 592, 611, 690, 439, 768, 645, 795, 611, 333, 863, 333, 658, 500,
	500, 631, 549, 549, 494, 439, 521, 411, 603, 329, 603, 549, 549, 576, 521, 549,
	549, 521, 549, 603, 439, 576, 713, 686, 493, 686, 494, 480, 200, 480, 549, 0,
	790, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	750, 620, 247, 549, 167, 713, 500, 753, 753, 753, 753, 1042, 987, 603, 987, 603,
	400, 549, 411, 549, 549, 713, 494, 460, 549, 549, 549, 549, 1000, 603, 1000, 658,
	823, 686, 795, 987, 768, 768, 823, 768, 768, 713, 713, 713, 713, 713, 713, 713,
	768, 713, 790, 790, 890, 823, 549, 250, 713, 603, 603, 1042, 987, 603, 987, 603,
	494, 329, 790, 790, 786, 713, 384, 384, 384, 384, 384, 384, 494, 494, 494, 494,
	0, 329, 274, 686, 686, 686, 384, 384, 384, 384, 384, 384, 494, 494, 494, 0,
}
