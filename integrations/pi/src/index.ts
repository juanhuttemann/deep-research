// deep-research in Pi: the deep_research and research_ask tools, and the
// /research, /research-model, /research-doctor and /research-stop commands.
// drsh does the research; Pi's model answers its model calls through a local
// bridge (bridge.ts), so a run needs no model setup in drsh and its usage is
// counted in the session.

import { mkdir, mkdtemp } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import type { Api, Model } from "@earendil-works/pi-ai";
import { StringEnum } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ExtensionContext, Theme } from "@earendil-works/pi-coding-agent";
import { Text } from "@earendil-works/pi-tui";
import { Type, type TSchema } from "typebox";
import type { Answer } from "./args.ts";
import { startBridge, type Bridge } from "./bridge.ts";
import type { Question } from "./events.ts";
import { outcomeLines, progressLines, type ResearchDetails } from "./render.ts";
import { research, type Hooks, type Request, type Result, type RunRecord } from "./research.ts";
import { runPlain } from "./runner.ts";
import {
	completions,
	describe,
	effective,
	fromParams,
	implied,
	paramName,
	parseCommandArgs,
	parseSettings,
	valueLabel,
	type Setting,
} from "./settings.ts";

const modelEntry = "deep-research-model";
const runEntry = "deep-research-run";

// ModelChoice is what /research-model picked: Pi's current model, one model
// from Pi's registry, or drsh's own configured model (no bridge).
type ModelChoice = { kind: "pi" } | { kind: "drsh" } | { kind: "model"; provider: string; id: string };

const installHelp = [
	"Install drsh, the deep-research CLI:",
	"  Linux, macOS, WSL2: curl -fsSL https://raw.githubusercontent.com/juanhuttemann/deep-research/main/scripts/install.sh | bash",
	"  Windows: iex (irm https://raw.githubusercontent.com/juanhuttemann/deep-research/main/scripts/install.ps1)",
	"or set DRSH_BIN to its path.",
].join("\n");

function reportsRoot(): string {
	if (process.env.DEEP_RESEARCH_PI_RUNS) return process.env.DEEP_RESEARCH_PI_RUNS;
	const cache =
		process.env.XDG_CACHE_HOME ||
		(process.platform === "win32" ? process.env.LOCALAPPDATA : undefined) ||
		join(homedir(), process.platform === "darwin" ? "Library/Caches" : ".cache");
	return join(cache ?? join(homedir(), ".cache"), "deep-research-pi", "runs");
}

// newReportsDir is a fresh directory for one run's files. It outlives the
// session, so research_ask and resume still work after a restart, and
// nothing is written into the user's project.
// mkdtemp's suffix makes it unique: two sessions asking the same question
// in the same second would otherwise share one, and run each other's plan.
async function newReportsDir(question: string): Promise<string> {
	const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
	const slug = question.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 40) || "run";
	await mkdir(reportsRoot(), { recursive: true });
	return mkdtemp(join(reportsRoot(), `${stamp}-${slug}-`));
}

// throttle limits f to once per ms. cancel drops a pending call: one firing
// after the run ended would redraw progress the run already cleared.
function throttle(f: () => void, ms: number): { call(): void; cancel(): void } {
	let last = 0;
	let timer: NodeJS.Timeout | undefined;
	return {
		call() {
			const wait = last + ms - Date.now();
			if (wait <= 0) {
				last = Date.now();
				f();
			} else if (!timer) {
				timer = setTimeout(() => {
					timer = undefined;
					last = Date.now();
					f();
				}, wait);
			}
		},
		cancel() {
			if (timer) clearTimeout(timer);
			timer = undefined;
		},
	};
}

// untilAborted is p, or undefined once signal aborts: the editor takes no
// signal, and a run stopped (Esc, /research-stop) while it is open must not
// wait for it to be closed, its endpoint listening meanwhile.
function untilAborted<T>(p: Promise<T>, signal?: AbortSignal): Promise<T | undefined> {
	if (!signal) return p;
	if (signal.aborted) return Promise.resolve(undefined);
	return new Promise((resolve, reject) => {
		const stop = () => resolve(undefined);
		signal.addEventListener("abort", stop, { once: true });
		p.then(
			(v) => {
				signal.removeEventListener("abort", stop);
				resolve(v);
			},
			(e) => {
				signal.removeEventListener("abort", stop);
				reject(e);
			},
		);
	});
}

