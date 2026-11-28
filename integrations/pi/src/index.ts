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
import { Box, Text } from "@earendil-works/pi-tui";
import { Type, type TSchema } from "typebox";
import { checkAnswers, clip, maxAnswerChars, maxAnswers, type Answer } from "./args.ts";
import { startBridge, type Bridge } from "./bridge.ts";
import type { Question } from "./events.ts";
import { planLines, subTopicNames, tooMany, withSubTopics, type Plan } from "./plan.ts";
import { outcomeLines, progressLines, type ResearchDetails, statusLabel, tickMs } from "./render.ts";
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
	step,
	valueLabel,
	type Setting,
} from "./settings.ts";
import { type MenuResult, pick as pickWithNotice, SettingsMenu, startLabel } from "./menu.ts";

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

// ticker draws the live view every ms while live() holds, not only when
// drsh sends an event: a model call is a minute with no events, and the
// spinner and clock must keep moving through it. While the view is still
// (the user answering or reviewing) it draws on each update only, so a
// tool call does not send the same view ten times a second. cancel stops
// it: one firing after the run ended would redraw progress the run already
// cleared.
function ticker(f: () => void, ms: number, live: () => boolean): { call(): void; cancel(): void } {
	let timer: NodeJS.Timeout | undefined;
	return {
		call() {
			if (!timer) {
				timer = setInterval(() => live() && f(), ms);
				f();
			} else if (!live()) {
				f();
			}
		},
		cancel() {
			if (timer) clearInterval(timer);
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

// showLines puts lines in a widget above the editor. Pi cuts a widget given
// as lines at ten, and a run's view (the sub-agents of a deep plan and its
// follow-ups, the counts, the fact-check's notes) runs past that, losing
// its last lines; the plan of a review the same. In the terminal they go in
// as a component, which is not cut; elsewhere as lines.
function showLines(ctx: ExtensionContext, key: string, lines: string[] | undefined): void {
	if (lines === undefined || ctx.mode !== "tui") {
		ctx.ui.setWidget(key, lines);
		return;
	}
	const text = lines.join("\n");
	ctx.ui.setWidget(key, () => new Text(text, 1, 0));
}

export default function deepResearch(pi: ExtensionAPI) {
	const bin = process.env.DRSH_BIN || "drsh";
	let settings: Setting[] = [];
	let loadError = "";
	let choice: ModelChoice = { kind: "pi" };
	// lastReport is the last run that wrote a .json, what research_ask reads
	// by default: a run stopped or failed after it leaves none, and must not
	// hide the one before.
	let lastReport: RunRecord | undefined;
	// lastRun is the last run of any outcome: /research-resume continues it
	// when it left a checkpoint.
	let lastRun: RunRecord | undefined;
	let commandRun: AbortController | undefined;
	// commandEnded settles when commandRun's run has wound down.
	let commandEnded: Promise<void> = Promise.resolve();
	// session counts session starts. A run stopped by a session switch ends
	// after the next session has started; recorded there, it became that
	// session's last run, the one /research-resume picks.
	let session = 0;

	async function loadSettings(): Promise<Setting[]> {
		try {
			const r = await runPlain(bin, ["settings"]);
			if (r.code !== 0) {
				const old = /unknown command|unknown flag/i.test(r.stderr);
				throw new Error(old ? `drsh is too old for this extension: run \`drsh update\`. (${r.stderr.trim()})` : r.stderr.trim() || `drsh settings exited ${r.code}`);
			}
			settings = parseSettings(r.stdout);
			loadError = "";
			// Wherever drsh first answers (the session's start, /research,
			// /research-doctor, the stand-in), the real tools replace the
			// stand-in: drsh installed mid-session is usable from then on.
			registerTools(settings);
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
		if (rec.json) lastReport = rec;
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
			if (answer.trim()) out.push({ question: q.text, answer: clip(answer) });
		}
		return out;
	}

	// reviewPlan shows the plan's sub-topics as a list, with a menu to run it,
	// edit the sub-topics as plain text (one per line), or stop. Esc stops.
	async function reviewPlan(ctx: ExtensionContext, raw: unknown, signal?: AbortSignal): Promise<unknown | "stop"> {
		const run = "▶ Run this plan";
		const edit = "Edit sub-topics";
		const stop = "Cancel";
		let plan = (raw ?? {}) as Plan;
		const planned = subTopicNames(plan).length;
		const max = settings.find((x) => x.key === "topics")?.max;
		// notice is why the last edit was not taken, shown under the options.
		let notice: string | undefined;
		try {
			for (;;) {
				showLines(ctx, "deep-research-plan", planLines(plan, planned));
				const pick = await pickWithNotice(ctx, "Review the plan", [run, edit, stop], { notice, signal });
				notice = undefined;
				if (pick === undefined || pick === stop) return "stop";
				if (pick === run) return plan;
				const text = await untilAborted(
					ctx.ui.editor("Sub-topics, one per line: change, add or delete lines", subTopicNames(plan).join("\n")),
					signal,
				);
				if (text === undefined) continue;
				const edited = withSubTopics(plan, text);
				if (!edited.sub_topics?.length) {
					notice = "A plan needs at least one sub-topic";
					continue;
				}
				notice = tooMany(edited, max);
				if (notice) continue;
				plan = edited;
			}
		} finally {
			showLines(ctx, "deep-research-plan", undefined);
		}
	}

	async function run(
		ctx: ExtensionContext,
		req: Request,
		modelName: string | undefined,
		signal: AbortSignal | undefined,
		hooks: Hooks,
	): Promise<{ result: Result; bridge?: Bridge }> {
		const started = session;
		const s = await ensureSettings();
		const { model, label } = resolveModel(ctx, modelName);
		const reports = await newReportsDir(req.kind === "new" ? req.run.question : "resume");
		const { value, bridge } = await withEndpoint(ctx, model, signal, (env) =>
			research({ bin, settings: s, env, signal, reports, model: label, req, hooks }),
		);
		if (started === session) remember(value.record);
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
				Type.Array(Type.Object({ question: Type.String(), answer: Type.String({ maxLength: maxAnswerChars }) }), {
					maxItems: maxAnswers,
					description: `The user's answers to the clarifying questions an earlier call returned, at most ${maxAnswers}, each at most ${maxAnswerChars} characters. Given, no questions are asked.`,
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
		if (answers) checkAnswers(answers);
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
				const push = ticker(() => {
					if (latest) onUpdate?.({ content: [{ type: "text", text: progressLines(latest, plainTheme).join("\n") }], details: latest });
				}, tickMs, () => latest?.started !== undefined);
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
				const runFile = params.run || lastReport?.json;
				if (!runFile) {
					throw new Error("no research run in this session has written a report yet: pass run with a run's .json path, or run deep_research first");
				}
				const { model, label } = resolveModel(ctx, params.model);
				const { value: r, bridge } = await withEndpoint(ctx, model, signal, (env) =>
					runPlain(bin, ["ask", runFile, "-p", params.question], { env, signal }),
				);
				if (r.aborted) {
					throw new Error("research_ask was cancelled");
				}
				if (r.code !== 0) {
					throw new Error(`drsh ask failed: ${(r.stderr || r.stdout).trim().slice(-2000) || `exit ${r.code}`}`);
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
				await loadSettings();
				throw new Error("drsh is available now; call deep_research again");
			},
		});
	}

	// restoreBranch reads the model chosen and the last run and report from
	// the branch now in effect: entries on another branch are not this
	// conversation's.
	function restoreBranch(ctx: ExtensionContext): void {
		choice = { kind: "pi" };
		lastReport = undefined;
		lastRun = undefined;
		for (const e of ctx.sessionManager.getBranch()) {
			if (e.type !== "custom") continue;
			if (e.customType === modelEntry && e.data) choice = e.data as ModelChoice;
			if (e.customType === runEntry && e.data) lastRun = e.data as RunRecord;
			if (e.customType === runEntry && (e.data as RunRecord | undefined)?.json) lastReport = e.data as RunRecord;
		}
	}

	// A /research run's message is drawn as its card: what it found,
	// collapsed to the confidence, the claims the fact-check did not confirm
	// and where the files are. Drawn as its text, it was the model's
	// instructions and the whole report, which the agent's answer then said
	// again. The text still goes to the model as it was.
	pi.registerMessageRenderer<ResearchDetails>("deep-research", (message, { expanded, keyHint }, theme) => {
		const d = message.details;
		if (!d?.outcome) return undefined;
		const lines = [theme.fg("toolTitle", theme.bold("deep research ")) + theme.fg("accent", d.question), ...outcomeLines(d, expanded, theme)];
		lines.push(keyHint("app.tools.expand", expanded ? "to collapse" : "to expand"));
		const box = new Box(1, 1, (t) => theme.bg("customMessageBg", t));
		box.addChild(new Text(lines.join("\n"), 0, 0));
		return box;
	});

	pi.on("session_start", async (_event, ctx) => {
		session++;
		restoreBranch(ctx);
		try {
			await loadSettings();
		} catch {
			registerStandIn();
		}
	});

	// /tree moves to another branch of the same session file: the branch
	// left behind kept its report, run and model in force, so research_ask
	// and /research-resume went on with a run that branch never had.
	pi.on("session_tree", async (_event, ctx) => {
		restoreBranch(ctx);
	});

	pi.on("session_shutdown", async () => {
		commandRun?.abort();
	});

	// settingsScreen is the start screen: each setting at its value, Start
	// first.
	// ←/→ step a row through its choices in place; Enter opens its full
	// option list, and the cursor comes back to that row. A row the depth
	// holds (instant) is not changed under it, as drsh's own /mode refuses:
	// the depth wins, so a value chosen there would do nothing for this run
	// and come into force unseen if the depth were changed after.
	async function settingsScreen(ctx: ExtensionContext, s: Setting[], values: Record<string, string>): Promise<Record<string, string> | undefined> {
		let selected = 0;
		const rows = () => {
			const eff = effective(s, values);
			return s.map((x) => `${x.label}: ${valueLabel(x, eff[x.key])}`);
		};
		// heldWhy is why x cannot change now, or undefined when it can.
		const heldWhy = (x: Setting) => {
			if (implied(s, values)[x.key] === undefined) return undefined;
			const by = s.find((y) => y.choices.find((o) => o.value === (values[y.key] ?? y.value))?.implies?.[x.key] !== undefined);
			const name = by?.label ?? "depth";
			const depth = by ? `${valueLabel(by, values[by.key] ?? by.value)} ${name}` : "the depth";
			return `${depth} holds every option at its least: pick another ${name} to change ${x.label}`;
		};
		// describe is the row's help and the note on its value ("≤3").
		const describe = (row: number) => {
			const x = s[row];
			const v = effective(s, values)[x.key];
			const note = x.choices.find((o) => o.value === v)?.note;
			return note ? `${x.help} (${valueLabel(x, v)}: ${note})` : x.help;
		};
		// The menu checks heldWhy before stepping; this is the select's guard.
		const stepRow = (row: number, dir: 1 | -1) => {
			const x = s[row];
			if (heldWhy(x) === undefined) values = { ...values, [x.key]: step(x, effective(s, values)[x.key], dir) };
		};
		const title = "deep-research settings";
		// Outside the terminal (Pi's RPC mode) custom components are not
		// drawn and custom() answers undefined: the plain select, no arrows.
		const viaSelect = async (): Promise<MenuResult> => {
			const r = rows();
			const pick = await ctx.ui.select(title, [startLabel, ...r]);
			if (pick === undefined) return { kind: "cancel" };
			return pick === startLabel ? { kind: "start" } : { kind: "edit", row: r.indexOf(pick) };
		};
		const viaMenu = () =>
			ctx.ui.custom<MenuResult | undefined>((_tui, theme, kb, done) => {
				const held = (row: number) => heldWhy(s[row]);
				const menu = new SettingsMenu(theme, kb, { title, rows, step: stepRow, held, describe, selected }, (r) => {
					selected = menu.selectedLine;
					done(r);
				});
				return menu;
			});
		for (;;) {
			const pick = (ctx.mode === "tui" ? await viaMenu() : undefined) ?? (await viaSelect());
			if (pick.kind === "cancel") return undefined;
			if (pick.kind === "start") return values;
			const x = s[pick.row];
			// Only the select lets Enter through on a held row: it has no
			// line of its own to say why.
			const why = heldWhy(x);
			if (why !== undefined) {
				ctx.ui.notify(why, "warning");
				continue;
			}
			const opts = x.choices.map((o) => `${o.label}${o.note ? ` — ${o.note}` : ""}`);
			const v = await ctx.ui.select(`${x.label}: ${x.help}`, opts);
			if (v !== undefined) values = { ...values, [x.key]: x.choices[opts.indexOf(v)].value };
		}
	}

	pi.registerCommand("research", {
		description: "Research a question, with settings and plan review",
		getArgumentCompletions: (prefix) => (settings.length ? completions(settings, prefix) : null),
		handler: (args, ctx) => commandRunWith(ctx, (abort) => researchCommand(args, ctx, abort)),
	});

	pi.registerCommand("research-resume", {
		description: "Resume the last stopped research run, or the checkpoint given",
		handler: (args, ctx) =>
			commandRunWith(ctx, async (abort) => {
				const checkpoint = args.trim() || lastRun?.checkpoint;
				if (checkpoint) return { running: backgroundRun(ctx, abort, { kind: "resume", checkpoint }) };
				// Stopped before its plan: nothing was spent and drsh kept no
				// checkpoint, so it is asked again as it was, settings and
				// answers included, with the plan to review.
				if (lastRun?.request) {
					return { running: backgroundRun(ctx, abort, { kind: "new", run: structuredClone(lastRun.request), review: true }) };
				}
				// Only the last run is resumed: one that ended after a
				// stopped run superseded it.
				const why = lastRun ? `The last research run (${lastRun.status}) left nothing to resume` : "No research run in this session yet";
				ctx.ui.notify(`${why}; /research-resume <checkpoint path> resumes another`, "info");
				return undefined;
			}),
	});

	// commandRunWith runs start holding commandRun, the one /research run
	// /research-stop ends. start awaits its prompts and hands back the run
	// it started, unawaited; undefined when it ended before a run.
	async function commandRunWith(ctx: ExtensionContext, start: (abort: AbortController) => Promise<{ running: Promise<void> } | undefined>): Promise<void> {
		// A stopped run takes a moment to wind down (drsh's exit, the
		// bridge closing): a command typed in it was refused as "already
		// going". Wait for it instead.
		if (commandRun?.signal.aborted) await commandEnded;
		if (commandRun) {
			ctx.ui.notify("A research run is already going; /research-stop ends it", "warning");
			return;
		}
		// Taken before the first await: the settings and the prompts wait
		// on the user, and a second /research in that time passed the
		// check above and started a run of its own; the first to end then
		// cleared this while the other went on, past /research-stop.
		const abort = new AbortController();
		commandRun = abort;
		let running: Promise<void> | undefined;
		try {
			running = (await start(abort))?.running;
		} finally {
			if (!running) commandRun = undefined;
		}
		// The run goes on after the handler returns: Pi runs one submitted
		// command at a time, and a handler that awaited the whole run left
		// /research-stop queued behind it until the run had ended on its own.
		// backgroundRun catches its own failures; the catch is for one in
		// its cleanup, which would otherwise be an unhandled rejection.
		if (running) {
			commandEnded = running
				.catch((err) => ctx.ui.notify(`research failed: ${(err as Error).message}`, "error"))
				.finally(() => {
					if (commandRun === abort) commandRun = undefined;
				});
		}
	}

	// researchCommand is /research once it holds commandRun: the prompts,
	// awaited, then the run, started and handed back unawaited (in an
	// object: await would unwrap a promise returned bare, and wait for the
	// whole run again); undefined when the prompts ended it before a run.
	async function researchCommand(args: string, ctx: ExtensionContext, abort: AbortController): Promise<{ running: Promise<void> } | undefined> {
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
		const question = parsed.question || (await ctx.ui.input("Research question", "what should the run find out?", { signal: abort.signal }))?.trim();
		if (!question) return;
		let values = parsed.values;
		if (Object.keys(values).length === 0 && parsed.domains.length === 0) {
			const chosen = await settingsScreen(ctx, s, values);
			if (!chosen) return;
			values = chosen;
		}
		// /research-stop while the prompts were open.
		if (abort.signal.aborted) return;
		const req: Request = { kind: "new", run: { question, values, domains: parsed.domains }, review: true };
		return { running: backgroundRun(ctx, abort, req) };
	}

	// backgroundRun is a /research run from its clarifying questions on (or
	// a resumed one), with its live view; every failure ends in a notice, so
	// the promise never rejects.
	async function backgroundRun(ctx: ExtensionContext, abort: AbortController, req: Request): Promise<void> {
		const status = ticker(() => {
			if (!latest) return;
			showLines(ctx, "deep-research", progressLines(latest, ctx.ui.theme));
			ctx.ui.setStatus("deep-research", `research: ${statusLabel(latest)}`);
		}, tickMs, () => latest?.started !== undefined);
		let latest: ResearchDetails | undefined;
		try {
			const { result, bridge } = await run(ctx, req, undefined, abort.signal, {
				update: (d) => {
					latest = d;
					status.call();
				},
				ask: (qs) => askQuestions(ctx, qs, abort.signal),
				review: (plan) => reviewPlan(ctx, plan, abort.signal),
			});
			// The usage goes on the run's card: a notice of it passed in a
			// moment, apart from the run it was for.
			if (bridge?.usage.totalTokens) {
				result.details.usage = { tokens: bridge.usage.totalTokens, cost: bridge.usage.cost.total };
			}
			pi.sendMessage(
				{ customType: "deep-research", content: result.text, display: true, details: result.details },
				{ triggerTurn: result.details.outcome !== undefined && result.details.outcome.status !== "cancelled" },
			);
		} catch (err) {
			ctx.ui.notify(`research failed: ${(err as Error).message}`, "error");
		} finally {
			status.cancel();
			showLines(ctx, "deep-research", undefined);
			ctx.ui.setStatus("deep-research", undefined);
		}
	}

	pi.registerCommand("research-stop", {
		description: "Stop the /research run, keeping a checkpoint to resume from",
		handler: async (_args, ctx) => {
			if (!commandRun) {
				ctx.ui.notify("No /research run is going (a run the agent started stops with Esc)", "info");
				return;
			}
			commandRun.abort();
		},
	});

	pi.registerCommand("research-model", {
		description: "Choose the model research runs use",
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
		description: "Check the model, search and scraper a run needs",
		handler: async (_args, ctx) => {
			let model: Model<Api> | undefined;
			try {
				model = resolveModel(ctx).model;
			} catch (err) {
				ctx.ui.notify((err as Error).message, "error");
				return;
			}
			ctx.ui.setStatus("deep-research", "checking drsh…");
			// The check a user runs after installing drsh: loading its settings
			// brings in the real tools. A drsh that cannot load them is
			// reported by the doctor run below.
			await ensureSettings().catch(() => undefined);
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
