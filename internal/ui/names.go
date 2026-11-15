package ui

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/compute"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// The report is read without the run's audit: an ID ("m1", "c5") or the
// checker's "finding 5" means nothing in a PDF someone was sent. What the
// report prints names each claim, computation and source as a reader knows
// it; the IDs stay in the .json, for the audit and for tools.

// namer names an analysis's claims and computations for the report: short
// names for formulas and inputs (the subject, "Burj Khalifa", or the year
// when every claim is about one subject), full names for reasons (the
// claim's own words, quoted).
type namer struct {
	short, full map[string]string
	ids         []string // longest first, so "c1.6" is matched before "c1"
}

func newNamer(a *agent.Analysis) *namer {
	n := &namer{short: map[string]string{}, full: map[string]string{}}
	if a == nil {
		return n
	}
	for _, c := range a.Claims {
		if _, ok := n.full[c.ID]; ok {
			continue
		}
		n.full[c.ID] = quoted(c.Text)
		n.short[c.ID] = shortClaimName(c, a.Claims)
	}
	for _, c := range a.Computations {
		if _, ok := n.full[c.ID]; ok {
			continue
		}
		name := oneLine(c.Label)
		if name == "" {
			name = "the " + strings.ReplaceAll(c.Op, "_", " ") + " of " + strings.Join(c.Inputs, ", ")
		}
		n.full[c.ID], n.short[c.ID] = name, name
	}
	for _, u := range units(a) {
		if _, ok := n.full[u.id]; !ok {
			n.full[u.id] = quoted(cmp.Or(u.r.Statement, strings.TrimSpace(u.r.Choose+" when "+u.r.When)))
			n.short[u.id] = n.full[u.id]
		}
	}
	for _, ch := range a.Charts {
		if _, ok := n.full[ch.ID]; !ok {
			n.full[ch.ID] = "the chart " + quoted(firstNonEmpty(ch.Title, "untitled"))
			n.short[ch.ID] = n.full[ch.ID]
		}
	}
	for id := range n.full {
		n.ids = append(n.ids, id)
	}
	slices.SortFunc(n.ids, func(x, y string) int { return cmp.Or(len(y)-len(x), strings.Compare(x, y)) })
	// A computation's name ("the sum of c1, c2", or a label a model wrote
	// with IDs in it) names what it cites.
	for _, c := range a.Computations {
		if name := n.say(n.full[c.ID], false); name != n.full[c.ID] {
			n.full[c.ID], n.short[c.ID] = name, name
		}
	}
	return n
}

// shortClaimName is the shortest name that tells c from the other claims:
// its subject, its subject and year, its subject and criterion, its year,
// a short scope, then any of those with the site it was read on ("Apple
// 2024, bullfincher.io", when three sites gave a 2024 figure), else its own
// words.
func shortClaimName(c agent.Claim, all []agent.Claim) string {
	others := map[string]bool{}
	for _, o := range all {
		if o.ID != c.ID {
			for _, name := range nameCandidates(o) {
				others[name] = true
			}
		}
	}
	for _, name := range nameCandidates(c) {
		if name != "" && !others[name] {
			return name
		}
	}
	return quoted(c.Text)
}

// nameCandidates are the names a claim may go by, shortest first: its
// subject, with its year, with the site it was read on ("Solar,
// ember-energy.org" beside a Reuters figure for the same), then its
// criterion, its year or a short scope.
func nameCandidates(c agent.Claim) []string {
	option := oneLine(c.Option)
	year := cmp.Or(compute.SoleYear(c.Scope), compute.SoleYear(c.Text))
	site := ""
	if len(c.Sources) > 0 {
		site = strings.TrimPrefix(tools.DomainOf(c.Sources[0]), "www.")
	}
	withSite := func(s string) string {
		if s == "" || site == "" {
			return ""
		}
		return s + ", " + site
	}
	optYear := strings.TrimSpace(option + " " + year)
	optCrit := strings.TrimSpace(option + " " + oneLine(c.Criterion))
	return []string{option, optYear, withSite(optYear), withSite(option), optCrit, year,
		shortScope(c.Scope), withSite(optCrit), withSite(year)}
}

