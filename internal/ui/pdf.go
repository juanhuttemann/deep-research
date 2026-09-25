package ui

import (
	"bytes"
	"fmt"
	"strings"
)

// Minimal, dependency-free PDF writer. It streams word-wrapped text lines in a
// single Helvetica font, paginated so a long report is not clipped at the
// bottom of page one. The output is a valid PDF 1.4 document: every reserved
// object is written, the xref lists them all as in-use, and each content
// stream declares its exact byte length.

// US Letter layout, in PDF user-space units (1/72").
const (
	pdfLeading      = 14 // baseline-to-baseline distance, i.e. the T* step
	pdfFontSize     = 11
	pdfLeftMargin   = 50
	pdfTopBaseline  = 740 // first baseline, measured from the bottom edge
	pdfBottomMargin = 50
	pdfLinesPerPage = (pdfTopBaseline - pdfBottomMargin) / pdfLeading
)

// BuildPDF turns wrapped text lines into a PDF document and returns the bytes.
//
// Object layout: 1 catalog, 2 page tree, 3 font, then two objects per page
// (the page itself, then its content stream).
func BuildPDF(lines []string) []byte {
	const (
		catalogObj = 1
		pagesObj   = 2
		fontObj    = 3
		firstPage  = 4
	)
	pageObj := func(i int) int { return firstPage + 2*i }

	pages := paginate(lines, pdfLinesPerPage)
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", pageObj(i))
	}

	objects := []string{
		fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesObj),
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}
	for i, page := range pages {
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] "+
				"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
				pagesObj, fontObj, pageObj(i)+1),
			contentStream(page))
	}

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
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\n", size, catalogObj)
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// paginate splits lines into pages of at most n lines. An empty document still
// gets one (blank) page: a page tree with no kids is not a valid PDF.
func paginate(lines []string, n int) [][]string {
	if len(lines) == 0 {
		return [][]string{nil}
	}
	var pages [][]string
	for len(lines) > n {
		pages = append(pages, lines[:n])
		lines = lines[n:]
	}
	return append(pages, lines)
}

// contentStream renders one page's text as a content-stream object body.
func contentStream(lines []string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "BT\n/F1 %d Tf\n%d TL\n%d %d Td\n",
		pdfFontSize, pdfLeading, pdfLeftMargin, pdfTopBaseline)
	for _, ln := range lines {
		s.WriteString("(" + escapePDF(ln) + ") Tj\nT*\n")
	}
	s.WriteString("ET")
	body := s.String()
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body)
}

// winAnsi maps the typographic runes the report text actually uses onto their
// WinAnsiEncoding slots. WinAnsi is Latin-1 except for 0x80-0x9F, so anything
// else in Latin-1 passes through as its own byte and the rest becomes '?'.
var winAnsi = map[rune]byte{
	'€': 0x80, '‚': 0x82, '„': 0x84, '…': 0x85,
	'†': 0x86, '‡': 0x87, '‰': 0x89, '‹': 0x8b,
	'‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94,
	'•': 0x95, '–': 0x96, '—': 0x97, '™': 0x99,
	'›': 0x9b,
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
			b, ok := winAnsi[r]
			if !ok {
				if r > 0xff || r < 0x20 {
					b = '?'
				} else {
					b = byte(r)
				}
			}
			fmt.Fprintf(&sb, "\\%03o", b)
		}
	}
	return sb.String()
}

// wrapText breaks s onto lines of at most width display columns for the PDF,
// preserving the blank lines that separate the report's paragraphs. The
// per-paragraph wrapping is wrapWords, the same word-boundary wrap the live
// frame uses; cutting at an exact rune count instead split words in half
// ("supply-chain" became "su" / "pply-chain") all through the exports.
func wrapText(s string, width int) []string {
	if width <= 0 {
		width = 75
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		if strings.TrimSpace(paragraph) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapWords(paragraph, width)...)
	}
	return out
}
