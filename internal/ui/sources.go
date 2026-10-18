package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// ErrNoUsableSources is a run whose every listed domain failed the preflight.
// Researching nothing would spend the planner and every phase after it on a
// report with no sources, so the run stops before the first model call.
var ErrNoUsableSources = errors.New("none of the listed domains can be researched")

// checkSources proves each listed domain can be searched and read, and
// confines the run's search to the ones that can. A failing domain is dropped
// and named; the run goes on with the rest. It runs before planning, so a
// dead list costs no model call.
func (o Options) checkSources(ctx context.Context, sink *MultiSink, domains []string) ([]string, error) {
	sc, ok := o.Assistant.(agent.SourceScoper)
	if len(domains) == 0 {
		// An assistant used for an earlier run keeps that run's scope until
		// told otherwise, and this run's record would not say it was there.
		if ok {
			sc.ScopeTo(nil)
		}
		return nil, nil
	}
	if !ok {
		return nil, fmt.Errorf("%w: %w here", tools.ErrSearchConfig, agent.ErrNoScopedSearch)
	}
	stop := func() {}
	if !o.Quiet && !o.JSONL {
		_, stop = StatusSpinner(o.Stderr, fmt.Sprintf("Checking %d %s", len(domains), plural(len(domains), "source", "sources")))
	}
	checks, err := sc.CheckDomains(ctx, domains)
	stop()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	var usable []string
	for i := range checks {
		if o.JSONL {
			sink.Emit(Event{Type: SourceCheck, Phase: "Sources", Domain: checks[i].Domain, Status: checks[i].Status, Detail: checks[i].Detail, Check: &checks[i]})
		}
		if checks[i].Usable() {
			usable = append(usable, checks[i].Domain)
		}
	}
	if !o.Quiet && !o.JSONL {
		_, _ = fmt.Fprint(o.Stderr, sourceTable(checks, len(usable)))
	}
	if err := searchDown(checks); err != nil {
		return nil, err
	}
	if len(usable) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoUsableSources, failedSummary(checks))
	}
	sc.ScopeTo(usable)
	return usable, nil
}

// sourceTable is the preflight's result for the terminal: one line per domain
// and what the run will use.
func sourceTable(checks []agent.DomainCheck, usable int) string {
	width := 0
	for _, c := range checks {
		width = max(width, len(c.Domain))
	}
	var b strings.Builder
	b.WriteString("Sources\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "  %s %-*s  %s\n", sourceGlyph(c.Status), width, c.Domain, c.Line())
	}
	if usable > 0 && usable < len(checks) {
		fmt.Fprintf(&b, "Researching %d of %d domains.\n", usable, len(checks))
	}
	return b.String()
}

// sourceGlyph marks a domain the run uses in full, from snippets, or not at
// all.
func sourceGlyph(status string) string {
	switch status {
	case tools.DomainOK:
		return "✓"
	case tools.DomainSnippetOnly:
		return "~"
	default:
		return "✗"
	}
}

// searchDown is the run's error when the search failed on every domain: the
// sites were never asked about, so the fault is the search, and "wikipedia.org
// search-failed" sent the reader to the list while SearXNG was not running.
func searchDown(checks []agent.DomainCheck) error {
	for _, c := range checks {
		if c.SearchErr() == nil {
			return nil
		}
	}
	if len(checks) == 0 {
		return nil
	}
	return noSearch("no search answered while checking the listed sites", checks[0].SearchErr())
}

// failedSummary names each domain and why it failed, for the error a run with
// none left stops on.
func failedSummary(checks []agent.DomainCheck) string {
	parts := make([]string, len(checks))
	for i, c := range checks {
		parts[i] = c.Domain + " " + c.Status
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
