package agent

import (
	"testing"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/message"
)

// A reply is the text after the model's tool calls: the sentence it wrote
// before an edit, saying what it was about to do, read as the reply twice.
func TestChatReplyIsTheTextAfterTheTools(t *testing.T) {
	r := &agent.Response{Messages: []*message.Message{
		{Role: message.RoleAssistant, Contents: []message.Content{&message.TextContent{Text: "I'll condense it."}, &message.FunctionCallContent{CallID: "1", Name: "edit_report"}}},
		{Role: message.RoleTool, Contents: []message.Content{&message.FunctionResultContent{CallID: "1", Result: "ok"}}},
		{Role: message.RoleAssistant, Contents: []message.Content{&message.TextContent{Text: "I've cut it to one sentence."}}},
	}}
	if got := finalReply(r); got != "I've cut it to one sentence." {
		t.Errorf("reply = %q", got)
	}
	r.Messages = r.Messages[:2]
	if got := finalReply(r); got != "I'll condense it." {
		t.Errorf("with nothing after the tools, reply = %q", got)
	}
}
