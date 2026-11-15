package ui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The terminal and the serve page drifted apart: the terminal grew topic
// groups, colours and phase views, and the page kept a flat log, since
// nothing said a change to one surface was a change to the other.
// docs/surfaces.md lists what the two share; this holds the table, the page
// and the browser test to each other. It checks the structure, not the
// behaviour: that is parity.spec.js's, run by make web.

const (
	surfacesDoc = "../../docs/surfaces.md"
	webPage     = "../web/index.html"
	browserSpec = "../web/browser/parity.spec.js"
)

// surfaceFeature is one row of the table: its ID and the Go declarations
// the terminal draws it with.
type surfaceFeature struct {
	id       string
	terminal []string
}

var (
	featureRow  = regexp.MustCompile("^\\| `([a-z-]+)` \\|")
	codeSpan    = regexp.MustCompile("`([^`]+)`")
	featureAttr = regexp.MustCompile(`data-feature="([^"]+)"`)
	specTag     = regexp.MustCompile(`test\("\[([a-z-]+)\]`)
)

// surfaceFeatures reads the table's rows; the terminal's column is the
// third.
func surfaceFeatures(t *testing.T) []surfaceFeature {
	t.Helper()
	raw, err := os.ReadFile(surfacesDoc)
	if err != nil {
		t.Fatal(err)
	}
	var out []surfaceFeature
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		m := featureRow.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		cells := strings.Split(sc.Text(), " | ")
		if len(cells) < 4 {
			t.Fatalf("surfaces.md row %q has no terminal column", m[1])
		}
		f := surfaceFeature{id: m[1]}
		for _, c := range codeSpan.FindAllStringSubmatch(cells[2], -1) {
			f.terminal = append(f.terminal, c[1])
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		t.Fatal("surfaces.md lists no features")
	}
	return out
}

// pageFeatures are the data-feature markers on the page, each attribute a
// space-separated list.
func pageFeatures(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(webPage)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range featureAttr.FindAllStringSubmatch(string(raw), -1) {
		for _, id := range strings.Fields(m[1]) {
			out[id] = true
		}
	}
	return out
}

// specFeatures are the features the browser test's names are tagged with.
func specFeatures(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(browserSpec)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range specTag.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	return out
}

// uiDecls are the package's declarations outside its tests: functions as
// "Name" or "Type.Method", and package-level vars and consts.
func uiDecls(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	files, err := fs.Glob(os.DirFS("."), "*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			for _, n := range declNames(d) {
				out[n] = true
			}
		}
	}
	return out
}

func declNames(d ast.Decl) []string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil || len(d.Recv.List) == 0 {
			return []string{d.Name.Name}
		}
		return []string{receiverName(d.Recv.List[0].Type) + "." + d.Name.Name}
	case *ast.GenDecl:
		var out []string
		for _, s := range d.Specs {
			if v, ok := s.(*ast.ValueSpec); ok {
				for _, n := range v.Names {
					out = append(out, n.Name)
				}
			}
		}
		return out
	}
	return nil
}

func receiverName(e ast.Expr) string {
	if s, ok := e.(*ast.StarExpr); ok {
		e = s.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func TestSurfacesParity(t *testing.T) {
	features := surfaceFeatures(t)
	page, spec, decls := pageFeatures(t), specFeatures(t), uiDecls(t)
	listed := map[string]bool{}
	for _, f := range features {
		listed[f.id] = true
		if !page[f.id] {
			t.Errorf("%s: no data-feature=%q marker in index.html: the page lacks a feature the terminal has", f.id, f.id)
		}
		if !spec[f.id] {
			t.Errorf("%s: no test tagged [%s] in parity.spec.js", f.id, f.id)
		}
		if len(f.terminal) == 0 {
			t.Errorf("%s: names no terminal function", f.id)
		}
		for _, d := range f.terminal {
			if !decls[d] {
				t.Errorf("%s: the terminal's %s is gone from internal/ui; update docs/surfaces.md with what draws it now", f.id, d)
			}
		}
	}
	for id := range page {
		if !listed[id] {
			t.Errorf("index.html marks data-feature %q, which docs/surfaces.md does not list", id)
		}
	}
	for id := range spec {
		if !listed[id] {
			t.Errorf("parity.spec.js tests [%s], which docs/surfaces.md does not list", id)
		}
	}
}

// The browser test's run is a run's events: each line but the server's own
// start line decodes as the events the driver writes, so the page is tested
// on what a run sends it.
func TestBrowserFixtureIsEvents(t *testing.T) {
	raw, err := os.ReadFile("../web/browser/testdata/run.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	types := map[EventType]bool{}
	for i, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if i == 0 && bytes.Contains(line, []byte(`"type":"start"`)) {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		var e Event
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("line %d: %v\n%s", i+1, err, line)
		}
		types[e.Type] = true
	}
	for _, want := range []EventType{PlanReady, Phase, SubAgent, Search, Read, Verify, Citation, Thinking, Error, Info} {
		if !types[want] {
			t.Errorf("the fixture has no %q event: the page's handling of it goes untested", want)
		}
	}
}
