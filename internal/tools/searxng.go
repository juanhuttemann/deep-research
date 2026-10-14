package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
)

// SearxSpaceURL is the public SearXNG instance list "auto" discovers from.
const SearxSpaceURL = "https://searx.space/data/instances.json"

// maxAutoInstances bounds how many discovered instances a query may try.
// Public instances are donated capacity; a query that fails everywhere should
// give up rather than walk the whole list.
const maxAutoInstances = 10

// maxSearchTimeout caps one request to one instance. SearXNG answers within
// its own engine timeouts or not at all, and with several instances to try, a
// dead one must not hold a query for the model call's two minutes.
const maxSearchTimeout = 30 * time.Second

// A query may try multiple instances, but slow ones must not multiply its
// wall time by the full candidate count. One minute allows two full request
// timeouts and covers discovery and JSON/HTML fallback under one deadline.
const maxQueryTimeout = 2 * maxSearchTimeout

// An instance in a pool that refused a query rests before it is asked again.
// Without this every query walked the whole list: once public instances
// started answering with challenges, each search spent its full deadline
// (one slow instance alone holds a request timeout) relearning that all ten
// refuse. A challenge, a 403 or an address that does not answer is how that
// instance treats this client and outlasts a run, so it rests long; a limiter
// or a timeout is load, and passes.
const (
	refusalRest = 10 * time.Minute
	loadRest    = 2 * time.Minute
)

// errChallenge marks an instance that answered with a bot challenge instead
// of results.
var errChallenge = errors.New("bot challenge instead of results")

// errNotJSON marks a JSON request answered with something else.
var errNotJSON = errors.New("response is not JSON")

// SearXNGClient searches one or more SearXNG instances. With several, a query
// goes to the instance that answered last and moves on when one refuses; an
// instance that refuses the JSON API is read through its HTML page instead.
type SearXNGClient struct {
	HTTPClient  *http.Client
	ResultLimit int
	// Public marks the instances as third parties' donated capacity, whose
	// refusal the run explains differently from the reader's own servers
	// failing. "auto" sets it.
	Public bool

	mu        sync.Mutex
	instances []*searxInstance
	// now is the clock rest periods are measured on; nil is time.Now.
	now func() time.Time
	// Discovery is cached only after success, so a transient failure or a
	// cancelled query cannot poison every later search in the run.
	discover func(ctx context.Context) ([]string, error)
	// discovering is the discovery under way, if any: the queries that
	// arrive while it runs wait for it rather than each starting their own.
	discovering *discovery
}

// discovery is one attempt at the public instance list, shared by every
// query that asks while it runs.
type discovery struct {
	done chan struct{}
	err  error
}

type searxInstance struct {
	url string
	// html is set once the instance refused format=json. The JSON API is off
	// by default and most public instances never enable it, but their HTML
	// page serves the same results.
	html atomic.Bool

	mu sync.Mutex
	// restUntil is when the instance may be asked again after refusing, and
	// refusal what it said, so a query that skips it can still say why.
	restUntil time.Time
	refusal   error
}

// resting reports the refusal that still benches the instance at now, if any.
func (in *searxInstance) resting(now time.Time) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if now.Before(in.restUntil) {
		return in.refusal
	}
	return nil
}

func (in *searxInstance) rest(now time.Time, err error) {
	d := loadRest
	switch SearchStatus(err) {
	case "challenge", "blocked", "unreachable":
		d = refusalRest
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.restUntil, in.refusal = now.Add(d), err
}

// NewSearXNGClient creates a client for baseURL: one instance, a
// comma-separated list, or "auto" for public instances listed on searx.space.
func NewSearXNGClient(baseURL string, timeout time.Duration) *SearXNGClient {
	if timeout <= 0 || timeout > maxSearchTimeout {
		timeout = maxSearchTimeout
	}
	c := &SearXNGClient{HTTPClient: &http.Client{Timeout: timeout}, ResultLimit: 10}
	if strings.EqualFold(strings.TrimSpace(baseURL), "auto") {
		c.Public = true
		c.discover = func(ctx context.Context) ([]string, error) {
			return DiscoverInstances(ctx, c.HTTPClient, SearxSpaceURL)
		}
		return c
	}
	for u := range strings.SplitSeq(baseURL, ",") {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			c.instances = append(c.instances, &searxInstance{url: u})
		}
	}
	return c
}

