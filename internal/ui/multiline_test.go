package ui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// The question box took one line: Enter ran it, and a pasted break was a
// space. Ctrl-J (LF, where raw mode leaves Enter CR) and Enter after a
// backslash start a new line; Enter alone still runs it.
func TestQuestionBoxTakesSeveralLines(t *testing.T) {
	cases := []struct{ name, keys, want string }{
		{"ctrl-j", "line one\nline two\r", "line one\nline two"},
		{"backslash enter", "line one\\\rline two\r", "line one\nline two"},
		{"a backslash elsewhere stays", "a\\b\r", "a\\b"},
		{"up keeps the column", "abcd\nxy\x1b[A\x1b[DZ\r", "aZbcd\nxy"},
		{"down past the last line goes to its end", "ab\ncd\x1b[A\x1b[A\x1b[BQ\x1b[BW\r", "ab\nQcdW"},
		{"ctrl-w stops at a line break", "one\ntwo\x17x\r", "one\nx"},
		{"paste keeps its lines", "\x1b[200~a\r\n\tb  \r\n\r\n\x1b[201~\r", "a\n b"},
	}
	for _, c := range cases {
		r := NewRenderer(&bytes.Buffer{}, Theme{})
		got := askQuestion(NewByteReader([]byte(c.keys)), r, agent.Setup{}, nil, "an example?", "")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// A one-line box (a sub-topic's name) still ends on either.
	if got, ok := readPromptRaw(NewByteReader([]byte("name\nrest")), nil, nil, promptOpts{}); !ok || got != "name" {
		t.Errorf("one-line box: got %q, %v; want it ended at the LF", got, ok)
	}
}

// Each line of the text is its own rows, wrapped under the label, and the
// cursor at a line's end is a cell of its own on that line.
func TestBoxDrawsEachLine(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, Theme{})
	r.width = 30
	rows := r.labelledRows("ask ▸ ", "first\n"+strings.Repeat("x", 30), len("first"))
	want := []string{"  ask ▸ first█", "        " + strings.Repeat("x", 21), "        " + strings.Repeat("x", 9)}
	if strings.Join(rows, "|") != strings.Join(want, "|") {
		t.Errorf("rows =\n%q\nwant\n%q", rows, want)
	}
	if rows := r.labelledRows("ask ▸ ", "a\nb", 0); rows[0] != "  ask ▸ █a" || rows[1] != "        b" {
		t.Errorf("cursor at the start, no colour: %q", rows)
	}
	r.T.Enabled = true
	if rows := r.labelledRows("ask ▸ ", "a\n\nb", 2); len(rows) != 3 || !strings.Contains(rows[1], "█") {
		t.Errorf("cursor on the empty middle line: %q", rows)
	}
}

// A question over several lines is a title in one: a break inside a frame
// row moved the cursor off the row the frame had counted.
func TestMultiLineQuestionIsOneRowInTheFrame(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 30, 100)
	r.RenderBrief(&Plan{Question: "why\nthis?", SubTopics: []SubTopic{{Name: "A"}}})
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, lines := range [][]string{r.treeLines(), r.briefLines()} {
		for _, l := range lines {
			if strings.Contains(l, "\n") {
				t.Fatalf("a frame row holds a line break: %q", l)
			}
		}
		if !strings.Contains(strings.Join(lines, "|"), "why this?") {
			t.Errorf("the question is not shown on one row:\n%s", strings.Join(lines, "\n"))
		}
	}
	if got := OneLine(" a\n\n b  c "); got != "a b c" {
		t.Errorf("OneLine = %q", got)
	}
}

// cooked is an Input whose terminal would not go raw.
type cooked struct{ Input }

func (cooked) Raw() bool { return false }

// A terminal that would not go raw (every Windows one) maps Enter to LF,
// which in a box of several lines is the new-line key: its question could
// never be sent. There the box is one line, and LF sends it.
func TestCookedTerminalSendsOnLF(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, Theme{})
	in := cooked{NewByteReader([]byte("why?\nmore"))}
	if got := askQuestion(in, r, agent.Setup{}, nil, "an example?", ""); got != "why?" {
		t.Errorf("got %q, want the question sent at the LF", got)
	}
}

