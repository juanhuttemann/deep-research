package ui

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// ReportSpec is the shape a report is written in. It is the agent package's
// type, so the plan and the replay trace carry the same one.
type ReportSpec = agent.ReportSpec

// reportPreset is one format: the length and table amount it asks for, and
// how it organizes the explanation.
type reportPreset struct {
	words  int    // the explanation's length; 0 leaves it to the question
	tables string // a ReportTables value; "" says nothing about tables
	shape  string // the organization, "" for the summarizer's default
	// tabular shapes are made of tables, and mean nothing beside "none".
	tabular bool
	note    string // the choice's note on every surface
}

// reportPresets are the formats by their ReportSpec.Format.
var reportPresets = map[string]reportPreset{
	"brief": {words: 300, tables: "some", note: "≈300 words",
		shape: "Keep it short: one section with the claims that decide the answer and nothing else, no separate" +
			" section on how the claims bear on it, and the limits in one or two sentences."},
	"detailed": {words: 1500, tables: "many", note: "≈1500 words",
		shape: "Go into depth: sections and sub-sections organized as the question needs, discussing each claim" +
			" you may state with its source and a quote, and the limits in full."},
	"table": {tables: "many", note: "tables first", tabular: true,
		shape: "Lead with tables: after a sentence under the first heading, the comparison in tables, prose only" +
			" to connect and qualify them. A question with no alternatives to compare gets tables of the figures," +
			" dates or positions the evidence gives, not an invented comparison."},
}

// tableLines are what each table amount asks for.
var tableLines = map[string]string{
	"none": "No tables: everything in prose.",
	"some": "At most one or two tables, only where alternatives or figures are compared.",
	"many": "A table for each set of alternatives, figures or dimensions the evidence compares.",
}

// reportShape is the summarizer's "Report shape:" block for spec, appended
// to its input; "" for the standard report, whose input is unchanged. The
// target words count the explanation only: the answer section and the
// fact-check and citation sections are the program's, whatever the length.
func reportShape(spec ReportSpec) string {
	if spec == (ReportSpec{}) {
		return ""
	}
	p := reportPresets[spec.Format]
	var lines []string
	// --format table --tables none asked the summarizer both to lead with
	// tables and to write none: the explicit amount wins, and the report is
	// organized as the question needs.
	if p.shape != "" && (!p.tabular || spec.Tables != "none") {
		lines = append(lines, p.shape)
	}
	words := spec.Words
	if words == 0 {
		words = p.words
	}
	if words > 0 {
		lines = append(lines, fmt.Sprintf("About %d words for the explanation, not counting the answer section the"+
			" program writes.", words))
	}
	// A table amount given beside the format replaces the format's own.
	if t := tableLines[cmp.Or(spec.Tables, p.tables)]; t != "" {
		lines = append(lines, t)
	}
	lines = append(lines, "Whatever the shape: open with a ## heading and a sentence of prose under it, never"+
		" a table; keep the limits that change the answer; the rules for links, quotes and which claims to state"+
		" are unchanged.")
	return "\nReport shape, to follow over the default organization:\n- " + strings.Join(lines, "\n- ") + "\n"
}
