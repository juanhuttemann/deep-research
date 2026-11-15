package ui

import (
	"fmt"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/compute"
)

// A chart in the PDF is drawn from the run's own charts, as vectors, from
// the same shapes the SVG is: an image in the report is drawn only when its
// name is a chart the run drew (drawnCharts), never read from a file, so a
// report's link cannot make the export embed anything else.

// Chart inks, as PDF "r g b" operands: the SVG's light set.
const (
	pdfChartSeries = "0.165 0.471 0.839" // #2a78d6
	pdfChartGrid   = "0.882 0.878 0.851" // #e1e0d9
	pdfChartAxis   = "0.765 0.761 0.718" // #c3c2b7
	pdfChartMuted  = "0.537 0.529 0.506" // #898781
	pdfChartText   = "0.322 0.318 0.306" // #52514e
)

// pdfChartAspect is a chart's height for its width.
const pdfChartAspect = 0.5

// stroke draws a line through pts.
func (p *pdfPage) stroke(pts [][2]float64, rgb string, width float64) {
	if len(pts) < 2 {
		return
	}
	fmt.Fprintf(&p.ops, "q %s RG %s w 1 J 1 j %s %s m", rgb, pdfNum(width), pdfNum(pts[0][0]), pdfNum(pts[0][1]))
	for _, pt := range pts[1:] {
		fmt.Fprintf(&p.ops, " %s %s l", pdfNum(pt[0]), pdfNum(pt[1]))
	}
	p.ops.WriteString(" S Q\n")
}

// fillRGB paints a coloured rectangle whose lower-left corner is (x, y).
func (p *pdfPage) fillRGB(x, y, w, h float64, rgb string) {
	fmt.Fprintf(&p.ops, "q %s rg %s %s %s %s re f Q\n", rgb, pdfNum(x), pdfNum(y), pdfNum(w), pdfNum(h))
}

// disc paints a filled circle of radius r at (cx, cy), as four Béziers.
func (p *pdfPage) disc(cx, cy, r float64, rgb string) {
	const k = 0.5523
	n := func(v float64) string { return pdfNum(v) }
	fmt.Fprintf(&p.ops, "q %s rg %s %s m %s %s %s %s %s %s c %s %s %s %s %s %s c %s %s %s %s %s %s c %s %s %s %s %s %s c f Q\n",
		rgb, n(cx+r), n(cy),
		n(cx+r), n(cy+k*r), n(cx+k*r), n(cy+r), n(cx), n(cy+r),
		n(cx-k*r), n(cy+r), n(cx-r), n(cy+k*r), n(cx-r), n(cy),
		n(cx-r), n(cy-k*r), n(cx-k*r), n(cy-r), n(cx), n(cy-r),
		n(cx+k*r), n(cy-r), n(cx+r), n(cy-k*r), n(cx+r), n(cy))
}

// image is an image in the report: a chart the run drew becomes a block of
// its own; anything else keeps its alt text, as before.
func (p *pdfParser) image(dest string) bool {
	c, ok := p.charts[strings.TrimSpace(dest)]
	if !ok {
		return false
	}
	p.blocks = append(p.blocks, pdfBlock{kind: blockChart, chart: &c, indent: p.indent})
	p.cur = -1
	return true
}

// chart draws a chart block at the full text width.
func (l *pdfLayout) chart(b pdfBlock, left float64) {
	w := pdfPageWidth - pdfMargin - left
	h := w * pdfChartAspect
	l.space(6)
	l.room(h)
	top := l.y
	at := func(x, y float64) (float64, float64) { return left + x, top - y }
	for _, s := range compute.Layout(*b.chart, w, h) {
		l.page.chartShape(s, at)
	}
	l.y -= h
	l.space(6)
}

func (p *pdfPage) chartShape(s compute.Shape, at func(x, y float64) (float64, float64)) {
	switch s.Kind {
	case compute.ShapeText:
		font, ink := fontRegular, pdfChartMuted
		switch s.Role {
		case compute.RoleTitle:
			font, ink = fontBold, pdfInk
		case compute.RoleValue:
			ink = pdfChartText
		}
		text := pdfDisplay(s.Text)
		x, y := at(s.X, s.Y)
		switch w := pdfWidth(text, font, s.Size); s.Anchor {
		case "middle":
			x -= w / 2
		case "end":
			x -= w
		}
		p.text(x, y, font, s.Size, ink, text)
	case compute.ShapeLine:
		ink := pdfChartGrid
		if s.Role == compute.RoleAxis {
			ink = pdfChartAxis
		}
		x0, y0 := at(s.X, s.Y)
		x1, y1 := at(s.X2, s.Y2)
		p.stroke([][2]float64{{x0, y0}, {x1, y1}}, ink, 0.5)
	case compute.ShapeRect:
		x, y := at(s.X, s.Y+s.H)
		p.fillRGB(x, y, s.W, s.H, pdfChartSeries)
	case compute.ShapePath:
		pts := make([][2]float64, len(s.Points))
		for i, pt := range s.Points {
			pts[i][0], pts[i][1] = at(pt[0], pt[1])
		}
		p.stroke(pts, pdfChartSeries, 1.5)
	case compute.ShapeDot:
		x, y := at(s.X, s.Y)
		p.disc(x, y, s.W*0.75, pdfChartSeries)
	}
}
