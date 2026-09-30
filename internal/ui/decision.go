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
		blockUnits(a, nil)
		if fc != nil && len(fc.Verdicts) > 0 {
			fillVerdictLists(fc, proseEntries(fc.Verdicts, findings, pages))
		}
		return nil
	}
	verdicts := map[string][]agent.Verdict{}
	var inferences map[string][]agent.Inference
	if fc != nil {
		verdicts, unknown = verdictsByClaim(a, fc)
		inferences = inferencesByUnit(a, fc, &unknown)
	}
	for i := range a.Claims {
		if c := &a.Claims[i]; c.Status != statusUnsourced {
			c.Status, c.Note = judge(verdicts[c.ID], fc == nil, fcErr, findings, pages)
		}
	}
	blockUnits(a, inferences)
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

// unit is one statement of the answer the fact-check governs: a
// recommendation (r1, r2, ...) or a conclusion (k1, k2, ...), known by its
// place in the analysis, since the analyzer gives them no IDs.
type unit struct {
	id string
	r  *agent.Recommendation
}

// units are the analysis's recommendations, then its conclusions.
func units(a *agent.Analysis) []unit {
	var out []unit
	for i := range a.Recommendations {
		out = append(out, unit{"r" + strconv.Itoa(i+1), &a.Recommendations[i]})
	}
	for i := range a.Conclusions {
		out = append(out, unit{"k" + strconv.Itoa(i+1), &a.Conclusions[i]})
	}
	return out
}

// unitByID finds a unit by its ID.
func unitByID(a *agent.Analysis, id string) (unit, bool) {
	for _, u := range units(a) {
		if u.id == id {
			return u, true
		}
	}
	return unit{}, false
}

// statement is how a unit reads to the fact-check and the repair pass.
func statement(r agent.Recommendation) string {
	if r.Statement != "" {
		return "conclude: " + r.Statement
	}
	return "choose " + r.Choose + " when " + r.When
}

// blockUnits blocks every unit that names a claim that is not supported, or
// names none: the claims it names are its premises, and a conclusion
// standing on what is left after one of them failed is one the evidence did
// not reach. One whose premises all passed is blocked too unless the
// fact-check found it follows from them: supported claims do not make a
// conclusion follow. inferences is nil when there was no fact-check.
func blockUnits(a *agent.Analysis, inferences map[string][]agent.Inference) {
	byID := map[string]agent.Claim{}
	for _, c := range a.Claims {
		byID[c.ID] = c
	}
	for _, u := range units(a) {
		r := u.r
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
			reasons = inferenceFailure(inferences[u.id])
		}
		r.Blocked = strings.Join(reasons, "; ")
	}
}

// inferenceFailure is why a unit whose premises passed does not stand on the
// checker's judgement of it, or nothing when it does.
func inferenceFailure(vs []agent.Inference) []string {
	switch {
	case len(vs) == 0:
		return []string{"the fact-check did not judge whether it follows from its claims"}
	case len(vs) > 1:
		return []string{"the fact-check judged it more than once"}
	case !vs[0].Follows:
		return []string{followPrefix + vs[0].Reason}
	}
	return nil
}

// inferencesByUnit groups the checker's judgements by unit ID, adding to
// unknown the IDs that are not a unit's.
func inferencesByUnit(a *agent.Analysis, fc *agent.FactCheckResult, unknown *[]string) map[string][]agent.Inference {
	known := map[string]bool{}
	for _, u := range units(a) {
		known[u.id] = true
	}
	by := map[string][]agent.Inference{}
	for _, inf := range fc.Inferences {
		if !known[inf.ID] {
			*unknown = append(*unknown, inf.ID)
			continue
		}
		by[inf.ID] = append(by[inf.ID], inf)
	}
	return by
}

