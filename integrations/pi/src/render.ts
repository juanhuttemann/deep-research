// How a research call looks in Pi's transcript: the run's progress while it
// goes (phase, one line per sub-agent, counts, the latest status or
// reasoning), then what it found.

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
	state?: RunState;
	outcome?: OutcomeDetails;
}

function glyph(a: SubAgent, t: Theme): string {
	switch (a.state) {
		case "done":
			return t.fg("success", "●");
		case "error":
			return t.fg("error", "✗");
		case "running":
			return t.fg("accent", "◐");
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

export function progressLines(d: ResearchDetails, t: Theme): string[] {
	const s = d.state;
	const lines: string[] = [];
	const phase = s?.phase || stageLabel[d.stage];
	lines.push(`${t.fg("accent", phase)}${s?.phaseDetail ? t.fg("muted", ` · ${s.phaseDetail}`) : ""}`);
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
		lines.push(`  ${glyph(a, t)} ${oneLine(a.name, 50)}${pct}${line}`);
	}
	const counts = [`${s.searches} searches`, `${s.reads} pages`, `${s.sources} sources`];
	if (s.tokens) counts.push(`${s.tokens} tokens`);
	lines.push(t.fg("muted", counts.join(" · ")));
	if (s.thinking) {
		lines.push(t.fg("dim", t.italic(`… ${oneLine(s.thinking, 110)}`)));
	} else if (s.status) {
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
