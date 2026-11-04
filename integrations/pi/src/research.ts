// One research call, stage by stage: the clarifying questions, the plan
// review when asked for, the run, then what it wrote. Each stage is its own
// drsh process on the same endpoint; drsh decides whether there is anything
// to ask (clarify off, instant), so this knows no setting by name.

import { existsSync } from "node:fs";
import { readdir, readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { newRunArgs, planRunArgs, resumeArgs, type Answer, type NewRun } from "./args.ts";
import { initialState, reduce, type Question } from "./events.ts";
import type { ResearchDetails } from "./render.ts";
import { content, digest, type Digest, type Outcome } from "./result.ts";
import { runJsonl, type JsonlResult } from "./runner.ts";
import type { Setting } from "./settings.ts";

export type Request = { kind: "new"; run: NewRun; review: boolean } | { kind: "resume"; checkpoint: string };

export interface Hooks {
	update(d: ResearchDetails): void;
	// ask puts the clarifying questions to the user; absent with no UI, when
	// they go back to the model instead.
	ask?(qs: Question[]): Promise<Answer[] | "stop">;
	// review shows the plan for editing; absent unless a review was asked for.
	review?(plan: unknown): Promise<unknown | "stop">;
}

export interface Call {
	bin: string;
	settings: Setting[];
	env: Record<string, string>;
	signal?: AbortSignal;
	reports: string;
	model?: string;
	req: Request;
	hooks: Hooks;
}

// RunRecord is a finished run, kept for research_ask and resume.
export interface RunRecord {
	question: string;
	json?: string;
	md?: string;
	checkpoint?: string;
	status: string;
}

export interface Result {
	text: string;
	details: ResearchDetails;
	isError: boolean;
	record?: RunRecord;
}

function questionOf(req: Request): string {
	return req.kind === "new" ? req.run.question : `resume ${req.checkpoint}`;
}

// stage runs one drsh process, folding its events into the view.
async function stage(c: Call, details: ResearchDetails, args: string[]): Promise<JsonlResult> {
	return runJsonl(c.bin, args, {
		env: c.env,
		signal: c.signal,
		onEvent: (e) => {
			details.state = reduce(details.state ?? initialState(), e);
			c.hooks.update(details);
		},
	});
}

function stopped(details: ResearchDetails, why: string): Result {
	details.stage = "done";
	details.outcome = { status: "cancelled", detail: why, artifacts: [] };
	return { text: `deep-research run cancelled: ${why}. Nothing more was spent.`, details, isError: false };
}

// failedStage is a stage that ended without the output it exists for.
function failedStage(details: ResearchDetails, r: JsonlResult, what: string): Result | undefined {
	if (r.aborted && r.done?.status !== "failed") {
		return stopped(details, `stopped while ${what}`);
	}
	if (r.done?.status === "complete") {
		return undefined;
	}
	const detail = r.done?.detail || `drsh ended without finishing while ${what}${r.stderr ? `: ${r.stderr.trim().slice(-1500)}` : ""}`;
	details.stage = "done";
	details.outcome = { status: "failed", detail, artifacts: [] };
	return { text: `deep-research run failed while ${what}: ${detail}`, details, isError: true };
}

async function clarify(c: Call, run: NewRun, details: ResearchDetails): Promise<Result | undefined> {
	if (run.answers) {
		return undefined;
	}
	details.stage = "clarify";
	c.hooks.update(details);
	const r = await stage(c, details, newRunArgs(c.settings, run, c.reports, "clarify"));
	const failed = failedStage(details, r, "asking the clarifying questions");
	if (failed) return failed;
	const qs = r.questions ?? [];
	if (qs.length === 0) {
		return undefined;
	}
	if (!c.hooks.ask) {
		details.stage = "clarify";
		const list = qs.map((q, i) => `${i + 1}. ${q.text}${q.options?.length ? ` (for example: ${q.options.join(" / ")})` : ""}`);
		return {
			text: [
				"The question leaves open what to research. Nothing was searched yet.",
				"Ask the user these questions, then call deep_research again with the same parameters and answers set to [{question, answer}] (an answer may be left out):",
				...list,
			].join("\n"),
			details,
			isError: false,
		};
	}
	const answers = await c.hooks.ask(qs);
	if (answers === "stop") {
		return stopped(details, "the clarifying questions were dismissed");
	}
	run.answers = { questions: qs, answers };
	return undefined;
}

// planned is the command line for the run itself: as asked, or the plan the
// user reviewed and maybe edited.
async function planned(c: Call, run: NewRun, review: boolean, details: ResearchDetails): Promise<string[] | Result> {
	if (!review || !c.hooks.review) {
		return newRunArgs(c.settings, run, c.reports, "run");
	}
	details.stage = "plan";
	c.hooks.update(details);
	const r = await stage(c, details, newRunArgs(c.settings, run, c.reports, "plan"));
	const failed = failedStage(details, r, "planning");
	if (failed) return failed;
	const plan = await c.hooks.review(r.plan);
	if (plan === "stop") {
		return stopped(details, "the plan was not confirmed");
	}
	const file = join(c.reports, "plan.json");
	await writeFile(file, JSON.stringify(plan, null, 2));
	return planRunArgs(file, c.reports, run.trace);
}

async function readJSON(path: string | undefined): Promise<unknown> {
	if (!path || !existsSync(path)) return undefined;
	try {
		return JSON.parse(await readFile(path, "utf8"));
	} catch {
		return undefined;
	}
}

// checkpointLeft is the checkpoint a run that ended with no done line left
// on disk. drsh saves it after each step, as <reports>/*.partial.json, and
// names it only in the done line, which a killed run never writes: on
// Windows, where stopping a child process kills it outright, that is every
// stopped run. Each run has a reports directory of its own, so a checkpoint
// there is this run's; a resumed run saves to the checkpoint it resumed. A
// file that is not one drsh can resume from (no question or no plan, as
// replay.Load refuses) is not offered.
async function checkpointLeft(c: Call): Promise<string | undefined> {
	let candidates: string[];
	if (c.req.kind === "resume") {
		candidates = [c.req.checkpoint];
	} else {
		const names = await readdir(c.reports).catch(() => [] as string[]);
		candidates = names.filter((n) => n.endsWith(".partial.json")).map((n) => join(c.reports, n));
	}
	const valid: { path: string; mtime: number }[] = [];
	for (const path of candidates) {
		// A file can go between the read and the stat (a cleanup, an
		// antivirus on Windows): it is then not one to offer.
		const mtime = await stat(path).then((st) => st.mtimeMs, () => undefined);
		if (mtime !== undefined && resumable(await readJSON(path))) {
			valid.push({ path, mtime });
		}
	}
	return valid.sort((a, b) => b.mtime - a.mtime)[0]?.path;
}

function resumable(raw: unknown): boolean {
	const t = raw as { question?: unknown; plan?: unknown } | null | undefined;
	return typeof t?.question === "string" && t.question.trim() !== "" && Array.isArray(t.plan) && t.plan.length > 0;
}

// outcome reads what a run's done line says it wrote.
async function outcome(c: Call, r: JsonlResult): Promise<{ o: Outcome; isError: boolean }> {
	const model = c.model;
	if (!r.done) {
		const status = r.aborted ? "cancelled" : "failed";
		const detail = r.aborted
			? "the run was stopped before it could end cleanly"
			: `drsh exited (code ${r.code}) without a done line, so it was killed or crashed${r.stderr ? `: ${r.stderr.trim().slice(-1500)}` : ""}`;
		const checkpoint = await checkpointLeft(c);
		return { o: { status, detail, artifacts: [], model, checkpoint }, isError: !r.aborted };
	}
	const artifacts = r.done.artifacts ?? [];
	const md = artifacts.find((p) => p.endsWith(".md"));
	const json = artifacts.find((p) => p.endsWith(".json") && !p.endsWith(".trace.json"));
	const o: Outcome = {
		status: r.done.status ?? "failed",
		detail: r.done.detail,
		model,
		artifacts,
		checkpoint: r.done.checkpoint,
		reportPath: md,
		jsonPath: json,
	};
	if (md && existsSync(md)) o.report = await readFile(md, "utf8");
	const raw = await readJSON(json);
	if (raw) o.digest = digest(raw);
	const written = Boolean(o.report && o.digest);
	if (o.status === "complete" && !written) {
		// An export failure only warns; a complete run with no report is a
		// failure to the caller all the same.
		o.status = "failed";
		o.detail = `the run finished but its report files were not written${r.stderr ? `: ${r.stderr.trim().slice(-1500)}` : ""}`;
	}
	return { o, isError: o.status === "failed" };
}

function finish(details: ResearchDetails, o: Outcome, question: string): Pick<Result, "details" | "record"> {
	details.stage = "done";
	const d: Digest | undefined = o.digest;
	details.outcome = { status: o.status, detail: o.detail, artifacts: o.artifacts, checkpoint: o.checkpoint, digest: d };
	return {
		details,
		record: { question, json: o.jsonPath, md: o.reportPath, checkpoint: o.checkpoint, status: o.status },
	};
}

export async function research(c: Call): Promise<Result> {
	const details: ResearchDetails = { question: questionOf(c.req), model: c.model, stage: "run" };
	let args: string[];
	if (c.req.kind === "resume") {
		args = resumeArgs(c.req.checkpoint, c.reports);
	} else {
		const asked = await clarify(c, c.req.run, details);
		if (asked) return asked;
		const p = await planned(c, c.req.run, c.req.review, details);
		if (!Array.isArray(p)) return p;
		args = p;
	}
	details.stage = "run";
	details.state = initialState();
	c.hooks.update(details);
	const r = await stage(c, details, args);
	const { o, isError } = await outcome(c, r);
	return { text: content(o), isError, ...finish(details, o, details.question) };
}
