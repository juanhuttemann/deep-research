package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

// namedPageReader is the built-in reader answering every public-looking URL
// from pages by its path: a path missing from it is a 404, and "/slow" never
// answers before the reader gives up.
func namedPageReader(t *testing.T, pages map[string]string) *tools.DirectClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	c := tools.NewDirectClient(200 * time.Millisecond)
	// Only this test transport sends the public hostname to the local double.
	c.HTTPClient.Transport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	return c
}

// namingModel answers every search call with findings naming urls, each
// described in words no page holds.
func namingModel(prompts *[]string, urls ...string) runFunc {
	return func(_ context.Context, prompt string) (string, error) {
		if prompts != nil {
			*prompts = append(*prompts, prompt)
		}
		var items []string
		for _, u := range urls {
			items = append(items, fmt.Sprintf(`{"title":"remembered","content":"invented evidence","url":%q}`, u))
		}
		return `{"findings":[` + strings.Join(items, ",") + `]}`, nil
	}
}

type refusingEngine struct{}

func (refusingEngine) Name() string { return "searxng" }
func (refusingEngine) Search(context.Context, string) ([]tools.SearchResult, error) {
	return nil, errors.New("search unavailable")
}

type offTopicEngine struct{}

func (offTopicEngine) Name() string { return "searxng" }
func (offTopicEngine) Search(context.Context, string) ([]tools.SearchResult, error) {
	return []tools.SearchResult{{Title: "Ruby hotels", URL: "https://hotels.example/ruby", Content: "rooms"}}, nil
}

const tidalPage = `<html><title>Tidal study</title><main><p>Measured tidal storage of 4 GWh.</p></main></html>`

