package main

import (
	"os"
	"testing"
)

// TestSVGIsCurrent fails when the README's diagram is not what the tables
// draw: someone edited the SVG by hand, or a table or a constant it reads
// from internal/ui changed without `make diagram`.
func TestSVGIsCurrent(t *testing.T) {
	want, err := draw()
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	got, err := os.ReadFile("../../docs/images/pipeline.svg")
	if err != nil {
		t.Fatalf("read the checked-in diagram: %v", err)
	}
	if string(got) != want {
		t.Errorf("docs/images/pipeline.svg is stale: run make diagram")
	}
}

// TestSpreadRejectsARowThatDoesNotFit pins the overflow guard: a row too
// wide for the canvas must fail, not draw boxes over each other.
func TestSpreadRejectsARowThatDoesNotFit(t *testing.T) {
	if _, err := spread([]int{400, 400, 200}); err == nil {
		t.Errorf("spread accepted 1000px of boxes on a %dpx row", right-left)
	}
	xs, err := spread([]int{100, 100})
	if err != nil {
		t.Fatalf("spread: %v", err)
	}
	if xs[0] != left || xs[1]+100 != right {
		t.Errorf("two boxes placed at %v, want them flush with %d and %d", xs, left, right)
	}
}
