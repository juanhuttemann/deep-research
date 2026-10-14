package ui

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Minimal, dependency-free PDF writer. The report is laid out by
// pdf_layout.go into pages of positioned text, rules and link rectangles;
// this file turns those pages into a valid PDF 1.4 document: every object is
// written, the xref lists them all as in-use, and each content stream
// declares its exact byte length. The fonts are the standard Helvetica and
// Courier faces, which every viewer has, so nothing is embedded.

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
)

// pdfFontNames are the resource order: fontRegular is /F1.
var pdfFontNames = [...]string{"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique", "Courier"}

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
// font and colour so no graphics state leaks from one to the next.
func (p *pdfPage) text(x, y float64, f pdfFont, size float64, rgb, s string) {
	fmt.Fprintf(&p.ops, "BT /F%d %s Tf %s rg 1 0 0 1 %s %s Tm (%s) Tj ET\n",
		f+1, pdfNum(size), rgb, pdfNum(x), pdfNum(y), escapePDF(s))
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
		n := add("<< /Type /Font /Subtype /Type1 /BaseFont /" + name + " /Encoding /WinAnsiEncoding >>")
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
// else in Latin-1 passes through as its own byte and the rest becomes '?'.
// The fact-check marks have no WinAnsi glyph and get the nearest that
// prints, rather than a '?' on every unverified claim.
var winAnsi = map[rune]byte{
	'€': 0x80, '‚': 0x82, '„': 0x84, '…': 0x85,
	'†': 0x86, '‡': 0x87, '‰': 0x89, '‹': 0x8b,
	'‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94,
	'•': 0x95, '–': 0x96, '—': 0x97, '™': 0x99,
	'›': 0x9b, '✗': 0xd7, '⚠': '!',
}

// escapePDF encodes a string as a PDF literal string in WinAnsiEncoding.
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
			fmt.Fprintf(&sb, "\\%03o", pdfGlyph(r))
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

// pdfGlyph is the byte the PDF draws for r: its own, or '?' when it has none.
func pdfGlyph(r rune) byte {
	if b, ok := pdfByte(r); ok {
		return b
	}
	return '?'
}

// pdfHasUnsupportedText reports whether escapePDF would write a '?' for any of
// text. It asks pdfByte, as escapePDF does, so accented Latin and the mapped
// punctuation never warn. Whitespace is skipped: layout draws every kind as a
// plain space, or as a line break, before a line is encoded.
func pdfHasUnsupportedText(text string) bool {
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if _, ok := pdfByte(r); !ok {
			return true
		}
	}
	return false
}

// pdfWidth is the advance width of s in points, measured glyph by glyph as
// the viewer will draw it.
func pdfWidth(s string, f pdfFont, size float64) float64 {
	units := 0
	for _, r := range s {
		b := pdfGlyph(r)
		switch f {
		case fontMono:
			units += 600
		case fontBold, fontBoldItalic:
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