// Search runs query on the first instance that answers.
func (c *SearXNGClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, maxQueryTimeout)
	defer cancel()
	insts, err := c.candidates(ctx)
	if err != nil {
		return nil, err
	}
	// A single instance is the reader's own server: benching it would turn a
	// restart into minutes of searches that never ask.
	pool := len(insts) > 1
	failed := &poolError{total: len(insts), public: c.Public}
	for _, in := range insts {
		if why := in.resting(c.clock()); pool && why != nil {
			failed.skipped++
			failed.errs = append(failed.errs, fmt.Errorf("%s: %w", in.url, why))
			continue
		}
		res, err := c.searchOne(ctx, in, query)
		if err == nil {
			c.prefer(in)
			return res, nil
		}
		if ctx.Err() != nil {
			// The query's deadline, not the instance's answer.
			failed.errs = append(failed.errs, fmt.Errorf("%s: %w", in.url, err))
			break
		}
		if pool {
			in.rest(c.clock(), err)
		}
		// An unreachable instance already names itself.
		if _, named := errors.AsType[*unreachableError](err); !named {
			err = fmt.Errorf("%s: %w", in.url, err)
		}
		failed.errs = append(failed.errs, err)
	}
	if len(failed.errs) == 1 && failed.skipped == 0 {
		return nil, failed.errs[0]
	}
	return nil, failed
}

func (c *SearXNGClient) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// poolError is a query every instance of a pool failed. Its message counts
// the failures by kind: the instances' own errors joined made a wall of ten
// URLs, challenge scripts and timeouts in which the one fact the reader needs
// — the public instances refuse this client — was lost. Unwrap keeps every
// instance's error for errors.Is and SearchStatus.
type poolError struct {
	total, skipped int
	public         bool
	errs           []error
}

func (e *poolError) Error() string {
	counts := map[string]int{}
	var kinds []string
	for _, err := range e.errs {
		k := failureKind(err)
		if counts[k] == 0 {
			kinds = append(kinds, k)
		}
		counts[k]++
	}
	slices.SortStableFunc(kinds, func(a, b string) int { return cmp.Compare(counts[b], counts[a]) })
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%d %s", counts[k], k)
	}
	who := "SearXNG instances"
	if e.public {
		who = "public SearXNG instances"
	}
	summary := strings.Join(parts, ", ")
	// A query that asked nothing did not fail anywhere: saying "all failed"
	// for it read as ten fresh refusals each time.
	if e.skipped == e.total {
		return fmt.Sprintf("not searched: every one of %d %s refused within the last minutes (%s)", e.total, who, summary)
	}
	msg := fmt.Sprintf("all %d %s failed: %s", e.total, who, summary)
	if len(e.errs) < e.total {
		// The query's deadline ended the walk: "all ten failed" over three
		// counted failures claimed refusals from instances never asked.
		msg = fmt.Sprintf("%d of %d %s failed before the query's deadline: %s", len(e.errs), e.total, who, summary)
	}
	if e.skipped > 0 {
		msg += fmt.Sprintf(" (%d not asked again, having refused within the last minutes)", e.skipped)
	}
	return msg
}

func (e *poolError) Unwrap() []error { return e.errs }

// failureKind names one instance's failure for the pool's summary, as a
// participle that reads after a count: "9 challenged, 1 timed out".
func failureKind(err error) string {
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return "timed out"
	}
	if errors.Is(err, errOffTopic) {
		return "off-topic only"
	}
	switch s := SearchStatus(err); s {
	case "challenge":
		return "challenged"
	case "unavailable":
		return "failed otherwise"
	default:
		return s
	}
}

// SearchResting reports whether err is a query that asked no instance at
// all, every one still resting from a refusal. More queries now fail the same
// way until a rest ends, so a caller has no reason to send them.
func SearchResting(err error) bool {
	pe, ok := errors.AsType[*poolError](err)
	return ok && pe.skipped == pe.total
}

// PublicSearchRefused reports whether err is every public SearXNG instance
// refusing a query with a challenge, a block or a limiter: not a fault to fix
// in the reader's setup, but the sign that the public pool will not serve
// this run and a search of their own would.
func PublicSearchRefused(err error) bool {
	pe, ok := errors.AsType[*poolError](err)
	if !ok || !pe.public {
		return false
	}
	switch SearchStatus(err) {
	case "challenge", "blocked", "rate-limited":
		return true
	}
	return false
}

