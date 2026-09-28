package ui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// The decision: what the run may conclude from the analysis, held to the
// sources and the fact-check in code rather than by asking the models. The
// analyzer writes claims and recommendations freely, the fact-checker judges
// them, and nothing here trusts either to have kept to the rules it was given.

// Claim statuses. Only a supported claim can carry a recommendation.
const (
	statusSupported    = "supported"
	statusPartial      = "partial"
	statusContradicted = "contradicted"
	statusDisputed     = "disputed"
	statusInsufficient = "insufficient"
	// statusUnsourced is a claim that names no page the run fetched.
	statusUnsourced = "unsourced"
)

// checkAnalysis holds the analysis to what the run retrieved: a claim keeps
// only sources that are pages the run fetched, and one left with none is
// unsourced; claim IDs are unique; a conflict names only claims that exist.
// The model writes all of these freely, and an invented URL, a page it never
// saw or a dangling claim ID reached the report looking sourced.
func checkAnalysis(a *agent.Analysis, findings []agent.Finding) {
	evidence := evidenceURLs(findings)
	ids := map[string]bool{}
	for i := range a.Claims {
		c := &a.Claims[i]
		c.Sources = claimSources(c.Sources, findings, evidence)
		if len(c.Sources) == 0 {
			c.Status, c.Note = statusUnsourced, "no page the run fetched states it"
		}
		// A repeated ID made every reference to it ambiguous; references go
		// to the first claim that used it.
		if ids[c.ID] {
			c.ID = c.ID + "." + strconv.Itoa(i+1)
		}
		ids[c.ID] = true
	}
	for i := range a.Conflicts {
		a.Conflicts[i].Claims = slices.DeleteFunc(a.Conflicts[i].Claims, func(id string) bool { return !ids[id] })
	}
}

// evidenceURLs are the pages a claim may cite: the ones the run fetched. A
// run that fetched nothing (search off) has only the model's own findings,
// which the report already presents as unverified, so they all count.
func evidenceURLs(findings []agent.Finding) map[string]bool {
	retrieved := sourcesRetrieved(findings)
	out := map[string]bool{}
	for _, f := range findings {
		if f.URL != "" && (fetched(f) || !retrieved) {
			out[tools.CanonicalURL(f.URL)] = true
		}
	}
	return out
}

// claimSources keeps the sources that are evidence, in order and once each.
func claimSources(srcs []string, findings []agent.Finding, evidence map[string]bool) []string {
	var out []string
	for _, s := range srcs {
		s = resolveSource(s, findings)
		if s != "" && evidence[tools.CanonicalURL(s)] && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// resolveSource turns a source given as the prompt's entry number ("3",
// "#3") into that entry's URL; anything else is returned as it is.
func resolveSource(s string, findings []agent.Finding) string {
	if n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(s), "#")); err == nil && n >= 1 && n <= len(findings) {
		return findings[n-1].URL
	}
	return strings.TrimSpace(s)
}

// govern applies the fact-check to the analysis. Each claim takes the status
// its verdict gives it, checked: a claim with no verdict, or with two, is
// insufficient; a "supported" verdict must quote a passage that is in a page
// the run fetched. A recommendation is approved only when every claim it
// names is supported; otherwise it is blocked, and says which claim failed.
// A fact-check that did not run approves nothing.
//
// It returns the IDs the checker judged that the analysis does not contain,
// which are ignored: a verdict can only judge a claim, never add one.
func govern(a *agent.Analysis, fc *agent.FactCheckResult, fcErr error, findings []agent.Finding) (unknown []string) {
	if len(a.Claims) == 0 {
		if fc != nil {
			deriveVerdictLists(fc, nil)
		}
		return nil
	}
	verdicts := map[string][]agent.Verdict{}
	if fc != nil {
		verdicts, unknown = verdictsByClaim(a, fc)
	}
	pages := evidencePages(findings)
	for i := range a.Claims {
		if c := &a.Claims[i]; c.Status != statusUnsourced {
			c.Status, c.Note = judge(verdicts[c.ID], fc == nil, fcErr, findings, pages)
		}
	}
	blockRecommendations(a)
	if fc != nil {
		deriveVerdictLists(fc, a.Claims)
	}
	return unknown
}

