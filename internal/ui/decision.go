package ui

import (
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
	"golang.org/x/text/unicode/norm"
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
	pages := fetchedPages(findings)
	// An analysis with no claims gives its recommendations nothing to stand
	// on: they are blocked like any other that names no claim, and prose
	// verdicts are held to the same quote check before the reports list them.
	if len(a.Claims) == 0 {
		blockRecommendations(a, nil)
		if fc != nil && len(fc.Verdicts) > 0 {
			fillVerdictLists(fc, proseEntries(fc.Verdicts, findings, pages))
		}
		return nil
	}
	verdicts := map[string][]agent.Verdict{}
	var inferences map[string][]agent.Inference
	if fc != nil {
		verdicts, unknown = verdictsByClaim(a, fc)
		inferences = inferencesByRecommendation(a, fc, &unknown)
	}
	for i := range a.Claims {
		if c := &a.Claims[i]; c.Status != statusUnsourced {
			c.Status, c.Note = judge(verdicts[c.ID], fc == nil, fcErr, findings, pages)
		}
	}
	blockRecommendations(a, inferences)
	// Derived even when the checker answered in the older list shape: its
	// lists say nothing about the claims by ID, and kept as they came they
	// reported as confirmed a claim this pass had rejected.
	if fc != nil {
		fillVerdictLists(fc, claimEntries(a.Claims))
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
	case len(pages) == 0:
		return statusInsufficient, "no page was retrieved: the findings are the model's own recollection"
	case len(vs) == 0:
		return statusInsufficient, "the fact-check gave no verdict for it"
	case len(vs) > 1:
		return statusInsufficient, "the fact-check gave it conflicting verdicts"
	}
	v := vs[0]
	switch v.Status {
	case statusSupported:
		if !quotesLocated(v.Evidence, findings, pages) {
			return statusInsufficient, "quote_not_located: a quoted passage is not in the page it cites; " + v.Reason
		}
		return statusSupported, v.Reason
	case statusPartial, statusContradicted, statusDisputed, statusInsufficient:
		return v.Status, v.Reason
	}
	return statusInsufficient, fmt.Sprintf("the fact-check gave it an unrecognized status %q", v.Status)
}

// fetchedPages maps each page the run fetched to the text it holds. Unlike
// the sources a claim may cite, it has no exception for a run that fetched
// nothing: text the model wrote itself is not a page to quote from, and a
// quote located in it verified the model against its own recollection.
func fetchedPages(findings []agent.Finding) map[string]string {
	pages := map[string]string{}
	for _, f := range findings {
		if f.URL != "" && fetched(f) {
			pages[tools.CanonicalURL(f.URL)] = f.Content
		}
	}
	return pages
}

// quotesLocated reports whether there is evidence and every item quotes a
// passage that is in the page it names. Every one, not any: a genuine quote
// beside an invented one left the verdict standing on the invented passage,
// which may be the one that carried the claim. It proves provenance only,
// that the words are the source's; whether they support the claim is the
// checker's judgement.
func quotesLocated(evidence []agent.Evidence, findings []agent.Finding, pages map[string]string) bool {
	for _, e := range evidence {
		page, ok := pages[tools.CanonicalURL(resolveSource(e.Source, findings))]
		if !ok || !locate(page, e.Quote) {
			return false
		}
	}
	return len(evidence) > 0
}

// locate reports whether quote is a contiguous passage of page. An exact
// match comes first; failing that, both sides are compared as the text a
// reader sees: link targets, emphasis and code delimiters, backslash escapes
// and table bars removed, typographic quotes and dashes made plain, and
// whitespace collapsed. Only markup is removed. An underscore inside a word
// (cache_size) or a lone asterisk (2*3) is text and stays. Case, numbers,
// word order and accents are kept: they are what a quote is evidence of. A
// paraphrase or an elided quote is not located.
func locate(page, quote string) bool {
	// One accented letter can be written as one code point or two; a quote
	// copied from a page may use the other form than the stored text.
	page, quote = norm.NFC.String(page), norm.NFC.String(quote)
	q := strings.TrimSpace(quote)
	if q == "" {
		return false
	}
	if strings.Contains(page, q) {
		return true
	}
	vq, vp := visibleText(q), visibleText(page)
	if vq == "" {
		return false
	}
	if strings.Contains(vp, vq) {
		return true
	}
	// PDF text breaks words across lines with a hyphen ("capital re-\nserves"),
	// and a real hyphen can fall at a line end too ("well-\nknown"): the line
	// is joined both ways, only as a last attempt.
	for _, join := range []string{"$1$2", "$1-$2"} {
		if strings.Contains(visibleText(lineHyphen.ReplaceAllString(page, join)), vq) {
			return true
		}
	}
	return false
}

