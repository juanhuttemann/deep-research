// Package web serves one local page that launches a run, streams its events
// and shows what the fact-check decided. It knows nothing of the pipeline:
// the run is a function the cli injects, and the events are the raw --jsonl
// lines that run writes, passed through unparsed. If the page needs
// something, the event stream or the .json sidecar is missing it.
package web

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed index.html
var page []byte

// Request is what the page launches: the question, --mode and --sources a
// --jsonl run takes, with --plan-only to make the plan the page then edits,
// or the edited plan itself (--plan). The plan passes through as the page
// wrote it; the run checks it.
type Request struct {
	Question string          `json:"question,omitempty"`
	Mode     string          `json:"mode,omitempty"`
	Sources  int             `json:"sources,omitempty"`
	PlanOnly bool            `json:"plan_only,omitempty"`
	Plan     json.RawMessage `json:"plan,omitempty"`
	// Resume names an interrupted run's checkpoint in the reports
	// directory (--resume).
	Resume string `json:"resume,omitempty"`
}

// checkpointSuffix ends the file an interrupted run left in the reports
// directory, the one Resume names.
const checkpointSuffix = ".partial.json"

// isCheckpoint reports whether name is a checkpoint directly in the reports
// directory: a bare file name, so a request cannot name a path elsewhere.
func isCheckpoint(name string) bool {
	return strings.HasSuffix(name, checkpointSuffix) && path.Base(name) == name && !strings.ContainsAny(name, `/\`)
}

// isRunName reports whether name is a finished run's .json directly in the
// reports directory: a bare file name, and not a checkpoint or a trace,
// which hold no report.
func isRunName(name string) bool {
	return strings.HasSuffix(name, ".json") && path.Base(name) == name && !strings.ContainsAny(name, `/\`) &&
		!isCheckpoint(name) && !strings.HasSuffix(name, ".trace.json")
}

// RunFunc runs one request and writes its JSONL events to w, ending with the
// done line on every outcome.
type RunFunc func(ctx context.Context, r Request, w io.Writer)

// Turn is one question about a run and the answer it got, as the page shows
// the conversation.
type Turn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// AskFunc answers question about the run whose .json is named run in the
// reports directory, after the conversation so far. It returns Markdown.
type AskFunc func(ctx context.Context, run string, turns []Turn, question string) (string, error)

// maxLines bounds the replay buffer by line count, not bytes. Events are
// short (a status line, a URL); the longest is the plan event, a few KB, so
// the count bounds the memory too.
const maxLines = 10000

// Server holds one run at a time and the lines it has written so far.
//
// One run, not a run per id: a personal CLI writes every run to the same
// reports directory, and two at once would only race for it.
type Server struct {
	run     RunFunc
	reports fs.FS
	// Ask answers questions about a finished run; nil refuses them.
	Ask AskFunc
	// remove deletes a file in the reports directory: a discarded
	// checkpoint. Nil refuses every discard.
	remove func(name string) error
	ctx    context.Context
	max    int

	mu     sync.Mutex
	cancel context.CancelFunc // nil when no run is active
	// Ids are one counter across runs, seeded from the clock: a browser
	// holding an id from the previous run, or from before a restart, must
	// never be ahead of the run now streaming, or it would skip all of it.
	start   int64 // id of the current run's first line
	base    int64 // id of lines[0]
	lines   [][]byte
	partial []byte
	// changed is closed and replaced on every append: each stream waits on
	// it and then reads the buffer at its own pace, so a slow tab can fall
	// behind (and get a gap) but never holds up the run.
	changed chan struct{}
}

// New returns a server whose runs live as long as ctx, not as long as the
// request that launched them: a reload or a closed tab is not a decision to
// stop a run already paid for.
func New(ctx context.Context, reports fs.FS, run RunFunc) *Server {
	seed := time.Now().UnixMicro()
	return &Server{run: run, reports: reports, ctx: ctx, max: maxLines,
		start: seed, base: seed, changed: make(chan struct{})}
}

// Handler routes the page, the stream and the two controls.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/runs", s.runs)
	mux.HandleFunc("DELETE /api/runs/{name}", s.discard)
	mux.HandleFunc("POST /api/run", s.launch)
	mux.HandleFunc("POST /api/cancel", s.stop)
	mux.HandleFunc("GET /api/report/{name}", s.report)
	mux.HandleFunc("POST /api/ask", s.ask)
	mux.Handle("GET /reports/", http.StripPrefix("/reports/", http.FileServerFS(s.reports)))
	// Cross-origin protection refuses another site's POST (a cancel is a
	// simple request); the Host check refuses a page that rebound its own
	// name to 127.0.0.1, which is same-origin to the browser.
	return localOnly(http.NewCrossOriginProtection().Handler(mux))
}

// Serve listens on addr and serves the reports directory read-only. It
// returns only when the listener fails: ctx bounds the runs, not the server,
// which stops with the process.
func Serve(ctx context.Context, addr, reports string, run RunFunc, ask AskFunc, log io.Writer) error {
	if err := os.MkdirAll(reports, 0o755); err != nil {
		return err
	}
	// os.Root, not http.Dir: http.Dir follows a symlink out of the directory.
	root, err := os.OpenRoot(reports)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// localhost, whatever the bind: the Host check refuses any other name.
	_, _ = fmt.Fprintf(log, "serving on http://localhost:%d\n", ln.Addr().(*net.TCPAddr).Port)
	s := New(ctx, root.FS(), run)
	s.remove = root.Remove
	s.Ask = ask
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if h := strings.Trim(host, "[]"); h != "localhost" && h != "127.0.0.1" && h != "::1" {
			http.Error(w, "serve answers only to localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// run is one past run's .json artifact, or an interrupted run's checkpoint,
// as the start screen lists it.
type run struct {
	Name        string    `json:"name"`
	Modified    time.Time `json:"modified"`
	Interrupted bool      `json:"interrupted,omitempty"`
}

// runs lists the .json artifacts in the reports directory, newest first: the
// reports are the run history the page can open, so it keeps none of its own.
func (s *Server) runs(w http.ResponseWriter, _ *http.Request) {
	entries, err := fs.ReadDir(s.reports, ".")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// While a run is active its checkpoint is not an interrupted run, and
	// no other can be resumed until it ends.
	s.mu.Lock()
	active := s.cancel != nil
	s.mu.Unlock()
	out := []run{}
	for _, e := range entries {
		name := e.Name()
		interrupted := isCheckpoint(name)
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".trace.json") || (interrupted && active) {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, run{name, info.ModTime(), interrupted})
		}
	}
	slices.SortFunc(out, func(a, b run) int { return b.Modified.Compare(a.Modified) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out[:min(len(out), 50)])
}

// launch starts a run. It insists on a JSON body: a form or text/plain POST
// is a request another site can send without a preflight, and a run spends
// API credits.
func (s *Server) launch(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "launch takes application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Resume != "" && !isCheckpoint(req.Resume) {
		http.Error(w, "resume takes a checkpoint name from the reports directory", http.StatusBadRequest)
		return
	}
	if !s.begin(req) {
		http.Error(w, "a run is already active", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// begin claims the run slot and starts the run, or reports that one is
// already active. The slot is held until the run function returns, which is
// after its artifacts, its history record and its done line are written.
func (s *Server) begin(req Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancel = cancel
	s.start = s.base + int64(len(s.lines))
	s.base, s.lines, s.partial = s.start, nil, nil
	// The first line of every run, so a tab showing the last one knows to
	// clear it; it carries what was asked, which no ui event repeats. Not an
	// edited plan: the run's plan event carries that, and a 64 KB request
	// body would make this the longest line the buffer keeps.
	shown := req
	shown.Plan = nil
	start, _ := json.Marshal(struct {
		Type string    `json:"type"`
		Time time.Time `json:"time"`
		Request
	}{"start", time.Now(), shown})
	s.appendLocked(start)
	go func() {
		s.run(ctx, req, s)
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}()
	return true
}

// discard deletes an interrupted run's checkpoint: the work it saved is given
// up. Only a checkpoint, and not while a run is active, which may be the one
// writing it.
func (s *Server) discard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isCheckpoint(name) || s.remove == nil {
		http.Error(w, "only an interrupted run's checkpoint can be discarded", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	active := s.cancel != nil
	s.mu.Unlock()
	if active {
		http.Error(w, "a run is active", http.StatusConflict)
		return
	}
	if err := s.remove(name); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The report view is for reading; the audit beside it keeps every source. So
// citations come out, and out of the rendered HTML, not the Markdown: there a
// link's destination is an attribute and "<" and ">" are escaped everywhere
// else, so a pattern can only match what the reader sees. Rewriting the
// Markdown cut "(c1)" out of a URL and a code sample.
var (
	// citeGroup is parentheses holding only links: "(<a>a</a>, <a>b</a>)",
	// joined by ",", ";" or "and" as prose joins them, labels with markup or
	// not. The "<" that opens a link cannot occur in an attribute or in code.
	// A label is anything but "</a": a lazy .*? ran on to a later link and
	// took the prose between, "(A argues this, while B)", with it.
	// ponytail: a group joined by any other word ("or", "see") stays in; add
	// it here if reports write it.
	citeGroup = regexp.MustCompile(`\s*\((?:\s*(?:and\s+)?<a\b[^>]*>(?:[^<]|<[^/]|</[^a])*</a>\s*[,;]?)+\s*\)`)
	// claimIDs are claim IDs the summarizer left in, "[c4, c6]" or "(c1)".
	claimIDs = regexp.MustCompile(`\s*[\[(]c\d+(?:\s*(?:[,;]|and)\s*c\d+)*[\])]`)
	// htmlToken is a tag, or the text between two. Every byte is in some
	// token, an unclosed "<" included, so joining them loses nothing.
	htmlToken = regexp.MustCompile(`<[^>]*>?|[^<]+`)
	// markdown is shared by every request: goldmark sets its parsers up once
	// and keeps each conversion's state to the call.
	markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))
)

// uncite drops the citations from rendered report HTML: the source groups
// wherever they are, the claim IDs only in text outside code.
func uncite(html string) string {
	var out strings.Builder
	code := 0
	for _, tok := range htmlToken.FindAllString(citeGroup.ReplaceAllString(html, ""), -1) {
		switch {
		case strings.HasPrefix(tok, "<code") || strings.HasPrefix(tok, "<pre"):
			code++
		case tok == "</code>" || tok == "</pre>":
			code--
		case tok[0] != '<' && code == 0:
			tok = claimIDs.ReplaceAllString(tok, "")
		}
		out.WriteString(tok)
	}
	return out.String()
}

// report renders a run's report for reading: the .json sidecar's Markdown
// without its citations. Goldmark's default renderer omits raw HTML and
// dangerous link schemes; the page applies its own link rule on top.
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isRunName(name) {
		http.Error(w, "report takes a run's .json name from the reports directory", http.StatusBadRequest)
		return
	}
	data, err := fs.ReadFile(s.reports, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var sidecar struct {
		Report string `json:"report"`
	}
	// A summarizer that wrote nothing leaves a complete run with an empty
	// report; the page then shows the audit alone. (A failed run writes no
	// sidecar, and a partial one writes a placeholder report.)
	if json.Unmarshal(data, &sidecar) != nil || strings.TrimSpace(sidecar.Report) == "" {
		http.Error(w, "this run has no report", http.StatusNotFound)
		return
	}
	var out bytes.Buffer
	if err := markdown.Convert([]byte(sidecar.Report), &out); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, uncite(out.String()))
}

// maxAskBody bounds a question with the conversation before it, which the
// page sends whole each time: a long conversation outgrows a launch's limit.
const maxAskBody = 1 << 20

// ask answers a question about a finished run and returns the answer as
// written and rendered, its links kept: they are its sources. Like a launch it spends
// model requests, so it takes JSON only; it does not need the run slot, and
// a question can be asked while a run is going.
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "ask takes application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req struct {
		Run      string `json:"run"`
		Question string `json:"question"`
		Turns    []Turn `json:"turns"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAskBody)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch {
	case !isRunName(req.Run):
		http.Error(w, "ask takes a run's .json name from the reports directory", http.StatusBadRequest)
		return
	case strings.TrimSpace(req.Question) == "":
		http.Error(w, "the question is empty", http.StatusBadRequest)
		return
	case s.Ask == nil:
		http.Error(w, "asking is not available", http.StatusNotImplemented)
		return
	}
	if _, err := fs.Stat(s.reports, req.Run); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	answer, err := s.Ask(r.Context(), req.Run, req.Turns, strings.TrimSpace(req.Question))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var out bytes.Buffer
	if err := markdown.Convert([]byte(answer), &out); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The Markdown goes back too: the page returns it as the next question's
	// conversation, and the rendered text alone would lose the cited URLs.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Answer string `json:"answer"`
		HTML   string `json:"html"`
	}{answer, out.String()})
}