export default function deepResearch(pi: ExtensionAPI) {
	const bin = process.env.DRSH_BIN || "drsh";
	let settings: Setting[] = [];
	let loadError = "";
	let choice: ModelChoice = { kind: "pi" };
	let lastRun: RunRecord | undefined;
	let commandRun: AbortController | undefined;

	async function loadSettings(): Promise<Setting[]> {
		try {
			const r = await runPlain(bin, ["settings"]);
			if (r.code !== 0) {
				const old = /unknown command|unknown flag/i.test(r.stderr);
				throw new Error(old ? `drsh is too old for this extension: run \`drsh update\`. (${r.stderr.trim()})` : r.stderr.trim() || `drsh settings exited ${r.code}`);
			}
			settings = parseSettings(r.stdout);
			loadError = "";
		} catch (err) {
			const e = err as NodeJS.ErrnoException;
			loadError = e.code === "ENOENT" ? `drsh was not found (${bin}).\n${installHelp}` : e.message;
			throw new Error(loadError);
		}
		return settings;
	}

	async function ensureSettings(): Promise<Setting[]> {
		return settings.length ? settings : loadSettings();
	}

	// resolveModel is the model a run is answered by, or none when drsh's own
	// configured model was chosen.
	function resolveModel(ctx: ExtensionContext, named?: string): { model?: Model<Api>; label: string } {
		const want: ModelChoice = named
			? { kind: "model", provider: named.split("/")[0], id: named.split("/").slice(1).join("/") }
			: choice;
		if (want.kind === "drsh") {
			return { label: "drsh's configured model" };
		}
		const model = want.kind === "model" ? ctx.modelRegistry.find(want.provider, want.id) : ctx.model;
		if (!model) {
			throw new Error(
				want.kind === "model"
					? `no model ${want.provider}/${want.id} in Pi; pick one with /research-model`
					: "no model is selected in Pi; select one, or pick one with /research-model",
			);
		}
		return { model, label: `${model.provider}/${model.id}` };
	}

	// withEndpoint runs f with drsh pointed at Pi's model, and returns what
	// it returned with the usage the bridge counted.
	async function withEndpoint<T>(
		ctx: ExtensionContext,
		model: Model<Api> | undefined,
		signal: AbortSignal | undefined,
		f: (env: Record<string, string>) => Promise<T>,
	): Promise<{ value: T; bridge?: Bridge }> {
		if (!model) {
			return { value: await f({}) };
		}
		const bridge = await startBridge({
			model,
			signal,
			stream: (m, c, o) => ctx.modelRegistry.streamSimple(m, c, o),
		});
		try {
			return { value: await f(bridge.env), bridge };
		} finally {
			await bridge.close();
		}
	}

	function remember(rec: RunRecord | undefined): void {
		if (!rec) return;
		lastRun = rec;
		pi.appendEntry(runEntry, rec);
	}

	// askQuestions puts each clarifying question to the user: its options,
	// another answer typed in, or skip. Esc stops the run, as Ctrl-C at the
	// terminal's questions does.
	async function askQuestions(ctx: ExtensionContext, qs: Question[], signal?: AbortSignal): Promise<Answer[] | "stop"> {
		const other = "Other…";
		const skip = "Skip (let the run decide)";
		const out: Answer[] = [];
		for (const q of qs) {
			const pick = await ctx.ui.select(q.text, [...(q.options ?? []), other, skip], { signal });
			if (pick === undefined) return "stop";
			if (pick === skip) continue;
			const answer = pick === other ? await ctx.ui.input(q.text, "your answer", { signal }) : pick;
			if (answer === undefined) return "stop";
			if (answer.trim()) out.push({ question: q.text, answer: answer.trim() });
		}
		return out;
	}

	// reviewPlan opens the plan as JSON: sub-topics, their queries and terms,
	// follow-ups and the report shape can all be edited before any search.
	async function reviewPlan(ctx: ExtensionContext, plan: unknown, signal?: AbortSignal): Promise<unknown | "stop"> {
		let text = JSON.stringify(plan, null, 2);
		for (;;) {
			const edited = await untilAborted(ctx.ui.editor("Review the plan (edit, then save to run it; cancel to stop)", text), signal);
			if (edited === undefined) return "stop";
			try {
				return JSON.parse(edited);
			} catch (err) {
				ctx.ui.notify(`The plan is not valid JSON: ${(err as Error).message}`, "error");
				text = edited;
			}
		}
	}

	async function run(
		ctx: ExtensionContext,
		req: Request,
		modelName: string | undefined,
		signal: AbortSignal | undefined,
		hooks: Hooks,
	): Promise<{ result: Result; bridge?: Bridge }> {
		const s = await ensureSettings();
		const { model, label } = resolveModel(ctx, modelName);
		const reports = await newReportsDir(req.kind === "new" ? req.run.question : "resume");
		const { value, bridge } = await withEndpoint(ctx, model, signal, (env) =>
			research({ bin, settings: s, env, signal, reports, model: label, req, hooks }),
		);
		remember(value.record);
		return { result: value, bridge };
	}

	function schema(s: Setting[]): TSchema {
		const props: Record<string, TSchema> = {
			question: Type.Optional(
				Type.String({ description: "The question, the way the user means it, with its constraints (dates, region, versions, budget). The planner works from it alone." }),
			),
			resume: Type.Optional(
				Type.String({ description: "A checkpoint path a cancelled, failed or incomplete run reported: continues that run without repeating finished searches. Takes no other parameter." }),
			),
			answers: Type.Optional(
				Type.Array(Type.Object({ question: Type.String(), answer: Type.String() }), {
					description: "The user's answers to the clarifying questions an earlier call returned. Given, no questions are asked.",
				}),
			),
			domains: Type.Optional(
				Type.Array(Type.String(), { description: "Research only these sites, e.g. [\"go.dev\", \"pkg.go.dev\"]. Each is checked first; one that fails is dropped with a reason." }),
			),
			review_plan: Type.Optional(
				Type.Boolean({ description: "Show the user the plan (sub-topics and queries) to edit before searching. Only when the user asks to see or shape the plan." }),
			),
			trace: Type.Optional(Type.Boolean({ description: "Also write a trace of every search result and model phase, for comparing models later." })),
			model: Type.Optional(
				Type.String({ description: "provider/id of a Pi model to research with. Leave unset: the user's /research-model choice, else the current model." }),
			),
		};
		for (const x of s) {
			const description = describe(x);
			props[paramName(x.key)] = Type.Optional(
				x.count
					? Type.Integer({ minimum: 0, ...(x.max ? { maximum: x.max } : {}), description })
					: StringEnum(x.choices.map((o) => o.value), { description }),
			);
		}
		return Type.Object(props);
	}

	function requestOf(s: Setting[], p: Record<string, unknown>): Request {
		if (typeof p.resume === "string" && p.resume) {
			return { kind: "resume", checkpoint: p.resume };
		}
		const question = typeof p.question === "string" ? p.question.trim() : "";
		if (!question) {
			throw new Error("deep_research needs a question (or resume with a checkpoint path)");
		}
		const answers = Array.isArray(p.answers) ? (p.answers as Answer[]) : undefined;
		return {
			kind: "new",
			run: {
				question,
				values: fromParams(s, p),
				domains: Array.isArray(p.domains) ? (p.domains as string[]) : undefined,
				answers: answers ? { questions: [], answers } : undefined,
				trace: p.trace === true,
			},
			review: p.review_plan === true,
		};
	}

	function renderDetails(d: ResearchDetails | undefined, expanded: boolean, theme: Theme, fallback: string) {
		if (!d) return new Text(fallback, 0, 0);
		const lines = d.outcome ? outcomeLines(d, expanded, theme) : progressLines(d, theme);
		return new Text(lines.join("\n"), 0, 0);
	}

	function registerTools(s: Setting[]): void {
		pi.registerTool({
			name: "deep_research",
			label: "Deep research",
			description:
				"Research a question across the web and return a fact-checked, cited report: plans sub-topics, searches them in parallel, analyzes the evidence, checks every claim against the fetched page text. One run takes minutes and spends model requests: run it once per question, not as a quick lookup.",
			promptSnippet: "Fact-checked, cited web research on a question (minutes per run)",
			promptGuidelines: [
				"Use deep_research when the user asks to research, investigate or survey a topic, compare options with sources, or wants a sourced answer that needs many web pages.",
				"Leave the settings unset unless the user asks for them: the user's configured defaults apply. Use mode deep only when the user asks for thoroughness; mode instant is not fact-checked and only for a fast lookup the user asks for.",
				"When a run reports a checkpoint, offer to resume it rather than starting over.",
				"For a follow-up question the run's pages may answer, use research_ask instead of a new run.",
			],
			parameters: schema(s),
			async execute(_id, params, signal, onUpdate, ctx) {
				const p = params as Record<string, unknown>;
				const req = requestOf(await ensureSettings(), p);
				let latest: ResearchDetails | undefined;
				const push = throttle(() => {
					if (latest) onUpdate?.({ content: [{ type: "text", text: progressLines(latest, plainTheme).join("\n") }], details: latest });
				}, 250);
				const hooks: Hooks = {
					update: (d) => {
						latest = d;
						push.call();
					},
					ask: ctx.hasUI ? (qs) => askQuestions(ctx, qs, signal) : undefined,
					review: ctx.hasUI && req.kind === "new" && req.review ? (plan) => reviewPlan(ctx, plan, signal) : undefined,
				};
				const { result, bridge } = await run(ctx, req, typeof p.model === "string" ? p.model : undefined, signal, hooks).finally(() =>
					push.cancel(),
				);
				return {
					content: [{ type: "text", text: result.text }],
					details: result.details,
					isError: result.isError,
					usage: bridge?.usage,
				};
			},
			renderCall(args, theme) {
				const a = args as Record<string, unknown>;
				const title = theme.fg("toolTitle", theme.bold("deep research "));
				if (a.resume) return new Text(title + theme.fg("muted", `resume ${a.resume}`), 0, 0);
				const given = s
					.filter((x) => a[paramName(x.key)] !== undefined)
					.map((x) => `${x.label} ${valueLabel(x, String(a[paramName(x.key)]))}`);
				if (Array.isArray(a.domains) && a.domains.length) given.push(`on ${a.domains.join(", ")}`);
				return new Text(title + theme.fg("accent", String(a.question ?? "")) + (given.length ? theme.fg("muted", ` [${given.join(", ")}]`) : ""), 0, 0);
			},
			renderResult(result, { expanded }, theme) {
				const first = result.content[0];
				return renderDetails(result.details as ResearchDetails | undefined, expanded, theme, first?.type === "text" ? first.text : "");
			},
		});

		pi.registerTool({
			name: "research_ask",
			label: "Ask a research run",
			description:
				"Answer a follow-up question from the pages an earlier deep_research run fetched, without searching again. The answer is not fact-checked. A question those pages do not answer needs a new deep_research run.",
			promptSnippet: "Follow-up question answered from an earlier research run's pages (not fact-checked)",
			parameters: Type.Object({
				question: Type.String({ description: "The follow-up question." }),
				run: Type.Optional(Type.String({ description: "The run's .json artifact path. Default: the last run in this session." })),
				model: Type.Optional(Type.String({ description: "provider/id of a Pi model to answer with; default as for deep_research." })),
			}),
			async execute(_id, params, signal, _onUpdate, ctx) {
				const runFile = params.run || lastRun?.json;
				if (!runFile) {
					throw new Error("no research run in this session yet: pass run with a run's .json path, or run deep_research first");
				}
				const { model, label } = resolveModel(ctx, params.model);
				const { value: r, bridge } = await withEndpoint(ctx, model, signal, (env) =>
					runPlain(bin, ["ask", runFile, "-p", params.question], { env, signal }),
				);
				if (r.code !== 0) {
					throw new Error(`drsh ask failed: ${(r.stderr || r.stdout).trim().slice(-2000)}`);
				}
				return {
					content: [
						{
							type: "text",
							text: `Answer from the pages of ${runFile} (on ${label}). NOT fact-checked: say so.\n\n${r.stdout.trim()}`,
						},
					],
					details: { run: runFile, model: label },
					usage: bridge?.usage,
				};
			},
		});
	}

	// The tools are registered once drsh has said what its settings are; until
	// then, or when it cannot, a stand-in says what is wrong.
	function registerStandIn(): void {
		pi.registerTool({
			name: "deep_research",
			label: "Deep research",
			description: "Fact-checked, cited web research with the deep-research CLI (drsh). Unavailable until drsh is installed.",
			parameters: Type.Object({ question: Type.Optional(Type.String()) }),
			async execute() {
				const s = await loadSettings();
				registerTools(s);
				throw new Error("drsh is available now; call deep_research again");
			},
		});
	}

	pi.on("session_start", async (_event, ctx) => {
		choice = { kind: "pi" };
		lastRun = undefined;
		for (const e of ctx.sessionManager.getBranch()) {
			if (e.type !== "custom") continue;
			if (e.customType === modelEntry && e.data) choice = e.data as ModelChoice;
			if (e.customType === runEntry && e.data) lastRun = e.data as RunRecord;
		}
		try {
			registerTools(await loadSettings());
		} catch {
			registerStandIn();
		}
	});

	pi.on("session_shutdown", async () => {
		commandRun?.abort();
	});

	// settingsScreen is the start screen: each setting at its value, what
	// instant holds shown as held, Start first.
	async function settingsScreen(ctx: ExtensionContext, s: Setting[], values: Record<string, string>): Promise<Record<string, string> | undefined> {
		const start = "▶ Start";
		for (;;) {
			const eff = effective(s, values);
			const held = implied(s, values);
			const rows = s.map((x) => {
				const mark = held[x.key] !== undefined && values[x.key] === undefined ? " (held by the depth)" : "";
				return `${x.label}: ${valueLabel(x, eff[x.key])}${mark}`;
			});
			const pick = await ctx.ui.select("deep-research settings", [start, ...rows]);
			if (pick === undefined) return undefined;
			if (pick === start) return values;
			const x = s[rows.indexOf(pick)];
			const opts = x.choices.map((o) => `${o.label}${o.note ? ` — ${o.note}` : ""}`);
			const v = await ctx.ui.select(`${x.label}: ${x.help}`, opts);
			if (v !== undefined) values = { ...values, [x.key]: x.choices[opts.indexOf(v)].value };
		}
	}

	pi.registerCommand("research", {
		description: "Research a question: settings, clarifying questions and plan review, then a live run",
		getArgumentCompletions: (prefix) => (settings.length ? completions(settings, prefix) : null),
		handler: async (args, ctx) => {
			if (commandRun) {
				ctx.ui.notify("A research run is already going; /research-stop ends it", "warning");
				return;
			}
			let s: Setting[];
			try {
				s = await ensureSettings();
			} catch (err) {
				ctx.ui.notify((err as Error).message, "error");
				return;
			}
			let parsed;
			try {
				parsed = parseCommandArgs(s, args);
			} catch (err) {
				ctx.ui.notify((err as Error).message, "error");
				return;
			}
			if (!ctx.hasUI) {
				ctx.ui.notify("/research needs the interactive UI; ask the agent to use deep_research instead", "error");
				return;
			}
			const question = parsed.question || (await ctx.ui.input("Research question", "what should the run find out?"))?.trim();
			if (!question) return;
			let values = parsed.values;
			if (Object.keys(values).length === 0 && parsed.domains.length === 0) {
				const chosen = await settingsScreen(ctx, s, values);
				if (!chosen) return;
				values = chosen;
			}
			const abort = new AbortController();
			commandRun = abort;
			const status = throttle(() => {
				if (!latest) return;
				const lines = progressLines(latest, ctx.ui.theme);
				ctx.ui.setWidget("deep-research", lines);
				ctx.ui.setStatus("deep-research", `research: ${latest.state?.phase || latest.stage}`);
			}, 250);
			let latest: ResearchDetails | undefined;
			try {
				const req: Request = { kind: "new", run: { question, values, domains: parsed.domains }, review: true };
				const { result, bridge } = await run(ctx, req, undefined, abort.signal, {
					update: (d) => {
						latest = d;
						status.call();
					},
					ask: (qs) => askQuestions(ctx, qs, abort.signal),
					review: (plan) => reviewPlan(ctx, plan, abort.signal),
				});
				pi.sendMessage(
					{ customType: "deep-research", content: result.text, display: true, details: result.details },
					{ triggerTurn: result.details.outcome !== undefined && result.details.outcome.status !== "cancelled" },
				);
				if (bridge?.usage.totalTokens) {
					ctx.ui.notify(`research used ${bridge.usage.totalTokens} tokens ($${bridge.usage.cost.total.toFixed(4)}) on ${bridge.modelName}`, "info");
				}
			} catch (err) {
				ctx.ui.notify(`research failed: ${(err as Error).message}`, "error");
			} finally {
				status.cancel();
				commandRun = undefined;
				ctx.ui.setWidget("deep-research", undefined);
				ctx.ui.setStatus("deep-research", undefined);
			}
		},
	});

	pi.registerCommand("research-stop", {
		description: "Stop the research run /research started; it keeps a checkpoint to resume from",
		handler: async (_args, ctx) => {
			if (!commandRun) {
				ctx.ui.notify("No /research run is going (a run the agent started stops with Esc)", "info");
				return;
			}
			commandRun.abort();
		},
	});

	pi.registerCommand("research-model", {
		description: "Choose the model research runs use: Pi's current model, another of Pi's models, or drsh's own",
		handler: async (_args, ctx) => {
			const current = "Pi's current model (follows /model)";
			const own = "drsh's own configured model (drsh setup)";
			const models = ctx.modelRegistry.getAvailable().map((m) => `${m.provider}/${m.id}`);
			const pick = await ctx.ui.select("Model for research runs", [current, own, ...models]);
			if (pick === undefined) return;
			if (pick === current) choice = { kind: "pi" };
			else if (pick === own) choice = { kind: "drsh" };
			else {
				const [provider, ...id] = pick.split("/");
				choice = { kind: "model", provider, id: id.join("/") };
			}
			pi.appendEntry(modelEntry, choice);
			ctx.ui.notify(`Research runs use ${pick}`, "info");
		},
	});

	pi.registerCommand("research-doctor", {
		description: "Check what a research run depends on: the model (through Pi), search and the scraper",
		handler: async (_args, ctx) => {
			let model: Model<Api> | undefined;
			try {
				model = resolveModel(ctx).model;
			} catch (err) {
				ctx.ui.notify((err as Error).message, "error");
				return;
			}
			ctx.ui.setStatus("deep-research", "checking drsh…");
			try {
				const { value: r } = await withEndpoint(ctx, model, undefined, (env) => runPlain(bin, ["doctor"], { env }));
				const verdict = r.code === 0 ? "drsh doctor: ready" : "drsh doctor: something is broken (✗ lines)";
				pi.sendMessage({ customType: "deep-research-doctor", content: `${verdict}\n\n${(r.stdout + r.stderr).trim()}`, display: true });
			} catch (err) {
				const e = err as NodeJS.ErrnoException;
				ctx.ui.notify(e.code === "ENOENT" ? `drsh was not found.\n${installHelp}` : e.message, "error");
			} finally {
				ctx.ui.setStatus("deep-research", undefined);
			}
		},
	});
}

// plainTheme renders the progress as plain text for the model-facing partial
// content, which carries no colour.
const plainTheme = {
	fg: (_c: string, t: string) => t,
	bold: (t: string) => t,
	italic: (t: string) => t,
} as unknown as Theme;
