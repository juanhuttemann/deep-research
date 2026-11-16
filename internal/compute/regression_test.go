package compute

import (
	"strings"
	"testing"
)

func TestLongChartTitleWrapsAbovePlot(t *testing.T) {
	title := "Share of all 2024 Stack Overflow survey respondents who used the language in the past year (confirmed for two of the eight languages asked about)"
	shapes := Layout(Chart{Title: title, Unit: "% of respondents", Kind: KindBar, Points: []Point{{Label: "JavaScript", Value: 62.3, Text: "62.3"}, {Label: "Python", Value: 51, Text: "51"}}}, 504, 252)
	var lines []string
	lastTitleY, unitY, firstGridY := 0.0, 0.0, 1000.0
	for _, s := range shapes {
		switch s.Role {
		case RoleTitle:
			lines = append(lines, s.Text)
			lastTitleY = s.Y
		case RoleUnit:
			unitY = s.Y
		case RoleGrid, RoleAxis:
			if s.Y < firstGridY {
				firstGridY = s.Y
			}
		}
	}
	if len(lines) < 2 || strings.Join(lines, " ") != title {
		t.Fatalf("long title not wrapped intact: %q", lines)
	}
	if unitY <= lastTitleY || firstGridY <= unitY {
		t.Fatalf("header overlaps plot: title=%v unit=%v grid=%v", lastTitleY, unitY, firstGridY)
	}
}