// A question taller than half the screen showed all of it, pushing the
// cursor and the keys under the box off the screen. The box keeps to half
// the screen, scrolled to the cursor's row.
func TestTallQuestionKeepsTheCursorOnScreen(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, Theme{})
	r.width, r.rows = 40, 10 // half the screen, 5, leaves the box's 6 rows: 4
	var lines []string
	for i := range 20 {
		lines = append(lines, "line"+string(rune('a'+i)))
	}
	text := strings.Join(lines, "\n")
	rows := r.labelledRows("ask ▸ ", text, len(text))
	if len(rows) != 4 || !strings.HasSuffix(rows[3], "linet█") {
		t.Errorf("cursor at the end: %q", rows)
	}
	rows = r.labelledRows("ask ▸ ", text, 0)
	if len(rows) != 4 || rows[0] != "  ask ▸ █linea" {
		t.Errorf("cursor at the start: %q", rows)
	}
	rows = r.labelledRows("ask ▸ ", text, strings.Index(text, "linej"))
	if !strings.Contains(strings.Join(rows, "|"), "█linej") {
		t.Errorf("cursor in the middle, no colour: %q", rows)
	}
}

// questionRecorder records the question the planner was given and every
// search query the run made.
type questionRecorder struct {
	*fakeAssistant
	mu      sync.Mutex
	planned string
	queries []string
}

func (q *questionRecorder) Plan(ctx context.Context, question string, b agent.Breadth, r []agent.Clarification) (agent.Outline, error) {
	q.planned = question
	return q.fakeAssistant.Plan(ctx, question, b, r)
}

func (q *questionRecorder) ResearchDetail(ctx context.Context, query string, terms []string) (*agent.ResearchDetail, error) {
	q.mu.Lock()
	q.queries = append(q.queries, query)
	q.mu.Unlock()
	return q.fakeAssistant.ResearchDetail(ctx, query, terms)
}

// The planner flattened the question before any model saw it, so a
// question laid out over lines (options, a table, code) reached the plan,
// the analysis and the saved run as one line. The model gets its lines; a
// search query, which is one line, gets its words.
func TestMultiLineQuestionReachesTheModelWithItsLines(t *testing.T) {
	question := "Compare:\n  option A\n  option B"
	a := &questionRecorder{fakeAssistant: &fakeAssistant{}}
	var stdout, stderr bytes.Buffer
	res, err := Run(context.Background(), Options{
		Question: question, Assistant: a, DepthMode: "quick", OutDir: t.TempDir(),
		JSONL: true, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.planned != question || res.Plan.Question != question || res.Report.Question != question {
		t.Errorf("planner got %q, plan %q, report %q; want %q", a.planned, res.Plan.Question, res.Report.Question, question)
	}
	if !strings.Contains(a.analyzed, question) {
		t.Errorf("the analysis prompt lost the question's lines:\n%s", a.analyzed)
	}
	if len(a.queries) == 0 {
		t.Fatal("no searches")
	}
	for _, q := range a.queries {
		if strings.Contains(q, "\n") {
			t.Errorf("a search query holds a line break: %q", q)
		}
	}
}

// While edited, a tall question is shown from the cursor's row up; sent,
// it was redrawn with no cursor, which showed its first rows, and its last
// lines, the ones just read before sending, were gone from the transcript.
// Sent, it is written whole.
func TestSentQuestionIsKeptWhole(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{})
	r.width, r.rows = 40, 10
	b := newAskBox(r, "back", nil)
	q, ok := b.read(NewByteReader([]byte("first\nsecond\nthird\nfourth\nfifth\nLAST\r")))
	if !ok || q != "first\nsecond\nthird\nfourth\nfifth\nLAST" {
		t.Fatalf("read %q, %v", q, ok)
	}
	out := buf.String()
	sent := out[strings.LastIndex(out, "ask ▸ first"):]
	for _, l := range []string{"second", "fifth", "LAST"} {
		if !strings.Contains(sent, l) {
			t.Errorf("the sent question lost %q:\n%q", l, sent)
		}
	}
}

// On a screen too narrow for the label and a few columns of text, eight
// were forced beside the label: the row was wider than the screen, and the
// clip took the cursor with its end. The label goes; the cursor stays.
func TestNarrowBoxKeepsTheCursor(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, Theme{})
	r.width = 20
	rows := r.labelledRows("research ▸ ", "1234567", len("1234567"))
	for _, row := range rows {
		if dispWidth(row) > r.width-1 {
			t.Errorf("row %q is wider than the frame writes", row)
		}
	}
	if !strings.Contains(strings.Join(rows, ""), "█") {
		t.Errorf("no cursor: %q", rows)
	}
}