// approvedUnits are the recommendations and conclusions the fact-check let
// stand, recommendations first.
func approvedUnits(a *agent.Analysis) []agent.Recommendation {
	var out []agent.Recommendation
	for _, u := range units(a) {
		if u.r.Blocked == "" {
			out = append(out, *u.r)
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
	writePremisePackets(&sb, a, nil)
	sb.WriteString("Claims:\n")
	writeClaims(&sb, a.Claims)
	return sb.String()
}

// writePremisePackets lists the units, each followed by the full text of the
// claims it names and nothing else, for the fact-check to judge the
// inference on. Listed by ID at the end of the claims, the premises were
// dozens of lines away among thirty-six claims and pages of evidence, and
// the check approved a recommendation that turned its rule's "and" into
// "or" in five runs out of eleven. only limits the list to those IDs.
func writePremisePackets(sb *strings.Builder, a *agent.Analysis, only []string) {
	byID := map[string]agent.Claim{}
	for _, c := range a.Claims {
		byID[c.ID] = c
	}
	wrote := false
	for _, u := range units(a) {
		if only != nil && !slices.Contains(only, u.id) {
			continue
		}
		if !wrote {
			sb.WriteString("Recommendations and conclusions to judge, each with the premises it names:\n")
			wrote = true
		}
		fmt.Fprintf(sb, "  - %s: %s\n", u.id, statement(*u.r))
		for _, id := range u.r.Claims {
			if c, ok := byID[id]; ok {
				scope := ""
				if c.Scope != "" {
					scope = " [scope: " + c.Scope + "]"
				}
				fmt.Fprintf(sb, "      premise %s: %s%s\n", id, c.Text, scope)
			} else {
				fmt.Fprintf(sb, "      premise %s: (not a claim of the analysis)\n", id)
			}
		}
	}
	if wrote {
		sb.WriteString("\n")
	}
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
	writeDecided(&sb, a)
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

// writeDecided tells the summarizer the program writes the answer, from the
// approved recommendations and conclusions, and names the ones not
// established, which it may explain but never assert.
func writeDecided(sb *strings.Builder, a *agent.Analysis) {
	sb.WriteString("The report's \"## " + label(a, "answer") + "\" section is written by the program: it states the" +
		" approved recommendations and conclusions and names the others as not established. Do not write an answer" +
		" or restate it: open with a heading for the supporting explanation.\n")
	ok := approvedUnits(a)
	if len(ok) == 0 {
		sb.WriteString("Nothing was approved.\n")
	}
	for _, r := range ok {
		fmt.Fprintf(sb, "  - Approved: %s [%s]\n", statement(r), strings.Join(r.Claims, ", "))
	}
	sb.WriteString("\n")
	if blocked := notEstablishedUnits(a); len(blocked) > 0 {
		sb.WriteString("Not established: a claim each rests on did not pass the fact-check, or it does not follow" +
			" from them. Explain them under the limits; never assert them:\n")
		for _, r := range blocked {
			fmt.Fprintf(sb, "  - %s — %s\n", statement(r), r.Blocked)
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
	sb.WriteString("## " + label(a, "answer") + "\n\n")
	ok := approvedUnits(a)
	switch {
	case len(units(a)) == 0:
		sb.WriteString(label(a, "no_conclusion") + "\n")
	case len(ok) == 0:
		sb.WriteString(label(a, noApproval(a)) + "\n")
	}
	for _, r := range ok {
		writeChoice(&sb, r, "")
	}
	if noApproval(a) != noApprovalEvidence {
		return sb.String()
	}
	blocked := notEstablishedUnits(a)
	if len(blocked) > 0 {
		sb.WriteString("\n" + label(a, "not_established") + ":\n\n")
		for _, r := range blocked {
			writeChoice(&sb, r, notEstablished(a, r))
		}
	}
	return sb.String()
}

// notEstablishedUnits are the blocked units the answer names: the analyzer's
// own, except those a revision replaced. A revision that was blocked too is
// not named again; its original stands for it.
func notEstablishedUnits(a *agent.Analysis) []agent.Recommendation {
	var out []agent.Recommendation
	for _, u := range units(a) {
		if u.r.Blocked != "" && u.r.Revises == "" && !replaced(a, u.id) {
			out = append(out, *u.r)
		}
	}
	return out
}

// writeChoice writes one recommendation as a list item, with why it is not
// established when it is not.
func writeChoice(sb *strings.Builder, r agent.Recommendation, why string) {
	if st := readerText(r.Statement); st != "" {
		sb.WriteString("- " + st)
	} else {
		sb.WriteString("- **" + readerText(r.Choose) + "**")
		if when := readerText(r.When); when != "" {
			sb.WriteString(": " + when)
		}
	}
	if why != "" {
		sb.WriteString(" (" + why + ")")
	}
	sb.WriteString("\n")
}

// claimRefs are claim IDs the analyzer wrote into a statement ("(c1)",
// "(c9, c10)"): they mean nothing to a reader of the answer.
var claimRefs = regexp.MustCompile(`\s*\((?:c\d+(?:\s*[,;]\s*|\s+and\s+)?)+\)`)

// readerText is a statement as the answer prints it, without claim IDs.
func readerText(s string) string {
	return strings.TrimSpace(claimRefs.ReplaceAllString(s, ""))
}

// notEstablished names what blocked a recommendation: the claims that did not
// pass, by their text and status, or the checker's reason it does not follow.
func notEstablished(a *agent.Analysis, r agent.Recommendation) string {
	var parts []string
	for _, c := range a.Claims {
		if slices.Contains(r.Failed, c.ID) {
			parts = append(parts, label(a, c.Status)+": \u201c"+c.Text+"\u201d")
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; ")
	}
	if reason, ok := strings.CutPrefix(r.Blocked, followPrefix); ok {
		return label(a, "does_not_follow") + ": " + reason
	}
	return r.Blocked
}

// Why nothing was approved. A single "no recommendation passed" read as the
// candidates being disproved, when the check may not have run, or had no
// retrieved page to check them against. These are label keys.
const (
	noApprovalUnchecked = "no_approval_unchecked"
	noApprovalNoSources = "no_approval_no_sources"
	noApprovalEvidence  = "no_approval_evidence"
)

// noApproval is the label key for why nothing was approved.
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

// englishLabels are the words the program writes into a report when the
// analysis gave none in the question's language. A Spanish report read
// "Not established in this run" between Spanish sentences.
var englishLabels = map[string]string{
	"answer":            "Answer",
	"not_established":   "Not established in this run",
	"no_conclusion":     "The analysis stated no conclusion the fact-check could check.",
	"does_not_follow":   "does not follow from its claims",
	statusPartial:       "partial",
	statusContradicted:  "contradicted",
	statusDisputed:      "disputed",
	statusInsufficient:  "insufficient",
	statusUnsourced:     "unsourced",
	noApprovalUnchecked: "Verification could not be completed, so the proposed conclusions remain unverified.",
	noApprovalNoSources: "No source was retrieved, so the proposed conclusions could not be verified.",
	noApprovalEvidence:  "The evidence gathered does not establish the proposed conclusions.",
}

// label is the analysis's wording for key when it gave a plain one, the
// English otherwise. A value is written into the report as it is, so one
// with Markdown or a line break in it, or too long for a label, is not used.
func label(a *agent.Analysis, key string) string {
	v := strings.TrimSpace(a.Labels[key])
	if v == "" || len(v) > 200 || strings.ContainsAny(v, "\n#*_[]`<>|") {
		return englishLabels[key]
	}
	return v
}

// followPrefix starts a recommendation's blocked reason when the check found
// it does not follow from its claims.
const followPrefix = "it does not follow from its claims: "

// decidedAnswer is the report's answer for an analysis that made claims: the
// approved recommendations and conclusions, or why none stand. It is empty
// only for an analysis with no claims and nothing to govern (a model that
// skipped the structure), whose prose the summarizer is given instead: an
// empty list of conclusions never brings the unchecked answer back.
func decidedAnswer(a *agent.Analysis) string {
	if len(a.Claims) == 0 && len(units(a)) == 0 {
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
	heading := "## " + label(a, "answer")
	sum.Report = answer + "\n" + dropLeadingProse(dropAnswerSection(sum.Report, label(a, "answer")))
	sum.Executive = strings.TrimSpace(strings.TrimPrefix(answer, heading))
	return sum
}

// dropAnswerSection removes a leading "# title" line and an "Answer"
// section, sub-headings included, from a report. It knows the heading the
// summarizer is told to use, in its Markdown variants ("## Answer ##",
// indented); an answer under a heading of the summarizer's own choosing, or
// a conclusion stated in another section, is beyond it.
// ponytail: heading match only; holding every section's prose to the
// blocked recommendations would need the summarizer to return structure.
func dropAnswerSection(report, heading string) string {
	lines := strings.Split(strings.TrimSpace(report), "\n")
	if len(lines) > 0 && headingLevel(lines[0]) == 1 {
		lines = lines[1:]
	}
	var out []string
	skipLevel := 0
	for _, ln := range lines {
		if lvl := headingLevel(ln); lvl > 0 {
			switch {
			case isAnswerHeading(strings.Trim(strings.TrimSpace(ln), "# \t"), heading):
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

// isAnswerHeading reports whether a heading's text is the answer section's,
// in English or in the analysis's wording.
func isAnswerHeading(text, heading string) bool {
	return strings.EqualFold(text, "Answer") || strings.EqualFold(text, heading)
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

// The repair pass. A recommendation blocked for an over-broad condition lost
// an option the evidence supported: a heating question's dual-fuel option
// was blocked because it said "or" where its claim said "and", a
// programming question's Python option because it added a condition no
// claim stated. The analyzer revises each blocked recommendation from the
// claims that passed, and every revision is checked like the originals; a
// revision may cite no other claim, so no new fact enters without a
// verdict. The originals stay, blocked, for audit.

// maxRevisions bounds the candidates the repair pass may add, so it cannot
// turn one blocked recommendation into a list of guesses.
const maxRevisions = 4

// blockedUnits are the analyzer's own units the check did not approve.
func blockedUnits(a *agent.Analysis) []unit {
	var out []unit
	for _, u := range units(a) {
		if u.r.Blocked != "" && u.r.Revises == "" {
			out = append(out, u)
		}
	}
	return out
}

// repairPrompt asks the analyzer to revise the blocked recommendations and
// conclusions from the supported claims only. The analyzer's own
// instructions still apply: the language of what it writes, naming every
// claim a statement needs.
func repairPrompt(question string, a *agent.Analysis) string {
	var sb strings.Builder
	sb.WriteString("Question: " + question + "\n\n")
	sb.WriteString("These were not established: a claim they rest on did not pass the fact-check, or the check" +
		" found they do not follow from their claims.\n")
	for _, u := range blockedUnits(a) {
		fmt.Fprintf(&sb, "  - %s: %s [%s] — %s\n", u.id, statement(*u.r), strings.Join(u.r.Claims, ", "), u.r.Blocked)
	}
	sb.WriteString("\nSupported claims, the only ones you may cite:\n")
	writeClaims(&sb, supportedClaims(a))
	sb.WriteString("\nRevise each so these claims justify it exactly as written: narrow a condition or a" +
		" statement, split it, or drop what the claims do not support. Cite only the claims above, every one a" +
		" revision needs; give in \"revises\" the ID it replaces, a recommendation's (r...) as a recommendation," +
		" a conclusion's (k...) as a conclusion. Leave out one nothing above can replace. Return only JSON:\n" +
		`{"recommendations":[{"revises":"r1","choose":"...","when":"...","claims":["c1"]}],` +
		`"conclusions":[{"revises":"k1","statement":"...","claims":["c2"]}]}` + "\n")
	return sb.String()
}

// supportedClaims are the claims that passed the fact-check.
func supportedClaims(a *agent.Analysis) []agent.Claim {
	var out []agent.Claim
	for _, c := range a.Claims {
		if c.Status == statusSupported {
			out = append(out, c)
		}
	}
	return out
}

// acceptRevisions appends the revisions that cite only supported claims and
// revise a blocked unit of their own kind, up to maxRevisions, and returns
// their IDs. A revision citing any other claim is dropped: it would rest on
// a fact that has no verdict, or one that failed.
func acceptRevisions(a *agent.Analysis, rev *agent.Analysis) []string {
	blocked := map[string]bool{}
	for _, u := range blockedUnits(a) {
		blocked[u.id] = true
	}
	supported := map[string]bool{}
	for _, c := range supportedClaims(a) {
		supported[c.ID] = true
	}
	ok := func(r agent.Recommendation, prefix string) bool {
		return blocked[r.Revises] && strings.HasPrefix(r.Revises, prefix) && len(r.Claims) > 0 &&
			!slices.ContainsFunc(r.Claims, func(id string) bool { return !supported[id] })
	}
	var added []string
	for _, r := range rev.Recommendations {
		if len(added) < maxRevisions && ok(r, "r") {
			delete(blocked, r.Revises)
			r.Blocked, r.Failed, r.Statement = "", nil, ""
			a.Recommendations = append(a.Recommendations, r)
			added = append(added, "r"+strconv.Itoa(len(a.Recommendations)))
		}
	}
	for _, r := range rev.Conclusions {
		if len(added) < maxRevisions && ok(r, "k") {
			delete(blocked, r.Revises)
			r.Blocked, r.Failed, r.Choose, r.When = "", nil, "", ""
			a.Conclusions = append(a.Conclusions, r)
			added = append(added, "k"+strconv.Itoa(len(a.Conclusions)))
		}
	}
	return added
}

// revisionCheckPrompt asks the fact-checker whether each revision follows
// from its claims. It is given the claims the revisions cite and only the
// pages those claims come from: the claims already have their verdicts, and
// the question now is the inference.
func revisionCheckPrompt(a *agent.Analysis, added []string, findings []agent.Finding) string {
	cited := map[string]bool{}
	for _, id := range added {
		if u, ok := unitByID(a, id); ok {
			for _, c := range u.r.Claims {
				cited[c] = true
			}
		}
	}
	var claims []agent.Claim
	urls := map[string]bool{}
	for _, c := range a.Claims {
		if cited[c.ID] {
			claims = append(claims, c)
			for _, u := range c.Sources {
				urls[tools.CanonicalURL(u)] = true
			}
		}
	}
	var sb strings.Builder
	writePremisePackets(&sb, a, added)
	sb.WriteString("Claims:\n")
	writeClaims(&sb, claims)
	var pages []agent.Finding
	for _, f := range findings {
		if urls[tools.CanonicalURL(f.URL)] {
			pages = append(pages, f)
		}
	}
	return factCheckPrompt(sb.String(), pages, true, claims)
}

// judgeRevisions blocks each revision the check did not find following from
// its claims, and records the check's judgements for audit.
func judgeRevisions(a *agent.Analysis, added []string, fc, recheck *agent.FactCheckResult) {
	by := map[string][]agent.Inference{}
	for _, inf := range recheck.Inferences {
		by[inf.ID] = append(by[inf.ID], inf)
	}
	for _, id := range added {
		if u, ok := unitByID(a, id); ok {
			u.r.Blocked = strings.Join(inferenceFailure(by[id]), "; ")
		}
		if fc != nil {
			fc.Inferences = append(fc.Inferences, by[id]...)
		}
	}
}

// replaced reports whether a blocked unit has an approved revision, in which
// case the answer shows the revision and not the original.
func replaced(a *agent.Analysis, id string) bool {
	return slices.ContainsFunc(units(a), func(u unit) bool { return u.r.Revises == id && u.r.Blocked == "" })
}