// lineHyphen is a word hyphenated across a line break.
var lineHyphen = regexp.MustCompile(`(\p{L})-\n[ \t]*(\p{Ll})`)

var (
	// A link's target may hold one level of parentheses (a_(b)), and its
	// label one pair of brackets: a citation link is "[[43]](...)".
	mdLink    = regexp.MustCompile(`!?\[((?:[^\[\]]|\[[^\]]*\])*)\]\((?:[^()\s]|\([^()\s]*\))*(?:\s+"[^"]*")?\)`)
	htmlBreak = regexp.MustCompile(`(?i)<br\s*/?>`)
	// A numbered citation marker ("[43]") sits inside the sentence it cites,
	// and a quote of that sentence leaves it out.
	mdCite     = regexp.MustCompile(`\[\d{1,4}\]`)
	mdEscape   = regexp.MustCompile("\\\\([\\\\`*_{}\\[\\]()#+\\-.!|>~])")
	mdCode     = regexp.MustCompile("`([^`]*)`")
	mdStrong   = regexp.MustCompile(`(\*\*|__)(\S(?:.*?\S)?)(\*\*|__)`)
	mdStar     = regexp.MustCompile(`(^|[^*\p{L}\p{N}])\*([^*\s](?:[^*]*[^*\s])?)\*`)
	mdUnder    = regexp.MustCompile(`(^|[^\p{L}\p{N}_])_([^_\s](?:[^_]*[^_\s])?)_($|[^\p{L}\p{N}_])`)
	plainPunct = strings.NewReplacer("|", " ", "’", "'", "‘", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", " ", " ")
)

func visibleText(s string) string {
	// Scraped text keeps HTML entities ("term &lt; currentTerm") and line
	// breaks inside table cells ("<br>") that a quote writes as what they show.
	s = html.UnescapeString(htmlBreak.ReplaceAllString(s, " "))
	// NFKC also makes a PDF's ligature characters ("ﬁ") the letters they are.
	s = norm.NFKC.String(mdEscape.ReplaceAllString(s, "$1"))
	s = mdCite.ReplaceAllString(mdLink.ReplaceAllString(s, "$1"), "")
	s = mdCode.ReplaceAllString(s, "$1")
	// Emphasis rules are applied until nothing changes: a match takes the
	// space after it, so in "_subprime_ _mortgages_" one pass freed only the
	// first word.
	for prev := ""; prev != s; {
		prev = s
		s = mdStrong.ReplaceAllString(s, "$2")
		s = mdStar.ReplaceAllString(s, "$1$2")
		s = mdUnder.ReplaceAllString(s, "$1$2$3")
	}
	return strings.Join(strings.Fields(plainPunct.Replace(s)), " ")
}

// blockRecommendations blocks every recommendation that names a claim that is
// not supported, or names none: the claims it names are its premises, and a
// recommendation standing on what is left after one of them failed is one
// the evidence did not reach. One whose premises all passed is blocked too
// unless the fact-check found it follows from them: supported claims do not
// make a conclusion follow, and before this nothing checked that it did.
// inferences is nil when there was no fact-check to judge them.
func blockRecommendations(a *agent.Analysis, inferences map[string][]agent.Inference) {
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
		var reasons []string
		r.Failed = nil
		for _, id := range r.Claims {
			c, ok := byID[id]
			switch {
			case !ok:
				reasons = append(reasons, id+" is not a claim of the analysis")
			case c.Status != statusSupported:
				reasons = append(reasons, id+" "+c.Status+": "+c.Note)
				r.Failed = append(r.Failed, id)
			}
		}
		if len(reasons) == 0 && inferences != nil {
			reasons = inferenceFailure(inferences[recommendationID(i)])
		}
		r.Blocked = strings.Join(reasons, "; ")
	}
}

// inferenceFailure is why a recommendation whose premises passed does not
// stand on the checker's judgement of it, or nothing when it does.
func inferenceFailure(vs []agent.Inference) []string {
	switch {
	case len(vs) == 0:
		return []string{"the fact-check did not judge whether it follows from its claims"}
	case len(vs) > 1:
		return []string{"the fact-check judged it more than once"}
	case !vs[0].Follows:
		return []string{"it does not follow from its claims: " + vs[0].Reason}
	}
	return nil
}

// recommendationID is the ID the fact-check knows a recommendation by: its
// place in the analysis, since the analyzer gives recommendations none.
func recommendationID(i int) string { return "r" + strconv.Itoa(i+1) }

