//go:build eval

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

type evalPrompt struct {
	Label  string   `json:"label"`
	Prompt string   `json:"prompt"`
	IDs    []string `json:"ids"`
}
type evalCall struct {
	Prompt   string          `json:"prompt"`
	Response json.RawMessage `json:"response"`
	Error    string          `json:"error,omitempty"`
}
type evalTrace struct {
	mu       sync.Mutex
	Prompts  []evalPrompt    `json:"prompts"`
	Calls    []evalCall      `json:"calls"`
	Governed *agent.Analysis `json:"governed_analysis"`
}

func (e *evalTrace) recordParts(parts []checkPart) {
	for _, p := range parts {
		e.Prompts = append(e.Prompts, evalPrompt{p.label, p.prompt, p.ids})
	}
}

// Capture each parsed response before merging filters IDs or governance
// derives report lists. Keeping its pointer would record the later mutation.
func (e *evalTrace) capture(check func(context.Context, string) (*agent.FactCheckResult, error)) func(context.Context, string) (*agent.FactCheckResult, error) {
	return func(ctx context.Context, prompt string) (*agent.FactCheckResult, error) {
		fc, err := check(ctx, prompt)
		data, snapshotErr := json.Marshal(fc)
		call := evalCall{Prompt: prompt, Response: data}
		if err != nil {
			call.Error = err.Error()
		}
		e.mu.Lock()
		e.Calls = append(e.Calls, call)
		e.mu.Unlock()
		return fc, errors.Join(err, snapshotErr)
	}
}
func TestEvalTracePreservesResponseBeforeMutation(t *testing.T) {
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{{ID: "unasked", Status: "supported"}}}
	trace := &evalTrace{}
	check := trace.capture(func(context.Context, string) (*agent.FactCheckResult, error) { return fc, nil })
	if _, err := check(t.Context(), "original prompt"); err != nil {
		t.Fatal(err)
	}
	fc.Verdicts[0].Status = "insufficient"
	fc.Unverified = []string{"derived by governance"}
	var got agent.FactCheckResult
	if err := json.Unmarshal(trace.Calls[0].Response, &got); err != nil {
		t.Fatal(err)
	}
	if got.Verdicts[0].Status != "supported" || len(got.Unverified) != 0 || trace.Calls[0].Prompt != "original prompt" {
		t.Fatalf("response was mutated: %+v", got)
	}
}
