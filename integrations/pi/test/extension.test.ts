import assert from "node:assert/strict";
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { getKeybindings } from "@earendil-works/pi-tui";
import type { Theme } from "@earendil-works/pi-coding-agent";
import type { SettingsMenu } from "../src/menu.ts";
import { implied, step, valueLabel } from "../src/settings.ts";
import { fakeDrsh, settings } from "./helpers.ts";

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
		sendMessage: (_message: unknown, _options?: unknown) => {},
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

// On the settings screen with depth at instant, ←/→ on a row instant holds
// dropped its "(held by the depth)" and showed the held value as if chosen:
// the press was kept as a choice that the depth overrode for this run and
// that would take effect unseen once the depth changed. Enter on the row
// did the same through its option list.
test("a row the depth holds is not changed by the arrows or Enter, and the screen says why", async () => {
	const f = await load(fakeDrsh);
	const plain = { fg: (_c: string, t: string) => t, bold: (t: string) => t } as unknown as Theme;
	const [up, down, right, left, enter, esc] = ["\x1b[A", "\x1b[B", "\x1b[C", "\x1b[D", "\r", "\x1b"];
	// The presses of → that take depth from its default to instant.
	const mode = settings().find((x) => x.key === "mode")!;
	const values = mode.choices.map((o) => o.value);
	const toInstant = (values.indexOf("instant") - values.indexOf(mode.value) + values.length) % values.length;
	// The keys pressed on the screen; "snap" takes a copy of it there.
	const snap = "snap";
	const keys = [down, ...Array(toInstant).fill(right), down, right, left, snap, enter, snap, up, right, down, right, snap, esc];
	const screens: string[][] = [];
	let selects = 0;
	const ctx = {
		...f.ctx,
		hasUI: true,
		mode: "tui",
		ui: {
			...f.ctx.ui,
			input: async () => "a question",
			select: async () => {
				selects++;
				return undefined;
			},
			custom: <T>(factory: (tui: unknown, theme: Theme, kb: unknown, done: (r: T) => void) => SettingsMenu) =>
				new Promise<T>((resolve) => {
					const menu = factory(undefined, plain, getKeybindings(), resolve);
					for (const k of keys.splice(0)) {
						if (k === snap) screens.push(menu.render(100).map((l) => l.trim()));
						else menu.handleInput(k);
					}
				}),
		},
	};
	await f.commands.get("research")!.handler("", ctx);
	const row = (screen: string[], label: string) => screen.find((l) => l.replace(/^→ /, "").startsWith(label)) ?? "";
	const [arrows, entered, after] = screens;
	assert.match(row(arrows, "depth:"), /instant/, arrows.join("\n"));
	const topics = settings().find((x) => x.key === "topics")!;
	const label = (v: string) => `sub-topics: ${valueLabel(topics, v)}`;
	assert.equal(row(arrows, "sub-topics:").replace(/^→ /, ""), label(implied(settings(), { mode: "instant" }).topics), "the arrows moved a held row");
	// Why, under the last option, not as a notice above the screen.
	const why = /holds every option at its least: pick another depth to change sub-topics/;
	for (const screen of [arrows, entered]) {
		const at = screen.findIndex((l) => why.test(l));
		assert.ok(at > screen.findIndex((l) => l.includes("clarify:")), screen.join("\n"));
	}
	assert.equal(selects, 0, "Enter on a held row opened its option list");
	assert.deepEqual(f.notes, [], "the reason went to a notice");
	// Off instant, the row is free and shows the configured value, not a
	// press made while it was held, and the reason is gone.
	assert.doesNotMatch(row(after, "depth:"), /instant/, after.join("\n"));
	assert.equal(row(after, "sub-topics:").replace(/^→ /, ""), label(step(topics, topics.value, 1)), "a press made while held came into force");
	assert.ok(!after.some((l) => why.test(l)), after.join("\n"));
});

// Pi's RPC mode has dialogs (hasUI) but draws no custom component: its
// custom() answers undefined, and /research read .kind off that.
test("/research outside the terminal falls back to the plain settings select", async () => {
	const f = await load(fakeDrsh);
	const titles: string[] = [];
	const ctx = {
		...f.ctx,
		hasUI: true,
		mode: "rpc",
		ui: {
			...f.ctx.ui,
			input: async () => "a question",
			custom: async () => undefined,
			select: async (title: string) => {
				titles.push(title);
				return undefined;
			},
		},
	};
	await f.commands.get("research")!.handler("", ctx);
	assert.deepEqual(titles, ["deep-research settings"]);
});