// inferencesByRecommendation groups the checker's recommendation judgements
// by ID, adding to unknown the IDs that are not a recommendation's.
func inferencesByRecommendation(a *agent.Analysis, fc *agent.FactCheckResult, unknown *[]string) map[string][]agent.Inference {
	by := map[string][]agent.Inference{}
	for _, inf := range fc.Inferences {
		n, err := strconv.Atoi(strings.TrimPrefix(inf.ID, "r"))
		if err != nil || n < 1 || n > len(a.Recommendations) {
			*unknown = append(*unknown, inf.ID)
			continue
		}
		by[inf.ID] = append(by[inf.ID], inf)
	}
	return by
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

// verdictEntry is one line of the fact-check lists the reports render.
type verdictEntry struct{ text, status, note string }

// claimEntries are the claims with the statuses this pass gave them.
func claimEntries(claims []agent.Claim) []verdictEntry {
	var out []verdictEntry
	for _, c := range claims {
		out = append(out, verdictEntry{c.ID + ": " + c.Text, c.Status, c.Note})
	}
	return out
}

// proseEntries are the claims the checker split out of prose, each held to
// the same quote check a claim of the analysis is.
func proseEntries(vs []agent.Verdict, findings []agent.Finding, pages map[string]string) []verdictEntry {
	var out []verdictEntry
	for _, v := range vs {
		status, note := v.Status, v.Reason
		if status == statusSupported && (len(pages) == 0 || !quotesLocated(v.Evidence, findings, pages)) {
			status, note = statusInsufficient, "quote_not_located: "+v.Reason
		}
		out = append(out, verdictEntry{strings.TrimSpace(v.ID + ": " + v.Claim), status, note})
	}
	return out
}

// fillVerdictLists replaces the fact-check's verified, unverified and
// contradiction lists, which the reports render, with the given entries.
func fillVerdictLists(fc *agent.FactCheckResult, entries []verdictEntry) {
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
	if len(a.Recommendations) > 0 {
		sb.WriteString("\nRecommendations (id: choose ... when ...; the claims it rests on):\n")
		for i, r := range a.Recommendations {
			fmt.Fprintf(&sb, "  - %s: choose %s when %s [%s]\n", recommendationID(i), r.Choose, r.When, strings.Join(r.Claims, ", "))
		}
	}
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
		sb.WriteString("Claims that did not pass the fact-check. Never state them as fact: a disputed one may be" +
			" described as a disagreement where it bears on the answer, the rest belong under the limits:\n")
		writeClaims(&sb, failed)
		sb.WriteString("\n")
	}
	if fc == nil {
		sb.WriteString("The fact-check could not be completed, so nothing passed verification. Say so, without" +
			" implying the candidate recommendations are wrong.\n\n")
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
	if len(a.Recommendations) > 0 {
		sb.WriteString("The report's \"## Answer\" section is written by the program: it lists the approved" +
			" recommendations and names the others as not established. Do not write an answer or restate it:" +
			" open with a heading for the supporting explanation.\n")
		ok := approvedRecommendations(a)
		if len(ok) == 0 {
			sb.WriteString("No recommendation was approved.\n")
		}
		for _, r := range ok {
			fmt.Fprintf(sb, "  - Approved: choose %s when %s [%s]\n", r.Choose, r.When, strings.Join(r.Claims, ", "))
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

// renderAnswer is the report's answer section, written from the checked
// decision rather than left to the summarizer, whose prose could state a
// recommendation the fact-check blocked. The approved recommendations come
// first, in the analyzer's own words and the question's language. The
// blocked ones follow as not established, each with the claim that failed:
// without them a two-option question answered for one option read as a
// decisive win for it, when the other was only not verified in this run.
// They are left out when the check did not run or had nothing to check
// against, since then there is no reason per option to give.
func renderAnswer(a *agent.Analysis) string {
	var sb strings.Builder
	sb.WriteString("## Answer\n\n")
	ok := approvedRecommendations(a)
	if len(ok) == 0 {
		sb.WriteString(noApproval(a) + "\n")
	}
	for _, r := range ok {
		writeChoice(&sb, r, "")
	}
	if noApproval(a) != noApprovalEvidence {
		return sb.String()
	}
	var blocked []agent.Recommendation
	for _, r := range a.Recommendations {
		if r.Blocked != "" {
			blocked = append(blocked, r)
		}
	}
	if len(blocked) > 0 {
		sb.WriteString("\nNot established in this run:\n\n")
		for _, r := range blocked {
			writeChoice(&sb, r, notEstablished(r, a.Claims))
		}
	}
	return sb.String()
}

// writeChoice writes one recommendation as a list item, with why it is not
// established when it is not.
func writeChoice(sb *strings.Builder, r agent.Recommendation, why string) {
	sb.WriteString("- **" + strings.TrimSpace(r.Choose) + "**")
	if when := strings.TrimSpace(r.When); when != "" {
		sb.WriteString(": " + when)
	}
	if why != "" {
		sb.WriteString(" (" + why + ")")
	}
	sb.WriteString("\n")
}

// notEstablished names what blocked a recommendation: the claims that did not
// pass, by their text and status, or the checker's reason it does not follow.
func notEstablished(r agent.Recommendation, claims []agent.Claim) string {
	var parts []string
	for _, c := range claims {
		if slices.Contains(r.Failed, c.ID) {
			parts = append(parts, c.Status+": \u201c"+c.Text+"\u201d")
		}
	}
	if len(parts) == 0 {
		return r.Blocked
	}
	return strings.Join(parts, "; ")
}

// Why nothing was approved. A single "no recommendation passed" read as the
// candidates being disproved, when the check may not have run, or had no
// retrieved page to check them against.
const (
	noApprovalUnchecked = "Verification could not be completed, so the proposed conclusions remain unverified."
	noApprovalNoSources = "No source was retrieved, so the proposed conclusions could not be verified."
	noApprovalEvidence  = "The evidence gathered does not establish the proposed conclusions."
)

// noApproval is the answer when nothing was approved, naming the cause.
func noApproval(a *agent.Analysis) string {
	for _, c := range a.Claims {
		switch {
		case strings.HasPrefix(c.Note, "fact_check_unavailable"):
			return noApprovalUnchecked
		case strings.HasPrefix(c.Note, "no page was retrieved"):
			return noApprovalNoSources
		}
	}
	return noApprovalEvidence
}

// decidedAnswer is the report's answer for an analysis that made
// recommendations: the approved ones, or a statement that none passed. It is
// empty for an analysis that made none, whose answer the summarizer writes.
func decidedAnswer(a *agent.Analysis) string {
	if len(a.Recommendations) == 0 {
		return ""
	}
	return renderAnswer(a)
}

// withAnswer puts the decided answer at the top of the report. A title line
// and an answer section the summarizer wrote anyway are removed: the first
// would sit below the answer, the second repeat it unchecked. When nothing
// was approved the summarizer's answer is removed all the same: it is the
// one place a blocked recommendation would otherwise be stated.
func withAnswer(sum *agent.Summary, a *agent.Analysis) *agent.Summary {
	answer := decidedAnswer(a)
	if answer == "" {
		return sum
	}
	sum.Report = answer + "\n" + dropLeadingProse(dropAnswerSection(sum.Report))
	sum.Executive = strings.TrimSpace(strings.TrimPrefix(answer, "## Answer"))
	return sum
}

// dropAnswerSection removes a leading "# title" line and an "Answer"
// section, sub-headings included, from a report. It knows the heading the
// summarizer is told to use, in its Markdown variants ("## Answer ##",
// indented); an answer under a heading of the summarizer's own choosing, or
// a conclusion stated in another section, is beyond it.
// ponytail: heading match only; holding every section's prose to the
// blocked recommendations would need the summarizer to return structure.
func dropAnswerSection(report string) string {
	lines := strings.Split(strings.TrimSpace(report), "\n")
	if len(lines) > 0 && headingLevel(lines[0]) == 1 {
		lines = lines[1:]
	}
	var out []string
	skipLevel := 0
	for _, ln := range lines {
		if lvl := headingLevel(ln); lvl > 0 {
			switch {
			case strings.EqualFold(strings.Trim(strings.TrimSpace(ln), "# \t"), "Answer"):
				skipLevel = lvl
			case skipLevel > 0 && lvl <= skipLevel:
				skipLevel = 0
			}
		}
		if skipLevel == 0 {
			out = append(out, ln)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// dropLeadingProse removes the text before a report's first heading. The
// summarizer is told the program writes the answer and to open with a
// heading; the paragraph it wrote above that heading anyway restated the
// answer, unchecked and in its own words. A report with no heading at all is
// kept whole.
func dropLeadingProse(report string) string {
	lines := strings.Split(report, "\n")
	for i, ln := range lines {
		if headingLevel(ln) > 0 {
			return strings.Join(lines[i:], "\n")
		}
	}
	return report
}

// headingLevel is an ATX heading's level, 0 for any other line.
func headingLevel(ln string) int {
	t := strings.TrimSpace(ln)
	n := len(t) - len(strings.TrimLeft(t, "#"))
	if n == 0 || n > 6 || (len(t) > n && t[n] != ' ' && t[n] != '\t') {
		return 0
	}
	return n
}
