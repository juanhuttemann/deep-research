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
	// The status keeps the first line of a transient info: in drsh 0.39 and
	// earlier the rest is the reasoning, which has its own event.
	assert.equal(s.status, "asking provider/model");
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
