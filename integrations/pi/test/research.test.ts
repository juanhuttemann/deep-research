import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import type { ResearchDetails } from "../src/render.ts";
import { research, type Hooks, type Request } from "../src/research.ts";
import { runJsonl } from "../src/runner.ts";
import { fakeDrsh, settings } from "./helpers.ts";

function call(req: Request, hooks: Partial<Hooks>, env: Record<string, string> = {}, signal?: AbortSignal) {
	const reports = mkdtempSync(join(tmpdir(), "pi-dr-"));
	const log = join(reports, "args.log");
	const updates: ResearchDetails[] = [];
	const run = research({
		bin: fakeDrsh,
		settings: settings(),
		env: { ...env, FAKE_LOG: log },
		signal,
		reports,
		model: "testprov/test-model",
		req,
		hooks: { update: (d) => updates.push(structuredClone(d)), ...hooks },
	});
	const argv = () =>
		existsSync(log)
			? readFileSync(log, "utf8")
					.trim()
					.split("\n")
					.map((l) => JSON.parse(l) as string[])
			: [];
	return { run, argv, updates, reports };
}

const ask = { kind: "new" as const, run: { question: "Is Go faster?", values: { topics: "2" } }, review: false };
type NewRequest = Extract<Request, { kind: "new" }>;
const fresh = (): NewRequest => structuredClone(ask);

test("with no one to ask, the questions go back to the model and nothing is searched", async () => {
	const c = call(fresh(), {});
	const r = await c.run;
	assert.equal(r.isError, false);
	assert.match(r.text, /Which workload\? \(for example: web servers \/ CLI tools\)/);
	assert.match(r.text, /answers set to/);
	assert.deepEqual(c.argv().map((a) => a.includes("--clarify-only")), [true], "only the clarify stage ran");
});

test("answers asked in Pi reach the run, and the run's report comes back", async () => {
	const c = call(fresh(), { ask: async (qs) => qs.map((q) => ({ question: q.text, answer: "web servers" })) });
	const r = await c.run;
	assert.equal(r.isError, false);
	assert.equal(r.details.outcome?.status, "complete");
	assert.match(r.text, /Confidence: medium/);
	assert.match(r.text, /Rust is faster \[1\]/);
	const runArgs = c.argv()[1];
	assert.ok(runArgs.some((a) => a.startsWith("--answers=") && a.includes("web servers")));
	assert.ok(runArgs.includes("--topics=2"));
	assert.equal(r.record?.json, join(c.reports, "run.json"));
	assert.ok(c.updates.some((d) => d.state?.subagents.length === 2), "progress reached the view");
});

test("a reviewed plan is what runs", async () => {
	const c = call(
		{ ...fresh(), review: true },
		{
			ask: async () => [],
			review: async (plan) => ({ ...(plan as object), sub_topics: [{ id: "1", name: "Edited" }] }),
		},
		{ FAKE_QUESTIONS: "none" },
	);
	const r = await c.run;
	assert.equal(r.details.outcome?.status, "complete");
	const last = c.argv().at(-1) ?? [];
	const planArg = last.find((a) => a.startsWith("--plan="));
	assert.ok(planArg, `the run took the plan file: ${last}`);
	assert.match(readFileSync(planArg.slice("--plan=".length), "utf8"), /Edited/);
	assert.ok(!last.some((a) => a.startsWith("--prompt=")), "a plan carries its question");
});

test("dismissing the questions or the plan stops before anything is spent", async () => {
	const q = await call(fresh(), { ask: async () => "stop" }).run;
	assert.equal(q.details.outcome?.status, "cancelled");
	const p = call({ ...fresh(), review: true }, { ask: async () => [], review: async () => "stop" }, { FAKE_QUESTIONS: "none" });
	assert.equal((await p.run).details.outcome?.status, "cancelled");
	assert.equal(p.argv().filter((a) => !a.includes("--clarify-only") && !a.includes("--plan-only")).length, 0);
});

test("Esc ends the run as a cancellation with a checkpoint to resume", async () => {
	const abort = new AbortController();
	const c = call(fresh(), { ask: async () => [] }, { FAKE_QUESTIONS: "none", FAKE_HANG: "1" }, abort.signal);
	setTimeout(() => abort.abort(), 300);
	const r = await c.run;
	assert.equal(r.details.outcome?.status, "cancelled");
	assert.match(r.text, /Checkpoint: .*run\.abc\.partial\.json/);
	assert.equal(r.isError, false);
});

test("a run killed with no done line is a failure that says so", async () => {
	const r = await call(fresh(), { ask: async () => [] }, { FAKE_QUESTIONS: "none", FAKE_RUN: "killed" }).run;
	assert.equal(r.isError, true);
	assert.match(r.text, /without a done line.*panic: something broke/s);
});

test("an incomplete run keeps its report, reason and checkpoint", async () => {
	const r = await call(fresh(), { ask: async () => [] }, { FAKE_QUESTIONS: "none", FAKE_RUN: "incomplete.jsonl" }).run;
	assert.equal(r.isError, false);
	assert.match(r.text, /incomplete; say so/);
	assert.match(r.text, /about ten minutes/);
	assert.match(r.text, /Rust is faster/);
});

test("resume runs the checkpoint alone", async () => {
	const c = call({ kind: "resume", checkpoint: "/r/x.partial.json" }, {});
	await c.run;
	const [only] = c.argv();
	assert.ok(only.includes("--resume=/r/x.partial.json"));
	assert.ok(!only.some((a) => a.startsWith("--prompt=")));
});

test("the runner does not start a run already cancelled", async () => {
	const abort = new AbortController();
	abort.abort();
	const r = await runJsonl(fakeDrsh, [], { signal: abort.signal });
	assert.equal(r.aborted, true);
	assert.equal(r.done, undefined);
});