// verdictsByClaim groups the verdicts by claim ID and names the IDs that are
// not the analysis's.
func verdictsByClaim(a *agent.Analysis, fc *agent.FactCheckResult) (map[string][]agent.Verdict, []string) {
	known := map[string]bool{}
	for _, c := range a.Claims {
		known[c.ID] = true
	}
	by := map[string][]agent.Verdict{}
	var unknown []string
	for _, v := range fc.Verdicts {
		if known[v.ID] {
			by[v.ID] = append(by[v.ID], v)
		} else if !slices.Contains(unknown, v.ID) {
			unknown = append(unknown, v.ID)
		}
	}
	return by, unknown
}

// judge is a claim's status and why, from the verdicts given for it.
func judge(vs []agent.Verdict, notRun bool, fcErr error, findings []agent.Finding, pages map[string]string) (string, string) {
	switch {
	case notRun:
		reason := "fact_check_unavailable"
		if fcErr != nil {
			reason += ": " + fcErr.Error()
		}
		return statusInsufficient, reason
	case len(vs) == 0:
		return statusInsufficient, "the fact-check gave no verdict for it"
	case len(vs) > 1:
		return statusInsufficient, "the fact-check gave it conflicting verdicts"
	}
	v := vs[0]
	switch v.Status {
	case statusSupported:
		if !quoteLocated(v.Evidence, findings, pages) {
			return statusInsufficient, "quote_not_located: no quoted passage is in the page it cites; " + v.Reason
		}
		return statusSupported, v.Reason
	case statusPartial, statusContradicted, statusDisputed, statusInsufficient:
		return v.Status, v.Reason
	}
	return statusInsufficient, fmt.Sprintf("the fact-check gave it an unrecognized status %q", v.Status)
}

// evidencePages maps each page a claim may cite to the text the run holds.
func evidencePages(findings []agent.Finding) map[string]string {
	ok := evidenceURLs(findings)
	pages := map[string]string{}
	for _, f := range findings {
		if key := tools.CanonicalURL(f.URL); ok[key] {
			pages[key] = f.Content
		}
	}
	return pages
}

// quoteLocated reports whether any evidence item quotes a passage that is in
// the page it names. It proves provenance only: that the words are the
// source's. Whether they support the claim is the checker's judgement.
func quoteLocated(evidence []agent.Evidence, findings []agent.Finding, pages map[string]string) bool {
	for _, e := range evidence {
		page, ok := pages[tools.CanonicalURL(resolveSource(e.Source, findings))]
		if ok && locate(page, e.Quote) {
			return true
		}
	}
	return false
}

// locate reports whether quote is a contiguous passage of page. An exact
// match comes first; failing that, both sides are compared as the text a
// reader sees, with Markdown's link targets, emphasis, code and table marks,
// escapes and typographic quotes and dashes set aside and whitespace
// collapsed. Case, numbers, word order and accents are kept: they are what a
// quote is evidence of. A paraphrase or an elided quote is not located.
func locate(page, quote string) bool {
	q := strings.TrimSpace(quote)
	if q == "" {
		return false
	}
	if strings.Contains(page, q) {
		return true
	}
	vq := visibleText(q)
	return vq != "" && strings.Contains(visibleText(page), vq)
}

