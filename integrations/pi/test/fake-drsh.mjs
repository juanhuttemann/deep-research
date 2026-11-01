#!/usr/bin/env node
// A stand-in for drsh in the tests: it logs its arguments, replays a fixture
// event stream for the stage its flags name, and writes the artifacts the
// done line names. FAKE_HANG makes the run wait for SIGINT and end as a
// cancellation, as drsh does since headless runs take signals. FAKE_HANG=kill
// is a stop on Windows, where the process dies with no done line: a new run
// saves its checkpoint first, as drsh does after each step (a resumed one
// saves to the checkpoint it resumed), then reports its phase, and exits on
// SIGINT without a word. FAKE_HANG=torn leaves a checkpoint cut off
// mid-write, FAKE_HANG=empty one with no plan.
import { appendFileSync, mkdirSync, readFileSync, writeFileSync, copyFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const fixtures = join(dirname(fileURLToPath(import.meta.url)), "fixtures");
const args = process.argv.slice(2);
if (process.env.FAKE_LOG) appendFileSync(process.env.FAKE_LOG, JSON.stringify(args) + "\n");
const flag = (name) => args.find((a) => a.startsWith(`--${name}=`))?.slice(name.length + 3);
const reports = flag("reports") ?? ".";

if (args[0] === "settings") {
	process.stdout.write(readFileSync(join(fixtures, "settings.json"), "utf8"));
	process.exit(0);
}
if (args[0] === "doctor") {
	process.stdout.write("✓ model\n✓ search\n");
	process.exit(0);
}
if (args[0] === "ask") {
	process.stdout.write(`answer to ${args[3]} from ${args[1]}\n`);
	process.exit(0);
}

function replay(name) {
	const text = readFileSync(join(fixtures, name), "utf8").replaceAll("REPORTS", reports);
	process.stdout.write(text);
}

if (args.includes("--clarify-only")) {
	replay(process.env.FAKE_QUESTIONS === "none" ? "clarify-none.jsonl" : "clarify.jsonl");
	process.exit(0);
}
if (args.includes("--plan-only")) {
	process.stdout.write(JSON.stringify({ type: "plan", plan: { question: "q", sub_topics: [{ id: "1", name: "Benchmarks" }] } }) + "\n");
	process.stdout.write(JSON.stringify({ type: "done", status: "complete" }) + "\n");
	process.exit(0);
}
if (["kill", "torn", "empty"].includes(process.env.FAKE_HANG)) {
	const saved = { kill: '{"question": "q", "plan": [{"name": "Benchmarks"}]}', torn: '{"question": "q", "plan": [', empty: '{"question": "q", "plan": []}' };
	if (!flag("resume")) writeFileSync(join(reports, "run.def.partial.json"), saved[process.env.FAKE_HANG]);
	process.stdout.write(JSON.stringify({ type: "phase", phase: "Research", detail: "Running sub-agents" }) + "\n");
	process.on("SIGINT", () => process.exit(1));
	setInterval(() => {}, 1000);
} else if (process.env.FAKE_HANG) {
	process.stdout.write(JSON.stringify({ type: "phase", phase: "Research", detail: "Running sub-agents" }) + "\n");
	process.on("SIGINT", () => {
		const cp = join(reports, "run.abc.partial.json");
		writeFileSync(cp, "{}");
		process.stdout.write(JSON.stringify({ type: "done", status: "cancelled", checkpoint: cp }) + "\n");
		process.exit(0);
	});
	setInterval(() => {}, 1000);
} else {
	const fixture = process.env.FAKE_RUN ?? "complete.jsonl";
	if (fixture === "killed") {
		process.stdout.write(JSON.stringify({ type: "phase", phase: "Research" }) + "\n");
		process.stderr.write("panic: something broke\n");
		process.exit(2);
	}
	mkdirSync(reports, { recursive: true });
	writeFileSync(join(reports, "run.md"), "# Report\n\nRust is faster [1].\n");
	copyFileSync(join(fixtures, process.env.FAKE_JSON ?? "run.json"), join(reports, "run.json"));
	replay(fixture);
}
