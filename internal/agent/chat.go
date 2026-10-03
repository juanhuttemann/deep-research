package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/provider/openaiprovider"
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
	impl, client, err := connect(cfg)
	if err != nil {
		return nil, err
	}
	ag := openaiprovider.NewChatCompletionsAgent(client, openaiprovider.AgentConfig{
		Model:        cfg.OpenAIModel,
		Instructions: cfg.ChatInstructions + "\n\n" + overview,
		Config:       agent.Config{Name: "chat", Tools: tools},
	})
	return &Chat{impl: impl, ag: ag}, nil
}

// Ask answers q, with the conversation so far ahead of it.
func (c *Chat) Ask(ctx context.Context, q string) (string, error) {
	var sb strings.Builder
	for _, t := range c.Turns {
		fmt.Fprintf(&sb, "Earlier question: %s\nYour answer: %s\n\n", t.Question, t.Answer)
	}
	sb.WriteString(q)
	answer, err := c.impl.call(ctx, c.ag, sb.String())
	// An answer cut off at the output limit is still most of an answer, and
	// prose, as a report is.
	if err != nil && !errors.Is(err, errCutOff) {
		return "", err
	}
	c.Turns = append(c.Turns, Turn{q, answer})
	return answer, nil
}
