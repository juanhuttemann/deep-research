package ui

import (
	"context"
	"fmt"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// replaceUnread asks once for other pages when a search's model-named pages
// were fetched and did not load, and puts what loads into det. With web
// search off such a page is kept on the model's word, unverified: a run that
// named two papers on a publisher behind a bot check cited both, read
// neither, and the fact-check could only call every claim on them
// insufficient, which blocked all three conclusions.
//
// The pages read come first, then the replacements, then the unread pages,
// so the sub-agent's budget goes to evidence somebody read before it goes to
// the model's recollection. The unread pages stay: when nothing replaces
// them, a run with no network beyond the model still answers, as before.
//
// room is what is left of the sub-agent's budget. When the pages read fill
// it, a replacement would be fetched and never used, so none is asked for.
// The read pages go first even then: in the model's order, a refused page
// named second took the topic's last slot and the page read after it was
// never cited.
func (d *Driver) replaceUnread(ctx context.Context, query string, det *agent.ResearchDetail, id string, room int) *agent.ResearchDetail {
	if det == nil {
		return det
	}
	det = mergeReplacements(det, &agent.ResearchDetail{})
	r, ok := d.Agent.(agent.Replacer)
	if !ok || d.readNew(det) >= room {
		return det
	}
	unread := unreadSignals(det)
	if len(unread) == 0 {
		return det
	}
	d.emit(Event{Type: Info, SubID: id, Detail: fmt.Sprintf("%d %s could not be read; asking for others", len(unread), pagesNoun(len(unread)))})
	rep, err := r.ReplaceUnread(ctx, query, unread)
	if err != nil || rep == nil {
		return det
	}
	return mergeReplacements(det, rep)
}

// readNew counts det's pages that were read and are not cited yet, the ones
// that will take a slot of the budget ahead of any replacement.
func (d *Driver) readNew(det *agent.ResearchDetail) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for i, s := range det.Signals {
		if i < len(det.Findings) && (s.Status == "ok" || s.Status == "degraded") && !d.citedURLs[tools.CanonicalURL(det.Findings[i].URL)] {
			n++
		}
	}
	return n
}

// unreadSignals are det's pages that were fetched and did not load, for a
// reason other than not existing (those were dropped) or being an address the
// reader may not fetch, which no other page fixes.
func unreadSignals(det *agent.ResearchDetail) []agent.SourceSignal {
	var out []agent.SourceSignal
	for i, s := range det.Signals {
		if i < len(det.Findings) && unreadSignal(s) {
			out = append(out, s)
		}
	}
	return out
}

// unreadSignal reports a model-named page kept unverified after its fetch
// failed. An unverified signal with no code was never fetched at all (no
// reader), and asking again would fetch nothing either.
func unreadSignal(s agent.SourceSignal) bool {
	return s.Status == "unverified" && s.Code != "" && s.Code != "blocked"
}

// mergeReplacements is det with rep's pages placed after det's read pages and
// before its unverified ones.
func mergeReplacements(det, rep *agent.ResearchDetail) *agent.ResearchDetail {
	out := *det
	out.Findings, out.Signals = nil, nil
	var unreadF []agent.Finding
	var unreadS []agent.SourceSignal
	for i, f := range det.Findings {
		s := agent.SourceSignal{URL: f.URL, Domain: tools.DomainOf(f.URL), Status: "unverified"}
		if i < len(det.Signals) {
			s = det.Signals[i]
		}
		// Every page kept on the model's word goes behind the replacements,
		// not only those asked about: none of them was read.
		if s.Status == "unverified" {
			unreadF, unreadS = append(unreadF, f), append(unreadS, s)
			continue
		}
		out.Findings, out.Signals = append(out.Findings, f), append(out.Signals, s)
	}
	out.Findings = append(append(out.Findings, rep.Findings...), unreadF...)
	out.Signals = append(append(out.Signals, rep.Signals...), unreadS...)
	out.Known = append(append([]agent.SkippedSource(nil), det.Known...), rep.Known...)
	return &out
}

func pagesNoun(n int) string {
	if n == 1 {
		return "page"
	}
	return "pages"
}

// settleSpare reports how each of det's pages from the from-th on went: the
// search returned them past the sub-agent's budget, so none is cited. Their
// reads were left spinning and then removed when the topic ended, so a page
// refused with 403 there, or a replacement that loaded but did not fit, left
// no trace of what happened to it. A dropped page says so as anywhere else;
// any other is "unused", a row that names its fetch's outcome and is not
// presented as a source.
func (d *Driver) settleSpare(det *agent.ResearchDetail, from int, id string, read func(url, domain string)) {
	for i := from; i < len(det.Findings); i++ {
		f := det.Findings[i]
		s := agent.SourceSignal{URL: f.URL, Domain: tools.DomainOf(f.URL), Status: "unverified"}
		if i < len(det.Signals) {
			s = det.Signals[i]
		}
		// A URL never fetched (no reader: unverified with no code) had no
		// read and is given none: counted as one, a run read pages nobody
		// asked for.
		if s.Status != "unverified" || s.Code != "" {
			read(f.URL, s.Domain)
		}
		line := verifyLabel(s)
		if s.Status != "dropped" {
			s.Status, line = "unused", line+" · not used: budget full"
		}
		d.emit(Event{Type: Verify, URL: f.URL, Domain: s.Domain, Status: s.Status, Code: s.Code, SubID: id, Line: line})
	}
}
