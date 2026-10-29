package agent

import (
	"fmt"
	"slices"
	"strings"
)

// ReportSpec is the shape a report is written in: a format preset and the
// targets that override it. The zero value is the standard report, so a
// plan or trace written before reports had a shape still means one. It is
// here, beside SubTopic, because the plan the UI builds and the trace the
// replay package records both carry it.
type ReportSpec struct {
	// Format is the preset: "" (standard), "brief", "detailed" or "table".
	Format string `json:"format,omitempty"`
	// Words is the explanation's approximate length; 0 leaves it to Format.
	Words int `json:"words,omitempty"`
	// Tables is how much goes in tables: "" (the format's), "none", "some"
	// or "many".
	Tables string `json:"tables,omitempty"`
}

// ReportFormats are the format presets, the standard one first.
var ReportFormats = []string{"standard", "brief", "detailed", "table"}

// ReportTables are the table amounts, the format's own (auto) first.
var ReportTables = []string{"auto", "none", "some", "many"}

// MaxReportWords bounds a word target: past it the summarizer's output limit,
// not the target, decides the length.
const MaxReportWords = 6000

// Normalized is s checked and in its one spelling: "standard" and "auto" are
// stored as "", so a default run's spec is the zero value whichever channel
// named the default, and the summarizer is sent no shape for it.
func (s ReportSpec) Normalized() (ReportSpec, error) {
	f := strings.ToLower(strings.TrimSpace(s.Format))
	if f != "" && !slices.Contains(ReportFormats, f) {
		return ReportSpec{}, fmt.Errorf("report format %q is not one of %s", s.Format, strings.Join(ReportFormats, ", "))
	}
	t := strings.ToLower(strings.TrimSpace(s.Tables))
	if t != "" && !slices.Contains(ReportTables, t) {
		return ReportSpec{}, fmt.Errorf("report tables %q is not one of %s", s.Tables, strings.Join(ReportTables, ", "))
	}
	if s.Words < 0 || s.Words > MaxReportWords {
		return ReportSpec{}, fmt.Errorf("report words %d is not between 0 (the format's) and %d", s.Words, MaxReportWords)
	}
	if f == ReportFormats[0] {
		f = ""
	}
	if t == ReportTables[0] {
		t = ""
	}
	return ReportSpec{Format: f, Words: s.Words, Tables: t}, nil
}
