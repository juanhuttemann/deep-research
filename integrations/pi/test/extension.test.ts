import assert from "node:assert/strict";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fakeDrsh } from "./helpers.ts";

// The extension's state across calls, through a stand-in for Pi's API: the
// tools and commands it registers, the session entries it appends, and a
// context with no UI, on drsh's own model, so no bridge is needed.

type Tool = { name: string; parameters: { properties: Record<string, unknown> }; execute: (...a: unknown[]) => Promise<any> };
type Handler = (event: unknown, ctx: unknown) => Promise<void>;

function fakePi() {
	const tools = new Map<string, Tool>();
	const commands = new Map<string, { handler: (args: string, ctx: unknown) => Promise<void> }>();
	const handlers = new Map<string, Handler>();
	const notes: string[] = [];
	const entries = [{ type: "custom", customType: "deep-research-model", data: { kind: "drsh" } }];
	const pi = {
		registerTool: (t: Tool) => tools.set(t.name, t),
		registerCommand: (name: string, c: { handler: (args: string, ctx: unknown) => Promise<void> }) => commands.set(name, c),
		on: (name: string, h: Handler) => handlers.set(name, h),
		appendEntry: (customType: string, data: unknown) => entries.push({ type: "custom", customType, data: data as { kind: "drsh" } }),
		sendMessage: () => {},
	};
	const ctx = {
		hasUI: false,
		ui: { notify: (m: string) => notes.push(m), setStatus: () => {} },
		sessionManager: { getBranch: () => entries },
		modelRegistry: {},
	};
	return { pi, ctx, tools, commands, handlers, notes };
}

// load imports the extension with DRSH_BIN set, which it reads when loaded.
async function load(bin: string) {
	const f = fakePi();
	const was = process.env.DRSH_BIN;
	process.env.DRSH_BIN = bin;
	try {
		const { default: deepResearch } = await import(`../src/index.ts?bin=${encodeURIComponent(bin)}`);
		deepResearch(f.pi);
	} finally {
		if (was === undefined) delete process.env.DRSH_BIN;
		else process.env.DRSH_BIN = was;
	}
	await f.handlers.get("session_start")?.({}, f.ctx);
	return f;
}

function withEnv<T>(env: Record<string, string>, f: () => Promise<T>): Promise<T> {
	const was = Object.fromEntries(Object.keys(env).map((k) => [k, process.env[k]]));
	Object.assign(process.env, env);
	return f().finally(() => {
		for (const [k, v] of Object.entries(was)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	});
}

// drsh installed after the session started was found by /research and
// /research-doctor, but the agent kept the stand-in tool until it called
// it once and was told to call again.
test("drsh installed mid-session replaces the stand-in wherever it is first found", async () => {
	const dir = mkdtempSync(join(tmpdir(), "pi-dr-bin-"));
	const bin = join(dir, "drsh");
	try {
		const f = await load(bin);
		assert.deepEqual(Object.keys(f.tools.get("deep_research")?.parameters.properties ?? {}), ["question"], "the stand-in");
		assert.equal(f.tools.has("research_ask"), false);
		// A wrapper, not a copy: the fake reads its fixtures beside itself.
		writeFileSync(bin, `#!/bin/sh\nexec "${process.execPath}" "${fakeDrsh}" "$@"\n`);
		chmodSync(bin, 0o755);
		await f.commands.get("research-doctor")?.handler("", f.ctx);
		assert.ok("topics" in (f.tools.get("deep_research")?.parameters.properties ?? {}), "the real tool, with drsh's settings");
		assert.ok(f.tools.has("research_ask"));
		const g = await load(bin);
		assert.ok("topics" in (g.tools.get("deep_research")?.parameters.properties ?? {}), "and at the next session's start");
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
});

// A run stopped after a finished one became the session's last run, and
// research_ask with no run path said no run had happened.
test("research_ask still reads the last run with a report after a later run is stopped", async () => {
	const runs = mkdtempSync(join(tmpdir(), "pi-dr-runs-"));
	const log = join(runs, "args.log");
	writeFileSync(log, "");
	try {
		await withEnv({ DEEP_RESEARCH_PI_RUNS: runs, FAKE_LOG: log }, async () => {
			const f = await load(fakeDrsh);
			const research = f.tools.get("deep_research");
			assert.ok(research);
			const done = await research.execute("1", { question: "Is Go faster?", answers: [] }, undefined, undefined, f.ctx);
			assert.equal(done.details.outcome.status, "complete");
			const first = done.details.outcome.artifacts.find((p: string) => p.endsWith("run.json"));
			const abort = new AbortController();
			setTimeout(() => abort.abort(), 300);
			const stopped = await withEnv({ FAKE_HANG: "1" }, () =>
				research.execute("2", { question: "Is Rust faster?", answers: [] }, abort.signal, undefined, f.ctx),
			);
			assert.equal(stopped.details.outcome.status, "cancelled");
			const r = await f.tools.get("research_ask")?.execute("3", { question: "Which benchmark?" }, undefined, undefined, f.ctx);
			assert.equal(r.details.run, first);
			assert.ok(readFileSync(log, "utf8").includes(JSON.stringify(["ask", first, "-p", "Which benchmark?"])));
		});
	} finally {
		rmSync(runs, { recursive: true, force: true });
	}
});

// /research took its run slot only after its prompts, so a second /research
// typed while the first waited on its question also passed the check; the
// first to end freed the slot while the other went on, past /research-stop.
test("a /research waiting on its prompts holds off a second one", async () => {
	const f = await load(fakeDrsh);
	let answer: (v: string | undefined) => void = () => {};
	const ctx = {
		...f.ctx,
		hasUI: true,
		ui: { ...f.ctx.ui, input: () => new Promise<string | undefined>((r) => (answer = r)) },
	};
	const research = f.commands.get("research")!.handler;
	const stop = f.commands.get("research-stop")!.handler;
	const first = research("", ctx);
	await new Promise((r) => setImmediate(r));
	await research("", ctx);
	assert.ok(f.notes.some((n) => /already going/.test(n)), `second /research was not held off: ${JSON.stringify(f.notes)}`);
	await stop("", ctx);
	answer(undefined);
	await first;
	f.notes.length = 0;
	await stop("", ctx);
	assert.ok(f.notes.some((n) => /No \/research run is going/.test(n)), "the slot was not freed when the first ended");
});

// research_ask stopped with Esc read as a failure with no reason at all:
// "drsh ask failed: ".
test("a cancelled research_ask says it was cancelled", async () => {
	const f = await load(fakeDrsh);
	const abort = new AbortController();
	abort.abort();
	await assert.rejects(
		f.tools.get("research_ask")!.execute("1", { question: "Which benchmark?", run: "/r/run.json" }, abort.signal, undefined, f.ctx),
		/research_ask was cancelled/,
	);
});
