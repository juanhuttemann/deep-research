package setup

import (
	"strings"
	"testing"
)

// The check's detail and fix were printed as one long line, and the
// terminal's wrap broke words mid-way ("blo / cked") and links with them,
// back at the first column under the check's name.
func TestHangWrapsUnderItsColumn(t *testing.T) {
	text := "public instances refuse automated queries; retrying will not help. Run SearXNG " +
		"in Docker: drsh setup search sets it up and prints the one command that starts it"
	got := Hang(text, 60, 13)
	rows := strings.Split(got, "\n")
	if len(rows) < 2 {
		t.Fatalf("want several rows at width 60, got %q", got)
	}
	indent := strings.Repeat(" ", 13)
	for i, row := range rows {
		if i > 0 && (!strings.HasPrefix(row, indent) || row[13] == ' ') {
			t.Errorf("row %d not indented to column 13: %q", i, row)
		}
		if w := len(strings.TrimPrefix(row, indent)); w > 60-13 {
			t.Errorf("row %d is %d wide, past the %d left of column 13: %q", i, w, 60-13, row)
		}
	}
	if strings.Join(strings.Fields(got), " ") != text {
		t.Errorf("words lost or split:\n%q\nwant\n%q", strings.Join(strings.Fields(got), " "), text)
	}
}

// A link longer than the row stays whole on its own row: split, the
// terminal no longer opens it.
func TestHangKeepsLongWordsWhole(t *testing.T) {
	link := "https://github.com/juanhuttemann/deep-research/blob/main/docs/services.md"
	got := Hang("see "+link+" for more", 40, 4)
	if !strings.Contains(got, link) {
		t.Errorf("link split: %q", got)
	}
}
