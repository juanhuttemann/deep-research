package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// Question is one thing the clarifier asks before planning; Clarification is
// a question and the reader's answer. Both are the agent package's, which
// the replay trace carries, and replay must not import ui.
type (
	Question      = agent.Question
	Clarification = agent.Clarification
)

// Clarified is what a run knows of the reading before it plans: the
// questions asked and the answers given. A question the reader skipped has
// no answer, and the run guesses at it as it always did.
//
// It is what --clarify-only prints and --answers reads back, questions
// included, so that the brief or the page's review can ask them again.
type Clarified struct {
	Questions []Question      `json:"questions"`
	Answers   []Clarification `json:"answers"`
}

// maxAnswer bounds an answer typed or handed in: it is a reading, not a
// second question, and it is sent to three phases.
const maxAnswer = 300

// Checked is c made safe to plan with: questions and answers trimmed, empty
// ones dropped, and each bounded. Questions are renumbered, as the
// clarifier's are, since their IDs only name them in the page's form.
func (c Clarified) Checked() (Clarified, error) {
	if len(c.Questions) > agent.MaxQuestions || len(c.Answers) > agent.MaxQuestions {
		return Clarified{}, fmt.Errorf("clarifications: at most %d questions", agent.MaxQuestions)
	}
	var out Clarified
	for _, q := range c.Questions {
		q.Text = strings.Join(strings.Fields(q.Text), " ")
		if q.Text == "" {
			continue
		}
		q.ID = fmt.Sprintf("q%d", len(out.Questions)+1)
		out.Questions = append(out.Questions, q)
	}
	for _, a := range c.Answers {
		a.Question = strings.Join(strings.Fields(a.Question), " ")
		a.Answer = strings.Join(strings.Fields(a.Answer), " ")
		if a.Question == "" || a.Answer == "" {
			continue
		}
		if utf8.RuneCountInString(a.Question) > maxAnswer || utf8.RuneCountInString(a.Answer) > maxAnswer {
			return Clarified{}, fmt.Errorf("clarifications: an answer is over %d characters", maxAnswer)
		}
		out.Answers = append(out.Answers, a)
	}
	return out, nil
}

// ParseAnswers reads --answers: the JSON --clarify-only printed, with the
// answers filled in. "[]" answers nothing; the run still asks no questions.
func ParseAnswers(s string) (Clarified, error) {
	var c Clarified
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Clarified{}, fmt.Errorf("--answers: want {\"questions\":[...],\"answers\":[{\"question\":\"...\",\"answer\":\"...\"}]}: %w", err)
	}
	return c.Checked()
}

// statedQuestion is the question as the analysis and the report are given
// it: as asked, then what the reader clarified about it. With nothing
// clarified it is the question alone, so those prompts are what they were.
func statedQuestion(p *Plan) string {
	return p.Question + agent.ClarifiedBlock(p.Clarifications)
}

// askFunc puts questions to the reader. stop is Ctrl-C, which ends the run.
type askFunc func(qs []Question, prior []Clarification) (answers []Clarification, stop bool)

// clarified is what the run plans for: the answers handed in, else the
// clarifier's questions put to the reader by ask. A run with no one to ask
// and no --clarify-only to print them for skips the call: an unanswerable
// question costs a model call and changes nothing.
func (o Options) clarified(ctx context.Context, ask askFunc) (Clarified, bool, error) {
	switch {
	case o.Answers != nil:
		return *o.Answers, false, nil
	case !o.Clarify || o.Plan != nil, ask == nil && !o.ClarifyOnly:
		return Clarified{}, false, nil
	}
	qs, err := o.askClarifier(ctx)
	if err != nil {
		return Clarified{}, false, err
	}
	c := Clarified{Questions: qs}
	if o.ClarifyOnly || len(qs) == 0 {
		return c, false, nil
	}
	answers, stop := ask(qs, nil)
	c.Answers = answers
	return c, stop, nil
}

// askClarifier is the clarifier's call, under the planning spinner where
// there is one. A failed check is not worth the run: it goes on unclarified,
// with the reason on stderr. A cancel is the reader's, and stops it.
func (o Options) askClarifier(ctx context.Context) ([]Question, error) {
	stopStatus := func() {}
	if !o.Quiet && !o.JSONL {
		_, stopStatus = StatusSpinner(o.Stderr, "Checking the question")
	}
	qs, err := o.Assistant.Clarify(ctx, o.Question)
	stopStatus()
	switch {
	case err == nil:
		return qs, nil
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return nil, err
	case o.ClarifyOnly:
		return nil, fmt.Errorf("clarify: %w", err)
	}
	_, _ = fmt.Fprintf(o.Stderr, "warning: could not check the question (%v); planning without clarifying\n", err)
	return nil, nil
}

// skipChoice ends a question's rows: no answer for it.
var skipChoice = Choice{Title: "skip", Note: "leave it to the research"}

const questionHints = "[enter] choose   [↑↓] move   [esc] skip the rest"

