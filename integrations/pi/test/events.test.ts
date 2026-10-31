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
	assert.equal(s.reads, 1);
	assert.equal(s.sources, 1);
	assert.equal(s.tokens, 1200);
	assert.deepEqual(s.errors, ["search failed: rate-limited"]);
	assert.deepEqual(s.domains.map((d) => d.domain), ["go.dev"]);
	// The status keeps the first line of a transient info: the rest is the
	// reasoning, which has its own event.
	assert.equal(s.status, "asking provider/model");
});

test("a call's reasoning restarts on its first piece and is joined after", () => {
	let s = initialState();
	s = reduce(s, { type: "thinking", status: "start", detail: "old" });
	s = reduce(s, { type: "thinking", status: "start", detail: "Weighing" });
	s = reduce(s, { type: "thinking", detail: " the options" });
	assert.equal(s.thinking, "Weighing the options");
	s = reduce(s, { type: "phase", phase: "Analyze" });
	assert.equal(s.thinking, "", "a new phase clears the last call's reasoning");
});

test("only JSON objects with a type are events", () => {
	assert.equal(parseLine("warning: something"), undefined);
	assert.equal(parseLine("{not json"), undefined);
	assert.equal(parseLine('{"no":"type"}'), undefined);
	assert.equal(parseLine('{"type":"done","status":"complete"}')?.status, "complete");
});
