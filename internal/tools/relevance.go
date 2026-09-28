package tools

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Relevance ranking for search results.
//
// A metasearch engine answers the query it was given with whatever its
// backends return, and those backends disagree wildly on a long technical
// question: a real run asking about two Go locking primitives came back with
// wholesale plant listings, a video channel and a trade-mark registry notice
// ranked *above* the articles that answered it. Trusting that ranking
// meant the per-query budget was spent before the useful pages were reached,
// and the noise was then scraped, counted as evidence and cited.
//
// Ranking here is deliberately lexical and local: no extra model call on a
// pipeline that already pays for several, and no dependency on an engine
// being configured well. What it matches is not taken from the query: the
// planner names, per sub-topic and in the question's language, the terms a
// relevant page must mention. Guessing them from the query's own words took
// English stopword lists that grew with every noisy run, and on any other
// language they kept function words and split accented ones apart.

// minRelevance is the floor a result must clear to be worth fetching. With the
// planner's two to five terms it rejects exactly the pages that mention none
// of them. The terms are distinctive by instruction, so one is already a real
// link to the question, and noiseCorpus in the tests holds that calibration.
// ponytail: one term in five clears it; raise it if a broad term such as a
// country name starts admitting noise.
const minRelevance = 0.2

// minDistinctiveTerms is the least a result set must be judged against. The
// terms come from the planner; a plan without them (its fallback, or a model
// that skipped the field) leaves the engine order standing rather than
// judging every page on one word. Note this is the *only* abstain: once a
// query is judgeable, rejecting every result is a real verdict, and reporting
// no evidence beats citing whatever ranked first.
const minDistinctiveTerms = 2

// fold lower-cases s and strips its combining marks, so "energía" matches the
// "Energia" a page title spelled without accents. It is the folding search
// analyzers apply, and like theirs it runs on both sides of a match: a script
// it alters (Japanese voiced kana lose their marks too) still matches itself.
func fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	if out, _, err := transform.String(t, strings.ToLower(s)); err == nil {
		return out
	}
	return strings.ToLower(s)
}

// ScoredResult pairs a search result with its relevance to the query, so the
// caller can report why a result was dropped rather than silently losing it.
type ScoredResult struct {
	SearXNGResult
	Score float64
}

// relevance is the share of the terms that the result's title and snippet
// mention. Substring matching is intentional: it costs nothing, absorbs
// morphology ("mutexes", "read-heavy") and needs no word boundaries, which
// CJK text does not mark.
func relevance(terms []string, title, snippet string) float64 {
	if len(terms) == 0 {
		return 1 // nothing to judge against: keep the result
	}
	text := fold(title + " " + snippet)
	matched := 0
	for _, t := range terms {
		if mentions(text, fold(t)) {
			matched++
		}
	}
	return float64(matched) / float64(len(terms))
}

// mentions reports whether folded text contains the folded term. A phrase
// counts when every one of its words does, in any order — a search engine's
// AND query: planners write "ventajas energía nuclear" and pages say
// "ventajas de la energía nuclear". Requiring more words can only reject, so
// a common one inside a phrase never admits a page.
func mentions(text, term string) bool {
	for _, w := range strings.Fields(term) {
		if !containsWord(text, w) {
			return false
		}
	}
	return true
}

// containsWord is a substring match that also lets a qualified identifier
// count by its last segment: the planner writes "sync.RWMutex", and the
// package's own documentation says "RWMutex.RLock". A segment under four
// characters is too common to stand alone ("com", "js").
func containsWord(text, w string) bool {
	if strings.Contains(text, w) {
		return true
	}
	i := strings.LastIndexAny(w, "._:/")
	return i >= 0 && utf8.RuneCountInString(w[i+1:]) >= 4 && strings.Contains(text, w[i+1:])
}

// judgeable drops the terms substring matching cannot judge. Under three
// bytes a term is one or two ASCII characters ("Go", "AI") or a single letter
// of another alphabet, and it sits inside so many words ("good", "said") that
// every page would mention it.
func judgeable(terms []string) []string {
	var out []string
	for _, t := range terms {
		if len(fold(t)) >= 3 {
			out = append(out, t)
		}
	}
	return out
}

