package tools

import "testing"

// qPrefix is the research question every sub-agent query is anchored on.
const qPrefix = "What are the practical trade-offs between sync.Mutex and sync.RWMutex in Go? "

// noiseResult is one search result the corpus expects to be kept or rejected.
type noiseResult struct {
	title    string
	url      string
	snippet  string
	relevant bool
}

// noiseCorpus is the calibration corpus for relevance scoring: four
// sub-agent queries from one run's plan, each with the mix a metasearch
// engine really returns for a long technical question — the handful of pages
// that answer it, and unrelated pages that share an incidental word with it
// ("trade", "practical", "guidelines", "read", "performance"). Those near
// misses are the point: rejecting a page that shares no vocabulary at all is
// easy, and a floor that also rejects these is the one worth having.
//
// Every site, title and snippet here is invented. The corpus is modelled on
// the shape and ratios of a live capture, not on its content.
var noiseCorpus = []struct {
	query   string
	results []noiseResult
}{
	{
		query: qPrefix + "Locking Semantics",
		results: []noiseResult{
			{"Venta de flores al por mayor", "https://viveros.example/mayorista", "Plantas ornamentales y ramos de temporada, con horario de apertura ampliado.", false},
			{"Lock It Right the First Time", "https://gophernotes.example/lock-it-right", "Go's sync.Mutex and sync.RWMutex: when the read-write lock changes the picture and when it does not.", true},
			{"Wholesale nursery price list", "https://viveros.example/precios", "Average wholesale prices for cut stems, updated every Wednesday morning.", false},
			{"The sync package, end to end", "https://gosnippets.example/sync-package", "Alongside sync.Mutex you can reach for sync.RWMutex, which adds RLock and RUnlock.", true},
			{"Fastener Holdings (FSTN) rivals, 2026", "https://tickerwatch.example/fstn", "The only metrics a day trader should watch before the opening bell.", false},
			{"Mutual exclusion, one page at a time", "https://concurrencynotes.example/mutual-exclusion", "sync.Mutex is the plain lock; sync.RWMutex admits many readers or a single writer.", true},
			{"Full-time family travel: the practical realities", "https://slowroads.example/living", "Travelling full time as a family is not a long holiday. It is a practical decision about money.", false},
			{"Choosing a lock in production Go", "https://fieldreports.example/locks-in-production", "Race conditions, sync.Mutex, sync.RWMutex, and the strategy we settled on after a year.", true},
			{"Spring bulb auction calendar", "https://viveros.example/subastas", "Growers meet fortnightly; entry stays free for registered buyers this season.", false},
			{"Garden centre opening hours", "https://pottingshed.example/hours", "Our shops open at eight on weekdays and close early on bank holidays.", false},
		},
	},
	{
		query: qPrefix + "Read Heavy Performance",
		results: []noiseResult{
			{"General technology discussion board", "https://boards.example/technology", "A community for news and argument about how technology gets built and used.", false},
			{"Concurrency control, mutex and read-write lock", "https://leafstack.example/concurrency-control", "sync.RWMutex grants shared read access; sync.Mutex can be too strict when reads dominate.", true},
			{"Voxel sandbox game community", "https://boards.example/sandbox-survival", "The official board for a small independent survival and exploration game.", false},
			{"Locking, in depth", "https://runtimejournal.example/locking-in-depth", "sync.Mutex and sync.RWMutex both prevent two goroutines touching the same state at once.", true},
			{"Voice chat channels for study groups", "https://studyhall.example/channels", "If you have not set up a voice server yet, this walkthrough covers the basics.", false},
			{"Shifting trade routes and the new tariffs", "https://worldledger.example/trade-routes", "Reports of the end of global trade are overstated, though tariff schedules keep moving.", false},
			{"Choosing a lock in production Go", "https://fieldreports.example/locks-in-production", "Race conditions, sync.Mutex, sync.RWMutex, and the strategy we settled on after a year.", true},
			{"Free cosmetic item event is live", "https://patchnotes.example/cosmetic-event", "A short quest chain hands out the reward in a few minutes of play.", false},
			{"Preventing data races the boring way", "https://plainconcurrency.example/data-races", "Reach for sync.RWMutex when the workload is read heavy, and avoid nesting locks.", true},
			{"How the trade deadline shaped a lost season", "https://sidelinecolumn.example/deadline", "A thin bench, poor save percentage and weak performance down the closing stretch.", false},
		},
	},
	{
		query: qPrefix + "Contention Scalability",
		results: []noiseResult{
			{"Woodworking channel uploads", "https://clips.example/workshop", "Short builds filmed in a small garage workshop, posted most weeks.", false},
			{"Choosing a lock in production Go", "https://fieldreports.example/locks-in-production", "Race conditions, sync.Mutex, sync.RWMutex, and the strategy we settled on after a year.", true},
			{"Encyclopedia entry: a video essayist", "https://reference.example/wiki/essayist", "A commentator known for long form retrospectives on obscure films.", false},
			{"The sync package, end to end", "https://gosnippets.example/sync-package", "Both sync.Mutex and sync.RWMutex satisfy the same small locking interface.", true},
			{"Landscape photography portfolio", "https://portfolio.example/landscapes", "Prints and a short note on the cameras used for each series.", false},
			{"Atomic flags versus locks", "https://plainconcurrency.example/atomics-vs-locks", "Practical advice on picking between sync.Mutex and sync.RWMutex once contention shows up.", true},
			{"Archive of talk recordings", "https://clips.example/archive", "A back catalogue of conference talks, sorted by year and track.", false},
			{"Fastener Holdings (FSTN) rivals, 2026", "https://tickerwatch.example/fstn", "The only metrics a day trader should watch before the opening bell.", false},
			{"Locks in Go, with worked examples", "https://kernelnotes.example/go-locks", "sync.RWMutex is still a lock, but it separates many readers from one writer.", true},
			{"Improve an existing trading bot", "https://gigboard.example/listing-248094", "Wanted: better structure and trade filtering for a pullback strategy bot.", false},
		},
	},
	{
		query: qPrefix + "Use Case Guidelines",
		results: []noiseResult{
			{"Riverbend State College", "https://riverbend.example/index", "An independent college with undergraduate programmes across six faculties.", false},
			{"3.1.4 Period to be considered — registry guidelines", "https://registry.example/mark-guidelines", "Under the regulation a mark becomes open to revocation; these guidelines set out the period.", false},
			{"Understanding the two locks in Go", "https://gophernotes.example/understanding-locks", "Go gives you sync.Mutex and sync.RWMutex; this walks through when each one earns its keep.", true},
			{"Tuition and fees", "https://riverbend.example/tuition", "Registering for classes means accepting financial responsibility for the term's charges.", false},
			{"A complete guide to locking in Go", "https://plainconcurrency.example/complete-guide", "In concurrent programming a mutex gives mutual exclusion; sync.Mutex is the plain form and sync.RWMutex the read-write one.", true},
			{"Academic programmes", "https://riverbend.example/programmes", "The college offers just over a hundred undergraduate and postgraduate programmes.", false},
			{"Choosing a lock in production Go", "https://fieldreports.example/locks-in-production", "Race conditions, sync.Mutex, sync.RWMutex, and the strategy we settled on after a year.", true},
			{"Student portal information", "https://riverbend.example/portal", "Your gateway to campus services, timetables and the new personal dashboard.", false},
			{"Preventing data races the boring way", "https://plainconcurrency.example/data-races", "This walkthrough prevents races with sync.Mutex before reaching for anything cleverer.", true},
			{"Full-time family travel: the practical realities", "https://slowroads.example/living", "Travelling full time as a family is not a long holiday. It is a practical decision about money.", false},
		},
	},
}

