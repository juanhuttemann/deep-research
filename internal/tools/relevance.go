package tools

import (
	"regexp"
	"sort"
	"strings"
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
// being configured well. It cannot judge meaning, so it only has to beat
// "trust the engine order", which it does by a wide margin on real output.

// minRelevance is the floor a result must clear to be worth fetching. It sits
// between the highest-scoring noise and the lowest-scoring genuine source
// measured across a live capture, and noiseCorpus in the tests holds that
// calibration: the floor exists to stop a thin result set being padded with
// pages sharing only an incidental word ("trade", "practical") with it.
const minRelevance = 0.2

// minDistinctiveTerms is the least a query must yield before its score means
// anything. Below it the scorer abstains and the engine order stands: a query
// in a script this tokenizer cannot split (CJK has no spaces) would otherwise
// score every result zero and discard the entire result set. Note this is the
// *only* abstain: once a query is judgeable, rejecting every result is a real
// verdict, and reporting no evidence beats citing whatever ranked first.
const minDistinctiveTerms = 2

// compoundPattern matches dotted or underscored identifiers — "sync.Mutex",
// "http_client". These are the terms that actually discriminate a technical
// page from an unrelated one, so they and their parts carry double weight.
var compoundPattern = regexp.MustCompile(`[a-z0-9]+(?:[._][a-z0-9]+)+`)

var wordPattern = regexp.MustCompile(`[a-z0-9]+`)

// stopWords are too common to say anything about a page's subject.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"this": true, "that": true, "when": true, "how": true, "why": true,
	"use": true, "using": true, "are": true, "what": true, "which": true,
	"its": true, "our": true, "your": true, "you": true, "can": true,
	"not": true, "but": true, "between": true, "into": true, "than": true,
	"there": true, "their": true, "does": true, "has": true, "have": true,
	"about": true, "over": true, "under": true, "more": true, "most": true,
	"some": true, "any": true, "all": true, "each": true, "other": true,
}

// ScoredResult pairs a search result with its relevance to the query, so the
// caller can report why a result was dropped rather than silently losing it.
type ScoredResult struct {
	SearXNGResult
	Score float64
}

// queryTerms reduces a query to weighted terms. Compound identifiers and
// their parts weigh double: a page mentioning "sync.RWMutex" is answering
// this question, while one mentioning "practical" merely shares a word.
func queryTerms(query string) map[string]float64 {
	query = strings.ToLower(query)
	terms := map[string]float64{}
	for _, compound := range compoundPattern.FindAllString(query, -1) {
		terms[compound] = 2
		for _, part := range strings.FieldsFunc(compound, func(r rune) bool { return r == '.' || r == '_' }) {
			if len(part) >= 3 && !stopWords[part] {
				terms[part] = 2
			}
		}
	}
	for _, word := range wordPattern.FindAllString(query, -1) {
		if len(word) >= 3 && !stopWords[word] && terms[word] == 0 {
			terms[word] = 1
		}
	}
	return terms
}

// relevance is the share of the query's weight that the result's title and
// snippet account for. Substring matching is intentional: it costs nothing
// and absorbs the morphology ("mutexes", "read-heavy") that exact word
// matching would miss.
func relevance(terms map[string]float64, title, snippet string) float64 {
	total := 0.0
	for _, w := range terms {
		total += w
	}
	if total == 0 {
		return 1 // nothing to judge against: keep the result
	}
	text := strings.ToLower(title + " " + snippet)
	matched := 0.0
	for t, w := range terms {
		if strings.Contains(text, t) {
			matched += w
		}
	}
	return matched / total
}

// rankByRelevance orders results by how well they answer the query and splits
// off the ones that do not clear the floor. Ranking matters more than the
// floor: the genuine sources are usually present but buried, so reordering
// alone is what puts them inside the per-query budget. Ties keep the engines'
// own order, which is still the best tiebreak available.
func rankByRelevance(in []SearXNGResult, query string) (kept, skipped []ScoredResult) {
	terms := queryTerms(query)
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
