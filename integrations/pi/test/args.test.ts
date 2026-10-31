import assert from "node:assert/strict";
import { test } from "node:test";
import { newRunArgs, planRunArgs, resumeArgs } from "../src/args.ts";
import { settings } from "./helpers.ts";

const s = settings();
const run = { question: "-x vs y", values: { topics: "2", "follow-ups": "off" }, domains: ["go.dev"] };

test("each stage's command line", () => {
	const clarify = newRunArgs(s, run, "/r", "clarify");
	assert.deepEqual(clarify, ["--jsonl", "--reports=/r", "--prompt=-x vs y", "--topics=2", "--no-follow-ups=true", "--domains=go.dev", "--clarify-only"]);
	const answers = { questions: [], answers: [{ question: "Which?", answer: "web" }] };
	const plan = newRunArgs(s, { ...run, answers }, "/r", "plan");
	assert.ok(plan.includes("--plan-only"));
	assert.ok(plan.includes(`--answers=${JSON.stringify(answers)}`));
	const full = newRunArgs(s, { ...run, trace: true }, "/r", "run");
	assert.ok(full.includes("--trace") && !full.includes("--plan-only") && !full.includes("--clarify-only"));
});

test("a plan or a checkpoint carries its own settings", () => {
	assert.deepEqual(planRunArgs("/r/plan.json", "/r"), ["--jsonl", "--reports=/r", "--plan=/r/plan.json"]);
	assert.deepEqual(resumeArgs("/r/x.partial.json", "/r"), ["--jsonl", "--reports=/r", "--resume=/r/x.partial.json"]);
});

test("a setting drsh does not know is refused, not dropped", () => {
	assert.throws(() => newRunArgs(s, { question: "q", values: { colour: "red" } }, "/r", "run"), /unknown setting/);
});
