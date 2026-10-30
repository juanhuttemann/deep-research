package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// twoQuestions is a clarifier's answer to an ambiguous question.
var twoQuestions = []agent.Question{
	{ID: "q1", Text: "Which market?", Options: []string{"United States", "European Union"}},
	{ID: "q2", Text: "For whom?", Options: []string{"a buyer", "an engineer", "a manager"}},
}

// clarifyRun runs question with clarifying on and the scripted keys.
func clarifyRun(t *testing.T, fa *fakeAssistant, keys string, edit func(*Options)) RunResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	o := Options{Question: "best heat pump", Assistant: fa, OutDir: t.TempDir(), Stdout: &stdout, Stderr: &stderr,
		KeyScript: keys, Clarify: true, PlanOnly: true}
	if edit != nil {
		edit(&o)
	}
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestClarifyingAnswersReachThePlanner(t *testing.T) {
	fa := &fakeAssistant{questions: twoQuestions}
	// Down to the second market, Enter; then a reading of one's own.
	res := clarifyRun(t, fa, "\x1b[B\rplant operator\r\r", nil)
	want := []Clarification{{Question: "Which market?", Answer: "European Union"}, {Question: "For whom?", Answer: "plant operator"}}
	if !slices.Equal(res.Plan.Clarifications, want) {
		t.Errorf("clarifications = %+v, want %+v", res.Plan.Clarifications, want)
	}
	if len(fa.readings) != 1 || !slices.Equal(fa.readings[0], want) {
		t.Errorf("planner read %+v, want %+v", fa.readings, want)
	}
	if len(res.Plan.Questions) != 2 {
		t.Errorf("plan kept %d questions, want 2 for the brief to ask again", len(res.Plan.Questions))
	}
	if res.Plan.Question != "best heat pump" {
		t.Errorf("question = %q: answers never rewrite it", res.Plan.Question)
	}
}

// Skip and Esc leave questions unanswered: the run guesses at them as it did
// before it could ask.
func TestSkippedQuestionsHaveNoAnswer(t *testing.T) {
	for name, keys := range map[string]string{
		"skip the first, Esc the rest": "\x1b[B\x1b[B\r\x1b\r",
		"Esc at the first":             "\x1b\r",
	} {
		t.Run(name, func(t *testing.T) {
			fa := &fakeAssistant{questions: twoQuestions}
			res := clarifyRun(t, fa, keys, nil)
			if res.Plan == nil || len(res.Plan.Clarifications) != 0 {
				t.Fatalf("plan = %+v, want one with no answers", res.Plan)
			}
			if len(fa.readings) != 1 || fa.readings[0] != nil {
				t.Errorf("planner read %+v, want no reading", fa.readings)
			}
		})
	}
}

func TestCtrlCAtAQuestionCancelsBeforePlanning(t *testing.T) {
	fa := &fakeAssistant{questions: twoQuestions}
	res := clarifyRun(t, fa, "\x03", nil)
	if !res.Cancelled || len(fa.readings) != 0 {
		t.Errorf("cancelled=%v plan calls=%d, want a cancel before any plan", res.Cancelled, len(fa.readings))
	}
}

// The check is a model call: a run with nobody to answer it, or told not to
// ask, or handed its answers or its plan, never makes it.
func TestNoClarifyCallWithoutSomeoneToAsk(t *testing.T) {
	cases := map[string]func(*Options){
		"clarify off": func(o *Options) { o.Clarify = false },
		"jsonl":       func(o *Options) { o.JSONL = true; o.KeyScript = "" },
		"no terminal": func(o *Options) { o.KeyScript = "" },
		"answers":     func(o *Options) { o.Answers = &Clarified{} },
		"plan":        func(o *Options) { o.Plan = PlanFor("q", DepthModes[1], []SubTopic{{Name: "A"}}, 0) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			fa := &fakeAssistant{questions: twoQuestions}
			clarifyRun(t, fa, "\r", edit)
			if fa.clarifies != 0 {
				t.Errorf("Clarify called %d times", fa.clarifies)
			}
		})
	}
}

func TestClearQuestionDrawsNoQuestions(t *testing.T) {
	fa := &fakeAssistant{}
	res := clarifyRun(t, fa, "\r", nil)
	if fa.clarifies != 1 || res.Plan == nil || res.Plan.Questions != nil {
		t.Errorf("clarifies=%d plan=%+v, want one check and a plan with nothing asked", fa.clarifies, res.Plan)
	}
}