// candidates is the instance list in the order to try, discovering it first
// for "auto".
//
// Discovery used to run under c.mu. Concurrent sub-agent queries then queued
// on a lock no deadline reaches, and when it failed each ran its own attempt
// in turn: N queries cost N discoveries back to back. Now one query runs it
// outside the lock and the others wait for its outcome, each until its own
// context ends. A failure is shared with the queries that waited for it and
// no further: a query that comes later tries again.
func (c *SearXNGClient) candidates(ctx context.Context) ([]*searxInstance, error) {
	for {
		c.mu.Lock()
		if c.discover == nil {
			defer c.mu.Unlock()
			if len(c.instances) == 0 {
				return nil, errors.New("no SearXNG instance configured")
			}
			return slices.Clone(c.instances), nil
		}
		d := c.discovering
		if d == nil {
			d = &discovery{done: make(chan struct{})}
			c.discovering = d
			c.mu.Unlock()
			c.runDiscovery(ctx, d)
		} else {
			c.mu.Unlock()
		}
		select {
		case <-d.done:
		case <-ctx.Done():
			return nil, fmt.Errorf("discover public SearXNG instances: %w", ctx.Err())
		}
		// A nil d.err means the list is in place and the loop returns it; an
		// attempt its own query cancelled is retried by whoever still waits.
		if d.err != nil && !errors.Is(d.err, errDiscoveryCancelled) {
			return nil, d.err
		}
		if d.err != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("discover public SearXNG instances: %w", ctx.Err())
		}
	}
}

// errDiscoveryCancelled marks an attempt that ended because the query
// running it was cancelled or ran out of time: not an answer about
// searx.space, so the queries waiting on it discover again.
var errDiscoveryCancelled = errors.New("discovery cancelled")

// runDiscovery runs one attempt for every query waiting on d.
func (c *SearXNGClient) runDiscovery(ctx context.Context, d *discovery) {
	urls, err := c.discover(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	defer close(d.done)
	c.discovering = nil
	switch {
	case err != nil && ctx.Err() != nil:
		d.err = fmt.Errorf("discover public SearXNG instances: %w: %w", errDiscoveryCancelled, err)
	case err != nil:
		d.err = fmt.Errorf("discover public SearXNG instances: %w", err)
	default:
		c.discover = nil
		for _, u := range urls {
			// Public instances are read through HTML from the start: measured,
			// almost none serve JSON, and asking first doubles every query.
			in := &searxInstance{url: u}
			in.html.Store(true)
			c.instances = append(c.instances, in)
		}
	}
}

// Name is "searxng".
func (c *SearXNGClient) Name() string { return EngineSearXNG }

// Preferred is the instance the next query goes to first: after a success,
// the one that answered.
func (c *SearXNGClient) Preferred() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.instances) == 0 {
		return ""
	}
	return c.instances[0].url
}

// prefer moves the instance that just answered to the front, so the next
// query skips the ones that refused.
func (c *SearXNGClient) prefer(in *searxInstance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.Index(c.instances, in); i > 0 {
		c.instances = slices.Insert(slices.Delete(c.instances, i, i+1), 0, in)
	}
}

// searchOne queries one instance, through JSON unless it has refused it, and
// tags the results with it.
func (c *SearXNGClient) searchOne(ctx context.Context, in *searxInstance, query string) ([]SearchResult, error) {
	var res []SearchResult
	var err error
	if !in.html.Load() {
		res, err = c.searchJSON(ctx, in.url, query)
		if refusedJSON(err) {
			in.html.Store(true)
		}
	}
	if in.html.Load() {
		res, err = c.searchHTML(ctx, in.url, query)
	}
	for i := range res {
		res[i].instance = in.url
	}
	return res, err
}

// errOffTopic is an instance whose answer held nothing on the query's
// subject. Two runs got the same ten results for different queries — ruby
// hotels for any query starting "Ruby", chatbot homepages for "AI" — from
// an instance that searched the first word alone; counting that as an answer
// let it be asked first every time.
var errOffTopic = errors.New("answered with only off-topic results")

// restOffTopic rests instance after an answer that was all off-topic, as
// for a load failure: a healthy instance can miss on a niche query. The
// reader's lone instance never rests, as in Search.
func (c *SearXNGClient) restOffTopic(instance string) {
	c.mu.Lock()
	pool := len(c.instances) > 1
	i := slices.IndexFunc(c.instances, func(in *searxInstance) bool { return in.url == instance })
	var in *searxInstance
	if i >= 0 {
		in = c.instances[i]
		// Off the front, so the next query does not go to it first.
		c.instances = append(slices.Delete(c.instances, i, i+1), in)
	}
	c.mu.Unlock()
	if pool && in != nil {
		in.rest(c.clock(), errOffTopic)
	}
}