// rankByRelevance orders results by how well they answer the query and splits
// off the ones that do not clear the floor. Ranking matters more than the
// floor: the genuine sources are usually present but buried, so reordering
// alone is what puts them inside the per-query budget. Ties keep the engines'
// own order, which is still the best tiebreak available.
func rankByRelevance(in []SearXNGResult, terms []string) (kept, skipped []ScoredResult) {
	terms = judgeable(terms)
	if len(terms) < minDistinctiveTerms {
		for _, r := range in {
			kept = append(kept, ScoredResult{SearXNGResult: r, Score: 1})
		}
		return kept, nil
	}
	scored := make([]ScoredResult, len(in))
	for i, r := range in {
		scored[i] = ScoredResult{SearXNGResult: r, Score: relevance(terms, r.Title, r.Content)}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	for _, r := range scored {
		if r.Score >= minRelevance {
			kept = append(kept, r)
		} else {
			skipped = append(skipped, r)
		}
	}
	return kept, skipped
}

// ExcerptFor returns the passages of content that bear most on the queries,
// in page order and within limit bytes. A scraped page opens with whatever its site
// puts first, and documentation sites routinely spend their first kilobytes on
// a cookie dialog and navigation: a prefix cut handed the model those and cut
// off the section that answered the query.
//
// Passages are the page's blank-line-separated blocks, scored by the query
// words they contain, each weighted by how rare it is on this page. A word
// every block repeats (the product's name, a function word) weighs nothing, so
// no stopword list is needed. A query that matches no block gets the prefix.
// ponytail: lexical; a query in an unspaced script (Japanese, Chinese) is one
// word that rarely matches, and falls back to the prefix as it did before.
//
// The queries come in two tiers. Each query's best passage comes first, the
// first tier's before the second's, then every block by its best score
// against any one query. The fact-check excerpts each page by the claims it
// checks: those that cite the page first, then the others, since a page a
// claim does not cite can still contradict it. Chosen by the search that
// found it, a vendor's pricing page showed its node prices and left out the
// paragraph that contradicted the claim under check; with every claim in one
// tier, thirty-six claims' weak matches filled a page's excerpt and pushed
// out the passage the page was cited for.
func ExcerptFor(content string, first, second []string, limit int) string {
	if len(content) <= limit {
		return content
	}
	blocks := splitLeads(blockBreak.Split(content, -1))
	keep := pickBlocks(blocks, bestScores(blocks, first, second), limit)
	if keep == nil {
		return truncateUTF8Bare(content, limit)
	}
	var sb strings.Builder
	last := -1
	for i, b := range blocks {
		if !keep[i] {
			continue
		}
		switch {
		case last >= 0 && i == last+1:
			sb.WriteString("\n\n")
		case i > 0:
			sb.WriteString(elision)
		}
		sb.WriteString(b)
		last = i
	}
	return truncateUTF8Bare(sb.String(), limit)
}

// blockBreak separates passages: a blank line, which in scraped Markdown often
// holds the indentation of the list around it. Splitting on "\n\n" alone left
// a whole numbered list as one block that matched every query by its size.
var blockBreak = regexp.MustCompile(`\n[ \t]*\n`)

// elision marks text left out between two excerpted passages.
const elision = "\n[…]\n"

// queryWords are the distinct folded words of a query worth matching; under
// three bytes a word sits inside too many others (see judgeable).
func queryWords(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(fold(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) >= 3 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// blockScores weighs each block by the query words it mentions, a word
// counting log((n+1)/(df+1)): nothing when every block has it. The sum is
// divided by the block's length relative to the page's average, as BM25 does:
// otherwise a long block (a JSON sample, a table) wins by mentioning every
// word once somewhere in its bulk. Unlike BM25 a block shorter than average is
// not boosted: a navigation item that is one query word ("*   Memcached")
// outscored every paragraph that answered the query.
func blockScores(blocks, words []string) []float64 {
	folded := make([]string, len(blocks))
	total := 0
	for i, b := range blocks {
		folded[i] = fold(b)
		total += len(b)
	}
	avg := float64(total) / float64(max(len(blocks), 1))
	scores := make([]float64, len(blocks))
	for _, w := range words {
		var in []int
		for i, b := range folded {
			if strings.Contains(b, w) {
				in = append(in, i)
			}
		}
		idf := math.Log(float64(len(blocks)+1) / float64(len(in)+1))
		for _, i := range in {
			scores[i] += idf
		}
	}
	for i, b := range blocks {
		scores[i] /= 0.25 + 0.75*max(float64(len(b)), avg)/max(avg, 1)
	}
	return scores
}

// bestScores is each block's best score against any one query, each query's
// scores scaled so that its own best block scores 1, and that one block (the
// first, on a tie) raised above every other, a first-tier query's highest:
// on a page of near-identical paragraphs thirty blocks tied at the top for
// one query and filled the budget before another query's best block was
// reached. With one query the order is blockScores's own.
func bestScores(blocks, first, second []string) []float64 {
	best := make([]float64, len(blocks))
	for tier, queries := range [][]string{first, second} {
		lift := 3.0 - float64(tier) // 3 for the first tier, 2 for the second
		for _, q := range queries {
			s := blockScores(blocks, queryWords(q))
			top, at := 0.0, -1
			for i, v := range s {
				if v > top {
					top, at = v, i
				}
			}
			if at < 0 {
				continue
			}
			for i, v := range s {
				best[i] = max(best[i], v/top)
			}
			best[at] = max(best[at], lift)
		}
	}
	return best
}

// pickBlocks takes the best-scoring blocks that fit in limit, best first,
// each with its section context (see sectionContext), which counts against
// the limit like the block itself. A best block too long to fit is still
// taken, and clipped by the caller. It returns nil when no block scores.
func pickBlocks(blocks []string, scores []float64, limit int) []bool {
	order := make([]int, len(blocks))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	if len(order) == 0 || scores[order[0]] <= 0 {
		return nil
	}
	keep := make([]bool, len(blocks))
	used := 0
	for _, i := range order {
		if scores[i] <= 0 || keep[i] {
			continue
		}
		add := append([]int{i}, sectionContext(blocks, i)...)
		n := 0
		for _, j := range add {
			if !keep[j] {
				n += len(blocks[j]) + len(elision)
			}
		}
		if used > 0 && used+n > limit {
			continue
		}
		for _, j := range add {
			keep[j] = true
		}
		used += n
	}
	return keep
}

// sectionContext is what a passage needs from its section to keep its scope:
// the nearest heading above it and the paragraph that opens that section,
// when short; and when that heading is only a bold sub-heading ("**Serverless
// option**"), the section heading above it too ("### Example 3: ... a
// Memcached cache"). A price quoted from a pricing page's durability section
// read as a price for any engine, because the heading ("Durability") and the
// sentence under it ("a feature available with Valkey 9.0") were cut away.
func sectionContext(blocks []string, i int) []int {
	var ctx []int
	for h := i - 1; h >= 0; h-- {
		kind := headingKind(blocks[h])
		if kind == notHeading || (len(ctx) > 0 && kind == subHeading) {
			continue
		}
		ctx = append(ctx, h)
		if lead := h + 1; lead < i && headingKind(blocks[lead]) == notHeading && len(blocks[lead]) <= maxLeadBytes {
			ctx = append(ctx, lead)
		}
		if kind == sectionHeading {
			return ctx
		}
		i = h
	}
	return ctx
}

// maxLeadBytes bounds the opening paragraph kept as a section's context: a
// sentence or two that scope the section, not a second passage.
const maxLeadBytes = 300

// splitLeads splits a section's opening paragraph that is too long to be its
// context after its first sentence, which is where a section says what it is
// about ("Durability is a feature available with Valkey 9.0, ..."). Both
// halves stay verbatim text of the page, so a quote from either is located.
func splitLeads(blocks []string) []string {
	out := make([]string, 0, len(blocks))
	for i, b := range blocks {
		if i > 0 && headingKind(blocks[i-1]) != notHeading && headingKind(b) == notHeading && len(b) > maxLeadBytes {
			if cut := strings.Index(b, ". "); cut > 0 && cut < maxLeadBytes {
				out = append(out, b[:cut+1], strings.TrimLeft(b[cut+1:], " "))
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

var (
	setextRule = regexp.MustCompile(`^[-=]{3,}\s*$`)
	boldLine   = regexp.MustCompile(`^\*\*[^*]+\*\*:?$`)
)

// Heading kinds: a section heading is an ATX "#" line or a setext heading (a
// line underlined with --- or ===); a sub-heading is a line that is bold and
// nothing else, which scraped pages use under a section.
const (
	notHeading = iota
	sectionHeading
	subHeading
)

func headingKind(block string) int {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	switch {
	case strings.HasPrefix(lines[0], "#"),
		len(lines) == 2 && setextRule.MatchString(strings.TrimSpace(lines[1])):
		return sectionHeading
	case len(lines) == 1 && boldLine.MatchString(strings.TrimSpace(lines[0])):
		return subHeading
	}
	return notHeading
}

// truncateUTF8Bare cuts s to at most limit bytes on a rune boundary, with no
// marker: the prompt builder that calls Excerpt marks the cut itself.
func truncateUTF8Bare(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