// --clarify-only prints the questions in the shape --answers reads back,
// and plans nothing.
func TestClarifyOnlyStopsWithTheQuestions(t *testing.T) {
	fa := &fakeAssistant{questions: twoQuestions}
	var stdout bytes.Buffer
	res, err := Run(context.Background(), Options{Question: "q", Assistant: fa, Stdout: &stdout, Stderr: &bytes.Buffer{},
		JSONL: true, Clarify: true, ClarifyOnly: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Clarified == nil || len(res.Clarified.Questions) != 2 || len(fa.readings) != 0 {
		t.Fatalf("clarified=%+v plan calls=%d", res.Clarified, len(fa.readings))
	}
	if !strings.Contains(stdout.String(), `"type":"clarify"`) {
		t.Errorf("no clarify event on the stream:\n%s", stdout.String())
	}
	b, _ := json.Marshal(res.Clarified)
	if _, err := ParseAnswers(string(b)); err != nil {
		t.Errorf("--answers refuses what --clarify-only printed: %v", err)
	}
}

// With clarify off, the clarify step still ends with its (empty) questions,
// so the page moves on to the plan the one way.
func TestClarifyOnlyWithClarifyOffAsksNothing(t *testing.T) {
	fa := &fakeAssistant{questions: twoQuestions}
	var stdout bytes.Buffer
	res, err := Run(context.Background(), Options{Question: "q", Assistant: fa, Stdout: &stdout, Stderr: &bytes.Buffer{},
		JSONL: true, ClarifyOnly: true})
	if err != nil || fa.clarifies != 0 || res.Clarified == nil || len(res.Clarified.Questions) != 0 {
		t.Fatalf("err=%v clarifies=%d clarified=%+v", err, fa.clarifies, res.Clarified)
	}
	if !strings.Contains(stdout.String(), `"questions":[]`) {
		t.Errorf("want an empty question list on the stream:\n%s", stdout.String())
	}
}

// c in the brief asks again and plans for the new answers; Esc out of the
// questions keeps the plan, and makes no call.
func TestBriefAsksAgainAndPlansForNewAnswers(t *testing.T) {
	fa := &fakeAssistant{questions: twoQuestions[:1]}
	res := clarifyRun(t, fa, "\rc\x1b[B\r\r", func(o *Options) { o.PlanOnly = false })
	if len(fa.readings) != 2 {
		t.Fatalf("plan calls = %d, want the first and one for the new answer", len(fa.readings))
	}
	want := []Clarification{{Question: "Which market?", Answer: "European Union"}}
	if !slices.Equal(res.Plan.Clarifications, want) || !slices.Equal(fa.readings[1], want) {
		t.Errorf("plan read %+v, planner %+v, want %+v", res.Plan.Clarifications, fa.readings[1], want)
	}

	fa = &fakeAssistant{questions: twoQuestions[:1]}
	res = clarifyRun(t, fa, "\rc\x1b\r", func(o *Options) { o.PlanOnly = false })
	if len(fa.readings) != 1 || res.Plan.Clarifications[0].Answer != "United States" {
		t.Errorf("Esc: plan calls=%d answers=%+v, want the first plan kept", len(fa.readings), res.Plan.Clarifications)
	}
}

// A plan made again keeps what the brief chose: its depth and budget pin,
// its sites and its other review settings.
func TestReplanKeepsTheBriefsChoices(t *testing.T) {
	fa := &fakeAssistant{}
	old := PlanFor("q", DepthModes[2], []SubTopic{{Name: "A"}, {Name: "B"}}, 0)
	old, _ = old.WithSourcesPerTopic(7)
	old.NoFollowUps, old.Domains, old.Report = true, []string{"go.dev"}, ReportSpec{Format: "brief"}
	answers := []Clarification{{Question: "x?", Answer: "y"}}
	np, err := Options{Question: "q", Assistant: fa}.replan(context.Background(), old, answers)
	if err != nil {
		t.Fatal(err)
	}
	if np.Depth.Key != "deep" || np.PinnedPerTopic != 7 || !np.NoFollowUps || np.Domains[0] != "go.dev" ||
		np.Report.Format != "brief" || !slices.Equal(np.Clarifications, answers) {
		t.Errorf("replanned = %+v", np)
	}
}

// Nothing clarified, nothing changes: every phase is sent the question
// exactly as before clarifying existed.
func TestNoClarificationsLeaveThePromptsAsTheyWere(t *testing.T) {
	p := &Plan{Question: "q"}
	if statedQuestion(p) != "q" {
		t.Errorf("statedQuestion = %q", statedQuestion(p))
	}
	p.Clarifications = []Clarification{{Question: "Which market?", Answer: "EU"}}
	if got := analyzePrompt(statedQuestion(p), nil, nil); !strings.Contains(got, "Which market? EU") {
		t.Errorf("analysis prompt lacks the reading:\n%s", got)
	}
	if strings.Contains(factCheckPrompt("answer", nil, true, nil), "EU") {
		t.Error("the fact-check was given the reading")
	}
}

func TestPlanCarriesClarificationsThroughReadPlan(t *testing.T) {
	p := PlanFor("q", DepthModes[1], []SubTopic{{Name: "A"}}, 0)
	p.Questions, p.Clarifications = twoQuestions, []Clarification{{Question: "Which market?", Answer: "EU"}}
	b, _ := json.Marshal(p)
	got, err := ReadPlan(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != 2 || !slices.Equal(got.Clarifications, p.Clarifications) {
		t.Errorf("read back %+v / %+v", got.Questions, got.Clarifications)
	}
	p.Clarifications = slices.Repeat(p.Clarifications, 4)
	b, _ = json.Marshal(p)
	if _, err := ReadPlan(bytes.NewReader(b)); err == nil {
		t.Error("a plan with more answers than questions can be asked was read")
	}
}

func TestParseAnswers(t *testing.T) {
	c, err := ParseAnswers(`{"questions":[],"answers":[]}`)
	if err != nil || c.Answers != nil {
		t.Errorf("empty answers: %+v, %v", c, err)
	}
	if _, err := ParseAnswers(`{"answer":[]}`); err == nil {
		t.Error("a misspelt key was taken as no answers")
	}
	c, err = ParseAnswers(`{"answers":[{"question":" Which  market? ","answer":"EU"},{"question":"x","answer":" "}]}`)
	if err != nil || len(c.Answers) != 1 || c.Answers[0].Question != "Which market?" {
		t.Errorf("answers = %+v, %v; want the blank one dropped and the rest trimmed", c.Answers, err)
	}
	if _, err := ParseAnswers(`{"answers":[{"question":"x","answer":"` + strings.Repeat("a", 301) + `"}]}`); err == nil {
		t.Error("an answer over the bound was taken")
	}
}

// The picker opens on the answer given before: an option, a reading of
// one's own in the box, or skip for a question left unanswered.
func TestPriorAnswerOpensThePicker(t *testing.T) {
	q := twoQuestions[0]
	cases := []struct {
		prior    []Clarification
		at       int
		typedOwn string
	}{
		{nil, 0, ""},
		{[]Clarification{{Question: q.Text, Answer: "European Union"}}, 1, ""},
		{[]Clarification{{Question: q.Text, Answer: "Canada"}}, 0, "Canada"},
		{[]Clarification{}, 2, ""},
	}
	for _, c := range cases {
		if at, text := priorAnswer(q, c.prior); at != c.at || text != c.typedOwn {
			t.Errorf("prior %+v: at=%d text=%q, want %d %q", c.prior, at, text, c.at, c.typedOwn)
		}
	}
}

// Asked again from the brief, the questions' box closed with them: left
// open, the brief showed it under its sub-topics as a prompt for a name.
func TestBriefAfterAskingAgainHasNoAnswerBox(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, Theme{})
	r.width, r.rows, r.plain = 100, 40, false
	plan := PlanFor("q", DepthModes[1], []SubTopic{{Name: "A"}}, 0)
	plan.Questions = twoQuestions[:1]
	plan, _ = Options{Question: "q", Assistant: &fakeAssistant{}}.reclarify(context.Background(), NewByteReader([]byte("\x1b")), r, plan)
	r.RenderBrief(plan)
	if foot := strings.Join(r.briefFoot(), "\n"); strings.Contains(foot, "answer ▸") || !strings.Contains(foot, "[c]") {
		t.Errorf("brief foot after the questions:\n%s", foot)
	}
}

// The planner's progress while the brief plans again is the brief's note:
// printed to stderr, it landed on top of the brief being redrawn.
func TestReplanProgressStaysInTheBrief(t *testing.T) {
	var stderr bytes.Buffer
	r := NewRenderer(&bytes.Buffer{}, Theme{})
	r.width, r.rows, r.plain = 100, 40, false
	fa := &progressDuringPlan{fakeAssistant: &fakeAssistant{}, msg: "3 sub-topics so far"}
	plan := PlanFor("q", DepthModes[1], []SubTopic{{Name: "A"}}, 0)
	plan.Questions = twoQuestions[:1]
	o := Options{Question: "q", Assistant: fa, Stderr: &stderr}
	// Unanswered, the question reopens on skip; up from it is an answer,
	// so a new plan.
	np, _ := o.reclarify(context.Background(), NewByteReader([]byte("\x1b[A\r")), r, plan)
	if np == plan || stderr.Len() != 0 {
		t.Errorf("replanned=%v, stderr %q: the progress belongs in the brief", np != plan, stderr.String())
	}
	if fa.logf == nil {
		t.Fatal("no logger handed back after planning again")
	}
	fa.logf("later")
	if stderr.String() != "later\n" {
		t.Errorf("after planning again, progress went to %q, want stderr", stderr.String())
	}
}

// Ctrl-C at a question asked again from the brief cancels the run, as it
// does at the brief: it used to go back to the brief, as Esc does.
func TestCtrlCWhileAskingAgainCancels(t *testing.T) {
	fa := &fakeAssistant{questions: twoQuestions[:1]}
	res := clarifyRun(t, fa, "\rc\x03", func(o *Options) { o.PlanOnly = false })
	if !res.Cancelled || res.Report != nil {
		t.Errorf("cancelled=%v report=%v, want the run cancelled", res.Cancelled, res.Report != nil)
	}
}