// quoted is a claim's words in quotation marks, shortened to a line.
func quoted(s string) string {
	s = strings.TrimRight(oneLine(s), ".")
	if utf8.RuneCountInString(s) > 90 {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:89])) + "…"
	}
	return "“" + s + "”"
}

// say is s with every claim and computation ID in it replaced by its name,
// short or full. An ID is replaced only as a whole word: c1 in "c12" or in
// "Q1c1x" is not one.
func (n *namer) say(s string, full bool) string {
	if len(n.ids) == 0 || s == "" {
		return s
	}
	names := n.short
	if full {
		names = n.full
	}
	var sb strings.Builder
	for i := 0; i < len(s); {
		if !inURL(s, i) && !afterNumber(s, i) && (i == 0 || !idRune(s[i-1]) && !strings.ContainsRune("/=?#&.-", rune(s[i-1]))) {
			if id := n.idAt(s, i); id != "" {
				sb.WriteString(names[id])
				i += len(id)
				continue
			}
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}

// idAt is the longest ID that s holds as a whole word at i.
func (n *namer) idAt(s string, i int) string {
	for _, id := range n.ids {
		if !strings.HasPrefix(s[i:], id) {
			continue
		}
		end := i + len(id)
		// "c1" is not the start of "c1.6", nor of "c12".
		if end < len(s) && (idRune(s[end]) || s[end] == '.' && end+1 < len(s) && s[end+1] >= '0' && s[end+1] <= '9') {
			continue
		}
		return id
	}
	return ""
}

func idRune(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// findingRefs are the checker's references to the findings by their number
// in its prompt: "finding 5", "findings 2 and 4", "entries 1, 3".
var findingRefs = regexp.MustCompile(`(?i)\b(?:findings?|entr(?:y|ies))\s+(\d+(?:\s*(?:,|and|&)\s*\d+)*)\b`)

// nameFindings replaces the checker's numbered references to the findings
// with the titles they stand for. The numbers are the findings' places in
// the checker's prompt (writeFindingsFor), which no report shows.
func nameFindings(s string, findings []agent.Finding) string {
	return findingRefs.ReplaceAllStringFunc(s, func(m string) string {
		nums := regexp.MustCompile(`\d+`).FindAllString(m, -1)
		var titles []string
		for _, num := range nums {
			k, _ := strconv.Atoi(num)
			if k < 1 || k > len(findings) {
				return m
			}
			titles = append(titles, quoted(cmp.Or(findings[k-1].Title, findings[k-1].URL)))
		}
		if len(titles) == 1 {
			return titles[0]
		}
		return strings.Join(titles[:len(titles)-1], ", ") + " and " + titles[len(titles)-1]
	})
}

// capitalized is s with its first letter upper case.
func capitalized(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// dropID is s without a leading "c5: ", the ID older runs' fact-check
// entries began with, when it is one of the analysis's IDs.
func (n *namer) dropID(s string) string {
	id, rest, ok := strings.Cut(s, ": ")
	if _, known := n.full[id]; ok && known {
		return rest
	}
	return s
}

// shortScope is a scope short enough to name a claim by ("Q1 2025"), ""
// for one that is a sentence.
func shortScope(s string) string {
	if s = oneLine(s); utf8.RuneCountInString(s) <= 30 {
		return s
	}
	return ""
}

// inURL reports whether i is inside a URL or a Markdown link's target.
func inURL(s string, i int) bool {
	start := strings.LastIndexAny(s[:i], " \t\n(<")
	word := s[start+1 : i]
	return strings.Contains(word, "://") || strings.HasPrefix(word, "www.")
}

// afterNumber reports whether i follows a number and a space, where "m2"
// or "k1" is a unit or a grade, not an ID: "10 m2".
func afterNumber(s string, i int) bool {
	j := i
	for j > 0 && s[j-1] == ' ' {
		j--
	}
	return j < i && j > 0 && s[j-1] >= '0' && s[j-1] <= '9'
}