// A search that failed gave the run nothing, however well the model knew
// where the answer was. The pages it names are now read, and only what
// loads is kept: never its words about them.
func TestRefusedSearchFetchesURLsTheModelSuggests(t *testing.T) {
	var prompts []string
	a := &impl{
		searchTools: &tools.SearchTools{
			Engine:  refusingEngine{},
			Scraper: namedPageReader(t, map[string]string{"/study": tidalPage}), MaxURLsPerQuery: 3,
			Domains: []string{"public.example"},
		},
		search: namingModel(&prompts, "http://public.example/study", "http://public.example/missing", "http://elsewhere.example/study"),
	}
	det, err := a.ResearchDetail(context.Background(), "tidal storage", []string{"tidal", "storage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "public.example") {
		t.Errorf("prompts = %q, want one naming the source list", prompts)
	}
	if det.SearchErr == nil {
		t.Error("the search's failure was not carried for the run to count")
	}
	statuses := map[string]string{}
	for i, s := range det.Signals {
		statuses[s.URL] = s.Status
		if strings.Contains(det.Findings[i].Content, "invented evidence") {
			t.Errorf("the model's description of %s became evidence", s.URL)
		}
	}
	want := map[string]string{"http://public.example/study": "ok", "http://public.example/missing": "dropped"}
	if fmt.Sprint(statuses) != fmt.Sprint(want) {
		t.Errorf("statuses = %v, want %v (a page off the source list is not read)", statuses, want)
	}
	if !det.Usable() || !strings.Contains(det.Findings[0].Content, "Measured tidal storage") {
		t.Errorf("findings = %+v, want the page's text", det.Findings)
	}
}

// A search that answered with nothing on the subject is followed the same
// way, and what it did report off-topic is still reported.
func TestOffTopicSearchFetchesURLsTheModelSuggests(t *testing.T) {
	a := &impl{
		searchTools: &tools.SearchTools{Engine: offTopicEngine{}, Scraper: namedPageReader(t, map[string]string{"/study": tidalPage}), MaxURLsPerQuery: 3},
		search:      namingModel(nil, "http://public.example/study"),
	}
	det, err := a.ResearchDetail(context.Background(), "tidal storage", []string{"tidal", "storage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Skipped) != 1 || !det.Usable() || det.SearchErr != nil {
		t.Errorf("det = %+v, want the off-topic result reported and the named page read", det)
	}
}

// When the model suggests nothing that can be fetched, the answer is the search's
// own error, as it was with no fallback.
func TestRefusedSearchWithNothingReadableIsTheSearchError(t *testing.T) {
	a := &impl{
		searchTools: &tools.SearchTools{Engine: refusingEngine{}, Scraper: namedPageReader(t, nil), MaxURLsPerQuery: 3},
		search:      namingModel(nil, "https://127.0.0.1/admin", "not a url"),
	}
	det, err := a.ResearchDetail(context.Background(), "q", []string{"a", "b"})
	if err == nil || err.Error() != "search unavailable" || det != nil {
		t.Errorf("det=%+v err=%v, want the search's error", det, err)
	}
}

// With search off every finding was the model's word, even for a page any
// fetch would have read. Each named page is now read: one that loads is a
// source, one its server says does not exist is dropped, and one that could
// not be read for another reason stays as before, unverified, so a run with
// no network beyond the model still answers.
func TestSearchOffFetchesURLsTheModelSuggests(t *testing.T) {
	a := &impl{
		reader: &tools.SearchTools{Scraper: namedPageReader(t, map[string]string{"/study": tidalPage}), MaxURLsPerQuery: 3},
		search: namingModel(nil, "http://public.example/study", "http://public.example/missing", "http://public.example/slow"),
	}
	det, err := a.ResearchDetail(context.Background(), "tidal storage", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, s := range det.Signals {
		got = append(got, s.Status+" "+det.Findings[i].Content)
	}
	want := []string{"ok # Tidal study\n\nMeasured tidal storage of 4 GWh.", "dropped ", "unverified invented evidence"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// With scraper none (snippets only) every URL the model suggested was
// dropped, having no snippet to keep, and a run whose SearXNG was down still
// ended "no search answered". A suggested URL is fetched by the built-in
// reader when no scraper is set, with search on or off.
func TestSuggestedURLsAreFetchedWithScraperNone(t *testing.T) {
	reader := namedPageReader(t, map[string]string{"/study": tidalPage})
	a := &impl{
		searchTools: &tools.SearchTools{Engine: refusingEngine{}, Named: reader, MaxURLsPerQuery: 3},
		search:      namingModel(nil, "http://public.example/study"),
	}
	det, err := a.ResearchDetail(context.Background(), "tidal storage", nil)
	if err != nil || !det.Usable() || det.Signals[0].Status != "ok" {
		t.Fatalf("det=%+v err=%v, want the suggested URL fetched", det, err)
	}
	r, err := tools.NewReader(tools.Options{Engine: "off", Scraper: "none"})
	if err != nil || r == nil {
		t.Fatalf("reader = %v, err = %v, want one with search off and scraper none", r, err)
	}
	r.Named = reader
	a = &impl{reader: r, search: namingModel(nil, "http://public.example/study")}
	if det, err := a.ResearchDetail(context.Background(), "tidal storage", nil); err != nil || det.Signals[0].Status != "ok" {
		t.Fatalf("search off: det=%+v err=%v, want the suggested URL fetched", det, err)
	}
}

// Asked for each sub-topic alone, the model suggested the same best-known
// paper and nothing else every time, and every topic after the first added
// no evidence. Each request now lists the pages suggested before and asks
// for others beside them; a known page may be named again, since it costs
// nothing and carries the topic's query onto the page.
func TestSuggestionsListThePagesAlreadySuggested(t *testing.T) {
	var prompts []string
	a := &impl{
		searchTools: &tools.SearchTools{Engine: refusingEngine{}, Scraper: namedPageReader(t, map[string]string{"/study": tidalPage}), MaxURLsPerQuery: 3},
		search:      namingModel(&prompts, "http://public.example/study", "http://public.example/study/"),
	}
	for range 2 {
		if _, err := a.ResearchDetail(context.Background(), "tidal storage", nil); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(prompts[0], "already has") {
		t.Errorf("first prompt lists known pages: %q", prompts[0])
	}
	if !strings.Contains(prompts[1], "these pages:\n- http://public.example/study\n") || strings.Count(prompts[1], "public.example/study") != 1 ||
		!strings.Contains(prompts[1], "pages not in this list") {
		t.Errorf("second prompt = %q, want the suggested URL listed once and new pages asked for", prompts[1])
	}
}