// refusedJSON reports whether err is an instance declining the JSON API or
// its limiter refusing it, either of which its HTML page may still serve.
func refusedJSON(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.Code == http.StatusForbidden || he.Code == http.StatusTooManyRequests || he.Code == http.StatusTeapot
	}
	return errors.Is(err, errNotJSON)
}

func (c *SearXNGClient) get(ctx context.Context, base, query string, asJSON bool) (*http.Response, error) {
	u := base + "/search?q=" + url.QueryEscape(query) + "&categories=general"
	if asJSON {
		u += "&format=json"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}
	// An honest, stable User-Agent; the Accept headers are what SearXNG's bot
	// detection checks a browser-like request for.
	req.Header.Set("User-Agent", "deep-research-agent/1.0")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if asJSON {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/html")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if reason, ok := DialFailure(err); ok {
			return nil, &unreachableError{url: base, reason: reason, err: err}
		}
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, &httpError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

func (c *SearXNGClient) searchJSON(ctx context.Context, base, query string) ([]SearchResult, error) {
	resp, err := c.get(ctx, base, query, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Some instances ignore format=json and serve their HTML page with 200.
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return nil, errNotJSON
	}
	var data struct {
		Results      []SearchResult `json:"results"`
		Unresponsive [][]string     `json:"unresponsive_engines"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	if len(data.Results) == 0 && len(data.Unresponsive) > 0 {
		return nil, newEnginesDown(data.Unresponsive)
	}
	return c.limit(data.Results), nil
}

// enginesDownError is an instance that answered with no results while its
// own upstream engines failed. SearXNG says so with a 200, and taking it as
// an empty answer told a run that nothing on the web matched: after a burst
// of searches got Google suspended, every site of a --domains list was
// reported as one no engine indexes.
type enginesDownError struct {
	engines []string // "google cse (Suspended: too many requests)"
	limited bool     // one of them was rate-limited or suspended
}

func newEnginesDown(unresponsive [][]string) *enginesDownError {
	e := &enginesDownError{}
	for _, u := range unresponsive {
		if len(u) == 0 {
			continue
		}
		name, why := u[0], ""
		if len(u) > 1 {
			why = u[1]
		}
		e.engines = append(e.engines, fmt.Sprintf("%s (%s)", name, why))
		low := strings.ToLower(why)
		e.limited = e.limited || strings.Contains(low, "too many requests") || strings.Contains(low, "suspended")
	}
	return e
}

func (e *enginesDownError) Error() string {
	return "no results, and its engines did not answer: " + strings.Join(e.engines, ", ")
}

func (c *SearXNGClient) searchHTML(ctx context.Context, base, query string) ([]SearchResult, error) {
	resp, err := c.get(ctx, base, query, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	res, err := parseResultsHTML(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	return c.limit(res), nil
}

func (c *SearXNGClient) limit(res []SearchResult) []SearchResult {
	if len(res) > c.ResultLimit {
		return res[:c.ResultLimit]
	}
	return res
}

// parseResultsHTML reads SearXNG's own result markup: each article.result
// holds an h3 > a[href] with the title and a p.content with the snippet. A
// page with no results list at all (id="urls") is not a search that found
// nothing — it is a challenge or an error page.
func parseResultsHTML(r io.Reader) ([]SearchResult, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse search page: %w", err)
	}
	var out []SearchResult
	found := false
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if attr(n, "id") == "urls" {
			found = true
		}
		if n.Data == "article" && hasClass(n, "result") {
			if res, ok := parseArticle(n); ok {
				out = append(out, res)
			}
		}
	}
	if !found {
		return nil, errChallenge
	}
	return out, nil
}

func parseArticle(article *html.Node) (SearchResult, bool) {
	var res SearchResult
	for n := range article.Descendants() {
		switch {
		case n.Type != html.ElementNode:
		case n.Data == "a" && res.URL == "" && n.Parent != nil && n.Parent.Data == "h3":
			res.URL, res.Title = attr(n, "href"), text(n)
		case n.Data == "p" && hasClass(n, "content") && res.Content == "":
			res.Content = text(n)
		}
	}
	return res, res.URL != ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	return slices.Contains(strings.Fields(attr(n, "class")), class)
}

// text is n's visible text with whitespace collapsed.
func text(n *html.Node) string {
	var sb strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			sb.WriteString(d.Data)
			sb.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

// DialFailure reports whether err failed before any server answered, and why
// in a few words. A dial error's Op is "dial" only while the connection is
// being established, exactly the window where no server was reached: that
// covers connection refused, no route to host and network unreachable on
// every platform, without naming platform-specific errno values.
func DialFailure(err error) (reason string, ok bool) {
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return "host not found", true
	}
	opErr, ok := errors.AsType[*net.OpError](err)
	if !ok || opErr.Op != "dial" {
		return "", false
	}
	if opErr.Err == nil {
		return "connection failed", true
	}
	// "connect: connection refused" -> "connection refused".
	return strings.TrimPrefix(opErr.Err.Error(), "connect: "), true
}

// unreachableError is an instance nothing answered at. The raw error is a
// URL-escaped query and a syscall ("Get \"…&format=json\": dial tcp [::1]:8888:
// connect: connection refused"); what the reader acts on is which server and
// why.
type unreachableError struct {
	// service names what was not reached; empty reads as SearXNG, the one
	// engine whose address the reader configures.
	service     string
	url, reason string
	err         error
}

func (e *unreachableError) Error() string {
	return fmt.Sprintf("cannot reach %s at %s (%s)", cmp.Or(e.service, "SearXNG"), e.url, e.reason)
}

func (e *unreachableError) Unwrap() error { return e.err }

// UnreachableService names the service err could not reach — "SearXNG",
// "Exa" or "Tavily" — or "" when err is not a failure to reach one. The hint
// that follows a failed run depends on it: SEARXNG_URL is no help when the
// service nothing answered at was Exa.
func UnreachableService(err error) string {
	if ue, ok := errors.AsType[*unreachableError](err); ok {
		return cmp.Or(ue.service, "SearXNG")
	}
	return ""
}

// SearchStatus names how a search failed, for the run's events: a limiter or
// a challenge is a state to report, not an info line.
func SearchStatus(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errChallenge) {
		return "challenge"
	}
	var he *httpError
	if errors.As(err, &he) {
		switch he.Code {
		case http.StatusTooManyRequests:
			return "rate-limited"
		case http.StatusUnauthorized:
			// A missing or wrong key, which no retry or other instance will
			// fix — when the service takes one.
			if he.Keyed {
				return "unauthorized"
			}
		case http.StatusPaymentRequired, tavilyPlanLimit, tavilyKeyLimit:
			if he.Keyed {
				return "quota"
			}
		case http.StatusForbidden:
			return "blocked"
		case http.StatusTeapot, http.StatusAccepted:
			return "challenge"
		}
	}
	if _, ok := errors.AsType[*unreachableError](err); ok {
		return "unreachable"
	}
	if ed, ok := errors.AsType[*enginesDownError](err); ok && ed.limited {
		return "rate-limited"
	}
	return "unavailable"
}

// DiscoverInstances reads the searx.space list and returns the instances worth
// a probe: normal network, reachable, no analytics, some search success —
// best first. It is a candidate list, not a working one: searx.space's own
// checker is whitelisted by most limiters, so only a real query proves an
// instance will answer this client.
func DiscoverInstances(ctx context.Context, client *http.Client, listURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", listURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "deep-research-agent/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{Code: resp.StatusCode}
	}
	var data struct {
		Instances map[string]struct {
			NetworkType string `json:"network_type"`
			Analytics   any    `json:"analytics"`
			HTTP        struct {
				StatusCode int `json:"status_code"`
			} `json:"http"`
			Timing struct {
				Search struct {
					SuccessPercentage float64 `json:"success_percentage"`
				} `json:"search"`
			} `json:"timing"`
		} `json:"instances"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8*1024*1024)).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode instance list: %w", err)
	}
	type cand struct {
		url     string
		success float64
	}
	var cands []cand
	for u, in := range data.Instances {
		if in.NetworkType != "normal" || in.HTTP.StatusCode != http.StatusOK ||
			in.Timing.Search.SuccessPercentage <= 0 || (in.Analytics != nil && in.Analytics != false) {
			continue
		}
		cands = append(cands, cand{strings.TrimRight(u, "/"), in.Timing.Search.SuccessPercentage})
	}
	slices.SortFunc(cands, func(a, b cand) int {
		return cmp.Or(cmp.Compare(b.success, a.success), strings.Compare(a.url, b.url))
	})
	out := make([]string, 0, min(len(cands), maxAutoInstances))
	for _, c := range cands[:min(len(cands), maxAutoInstances)] {
		out = append(out, c.url)
	}
	if len(out) == 0 {
		return nil, errors.New("searx.space lists no usable instance")
	}
	return out, nil
}
