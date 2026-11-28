// How a research call looks in Pi's transcript: the run's progress while it
// goes (phase with a spinner and its clock, one line per sub-agent, counts,
// the latest status), then what it found. As in drsh's own live frame, the
// streaming reasoning is not shown: its latest words, replaced as they
// streamed, were too hard to read and follow.

import { homedir } from "node:os";
import { basename, dirname, sep } from "node:path";
import type { Theme } from "@earendil-works/pi-coding-agent";
import type { RunState, SubAgent } from "./events.ts";
import type { Digest } from "./result.ts";

export interface OutcomeDetails {
	status: string;
	detail?: string;
	artifacts: string[];
	checkpoint?: string;
	digest?: Digest;
}

// ResearchDetails is a research call's tool-result details: plain JSON, the
// state the view is drawn from.
export interface ResearchDetails {
	question: string;
	model?: string;
	stage: "clarify" | "plan" | "run" | "done";
	// started is when the stage began, in ms since the epoch: the clock
	// beside the spinner counts from it. Unset, nothing is working (the
	// user is answering or reviewing): no spinner and no clock.
	started?: number;
	state?: RunState;
	outcome?: OutcomeDetails;
	// usage is what a /research run spent, shown on its card: Pi counts a
	// tool call's usage itself, and the run's, sent as a message, nowhere.
	usage?: { tokens: number; cost: number };
}

// spinnerFrames and tickMs are drsh's (spinnerFrames, tickInterval in
// internal/ui/render.go): the frame is the time over tickMs, so every view
// drawn at the same moment shows the same one.
const spinnerFrames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
export const tickMs = 120;

function spinner(now: number, t: Theme): string {
	return t.fg("accent", spinnerFrames[Math.floor(now / tickMs) % spinnerFrames.length]);
}

