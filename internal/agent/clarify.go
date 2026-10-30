package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Question is one thing the clarifier asks before a run plans: a reading of
// the question the research would otherwise guess at (audience,
// jurisdiction, period, which product). It is here, beside SubTopic, because
// the plan the UI builds and the trace the replay package records both carry
// it.
type Question struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Options are the readings offered, the likeliest first. A question the
	// reader skips is not answered with any of them: the run guesses as it
	// did before clarifying existed.
	Options []string `json:"options"`
}

// Clarification is one question and the reader's answer to it, in words:
// what the planner, the analyzer and the summarizer are told.
type Clarification struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Bounds on what the clarifier may ask. More questions than this is an
// interview, not a check, and a question with one option asks nothing.
const (
	MaxQuestions = 3
	maxOptions   = 4
)

const defaultClarifierInstructions = `You check a research question before it is researched. Ask only what would change the research: a reading the question leaves open whose answer differs (the audience, the jurisdiction or market, the time frame, which of several products, people or events of that name, the use case). Never ask about the report's length, format or depth, about something the question already states, or for preferences that would not change what is searched. Most questions are clear: then ask nothing. Ask at most three, each with two to four short options, the likeliest reading first, in the language of the question. Never offer "other", "not sure" or "any" as an option: the reader can always answer in their own words or skip. Respond ONLY with JSON of the shape: {"questions":[{"id":"...","text":"...","options":["...","..."]}]}` //nolint:lll

// Clarify implements the clarifying check: one call, and no questions for a
// clear question or an answer that does not parse. An unreadable check is
// not worth stopping a run for: the run goes on as it would without it.
func (a *impl) Clarify(ctx context.Context, question string) ([]Question, error) {
	a.log(fmt.Sprintf("%-10s  %s · %s", "clarify", a.model, a.effortLabel(a.effort.Clarify)))
	out, err := a.clarify(ctx, "Question: "+strings.Join(strings.Fields(question), " "))
	if err != nil {
		return nil, err
	}
	return parseQuestions(out), nil
}

// parseQuestions decodes the clarifier's answer, keeping only questions that
// ask something: text and at least two distinct options.
func parseQuestions(out string) []Question {
	for payload := range jsonPayloads(out) {
		var obj struct {
			Questions []Question `json:"questions"`
		}
		if json.Unmarshal([]byte(payload), &obj) != nil || obj.Questions == nil {
			continue
		}
		return cleanQuestions(obj.Questions)
	}
	return nil
}

func cleanQuestions(qs []Question) []Question {
	var kept []Question
	for _, q := range qs {
		q.Text = strings.Join(strings.Fields(q.Text), " ")
		q.Options = distinctOptions(q.Options)
		if q.Text == "" || len(q.Options) < 2 {
			continue
		}
		// The ID is the model's, and only names the question in the page's
		// form: a missing or repeated one is renumbered.
		q.ID = fmt.Sprintf("q%d", len(kept)+1)
		if kept = append(kept, q); len(kept) == MaxQuestions {
			break
		}
	}
	return kept
}

func distinctOptions(opts []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, o := range opts {
		o = strings.Join(strings.Fields(o), " ")
		if o == "" || seen[strings.ToLower(o)] {
			continue
		}
		seen[strings.ToLower(o)] = true
		if out = append(out, o); len(out) == maxOptions {
			break
		}
	}
	return out
}

// ClarifiedBlock is the reader's answers as a prompt block, "" when there
// are none, so a run without them sends every phase exactly what it sent
// before clarifying existed.
func ClarifiedBlock(cs []Clarification) string {
	if len(cs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nThe reader clarified the question (research and answer it in this reading):\n")
	for _, c := range cs {
		sb.WriteString("- ")
		sb.WriteString(c.Question)
		sb.WriteString(" ")
		sb.WriteString(c.Answer)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
