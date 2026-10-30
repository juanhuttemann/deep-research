package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseQuestionsKeepsOnlyQuestionsThatAsk(t *testing.T) {
	out := `Here you go: {"questions":[
		{"id":"x","text":"Which market?","options":["US","EU","eu"," "]},
		{"id":"x","text":"Only one?","options":["yes"]},
		{"text":"  ","options":["a","b"]},
		{"text":"For whom?","options":["a","b","c","d","e"]},
		{"text":"Third?","options":["a","b"]},
		{"text":"Fourth?","options":["a","b"]}]}`
	qs := parseQuestions(out)
	if len(qs) != MaxQuestions {
		t.Fatalf("kept %d questions, want %d: %+v", len(qs), MaxQuestions, qs)
	}
	if qs[0].ID != "q1" || qs[1].ID != "q2" || len(qs[0].Options) != 2 || len(qs[1].Options) != maxOptions {
		t.Errorf("questions = %+v", qs)
	}
	for _, bad := range []string{"", "no questions here", `{"questions":"none"}`, `{"subtopics":[]}`} {
		if qs := parseQuestions(bad); len(qs) != 0 {
			t.Errorf("%q asked %+v", bad, qs)
		}
	}
}

// A failed call is the caller's to weigh; a cancel must stay a cancel.
func TestClarifyReturnsCallErrors(t *testing.T) {
	a := &impl{clarify: func(context.Context, string) (string, error) { return "", context.Canceled }}
	if _, err := a.Clarify(context.Background(), "q"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancel", err)
	}
}

// The planner gets the reading after its breadth and the question, and
// nothing new when there is none.
func TestPlanIsGivenTheReading(t *testing.T) {
	var sent string
	a := &impl{plan: func(_ context.Context, p string) (string, error) {
		sent = p
		return `{"subtopics":[{"name":"A"}]}`, nil
	}}
	if _, err := a.Plan(context.Background(), "q", Breadth{Max: 3}, nil); err != nil {
		t.Fatal(err)
	}
	if sent != planPrompt("q", Breadth{Max: 3}) {
		t.Errorf("unclarified plan prompt changed:\n%s", sent)
	}
	reading := []Clarification{{Question: "Which market?", Answer: "EU"}}
	if _, err := a.Plan(context.Background(), "q", Breadth{Max: 3}, reading); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(sent, "\n- Which market? EU") || !strings.Contains(sent, "Question: q") {
		t.Errorf("plan prompt lacks the reading:\n%s", sent)
	}
}