// Every labelled-relevant result must clear the floor and every labelled
// noise result must fall below it, on real engine output.
func TestRelevanceSeparatesSignalFromNoise(t *testing.T) {
	for _, f := range noiseCorpus {
		terms := queryTerms(f.query)
		for _, r := range f.results {
			got := relevance(terms, r.title, r.snippet)
			if r.relevant && got < minRelevance {
				t.Errorf("relevant source scored %.3f (floor %.2f): %s", got, minRelevance, r.url)
			}
			if !r.relevant && got >= minRelevance {
				t.Errorf("noise scored %.3f (floor %.2f): %s", got, minRelevance, r.url)
			}
		}
	}
}

// Ranking, not just filtering, is what saves a run: the relevant pages sit
// below the noise in engine order, so a budget applied to the engine's
// ranking spends every slot before reaching them.
func TestRankByRelevancePromotesSignalOverEngineOrder(t *testing.T) {
	for _, f := range noiseCorpus {
		var in []SearXNGResult
		want := 0
		for _, r := range f.results {
			in = append(in, SearXNGResult{Title: r.title, URL: r.url, Content: r.snippet})
			if r.relevant {
				want++
			}
		}
		kept, skipped := rankByRelevance(in, f.query)
		if len(kept) != want {
			t.Errorf("%s: kept %d results, want the %d relevant ones", f.query, len(kept), want)
		}
		if len(skipped) != len(in)-want {
			t.Errorf("%s: skipped %d, want %d", f.query, len(skipped), len(in)-want)
		}
		relevant := map[string]bool{}
		for _, r := range f.results {
			relevant[r.url] = r.relevant
		}
		for _, r := range kept {
			if !relevant[r.URL] {
				t.Errorf("%s: kept noise %s", f.query, r.URL)
			}
		}
		// A budget of three must now be spent entirely on relevant pages.
		for i, r := range kept {
			if i < 3 && !relevant[r.URL] {
				t.Errorf("%s: budget slot %d went to noise %s", f.query, i, r.URL)
			}
		}
	}
}

