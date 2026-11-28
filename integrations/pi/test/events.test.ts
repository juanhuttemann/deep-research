import assert from "node:assert/strict";
import { test } from "node:test";
import { initialState, parseLine, reduce } from "../src/events.ts";
import { events } from "./helpers.ts";

test("a run's events fold into what the progress view shows", () => {
	const s = events("complete.jsonl").reduce(reduce, initialState());
	assert.equal(s.phase, "Report");
	assert.deepEqual(
		s.subagents.map((a) => [a.name, a.state, a.progress]),
		[
			["Benchmarks", "done", 100],
			["Memory use", "error", 0],
		],
	);
	assert.equal(s.searches, 1);
	// The second read was fetched past the budget (verify status "unused"):
	// a read, never a source.
	assert.equal(s.reads, 2);
	assert.equal(s.sources, 1);
	assert.equal(s.tokens, 1200);
	assert.deepEqual(s.errors, ["search failed: rate-limited"]);
	assert.deepEqual(s.domains.map((d) => d.domain), ["go.dev"]);
	// The report event moved the run on: the last call's line went with it.
	assert.equal(s.status, "");
});

test("the status keeps the first line of a transient info, until the phase changes", () => {
	// In drsh 0.39 and earlier the rest is the reasoning, which has its own event.
	let s = reduce(initialState(), { type: "phase", phase: "Analyze" });
	s = reduce(s, { type: "info", transient: true, detail: "analyzing — listing what the sources show\nreasoning" });
	assert.equal(s.status, "analyzing — listing what the sources show");
	// The fact-check showed the analysis's last line under its own header.
	s = reduce(s, { type: "phase", phase: "Fact-Check" });
	assert.equal(s.status, "");
});

test("the fact-check's own notes are kept, the last three", () => {
	let s = initialState();
	for (const d of ["checking 4 claims in 2 calls at once", "3 of 4 claims supported", "1 of 2 revisions approved", "late"]) {
		s = reduce(s, { type: "info", phase: "Fact-Check", detail: d });
	}
	s = reduce(s, { type: "info", phase: "Research", detail: "follow-up round off" });
	// The agent's log of the call it makes, sent as an info in the phase.
	s = reduce(s, { type: "info", phase: "Fact-Check", detail: "fact-check  prov/model · provider's default effort claims c1-c7" });
	s = reduce(s, { type: "info", phase: "Fact-Check", detail: "analyze     prov/model · high" });
	assert.deepEqual(s.checked, ["3 of 4 claims supported", "1 of 2 revisions approved", "late"]);
	// A retry's notes push out the others, not the tally.
	s = reduce(s, { type: "info", phase: "Fact-Check", detail: "the fact-check returned no verdicts; asking once more" });
	assert.deepEqual(s.checked, ["3 of 4 claims supported", "late", "the fact-check returned no verdicts; asking once more"]);
});

// The streaming reasoning was shown as a line of its latest words, replaced
// as they streamed: too hard to read, as drsh's own frame found. It is not
// kept, so no view can draw it.
test("a call's reasoning is not kept for the view", () => {
	const before = JSON.stringify(initialState());
	const s = reduce(initialState(), { type: "thinking", status: "start", detail: "Weighing the options" });
	assert.equal(JSON.stringify(s), before);
});

test("only JSON objects with a type are events", () => {
	assert.equal(parseLine("warning: something"), undefined);
	assert.equal(parseLine("{not json"), undefined);
	assert.equal(parseLine('{"no":"type"}'), undefined);
	assert.equal(parseLine('{"type":"done","status":"complete"}')?.status, "complete");
});