// The tool call's view ticks for its spinner, and kept ticking while the plan
// waited on the user's review: the same still view sent ten times a second.
test("a tool call sends no ticks while the plan waits on the user", async () => {
	const f = await withEnv({ FAKE_QUESTIONS: "none" }, () => load(fakeDrsh));
	let updates = 0;
	let during = -1;
	const ctx = {
		...f.ctx,
		hasUI: true,
		ui: {
			...f.ctx.ui,
			setWidget: () => {},
			select: async () => {
				const before = updates;
				await new Promise((r) => setTimeout(r, 600));
				during = updates - before;
				return "▶ Run this plan";
			},
		},
	};
	const tool = f.tools.get("deep_research")!;
	await withEnv({ FAKE_QUESTIONS: "none" }, () =>
		tool.execute("id", { question: "q", review_plan: true }, undefined, () => updates++, ctx),
	);
	assert.equal(during, 0, `${during} updates while the review was open`);
	assert.ok(updates > 0, "the run itself sent no updates");
});

// Pi runs one submitted command at a time, and /research awaited its whole
// run: /research-stop typed during it waited behind the run and never
// stopped it. The handler now returns once the run has started.
test("/research returns once its run starts, so /research-stop can end it", async () => {
	await withEnv({ FAKE_HANG: "1", FAKE_QUESTIONS: "none" }, async () => {
		const f = await load(fakeDrsh);
		const sent: string[] = [];
		f.pi.sendMessage = (m: unknown) => void sent.push((m as { content: string }).content);
		let running = false;
		const ctx = {
			...f.ctx,
			hasUI: true,
			ui: {
				...f.ctx.ui,
				theme: { fg: (_c: string, t: string) => t, bold: (t: string) => t },
				setWidget: (_k: string, lines?: string[]) => {
					if (lines?.some((l) => l.includes("Research"))) running = true;
				},
				input: async () => "a question",
				select: async (title: string, options: string[]) => (title === "Review the plan" ? options[0] : "▶ Start"),
			},
		};
		const stop = f.commands.get("research-stop")!.handler;
		const handled = f.commands.get("research")!.handler("", ctx);
		try {
			let timer: NodeJS.Timeout | undefined;
			const late = new Promise((r) => (timer = setTimeout(() => r("still waiting"), 3000)));
			const first = await Promise.race([handled.then(() => "returned"), late]);
			clearTimeout(timer);
			assert.notEqual(first, "still waiting", "/research held the command slot for the run");
			const deadline = Date.now() + 3000;
			while (!running && Date.now() < deadline) await new Promise((r) => setTimeout(r, 20));
			assert.ok(running, "the run never started");
			await stop("", ctx);
			while (!sent.length && Date.now() < deadline + 3000) await new Promise((r) => setTimeout(r, 20));
			assert.match(sent[0] ?? "", /cancelled/, `the run did not end as cancelled: ${JSON.stringify(sent)}`);
		} finally {
			// A failure must not leave the hanging drsh holding the test open.
			await stop("", ctx);
			await handled;
		}
		f.notes.length = 0;
		await f.commands.get("research-stop")!.handler("", ctx);
		assert.ok(f.notes.some((n) => /No \/research run is going/.test(n)), "the run's slot was not freed when it ended");
	});
});