// clock is a duration as drsh writes it (formatDuration): m:ss, and h:mm:ss
// past an hour.
export function clock(ms: number): string {
	const total = Math.max(0, Math.floor(ms / 1000));
	const h = Math.floor(total / 3600);
	const m = Math.floor((total % 3600) / 60);
	const s = String(total % 60).padStart(2, "0");
	return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

// count abbreviates a large count: 10063 is 10.1K, 1500000 is 1.5M, and
// below a thousand the number as is. One decimal, as drsh's formatCount,
// dropped when it is .0 (1K, not 1.0K).
export function count(n: number): string {
	// From 999,950 the thousands round to 1000.0: that is 1M, not 1000K.
	const [div, unit] = n >= 999_950 ? [1e6, "M"] : n >= 1_000 ? [1e3, "K"] : [1, ""];
	if (!unit) return String(n);
	return `${(n / div).toFixed(1).replace(/\.0$/, "")}${unit}`;
}

function glyph(a: SubAgent, t: Theme, now: number): string {
	switch (a.state) {
		case "done":
			return t.fg("success", "●");
		case "error":
			return t.fg("error", "✗");
		case "running":
			return spinner(now, t);
		default:
			return t.fg("dim", "○");
	}
}

function oneLine(s: string, max: number): string {
	const flat = s.replace(/\s+/g, " ").trim();
	return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat;
}

const stageLabel: Record<ResearchDetails["stage"], string> = {
	clarify: "Asking what the question leaves open",
	plan: "Planning",
	run: "Starting",
	done: "Done",
};

// statusWords names a stage in Pi's footer, in one case throughout: the
// footer showed drsh's internal stage IDs ("clarify") beside its display
// names ("Fact-Check"), and "research: Research" said nothing.
const statusWords: Record<string, string> = {
	clarify: "Clarifying",
	plan: "Planning",
	run: "Starting",
	done: "Done",
	Plan: "Planning",
	Research: "Searching",
	Analyze: "Analyzing",
	"Fact-Check": "Fact-checking",
	Summarize: "Writing the report",
	Answer: "Answering",
	Report: "Writing the report",
};

// statusLabel is the footer's word for where a run is: the stage before the
// run, then drsh's phase, a phase this does not know shown as drsh names it.
export function statusLabel(d: ResearchDetails): string {
	const key = (d.stage === "run" && d.state?.phase) || d.stage;
	return statusWords[key] ?? key;
}

// isFollowUp is a sub-agent of the follow-up round: drsh numbers the planned
// sub-topics and opens a follow-up's ID with "f" (followUpPrefix in
// internal/ui/driver.go), which is how its own tree tells the two apart.
function isFollowUp(a: SubAgent): boolean {
	return a.id.startsWith("f");
}

const finished = (a: SubAgent) => a.state === "done" || a.state === "error";

// troubled is a sub-agent that failed, or ended with errors: its row stays
// when the rest fold into one line.
const troubled = (a: SubAgent) => a.state === "error" || Boolean(a.errors);

// agentLines is one line per sub-agent while the search goes. Once it is
// over and the run has moved on, the finished rows are one line: four green
// rows at 100% through the analysis and the fact-check repeated what that
// line says, and pushed the phase's own lines down. A failed one is kept.
function agentLines(s: RunState, t: Theme, now: number): string[] {
	const agents = s.subagents;
	if (agents.length && s.phase !== "Research" && agents.every(finished)) {
		const follow = agents.filter(isFollowUp).length;
		const split = follow ? ` (${agents.length - follow} planned, ${follow} follow-up${follow === 1 ? "" : "s"})` : "";
		const bad = agents.filter(troubled).length;
		const issues = bad ? `, ${bad} with errors` : "";
		const lines = [`  ${t.fg(bad ? "warning" : "success", "●")} ${t.fg("muted", `${agents.length} sub-topic${agents.length === 1 ? "" : "s"} searched${split}${issues}`)}`];
		for (const a of agents.filter(troubled)) lines.push(agentLine(a, t, now));
		return lines;
	}
	return agents.map((a) => agentLine(a, t, now));
}

function agentLine(a: SubAgent, t: Theme, now: number): string {
	const done = finished(a);
	const pct = a.progress && !done ? t.fg("muted", ` ${a.progress}%`) : "";
	const line = a.line && !done ? t.fg("dim", ` ${oneLine(a.line, 70)}`) : "";
	const tag = isFollowUp(a) ? t.fg("muted", "follow-up · ") : "";
	const errs = a.errors && a.state !== "error" ? t.fg("warning", ` ${a.errors} error${a.errors === 1 ? "" : "s"}`) : "";
	return `  ${glyph(a, t, now)} ${tag}${oneLine(a.name, 50)}${pct}${errs}${line}`;
}

// statusText is the latest transient line as the view shows it. Its
// trailing clock is the call's, beside the stage's in the header: two
// clocks that disagreed. In the fact-check, an analyzer call is the repair
// pass revising what the check refused, not the analysis again.
function statusText(s: RunState): string {
	let text = s.status.replace(/ · \d+:\d{2}(?::\d{2})?$/, "");
	if (s.phase === "Fact-Check") text = text.replace(/^analyzing\b/, "revising");
	return text;
}

// progressLines is the live view at now: the spinner's frame and the clock
// are read from it, so a view redrawn with no new event still moves. What is
// happening now (the status, an error) comes right under the header, before
// the list of sub-agents that can run long.
export function progressLines(d: ResearchDetails, t: Theme, now = Date.now()): string[] {
	const s = d.state;
	const lines: string[] = [];
	const phase = s?.phase || stageLabel[d.stage];
	const time = d.started ? t.fg("muted", ` · ${clock(now - d.started)}`) : "";
	const spin = d.started ? `${spinner(now, t)} ` : "";
	lines.push(`${spin}${t.fg("accent", phase)}${s?.phaseDetail ? t.fg("muted", ` · ${s.phaseDetail}`) : ""}${time}`);
	if (!s) {
		return lines;
	}
	const status = statusText(s);
	if (status) {
		lines.push(t.fg("dim", `  ${oneLine(status, 110)}`));
	}
	for (const e of s.errors.slice(-2)) {
		lines.push(t.fg("error", `! ${oneLine(e, 110)}`));
	}
	for (const c of s.domains) {
		const ok = c.status === "ok";
		lines.push(`  ${ok ? t.fg("success", "✓") : t.fg("warning", "!")} ${c.domain} ${t.fg("dim", c.status + (c.detail ? `: ${oneLine(c.detail, 60)}` : ""))}`);
	}
	lines.push(...agentLines(s, t, now));
	// Before the run, nothing is searched: "0 searches · 0 pages · 0
	// sources" under the clarifier and the planner was noise.
	const counts = d.stage === "run" || d.stage === "done" ? [`${s.searches} searches`, `${s.reads} pages`, `${s.sources} sources`] : [];
	if (s.tokens) counts.push(`${count(s.tokens)} tokens`);
	if (counts.length) lines.push(t.fg("muted", counts.join(" · ")));
	// A view saved before notes were kept has none.
	for (const c of s.checked ?? []) {
		lines.push(t.fg("muted", `fact-check: ${oneLine(c, 110)}`));
	}
	return lines;
}

// costText is a run's cost beside its tokens: none when the estimate is
// zero (a local model, or one Pi has no price for), where "$0.0000" read
// as a figure, and a bound when it rounds to zero.
export function costText(cost: number): string {
	if (!(cost > 0)) return "";
	if (cost < 0.0001) return " (<$0.0001)";
	return ` ($${cost.toFixed(cost < 0.01 ? 4 : 2)})`;
}

function tilde(p: string): string {
	const home = homedir();
	return home && p.startsWith(home + sep) ? `~${p.slice(home.length)}` : p;
}

// filesLines names a run's files. The paths share a long directory and a
// long name and differ in the extension: three of them in full filled three
// lines. Collapsed, the directory is named once and the files after it;
// expanded, each path in full, to copy.
export function filesLines(paths: string[], expanded: boolean, t: Theme): string[] {
	if (!paths.length) return [];
	if (expanded) return paths.map((p) => t.fg("dim", p));
	const dir = dirname(paths[0]);
	if (!paths.every((p) => dirname(p) === dir)) return paths.map((p) => t.fg("dim", p));
	const names = paths.map((p) => basename(p));
	const md = names.find((n) => n.endsWith(".md"));
	const stem = md ? md.slice(0, -".md".length) : "";
	const files =
		stem && names.every((n) => n.startsWith(`${stem}.`))
			? `${stem} ${names.map((n) => n.slice(stem.length)).join(" ")}`
			: names.join("  ");
	return [t.fg("dim", `files in ${tilde(dir)}${sep}`), t.fg("dim", `  ${files}`)];
}

function header(o: OutcomeDetails, model: string | undefined, t: Theme): string {
	const mark =
		o.status === "complete"
			? t.fg("success", "✓")
			: o.status === "incomplete"
				? t.fg("warning", "◑")
				: o.status === "cancelled"
					? t.fg("muted", "⊘")
					: t.fg("error", "✗");
	const parts = [o.status];
	if (o.digest?.factChecked && o.digest.confidence) parts.push(`confidence ${o.digest.confidence}`);
	if (o.digest && !o.digest.factChecked) parts.push("not fact-checked");
	if (model) parts.push(model);
	return `${mark} ${t.fg("toolTitle", parts.join(" · "))}`;
}

export function outcomeLines(d: ResearchDetails, expanded: boolean, t: Theme): string[] {
	const o = d.outcome;
	if (!o) {
		return [];
	}
	const lines = [header(o, d.model, t)];
	if (o.detail) {
		lines.push(t.fg(o.status === "complete" ? "muted" : "warning", oneLine(o.detail, expanded ? 2000 : 160)));
	}
	const g = o.digest;
	if (g?.factChecked) {
		const counts = Object.entries(g.verdicts).map(([k, n]) => `${n} ${k}`);
		// The claims as the run concluded them: the fact-check's verdicts,
		// the quote check after it, and the program's arithmetic.
		lines.push(t.fg("muted", `claims: ${counts.join(", ") || "none checked"} · ${g.sources} sources, ${g.cited} cited`));
		const shown = expanded ? g.unconfirmed : g.unconfirmed.slice(0, 3);
		for (const u of shown) {
			lines.push(`  ${t.fg("warning", u.status)} ${oneLine(u.claim, expanded ? 400 : 100)}`);
		}
		if (shown.length < g.unconfirmed.length) {
			lines.push(t.fg("dim", `  … ${g.unconfirmed.length - shown.length} more unconfirmed`));
		}
		for (const b of g.blocked) {
			lines.push(`  ${t.fg("error", `blocked ${b.kind}`)} ${oneLine(b.text, expanded ? 400 : 100)}`);
		}
	}
	if (o.checkpoint) {
		// The card is all a /research run shows after a stop (no agent turn
		// follows one): it says how to go on, as the text did.
		lines.push(t.fg("muted", `checkpoint: ${o.checkpoint}`));
		lines.push(t.fg("muted", "/research-resume continues it without repeating finished searches"));
	}
	if (d.usage?.tokens) {
		lines.push(t.fg("muted", `used ${count(d.usage.tokens)} tokens${costText(d.usage.cost)}`));
	}
	lines.push(...filesLines(o.artifacts, expanded, t));
	return lines;
}
