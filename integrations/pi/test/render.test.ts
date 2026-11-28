import assert from "node:assert/strict";
import { test } from "node:test";
import type { Theme } from "@earendil-works/pi-coding-agent";
import { initialState, reduce } from "../src/events.ts";
import { clock, count, progressLines, type ResearchDetails, tickMs } from "../src/render.ts";

const plain = { fg: (_c: string, s: string) => s, bold: (s: string) => s, italic: (s: string) => s } as unknown as Theme;

function running(): ResearchDetails {
	let s = initialState();
	s = reduce(s, { type: "phase", phase: "Research" });
	s = reduce(s, { type: "subagent", sub_id: "1", sub_name: "Causes", sub_state: "running" });
	s = reduce(s, { type: "thinking", status: "start", detail: "However, the search results reveal" });
	return { question: "q", stage: "run", started: 1_000_000, state: s };
}

test("clock reads as drsh writes times: m:ss, then h:mm:ss", () => {
	assert.equal(clock(0), "0:00");
	assert.equal(clock(65_900), "1:05");
	assert.equal(clock(3_725_000), "1:02:05");
	assert.equal(clock(-5), "0:00");
});

// The live view had no spinner and no clock: through a model call that sends
// no events for a minute it stood still and read as hung.
test("the live view moves with no new event: the spinner turns and the clock counts", () => {
	const d = running();
	const at = (ms: number) => progressLines(d, plain, d.started! + ms);
	assert.match(at(12_000)[0], /^. Research · 0:12$/);
	assert.notEqual(at(12_000)[0][0], at(12_000 + tickMs)[0][0], "the phase spinner did not turn");
	const agent = (lines: string[]) => lines.find((l) => l.includes("Causes")) ?? "";
	assert.notEqual(agent(at(0)), agent(at(tickMs)), "a running sub-agent's spinner did not turn");
	assert.match(at(75_000)[0], /1:15$/);
});

test("the planning stage, before any run state, has its spinner and clock", () => {
	const d: ResearchDetails = { question: "q", stage: "plan", started: 5_000 };
	assert.match(progressLines(d, plain, 8_000)[0], /^. Planning · 0:03$/);
});

test("the streaming reasoning is not shown", () => {
	const lines = progressLines(running(), plain, 1_000_000);
	assert.ok(!lines.some((l) => l.includes("search results reveal") || l.startsWith("…")), lines.join("\n"));
});

// While the plan waited on the user's review, "Planning" kept spinning and
// its clock kept counting the user's time as the model's.
test("a stage waiting on the user neither spins nor counts", () => {
	const d: ResearchDetails = { question: "q", stage: "plan" };
	assert.equal(progressLines(d, plain, 8_000)[0], "Planning");
});

test("counts past a thousand are abbreviated: 1K, 10.1K, 1.5M", () => {
	const cases: Array<[number, string]> = [
		[999, "999"],
		[1000, "1K"],
		[10063, "10.1K"],
		[999_949, "999.9K"],
		[999_950, "1M"],
		[1_500_000, "1.5M"],
		[2_000_000, "2M"],
	];
	for (const [n, want] of cases) assert.equal(count(n), want, `count(${n})`);
	const d = running();
	d.state!.tokens = 10063;
	assert.ok(progressLines(d, plain, d.started!).some((l) => l.endsWith("10.1K tokens")));
});
