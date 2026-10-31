package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/tool"
)

// Turn is one question about a run and the answer it got.
type Turn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Chat is a conversation about a finished run. It is not a pipeline phase:
// the model may call the tools it is given, which read the run's saved
// sources, and the framework runs those calls until the model answers.
//
// The conversation is carried as text in each question rather than in a
// framework session, so a retried call cannot leave half a turn behind, and
// the browser can hand back the conversation it shows.
type Chat struct {
	impl  *impl
	ag    *agent.Agent
	Turns []Turn
}

// NewChat starts a conversation whose model knows the run from overview and
// reads the rest through tools.
func NewChat(cfg Config, overview string, tools []tool.Tool) (*Chat, error) {
	impl, newAgent, err := connect(cfg, true)
	if err != nil {
		return nil, err
	}
	return &Chat{impl: impl, ag: newAgent("chat", cfg.ChatInstructions+"\n\n"+overview, tools)}, nil
}

// chatTurnsSent and chatAnswerRunes bound the conversation each question
// carries. The whole of it went with every question, and it is saved and
// reloaded with the run: a long one grew past the context window, and from
// then on every question about the run failed the same way. The latest
// turns are what a follow-up refers to; an older answer can be asked again.
const (
	chatTurnsSent   = 8
	chatAnswerRunes = 4000
)

// Ask answers q, with the latest of the conversation so far ahead of it.
func (c *Chat) Ask(ctx context.Context, q string) (string, error) {
	var sb strings.Builder
	turns := c.Turns
	if n := len(turns) - chatTurnsSent; n > 0 {
		fmt.Fprintf(&sb, "(%d earlier questions of this conversation are left out.)\n\n", n)
		turns = turns[n:]
	}
	for _, t := range turns {
		fmt.Fprintf(&sb, "Earlier question: %s\nYour answer: %s\n\n", clip(t.Question, chatAnswerRunes), clip(t.Answer, chatAnswerRunes))
	}
	sb.WriteString(q)
	answer, err := c.impl.call(ctx, c.ag, sb.String(), c.impl.effort.Chat)
	// An answer cut off at the output limit is still most of an answer, and
	// prose, as a report is.
	if err != nil && !errors.Is(err, errCutOff) {
		return "", err
	}
	c.Turns = append(c.Turns, Turn{q, answer})
	return answer, nil
}
