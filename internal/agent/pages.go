package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

// URLs the model suggests. With no search engine, or one that
// refused or found nothing on the subject, the model is asked for URLs that
// would answer the query and the program fetches them (tools.SearchTools.Read). The model chooses
// what to read; the evidence is what the page says. A failed search used to be
// an error and nothing else, because the fallback it replaced took the
// model's account of each page as the page: invented findings, counted as
// sources. A page read here is fetched like any search result, and one that
// does not load is dropped.

// searchDetail is a search's results in the form the run takes them.
func searchDetail(query string, res *tools.SearchResults) *ResearchDetail {
	det := &ResearchDetail{
		Findings: make([]Finding, len(res.Findings)),
		Signals:  res.Signals,
		Skipped:  res.Skipped,
		Known:    res.Known,
	}
	for i, f := range res.Findings {
		det.Findings[i] = Finding{Query: query, Title: f.Title, Content: f.Content, URL: f.URL, Confidence: f.Confidence}
	}
	return det
}

// Usable reports whether det gave the run anything: a page with text, or one
// the run already has.
func (det *ResearchDetail) Usable() bool {
	if det == nil {
		return false
	}
	if len(det.Known) > 0 {
		return true
	}
	for _, s := range det.Signals {
		if s.Status != "dropped" {
			return true
		}
	}
	return false
}

// searchOrNamed runs the configured search and, when it fails or gives
// nothing on the subject, fetches the URLs the model suggests.
func (a *impl) searchOrNamed(ctx context.Context, query string, terms []string) (*ResearchDetail, error) {
	res, err := a.searchTools.Search(ctx, query, terms)
	var det *ResearchDetail
	if err == nil {
		det = searchDetail(query, res)
		if det.Usable() {
			return det, nil
		}
	}
	if ctx.Err() != nil || errors.Is(err, tools.ErrSearchConfig) {
		return det, err
	}
	why := "nothing on the subject"
	if err != nil {
		why = tools.SearchStatus(err)
	}
	a.searchTools.LogIn(ctx, fmt.Sprintf("  %-9s %s · model suggesting URLs to fetch", a.searchTools.Engine.Name(), why))
	named := a.readNamed(ctx, query, a.searchTools.Domains)
	if named == nil {
		return det, err
	}
	// A page read here was announced as it began, so it is returned however
	// it went, for the run to settle; the search's own failure travels with
	// it, for the run to count when no page loaded (see SearchErr).
	named.SearchErr = err
	if det != nil {
		named.Findings = append(det.Findings, named.Findings...)
		named.Signals = append(det.Signals, named.Signals...)
		named.Skipped = det.Skipped
		named.Known = append(det.Known, named.Known...)
	}
	return named, nil
}

// readNamed asks the model for pages that answer query and reads them, nil
// when it named none that could be read. Only what loads is kept: whatever
// the model said about a page is not evidence.
func (a *impl) readNamed(ctx context.Context, query string, domains []string) *ResearchDetail {
	out, err := a.search(ctx, a.suggestPrompt(query, domains))
	if err != nil {
		a.searchTools.LogIn(ctx, "  the model suggested no URLs: "+err.Error())
		return nil
	}
	named := parseSearchResults(out, query)
	a.noteSuggested(named)
	res := a.searchTools.Read(ctx, query, namedResults(named))
	if len(res.Findings)+len(res.Known) == 0 {
		return nil
	}
	return searchDetail(query, res)
}

// namedResults are the model's findings as URLs to fetch: their URLs and
// titles, never their text.
func namedResults(findings []Finding) []tools.SearchResult {
	out := make([]tools.SearchResult, len(findings))
	for i, f := range findings {
		out[i] = tools.SearchResult{Title: f.Title, URL: f.URL}
	}
	return out
}