// A stopped run kept a checkpoint, but only the agent could continue it,
// through deep_research's resume: the user had no command for it.
test("/research-resume continues the last stopped run from its checkpoint", async () => {
	const dir = mkdtempSync(join(tmpdir(), "pi-dr-resume-"));
	const log = join(dir, "args.log");
	try {
		await withEnv({ FAKE_HANG: "1", FAKE_QUESTIONS: "none", FAKE_LOG: log }, async () => {
			const f = await load(fakeDrsh);
			const sent: string[] = [];
			f.pi.sendMessage = (m: unknown) => void sent.push((m as { content: string }).content);
			const ctx = {
				...f.ctx,
				hasUI: true,
				ui: {
					...f.ctx.ui,
					theme: { fg: (_c: string, t: string) => t, bold: (t: string) => t },
					setWidget: () => {},
					input: async () => "a question",
					select: async (title: string, options: string[]) => (title === "Review the plan" ? options[0] : "▶ Start"),
				},
			};
			const stop = f.commands.get("research-stop")!.handler;
			const resume = f.commands.get("research-resume")!.handler;
			await resume("", ctx);
			assert.ok(f.notes.some((n) => /No research run in this session yet/.test(n)), JSON.stringify(f.notes));
			// Each run hangs until stopped; wait for drsh to have started it.
			const runs = () => (existsSync(log) ? readFileSync(log, "utf8").trim().split("\n").map((l) => JSON.parse(l) as string[]) : []);
			const started = async (n: number) => {
				const deadline = Date.now() + 5000;
				while (runs().filter((a) => !a.includes("--plan-only") && !a.includes("--clarify-only")).length < n && Date.now() < deadline) await new Promise((r) => setTimeout(r, 20));
				await new Promise((r) => setTimeout(r, 100));
			};
			const ended = async (n: number) => {
				const deadline = Date.now() + 5000;
				while (sent.length < n && Date.now() < deadline) await new Promise((r) => setTimeout(r, 20));
			};
			try {
				await f.commands.get("research")!.handler("", ctx);
				await started(1);
				await stop("", ctx);
				await ended(1);
				assert.match(sent[0] ?? "", /\/research-resume/, "the cancelled run did not say how to resume");
				await resume("", ctx);
				await started(2);
				const last = runs().at(-1) ?? [];
				const checkpoint = /Checkpoint: (\S+?)\.? /.exec(sent[0])?.[1];
				assert.ok(checkpoint && last.includes(`--resume=${checkpoint}`), `resumed with ${JSON.stringify(last)}, want --resume=${checkpoint}`);
			} finally {
				await stop("", ctx);
				await ended(2);
			}
		});
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
});

// A run stopped by a session switch ended after the new session started and
// was recorded there, as the run /research-resume would pick.
test("a run that ends after the session changed is not the new session's to resume", async () => {
	await withEnv({ FAKE_HANG: "1", FAKE_QUESTIONS: "none" }, async () => {
		const f = await load(fakeDrsh);
		let ended = false;
		f.pi.sendMessage = () => void (ended = true);
		let running = false;
		const ctx = {
			...f.ctx,
			hasUI: true,
			ui: {
				...f.ctx.ui,
				theme: { fg: (_c: string, t: string) => t, bold: (t: string) => t },
				setWidget: (_k: string, lines?: string[]) => void (running ||= !!lines?.some((l) => l.includes("Research"))),
				input: async () => "a question",
				select: async (title: string, options: string[]) => (title === "Review the plan" ? options[0] : "▶ Start"),
			},
		};
		await f.commands.get("research")!.handler("", ctx);
		const deadline = Date.now() + 5000;
		while (!running && Date.now() < deadline) await new Promise((r) => setTimeout(r, 20));
		await f.handlers.get("session_start")?.({}, f.ctx);
		await f.commands.get("research-stop")!.handler("", ctx);
		while (!ended && Date.now() < deadline) await new Promise((r) => setTimeout(r, 20));
		await new Promise((r) => setTimeout(r, 50));
		f.notes.length = 0;
		await f.commands.get("research-resume")!.handler("", ctx);
		assert.ok(f.notes.some((n) => /No research run in this session yet/.test(n)), JSON.stringify(f.notes));
	});
});

// Stopped while planning, a run kept no checkpoint and recorded nothing:
// /research-resume said no run had happened, and typed while the stop was
// still winding down it was refused as "already going". Nothing had been
// spent, so it asks the question again; and it waits for the stop.
test("/research-resume after a stop while planning asks the same question again", async () => {
	const dir = mkdtempSync(join(tmpdir(), "pi-dr-replan-"));
	const log = join(dir, "args.log");
	try {
		await withEnv({ FAKE_HANG_PLAN: "1", FAKE_QUESTIONS: "none", FAKE_LOG: log }, async () => {
			const f = await load(fakeDrsh);
			const sent: string[] = [];
			f.pi.sendMessage = (m: unknown) => void sent.push((m as { content: string }).content);
			const ctx = {
				...f.ctx,
				hasUI: true,
				ui: {
					...f.ctx.ui,
					theme: { fg: (_c: string, t: string) => t, bold: (t: string) => t },
					setWidget: () => {},
					input: async () => "how fast is it",
					select: async () => "▶ Start",
				},
			};
			const plans = () =>
				existsSync(log)
					? readFileSync(log, "utf8").trim().split("\n").map((l) => JSON.parse(l) as string[]).filter((a) => a.includes("--plan-only"))
					: [];
			const until = async (ok: () => boolean) => {
				const deadline = Date.now() + 5000;
				while (!ok() && Date.now() < deadline) await new Promise((r) => setTimeout(r, 20));
				await new Promise((r) => setTimeout(r, 100));
			};
			const stop = f.commands.get("research-stop")!.handler;
			try {
				await f.commands.get("research")!.handler("--topics 3", ctx);
				await until(() => plans().length === 1);
				// The stop and the resume at once: the resume waits for the stop.
				await stop("", ctx);
				await f.commands.get("research-resume")!.handler("", ctx);
				assert.ok(!f.notes.some((n) => /already going|No research run/.test(n)), JSON.stringify(f.notes));
				assert.match(sent[0] ?? "", /stopped while planning/);
				await until(() => plans().length === 2);
				const [first, again] = plans();
				assert.deepEqual(again.filter((a) => !a.startsWith("--reports=")), first.filter((a) => !a.startsWith("--reports=")), "not the same question and settings");
				assert.ok(again.includes("--topics=3"), JSON.stringify(again));
			} finally {
				await stop("", ctx);
				await until(() => sent.length === 2);
			}
		});
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
});
