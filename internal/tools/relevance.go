package tools

import (
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
