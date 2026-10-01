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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
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
}

// RunFunc runs one request and writes its JSONL events to w, ending with the
// done line on every outcome.
type RunFunc func(ctx context.Context, r Request, w io.Writer)

// maxLines bounds the replay buffer. Events are short (a status line, a URL),
// so the line count bounds the memory too.
const maxLines = 10000

// Server holds one run at a time and the lines it has written so far.
//
// One run, not a run per id: a personal CLI writes every run to the same
// reports directory, and two at once would only race for it.
type Server struct {
	run     RunFunc
	reports fs.FS
	ctx     context.Context
	max     int

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
	mux.HandleFunc("POST /api/run", s.launch)
	mux.HandleFunc("POST /api/cancel", s.stop)
	mux.Handle("GET /reports/", http.StripPrefix("/reports/", http.FileServerFS(s.reports)))
	// Cross-origin protection refuses another site's POST (a cancel is a
	// simple request); the Host check refuses a page that rebound its own
	// name to 127.0.0.1, which is same-origin to the browser.
	return localOnly(http.NewCrossOriginProtection().Handler(mux))
}

// Serve listens on addr and serves the reports directory read-only. It
// returns only when the listener fails: ctx bounds the runs, not the server,
// which stops with the process.
func Serve(ctx context.Context, addr, reports string, run RunFunc, log io.Writer) error {
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
	srv := &http.Server{Handler: New(ctx, root.FS(), run).Handler(), ReadHeaderTimeout: 10 * time.Second}
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

// run is one past run's .json artifact, as the start screen lists it.
type run struct {
	Name     string    `json:"name"`
	Modified time.Time `json:"modified"`
}

// runs lists the .json artifacts in the reports directory, newest first: the
// reports are the run history the page can open, so it keeps none of its own.
func (s *Server) runs(w http.ResponseWriter, _ *http.Request) {
	entries, err := fs.ReadDir(s.reports, ".")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []run{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".trace.json") {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, run{name, info.ModTime()})
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
	// clear it; it carries what was asked, which no ui event repeats.
	start, _ := json.Marshal(struct {
		Type string    `json:"type"`
		Time time.Time `json:"time"`
		Request
	}{"start", time.Now(), req})
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
