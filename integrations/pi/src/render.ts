// How a research call looks in Pi's transcript: the run's progress while it
// goes (phase with a spinner and its clock, one line per sub-agent, counts,
// the latest status), then what it found. As in drsh's own live frame, the
// streaming reasoning is not shown: its latest words, replaced as they
// streamed, were too hard to read and follow.

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

// progressLines is the live view at now: the spinner's frame and the clock
// are read from it, so a view redrawn with no new event still moves.
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
	for (const c of s.domains) {
		const ok = c.status === "ok";
		lines.push(`  ${ok ? t.fg("success", "✓") : t.fg("warning", "!")} ${c.domain} ${t.fg("dim", c.status + (c.detail ? `: ${oneLine(c.detail, 60)}` : ""))}`);
	}
	for (const a of s.subagents) {
		const pct = a.progress ? t.fg("muted", ` ${a.progress}%`) : "";
		const line = a.line && a.state !== "done" ? t.fg("dim", ` ${oneLine(a.line, 70)}`) : "";
		lines.push(`  ${glyph(a, t, now)} ${oneLine(a.name, 50)}${pct}${line}`);
	}
	const counts = [`${s.searches} searches`, `${s.reads} pages`, `${s.sources} sources`];
	if (s.tokens) counts.push(`${count(s.tokens)} tokens`);
	lines.push(t.fg("muted", counts.join(" · ")));
	if (s.status) {
		lines.push(t.fg("dim", oneLine(s.status, 110)));
	}
	for (const e of s.errors.slice(-2)) {
		lines.push(t.fg("error", `! ${oneLine(e, 110)}`));
	}
	return lines;
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
		lines.push(t.fg("muted", `fact-check: ${counts.join(", ") || "no claims checked"} · ${g.sources} sources, ${g.cited} cited`));
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
		lines.push(t.fg("muted", `checkpoint: ${o.checkpoint}`));
	}
	const md = o.artifacts.find((p) => p.endsWith(".md"));
	if (md && !expanded) {
		lines.push(t.fg("dim", md));
	} else {
		for (const p of expanded ? o.artifacts : []) lines.push(t.fg("dim", p));
	}
	return lines;
}