// recall is research with web search off: the model suggests URLs
// and, with a reader, each is read. One that loads is a source like any
// search's. One that does not is kept as the model described it, unverified,
// as every finding was before pages were read, so that a run with no network
// beyond the model still answers; only a page its server says does not exist
// is dropped, since the model misremembered or invented it.
func (a *impl) recall(ctx context.Context, query string) (*ResearchDetail, error) {
	a.log("  llm agent searching · " + a.effortLabel(a.effort.Search))
	out, err := a.search(ctx, a.suggestPrompt(query, nil))
	if err != nil {
		return nil, err
	}
	findings := parseSearchResults(out, query)
	a.noteSuggested(findings)
	if a.reader == nil {
		return unverified(findings), nil
	}
	return recalled(findings, a.reader.Read(ctx, query, namedResults(findings))), nil
}

// unverified is the model's findings with nothing read: every source is the
// model's word.
func unverified(findings []Finding) *ResearchDetail {
	signals := make([]SourceSignal, len(findings))
	for i, f := range findings {
		// Nothing was fetched here: the model supplied both the finding and
		// its URL. Claiming "ok" made the UI render "✓ domain 200 OK" for a
		// page that was never requested, and might not exist.
		signals[i] = SourceSignal{URL: f.URL, Domain: tools.DomainOf(f.URL), Status: "unverified"}
	}
	return &ResearchDetail{Findings: findings, Signals: signals}
}

// recalled puts what reading the model's pages found onto its findings, in
// the model's order.
func recalled(findings []Finding, res *tools.SearchResults) *ResearchDetail {
	read := make(map[string]int, len(res.Findings))
	for i, f := range res.Findings {
		read[tools.CanonicalURL(f.URL)] = i
	}
	known := make(map[string]bool, len(res.Known))
	for _, k := range res.Known {
		known[tools.CanonicalURL(k.URL)] = true
	}
	det := &ResearchDetail{Known: res.Known}
	for _, f := range findings {
		key := tools.CanonicalURL(f.URL)
		if known[key] {
			continue
		}
		sig := SourceSignal{URL: f.URL, Domain: tools.DomainOf(f.URL), Status: "unverified"}
		if i, ok := read[key]; ok {
			page := res.Findings[i]
			switch s := res.Signals[i]; {
			case s.Status == "ok":
				f.Title, f.Content, sig = page.Title, page.Content, s
			case tools.PageGone(s):
				f.Content, sig = "", s
			default:
				sig.Code = s.Code
			}
		}
		det.Findings = append(det.Findings, f)
		det.Signals = append(det.Signals, sig)
	}
	return det
}

// maxAvoided bounds the URLs a suggestion prompt lists as already tried.
const maxAvoided = 40

// suggestPrompt is query as the model is asked for URLs: confined to the
// source list, and told which pages the run already has. Asked for each
// sub-topic alone, the model suggested the same best-known paper and
// nothing else for every one, and every topic after the first added no
// evidence. A page named again is not the fault: it is not fetched again,
// takes no slot of the topic's budget, and adds the topic's query to the
// page, so its excerpt covers this topic too. So the model may name a known
// page again when it is the best one for this topic, and is asked for pages
// not on the list beside it; another page of the same site is a new page.
func (a *impl) suggestPrompt(query string, domains []string) string {
	prompt := query
	if len(domains) > 0 {
		prompt += "\n\nName pages on these sites only: " + strings.Join(domains, ", ")
	}
	a.suggestedMu.Lock()
	tried := a.suggested[max(len(a.suggested)-maxAvoided, 0):]
	a.suggestedMu.Unlock()
	if len(tried) > 0 {
		prompt += "\n\nThe research already has or has tried these pages:\n- " + strings.Join(tried, "\n- ") +
			"\nName one of them again only if it is among the best pages for this query; it is not fetched again. " +
			"Most of what you name must be pages not in this list, which add evidence for this query: " +
			"other pages on the same sites count."
	}
	return prompt
}

// noteSuggested records the URLs the model suggested, once each, for later
// suggestions to know. They are recorded however their fetch goes: a URL
// that failed would fail again.
func (a *impl) noteSuggested(findings []Finding) {
	a.suggestedMu.Lock()
	defer a.suggestedMu.Unlock()
	for _, f := range findings {
		if slices.ContainsFunc(a.suggested, func(u string) bool { return tools.CanonicalURL(u) == tools.CanonicalURL(f.URL) }) {
			continue
		}
		a.suggested = append(a.suggested, f.URL)
	}
}
