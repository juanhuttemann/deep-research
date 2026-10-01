package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type sse struct {
	id    int64
	event string
	data  string
}

// readEvents reads SSE messages from body until it has n of them.
func readEvents(t *testing.T, sc *bufio.Scanner, n int) []sse {
	t.Helper()
	var out []sse
	var cur sse
	for len(out) < n && sc.Scan() {
		switch ln := sc.Text(); {
		case ln == "":
			out = append(out, cur)
			cur = sse{}
		case strings.HasPrefix(ln, "id: "):
			cur.id, _ = strconv.ParseInt(ln[4:], 10, 64)
		case strings.HasPrefix(ln, "event: "):
			cur.event = ln[7:]
		case strings.HasPrefix(ln, "data: "):
			cur.data = ln[6:]
		}
	}
	if len(out) < n {
		t.Fatalf("stream ended after %d of %d events: %v", len(out), n, sc.Err())
	}
	return out
}

// connect opens the event stream, resuming after cursor when it is not "".
func connect(t *testing.T, url, cursor string) (*bufio.Scanner, func()) {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/api/events", nil)
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	// A stream missing an event waits forever; the timeout fails it instead.
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return bufio.NewScanner(resp.Body), func() { _ = resp.Body.Close() }
}

func launch(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post(url+"/api/run", "application/json", strings.NewReader(`{"question":"q"}`))
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A reload resumes from Last-Event-ID: it gets every line after the one it
// saw, none twice, through to the done line, even though the run wrote each
// line in two pieces.
func TestReplayResumesWithoutDuplicates(t *testing.T) {
	step := make(chan struct{})
	run := func(ctx context.Context, _ Request, w io.Writer) {
		for i := range 4 {
			if i == 2 {
				<-step
			}
			_, _ = fmt.Fprintf(w, `{"type":"info","detail":"%d"}`, i)
			_, _ = io.WriteString(w, "\n")
		}
		_, _ = io.WriteString(w, `{"type":"done","status":"complete"}`+"\n")
	}
	srv := httptest.NewServer(New(context.Background(), fstest.MapFS{}, run).Handler())
	defer srv.Close()

	if code := launch(t, srv.URL); code != http.StatusAccepted {
		t.Fatalf("launch = %d", code)
	}
	sc, closeFirst := connect(t, srv.URL, "")
	first := readEvents(t, sc, 3) // start, 0, 1
	closeFirst()
	if !strings.Contains(first[0].data, `"type":"start"`) || !strings.Contains(first[2].data, `"detail":"1"`) {
		t.Fatalf("first connection got %v", first)
	}
	close(step)

	sc, closeSecond := connect(t, srv.URL, strconv.FormatInt(first[2].id, 10))
	defer closeSecond()
	rest := readEvents(t, sc, 3) // 2, 3, done
	for i, e := range rest {
		if e.id != first[2].id+int64(i)+1 {
			t.Errorf("event %d has id %d after cursor %d", i, e.id, first[2].id)
		}
	}
	if !strings.Contains(rest[0].data, `"detail":"2"`) || !strings.Contains(rest[2].data, `"type":"done"`) {
		t.Errorf("resumed stream = %v, want lines 2, 3 and done", rest)
	}
}

// Ids keep counting across runs, so a tab holding the last run's id replays
// the new run from its start line instead of skipping it; a cursor whose lines
// were dropped gets a gap, not a stream that quietly starts mid-run.
func TestCursorsAcrossRunsAndGaps(t *testing.T) {
	lines := 0
	run := func(_ context.Context, _ Request, w io.Writer) {
		for range lines {
			_, _ = io.WriteString(w, "{}\n")
		}
	}
	s := New(context.Background(), fstest.MapFS{}, run)
	s.max = 4
	wait := func() {
		for {
			s.mu.Lock()
			idle := s.cancel == nil
			s.mu.Unlock()
			if idle {
				return
			}
			runtime.Gosched()
		}
	}

	lines = 2
	s.begin(Request{})
	wait()
	got, _, _, _ := s.since(-1)
	if len(got) != 3 {
		t.Fatalf("first run replays %d lines, want start + 2", len(got))
	}
	last := s.base + int64(len(s.lines)) - 1

	lines = 10
	s.begin(Request{})
	wait()
	if s.start <= last {
		t.Fatalf("second run starts at id %d, not after the first run's %d", s.start, last)
	}
	got, from, gap, _ := s.since(last)
	if !gap || from != s.base || len(got) != s.max {
		t.Errorf("old cursor: gap=%v from=%d (base %d) lines=%d, want a gap and the %d kept lines", gap, from, s.base, len(got), s.max)
	}
	if _, _, gap, _ := s.since(s.base); gap {
		t.Error("a cursor inside the kept lines reported a gap")
	}
	if got, from, _, _ := s.since(1 << 62); from != s.base || len(got) != s.max {
		t.Errorf("a cursor ahead of the stream did not restart at the run: from=%d lines=%d", from, len(got))
	}
}

// The launch spends API credits, so it takes JSON only (another site's form
// or text/plain POST needs no preflight), answers only to localhost (a
// rebound name is same-origin to the browser), and runs one at a time.
func TestLaunchGuards(t *testing.T) {
	release := make(chan struct{})
	run := func(ctx context.Context, _ Request, w io.Writer) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		_, _ = io.WriteString(w, `{"type":"done","status":"cancelled"}`+"\n")
	}
	srv := httptest.NewServer(New(context.Background(), fstest.MapFS{}, run).Handler())
	defer srv.Close()
	defer close(release)

	resp, _ := http.Post(srv.URL+"/api/run", "text/plain", strings.NewReader(`{"question":"q"}`))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain launch = %d, want 415", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Host = "evil.example:80"
	resp, _ = http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host = %d, want 403", resp.StatusCode)
	}
	// A server that has run nothing says so, or a tab reconnecting after a
	// restart keeps showing the dead process's run as live.
	sc, closeIdle := connect(t, srv.URL, "12345")
	if got := readEvents(t, sc, 1); got[0].event != "idle" {
		t.Errorf("fresh server's first event = %+v, want idle", got[0])
	}
	closeIdle()
	// httptest listens on 127.0.0.1, which the Host check accepts.
	if code := launch(t, srv.URL); code != http.StatusAccepted {
		t.Fatalf("launch = %d", code)
	}
	if code := launch(t, srv.URL); code != http.StatusConflict {
		t.Errorf("second launch while one runs = %d, want 409", code)
	}
	resp, _ = http.Post(srv.URL+"/api/cancel", "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("cancel = %d, want 202", resp.StatusCode)
	}
	sc, done := connect(t, srv.URL, "")
	defer done()
	if got := readEvents(t, sc, 2); got[0].event == "idle" || !strings.Contains(got[1].data, "cancelled") {
		t.Errorf("cancelled run's stream = %v", got)
	}
}

