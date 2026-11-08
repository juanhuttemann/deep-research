package ui

import (
	"bytes"
	"fmt"
	"testing"
)

// BenchmarkIdleTail is an idle repaint of the live frame with the tail
// shown over a full history: what the ticker does several times a second.
func BenchmarkIdleTail(b *testing.B) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 100)
	for i := range 4 {
		id := fmt.Sprint(i + 1)
		r.Emit(Event{Type: SubAgent, SubID: id, SubName: "Topic " + id, SubState: "running"})
	}
	for i := range activityKeep {
		r.Emit(Event{Type: Search, SubID: fmt.Sprint(i%4 + 1), Line: fmt.Sprintf("Searching: query %d", i)})
	}
	r.ToggleActivity()
	r.mu.Lock()
	defer r.mu.Unlock()
	b.ReportAllocs()
	for b.Loop() {
		r.treeLines()
	}
}
