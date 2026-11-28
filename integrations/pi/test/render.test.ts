import assert from "node:assert/strict";
import { test } from "node:test";
import type { Theme } from "@earendil-works/pi-coding-agent";
import { initialState, reduce } from "../src/events.ts";
import { clock, costText, count, filesLines, progressLines, type ResearchDetails, statusLabel, tickMs } from "../src/render.ts";

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

// Before the run nothing is searched: "0 searches · 0 pages · 0 sources"
// stood under the clarifier and the planner.
test("before the run, the view counts tokens only", () => {
	const s = reduce(initialState(), { type: "token", tokens: 641 });
	const d: ResearchDetails = { question: "q", stage: "plan", started: 0, state: s };
	const lines = progressLines(d, plain, 0);
	assert.ok(!lines.some((l) => l.includes("searches")), lines.join("\n"));
	assert.ok(lines.includes("641 tokens"), lines.join("\n"));
	assert.equal(progressLines({ ...d, state: initialState() }, plain, 0).length, 1, "no counts at all before any token");
});

function searched(): ResearchDetails {
	let s = initialState();
	s = reduce(s, { type: "phase", phase: "Research" });
	s = reduce(s, { type: "subagent", sub_id: "1", sub_name: "Causes", sub_state: "done", progress: 100 });
	s = reduce(s, { type: "subagent", sub_id: "2", sub_name: "Timeline", sub_state: "running", progress: 40 });
	s = reduce(s, { type: "subagent", sub_id: "f1", sub_name: "Open question", sub_state: "queued" });
	return { question: "q", stage: "run", started: 0, state: s };
}

test("while searching, a follow-up is marked and a finished row drops its 100%", () => {
	const lines = progressLines(searched(), plain, 0);
	const row = (name: string) => lines.find((l) => l.includes(name)) ?? "";
	assert.match(row("Causes"), /● Causes$/);
	assert.match(row("Timeline"), /Timeline 40%$/);
	assert.match(row("Open question"), /follow-up · Open question/);
});

// Finished rows at 100% stayed through the analysis, the fact-check and the
// report, pushing the phase's own lines down.
test("once the search is over, its rows are one line, a failed one kept", () => {
	const d = searched();
	let s = d.state!;
	s = reduce(s, { type: "subagent", sub_id: "2", sub_state: "error" });
	s = reduce(s, { type: "subagent", sub_id: "f1", sub_state: "done" });
	s = reduce(s, { type: "phase", phase: "Analyze" });
	const lines = progressLines({ ...d, state: s }, plain, 0);
	assert.ok(lines.includes("  ● 3 sub-topics searched (2 planned, 1 follow-up), 1 with errors"), lines.join("\n"));
	assert.ok(lines.some((l) => l.includes("✗ Timeline")), "the failed row is kept");
	assert.ok(!lines.some((l) => l.includes("Causes")), lines.join("\n"));
});

// A failed search is an error event naming the sub-agent, which still ends
// done: folded, it was counted under a green dot with the rest.
test("a sub-agent that ended with errors keeps its row when the rest fold", () => {
	let s = searched().state!;
	s = reduce(s, { type: "error", sub_id: "1", detail: "search failed: rate-limited" });
	for (const id of ["2", "f1"]) s = reduce(s, { type: "subagent", sub_id: id, sub_state: "done" });
	s = reduce(s, { type: "phase", phase: "Analyze" });
	const lines = progressLines({ question: "q", stage: "run", started: 0, state: s }, plain, 0);
	assert.ok(lines.some((l) => l.endsWith("3 sub-topics searched (2 planned, 1 follow-up), 1 with errors")), lines.join("\n"));
	assert.ok(lines.some((l) => l.includes("Causes 1 error")), lines.join("\n"));
});

test("a view saved before the fact-check's notes were kept still draws", () => {
	const { checked: _, ...old } = initialState();
	assert.doesNotThrow(() => progressLines({ question: "q", stage: "clarify", state: old as never }, plain, 0));
});

// In the fact-check the status line read "analyzing — … · 0:08" under a
// header with its own clock at 3:52: the repair pass's analyzer call, with a
// second clock.
test("the status is under the header, with no clock of its own; in the fact-check an analyzer call is the revision", () => {
	let s = reduce(initialState(), { type: "phase", phase: "Fact-Check", detail: "Revising 2 statements the check did not approve" });
	s = reduce(s, { type: "info", transient: true, detail: "analyzing — listing what the sources show · 6 claims · 0:08" });
	s = reduce(s, { type: "info", phase: "Fact-Check", detail: "11 of 12 claims supported" });
	const lines = progressLines({ question: "q", stage: "run", started: 1_000, state: s }, plain, 233_000);
	assert.match(lines[0], /^. Fact-Check · Revising 2 statements the check did not approve · 3:52$/);
	assert.equal(lines[1], "  revising — listing what the sources show · 6 claims");
	assert.equal(lines.at(-1), "fact-check: 11 of 12 claims supported");
});

test("the footer names each stage in one case", () => {
	assert.equal(statusLabel({ question: "q", stage: "clarify" }), "Clarifying");
	assert.equal(statusLabel({ question: "q", stage: "plan", state: initialState() }), "Planning");
	const run = (phase: string) => statusLabel({ question: "q", stage: "run", state: { ...initialState(), phase } });
	assert.equal(run("Research"), "Searching");
	assert.equal(run("Fact-Check"), "Fact-checking");
	assert.equal(run(""), "Starting");
	assert.equal(run("Something New"), "Something New", "a phase not known is shown as drsh names it");
});

test("a zero cost is not shown; one that rounds to zero is bounded", () => {
	assert.equal(costText(0), "");
	assert.equal(costText(0.00004), " (<$0.0001)");
	assert.equal(costText(0.0031), " ($0.0031)");
	assert.equal(costText(1.234), " ($1.23)");
});

test("a run's files name their folder once; expanded, each path in full", () => {
	const dir = "/r/2026-run-abc";
	const paths = [`${dir}/Why-it-went-1f2e.md`, `${dir}/Why-it-went-1f2e.pdf`, `${dir}/Why-it-went-1f2e.json`];
	assert.deepEqual(filesLines(paths, false, plain), [`files in ${dir}/`, "  Why-it-went-1f2e .md .pdf .json"]);
	assert.deepEqual(filesLines(paths, true, plain), paths);
	assert.deepEqual(filesLines([], false, plain), []);
});