// answerQuestions puts each question to the reader in the picker, on the
// answer given before when there is one. back is Esc or the end of input,
// with the answers given before it; stop is Ctrl-C.
func answerQuestions(in Input, r *Renderer, qs []Question, prior []Clarification) (out []Clarification, back, stop bool) {
	for i, q := range qs {
		rows := make([]Choice, 0, len(q.Options)+1)
		for _, o := range q.Options {
			rows = append(rows, Choice{Title: o})
		}
		rows = append(rows, skipChoice)
		sp := pickSpec{title: "A FEW QUESTIONS FIRST", right: fmt.Sprintf("%d of %d", i+1, len(qs)), lead: q.Text,
			label: "answer", rows: rows, hints: questionHints, free: true}
		sp.at, sp.text = priorAnswer(q, prior)
		got := pickWith(in, r, sp)
		switch {
		case got.stop:
			return nil, true, true
		case got.back:
			return out, true, false
		case got.text != "":
			out = append(out, Clarification{Question: q.Text, Answer: truncate(got.text, maxAnswer)})
		case got.row < len(q.Options):
			out = append(out, Clarification{Question: q.Text, Answer: q.Options[got.row]})
		}
	}
	return out, false, false
}

// priorAnswer is where q's picker opens: on the option answered before, in
// its text box for an answer typed before, on skip for one skipped, else on
// the likeliest reading.
func priorAnswer(q Question, prior []Clarification) (at int, text string) {
	if prior == nil {
		return 0, ""
	}
	i := slices.IndexFunc(prior, func(c Clarification) bool { return c.Question == q.Text })
	if i < 0 {
		return len(q.Options), ""
	}
	if j := slices.Index(q.Options, prior[i].Answer); j >= 0 {
		return j, ""
	}
	return 0, prior[i].Answer
}

// askOnTerminal puts the questions on the terminal, in place, on an input
// and a renderer of their own, as Pick does: the input is closed before
// planning, so the brief that follows still discards the keys typed while
// the plan was made, and the guard that gives the terminal back closes the
// renderer with it, which drawn on the run's left the brief never shown.
func (o Options) askOnTerminal() askFunc {
	return func(qs []Question, prior []Clarification) ([]Clarification, bool) {
		sig := catchSignals(true)
		in := NewInput(o.Input, nil)
		r := NewRenderer(o.Stdout, Theme{Enabled: !o.NoColor})
		defer guardTerminal(sig, in, r)()
		keys := &keptInput{Input: in}
		defer r.Inline(keys)()
		answers, _, stop := answerQuestions(keys, r, qs, prior)
		return answers, stop
	}
}

// askScripted answers from the scripted keys the brief reads next.
func askScripted(in Input, r *Renderer) askFunc {
	return func(qs []Question, prior []Clarification) ([]Clarification, bool) {
		answers, _, stop := answerQuestions(in, r, qs, prior)
		return answers, stop
	}
}

// clarifiedLine is the brief's and the page's summary of the reading:
// the answers, in order.
func clarifiedLine(cs []Clarification) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.Answer
	}
	return strings.Join(parts, " · ")
}

// reclarify asks the plan's questions again from the brief and plans anew
// for the answers. Nothing changes unless a new plan is made: Esc, the same
// answers or a planner that fails keep the plan on screen, with the reason.
// Ctrl-C at a question cancels the run, as it does at the brief.
func (o Options) reclarify(ctx context.Context, in Input, r *Renderer, plan *Plan) (*Plan, bool) {
	// Asked again, a question left unanswered opens on skip, not on the
	// likeliest reading.
	prior := plan.Clarifications
	if prior == nil {
		prior = []Clarification{}
	}
	answers, back, stop := answerQuestions(in, r, plan.Questions, prior)
	// The picker's box is closed: left set, the brief drew it under its
	// sub-topics as a prompt waiting for a name.
	r.SetPrompt("", "")
	if back || stop || slices.Equal(answers, plan.Clarifications) {
		return plan, stop
	}
	note := func(msg string) {
		r.SetBriefNote("planning again for your answers… " + msg)
		r.RenderBrief(plan)
	}
	note("")
	// The planner's progress is the note's: printed, it landed on top of the
	// brief. Handed back after as buildPlan hands it back.
	o.Assistant.SetProgress(note)
	np, err := o.replan(ctx, plan, answers)
	o.Assistant.SetProgress(func(msg string) { _, _ = fmt.Fprintf(o.Stderr, "%s\n", msg) })
	// Keys pressed while the planner ran were pressed at a brief that was
	// about to be replaced: an Enter among them would launch a plan never
	// seen.
	discardTyped(in)
	if err != nil {
		r.SetBriefNote("could not plan again (" + err.Error() + "); kept the plan")
		return plan, false
	}
	r.SetBriefNote("")
	return np, false
}

// replan is plan made again for answers, with what the brief chose kept:
// its depth, its pinned budget, its other review settings and its sites.
// The sub-topics are the planner's: they were the old reading's.
func (o Options) replan(ctx context.Context, plan *Plan, answers []Clarification) (*Plan, error) {
	planner := NewPlanner(o.Assistant, plan.Depth)
	planner.SubTopics, planner.Reading = o.SubTopics, answers
	np, err := planner.Plan(ctx, o.Question)
	if err != nil {
		return nil, err
	}
	np.NoFollowUps, np.Report, np.Domains = plan.NoFollowUps, plan.Report, plan.Domains
	np.Questions, np.Clarifications = plan.Questions, answers
	if plan.PinnedPerTopic > 0 {
		np, _ = np.WithSourcesPerTopic(plan.PinnedPerTopic)
	}
	return np, nil
}

// discardTyped drops the keys waiting on a terminal. Scripted keys are
// kept: they are the rest of the script.
func discardTyped(in Input) {
	if k, ok := in.(*keptInput); ok {
		k.kept = nil
		in = k.Input
	}
	t, ok := in.(interface {
		terminalOwner
		NextWithin(time.Duration) (byte, error)
	})
	if !ok {
		return
	}
	for {
		if _, err := t.NextWithin(0); err != nil {
			return
		}
	}
}