// Scoring needs enough distinctive terms to judge with. A query the tokenizer
// cannot break up — CJK, or a couple of stopwords — must disable filtering
// rather than reject every result the engines returned.
func TestRelevanceDisabledWithoutDistinctiveTerms(t *testing.T) {
	for _, query := range []string{"日本語の検索", "how to", ""} {
		in := []SearXNGResult{
			{Title: "First", URL: "https://example.com/1", Content: "anything"},
			{Title: "Second", URL: "https://example.com/2", Content: "anything"},
		}
		kept, skipped := rankByRelevance(in, query)
		if len(kept) != 2 || len(skipped) != 0 {
			t.Errorf("%q: filtered without distinctive terms (kept %d, skipped %d)", query, len(kept), len(skipped))
		}
		if kept[0].URL != in[0].URL || kept[1].URL != in[1].URL {
			t.Errorf("%q: engine order not preserved: %+v", query, kept)
		}
	}
}

// Equal scores must not reshuffle the engines' own ranking.
func TestRankByRelevanceIsStable(t *testing.T) {
	in := []SearXNGResult{
		{Title: "sync.Mutex guide", URL: "https://a.example/1", Content: "sync.Mutex and sync.RWMutex"},
		{Title: "sync.Mutex guide", URL: "https://b.example/2", Content: "sync.Mutex and sync.RWMutex"},
	}
	kept, _ := rankByRelevance(in, "sync.Mutex vs sync.RWMutex in Go")
	if len(kept) != 2 || kept[0].URL != in[0].URL || kept[1].URL != in[1].URL {
		t.Errorf("tie broke engine order: %+v", kept)
	}
}

// A result set with nothing relevant in it must come back empty. Abstaining
// here — keeping the engine order when no result clears the floor — looks
// prudent and is not: a live run proved it admits an entire noise set intact
// in exactly the case the floor exists for, and the sub-agent then cited a
// retailer and a courier for a question about Go locks. A run that found no
// usable evidence has to say so.
func TestRelevanceRejectsAnAllNoiseResultSet(t *testing.T) {
	in := []SearXNGResult{
		{Title: "Parcel tracking", URL: "https://courier.example/track", Content: "Find where your package is with the tracking number."},
		{Title: "Grocery delivery slots", URL: "https://retailer.example/slots", Content: "Book a delivery slot for this week's shop."},
	}
	kept, skipped := rankByRelevance(in, "sync.Mutex versus sync.RWMutex contention in Go")
	if len(kept) != 0 {
		t.Errorf("kept noise rather than reporting no evidence: %+v", kept)
	}
	if len(skipped) != 2 {
		t.Errorf("rejections not reported: %+v", skipped)
	}
}