// The start screen lists past runs from the reports directory, newest first,
// without the trace files that sit beside them.
func TestRunsListsReportsNewestFirst(t *testing.T) {
	now := time.Now()
	reports := fstest.MapFS{
		"old-1.json":       {ModTime: now.Add(-time.Hour)},
		"new-2.json":       {ModTime: now},
		"new-2.md":         {ModTime: now},
		"new-2.trace.json": {ModTime: now},
	}
	srv := httptest.NewServer(New(context.Background(), reports, nil).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/runs")
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got []run
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Name != "new-2.json" || got[1].Name != "old-1.json" {
		t.Errorf("runs = %+v, want new-2.json then old-1.json", got)
	}
}

// The page calls the server by path; a typo in one is invisible until a
// button does nothing.
func TestPageCallsTheServedEndpoints(t *testing.T) {
	for _, path := range []string{`"/api/events"`, `"/api/runs"`, `"/api/run"`, `"/api/cancel"`, `"/reports/"`} {
		if !strings.Contains(string(page), path) {
			t.Errorf("index.html never calls %s", path)
		}
	}
}

// The page hands back the plan it edited; the server passes it to the run as
// the page wrote it, and the run checks it.
func TestLaunchPassesThePlanThrough(t *testing.T) {
	got := make(chan Request, 1)
	run := func(_ context.Context, r Request, w io.Writer) {
		got <- r
		_, _ = io.WriteString(w, `{"type":"done","status":"complete"}`+"\n")
	}
	srv := httptest.NewServer(New(context.Background(), fstest.MapFS{}, run).Handler())
	defer srv.Close()
	// A launch made before the last run released its slot is refused; the
	// page would try again, and so does this.
	post := func(body string) Request {
		t.Helper()
		for range 200 {
			resp, err := http.Post(srv.URL+"/api/run", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				return <-got
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("the server never took the launch")
		return Request{}
	}
	plan := `{"question":"q","sub_topics":[{"name":"a"}]}`
	if r := post(`{"plan":` + plan + `}`); string(r.Plan) != plan {
		t.Errorf("the run got plan %s, want %s", r.Plan, plan)
	}
	// The page's Research sends plan_only; misread, every launch would run
	// in full before the reader saw the plan.
	if r := post(`{"question":"q","plan_only":true}`); !r.PlanOnly {
		t.Error("plan_only was not read from the request")
	}
}

// listRuns is the start screen's list of runs.
func listRuns(t *testing.T, url string) []run {
	t.Helper()
	resp, err := http.Get(url + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out []run
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// discardRun asks to discard name and returns the status.
func discardRun(t *testing.T, url, name string) int {
	t.Helper()
	req, _ := http.NewRequest("DELETE", url+"/api/runs/"+name, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// checkpointServer serves a report and an interrupted run's checkpoint; its
// runs wait for release.
func checkpointServer(t *testing.T) (srv *httptest.Server, reports fstest.MapFS, got chan Request, release chan struct{}) {
	reports = fstest.MapFS{
		"q-1.json":              {ModTime: time.Now()},
		"q-1.ab12.partial.json": {ModTime: time.Now()},
	}
	got, release = make(chan Request, 1), make(chan struct{})
	s := New(context.Background(), reports, func(_ context.Context, r Request, w io.Writer) {
		got <- r
		<-release
		_, _ = io.WriteString(w, `{"type":"done","status":"complete"}`+"\n")
	})
	s.remove = func(name string) error { delete(reports, name); return nil }
	srv = httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, reports, got, release
}

// An interrupted run's checkpoint is listed apart from finished runs, so the
// start screen can offer to resume it, and not while a run is active, which
// may be the one writing it. Resume takes only a checkpoint's bare name in
// the reports directory.
func TestInterruptedRunsAreOfferedForResume(t *testing.T) {
	srv, _, got, release := checkpointServer(t)
	defer close(release)
	if l := listRuns(t, srv.URL); len(l) != 2 || !slices.ContainsFunc(l, func(r run) bool { return r.Interrupted && r.Name == "q-1.ab12.partial.json" }) {
		t.Errorf("runs = %+v, want the report and the checkpoint marked interrupted", l)
	}
	for _, bad := range []string{"../q-1.ab12.partial.json", "sub/q.partial.json", "q-1.json"} {
		if code := post(t, srv.URL, `{"resume":"`+bad+`"}`); code != http.StatusBadRequest {
			t.Errorf("resume %q = %d, want 400", bad, code)
		}
	}
	if code := post(t, srv.URL, `{"resume":"q-1.ab12.partial.json"}`); code != http.StatusAccepted {
		t.Fatalf("resume = %d", code)
	}
	if r := <-got; r.Resume != "q-1.ab12.partial.json" {
		t.Errorf("the run got resume %q", r.Resume)
	}
	if l := listRuns(t, srv.URL); len(l) != 1 || l[0].Interrupted {
		t.Errorf("runs while one is active = %+v, want no checkpoint offered", l)
	}
}

// Discard gives up an interrupted run's saved work: only a checkpoint, and
// not while a run is active.
func TestDiscardRemovesOnlyACheckpoint(t *testing.T) {
	srv, reports, got, release := checkpointServer(t)
	if code := post(t, srv.URL, `{"question":"q"}`); code != http.StatusAccepted {
		t.Fatalf("launch = %d", code)
	}
	<-got
	if code := discardRun(t, srv.URL, "q-1.ab12.partial.json"); code != http.StatusConflict {
		t.Errorf("discard during a run = %d, want 409", code)
	}
	close(release)
	for range 200 {
		if discardRun(t, srv.URL, "q-1.ab12.partial.json") == http.StatusNoContent {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := reports["q-1.ab12.partial.json"]; ok {
		t.Error("discard left the checkpoint")
	}
	if code := discardRun(t, srv.URL, "q-1.json"); code != http.StatusBadRequest {
		t.Errorf("discard of a report = %d, want 400", code)
	}
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url+"/api/run", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