var (
	mdLink     = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdPresents = strings.NewReplacer("*", "", "_", "", "`", "", "|", " ", `\`, "",
		"’", "'", "‘", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", " ", " ")
)

func visibleText(s string) string {
	s = mdPresents.Replace(mdLink.ReplaceAllString(s, "$1"))
	return strings.Join(strings.Fields(s), " ")
}

// blockRecommendations blocks every recommendation that names a claim that is
// not supported, or names none: the claims it names are its premises, and a
// recommendation standing on what is left after one of them failed is one
// the evidence did not reach.
func blockRecommendations(a *agent.Analysis) {
	byID := map[string]agent.Claim{}
	for _, c := range a.Claims {
		byID[c.ID] = c
	}
	for i := range a.Recommendations {
		r := &a.Recommendations[i]
		if len(r.Claims) == 0 {
			r.Blocked = "it names no claim to rest on"
			continue
		}
		var failed []string
		for _, id := range r.Claims {
			c, ok := byID[id]
			switch {
			case !ok:
				failed = append(failed, id+" is not a claim of the analysis")
			case c.Status != statusSupported:
				failed = append(failed, id+" "+c.Status+": "+c.Note)
			}
		}
		r.Blocked = strings.Join(failed, "; ")
	}
}

// approvedRecommendations are the recommendations the fact-check let stand.
func approvedRecommendations(a *agent.Analysis) []agent.Recommendation {
	var out []agent.Recommendation
	for _, r := range a.Recommendations {
		if r.Blocked == "" {
			out = append(out, r)
		}
	}
	return out
}

// deriveVerdictLists fills the fact-check's verified, unverified and
// contradiction lists, which the reports render, from the verdicts: from the
// claims' checked statuses when the analysis made claims, from the verdicts
// as given when it wrote prose. Output in the older list shape, with no
// verdicts, is left as it came.
func deriveVerdictLists(fc *agent.FactCheckResult, claims []agent.Claim) {
	if len(fc.Verdicts) == 0 {
		return
	}
	type entry struct{ text, status, note string }
	var entries []entry
	for _, c := range claims {
		entries = append(entries, entry{c.ID + ": " + c.Text, c.Status, c.Note})
	}
	if claims == nil {
		for _, v := range fc.Verdicts {
			entries = append(entries, entry{strings.TrimSpace(v.ID + ": " + v.Claim), v.Status, v.Reason})
		}
	}
	fc.Verified, fc.Unverified, fc.Contradictions = nil, nil, nil
	for _, e := range entries {
		switch e.status {
		case statusSupported:
			fc.Verified = append(fc.Verified, agent.VerifiedClaim{Claim: e.text, Verified: true, Evidence: e.note})
		case statusPartial:
			fc.Verified = append(fc.Verified, agent.VerifiedClaim{Claim: e.text, Verified: false, Evidence: "partial: " + e.note})
		case statusContradicted, statusDisputed:
			fc.Contradictions = append(fc.Contradictions, agent.Contradiction{Claim: e.text, Sources: []string{e.status + ": " + e.note}})
		default:
			fc.Unverified = append(fc.Unverified, e.text+" — "+e.status+": "+e.note)
		}
	}
}

// claimsToCheck is what the fact-checker verifies: the analysis's claims when
// it made them, since those are what the report is built from and each can
// be judged on its own; the answer prose otherwise.
func claimsToCheck(a *agent.Analysis) string {
	if len(a.Claims) == 0 {
		return a.Answer
	}
	var sb strings.Builder
	writeClaims(&sb, a.Claims)
	return sb.String()
}

// writeClaims lists claims one per line with what they are about and where
// they come from; a claim no retrieved page backs says so, and a checked
// claim that did not pass says how it failed.
func writeClaims(sb *strings.Builder, claims []agent.Claim) {
	for _, c := range claims {
		about := strings.Trim(c.Option+" / "+c.Criterion, " /")
		if c.Scope != "" {
			about = strings.Trim(about+"; "+c.Scope, "; ")
		}
		src := strings.Join(c.Sources, ", ")
		if src == "" {
			src = "no retrieved source"
		}
		fmt.Fprintf(sb, "  - %s [%s] %s (%s)", c.ID, about, c.Text, src)
		if c.Status != "" && c.Status != statusSupported {
			fmt.Fprintf(sb, " — %s: %s", c.Status, c.Note)
		}
		sb.WriteString("\n")
	}
}

// checkedSummarizePrompt is the summarizer's input when the analysis made
// claims: the checked decision and nothing unchecked. The analyzer's answer
// prose, its topic lists and its own conflict resolutions are left out: they
// were written before the fact-check, and a rejected conclusion in any of
// them reached the report through the summarizer.
func checkedSummarizePrompt(question string, a *agent.Analysis, fc *agent.FactCheckResult, findings []agent.Finding, retrieved bool) string {
	var sb strings.Builder
	sb.WriteString("Question: " + question + "\n\n")
	if a.Interpretation != "" {
		sb.WriteString("How the question is read: " + a.Interpretation + "\n\n")
	}
	writeRecommendations(&sb, a)
	var supported, failed []agent.Claim
	for _, c := range a.Claims {
		if c.Status == statusSupported {
			supported = append(supported, c)
		} else {
			failed = append(failed, c)
		}
	}
	if len(supported) > 0 {
		sb.WriteString("Supported claims, the only facts to state:\n")
		writeClaims(&sb, supported)
		sb.WriteString("\n")
	}
	if len(failed) > 0 {
		sb.WriteString("Claims that did not pass the fact-check. Mention them only as limits, never as fact:\n")
		writeClaims(&sb, failed)
		sb.WriteString("\n")
	}
	if fc == nil {
		sb.WriteString("The fact-check could not be completed, so no recommendation passed verification. Say so where" +
			" the answer would be, without implying the candidate recommendations are wrong.\n\n")
	}
	writeGaps(&sb, a.Gaps)
	sb.WriteString("Sources (title, URL, content) — link and quote these:\n")
	writeFindings(&sb, findings)
	if !retrieved {
		sb.WriteString(unretrievedNote)
	}
	return sb.String()
}

// writeRecommendations gives the summarizer the approved recommendations,
// which the program renders as the report's answer, and the blocked ones,
// which it may explain but never recommend.
func writeRecommendations(sb *strings.Builder, a *agent.Analysis) {
	var blocked []agent.Recommendation
	for _, r := range a.Recommendations {
		if r.Blocked != "" {
			blocked = append(blocked, r)
		}
	}
	if ok := approvedRecommendations(a); len(ok) > 0 {
		sb.WriteString("Approved recommendations. The report's \"## Answer\" section is written by the program from" +
			" these: do not write an answer section, begin with the comparison.\n")
		for _, r := range ok {
			fmt.Fprintf(sb, "  - Choose %s when %s [%s]\n", r.Choose, r.When, strings.Join(r.Claims, ", "))
		}
		sb.WriteString("\n")
	}
	if len(blocked) > 0 {
		sb.WriteString("Not concluded: a claim each rests on did not pass the fact-check. Explain them under the" +
			" limits; never recommend them:\n")
		for _, r := range blocked {
			fmt.Fprintf(sb, "  - %s when %s — %s\n", r.Choose, r.When, r.Blocked)
		}
		sb.WriteString("\n")
	}
}

// renderAnswer is the report's answer section, written from the approved
// recommendations rather than left to the summarizer: its prose could state
// a recommendation the fact-check blocked. It uses only the analyzer's own
// words, in the question's language, with no connecting words of its own.
func renderAnswer(recs []agent.Recommendation) string {
	var sb strings.Builder
	sb.WriteString("## Answer\n\n")
	for _, r := range recs {
		sb.WriteString("- **" + strings.TrimSpace(r.Choose) + "**")
		if when := strings.TrimSpace(r.When); when != "" {
			sb.WriteString(": " + when)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// withAnswer puts the rendered answer at the top of the report. A title line
// and an answer section the summarizer wrote anyway are removed: the first
// would sit below the answer, the second repeat it unchecked.
func withAnswer(sum *agent.Summary, recs []agent.Recommendation) *agent.Summary {
	if len(recs) == 0 {
		return sum
	}
	answer := renderAnswer(recs)
	sum.Report = answer + "\n" + dropAnswerSection(sum.Report)
	sum.Executive = strings.TrimSpace(strings.TrimPrefix(answer, "## Answer"))
	return sum
}

// dropAnswerSection removes a leading "# title" line and a "## Answer"
// section from a report.
func dropAnswerSection(report string) string {
	lines := strings.Split(strings.TrimSpace(report), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
		lines = lines[1:]
	}
	var out []string
	skipping := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "# ") || strings.HasPrefix(ln, "## ") {
			skipping = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(ln, "# ")), "Answer")
		}
		if !skipping {
			out = append(out, ln)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