func (s *Server) stop(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel == nil {
		http.Error(w, "no run is active", http.StatusConflict)
		return
	}
	cancel()
	w.WriteHeader(http.StatusAccepted)
}

// Write takes the run's JSONL output. A line can arrive in pieces (the JSONL
// sink writes the object and its newline separately), so only whole lines
// reach the buffer.
func (s *Server) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		s.appendLocked(bytes.Clone(s.partial[:i]))
		s.partial = s.partial[i+1:]
	}
}

func (s *Server) appendLocked(line []byte) {
	s.lines = append(s.lines, line)
	if len(s.lines) > s.max {
		s.lines = s.lines[1:]
		s.base++
	}
	close(s.changed)
	s.changed = make(chan struct{})
}

// since returns the lines after cursor, the id of the first, whether lines
// between the cursor and them were dropped, and the channel that signals the
// next append. A cursor from an earlier run, a malformed one, or one ahead of
// the stream (a counter from another process) starts at the current run.
func (s *Server) since(cursor int64) (lines [][]byte, from int64, gap bool, changed <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.base + int64(len(s.lines))
	from = cursor + 1
	if from < s.start || from > next {
		from = s.start
	}
	if from < s.base {
		from, gap = s.base, true
	}
	return slices.Clone(s.lines[from-s.base:]), from, gap, s.changed
}

// events streams the current run as SSE, resuming after Last-Event-ID.
// A cursor whose lines were already dropped gets an explicit gap event, never
// a stream that silently starts in the middle: the page cannot rebuild the
// frame from a partial history and says so.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	cursor, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if err != nil {
		cursor = -1
	}
	// The buffer is empty only before this process's first run. A tab that
	// reconnects to it after a restart still shows the old process's run as
	// live, and nothing else in the stream would tell it otherwise.
	s.mu.Lock()
	idle := len(s.lines) == 0
	s.mu.Unlock()
	if idle {
		_, _ = io.WriteString(w, "event: idle\ndata: {}\n\n")
	}
	for {
		lines, from, gap, changed := s.since(cursor)
		if gap {
			_, _ = io.WriteString(w, "event: gap\ndata: {}\n\n")
		}
		for i, ln := range lines {
			_, _ = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", from+int64(i), ln)
		}
		if len(lines) > 0 {
			cursor = from + int64(len(lines)) - 1
		}
		if rc.Flush() != nil {
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}
