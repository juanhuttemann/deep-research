// What a finished run tells the model and the view: a digest of the .json
// artifact (skills/deep-research/references/result-json.md) and the rules
// for reporting it, which the skill's section 4 states for an agent driving
// drsh by hand.

export interface Unconfirmed {
	id: string;
	claim: string;
	status: string;
	reason?: string;
}

export interface Blocked {
	kind: "recommendation" | "conclusion";
	text: string;
	failed: string[];
}

export interface Digest {
	question: string;
	depth: string;
	confidence: string;
	// factChecked is false for an instant run, which answers in one call
	// with no fact-check at all.
	factChecked: boolean;
	verdicts: Record<string, number>;
	unconfirmed: Unconfirmed[];
	blocked: Blocked[];
	gaps: string[];
	sources: number;
	cited: number;
	error?: string;
}

interface RunJSON {
	question?: string;
	depth?: string;
	confidence?: string;
	error?: string;
	analysis?: {
		claims?: { id: string; claim: string; status?: string }[];
		recommendations?: Rec[];
		conclusions?: Rec[];
		gaps?: string[] | null;
	};
	fact_check?: {
		verdicts?: { id: string; claim?: string; status: string; reason?: string }[];
	} | null;
	citations?: { cited?: boolean }[];
}

interface Rec {
	choose?: string;
	when?: string;
	statement?: string;
	blocked?: string;
	failed?: string[];
}

function recText(r: Rec): string {
	if (r.statement) return r.statement;
	return [r.choose, r.when && `when ${r.when}`].filter(Boolean).join(" ");
}

export function digest(raw: unknown): Digest {
	const j = (raw ?? {}) as RunJSON;
	const verdicts = j.fact_check?.verdicts ?? [];
	const counts: Record<string, number> = {};
	for (const v of verdicts) {
		counts[v.status] = (counts[v.status] ?? 0) + 1;
	}
	const claims = new Map((j.analysis?.claims ?? []).map((c) => [c.id, c.claim]));
	const unconfirmed = verdicts
		.filter((v) => v.status !== "supported")
		.map((v) => ({ id: v.id, claim: v.claim || claims.get(v.id) || "", status: v.status, reason: v.reason }));
	const blocked: Blocked[] = [];
	for (const [kind, list] of [
		["recommendation", j.analysis?.recommendations],
		["conclusion", j.analysis?.conclusions],
	] as const) {
		for (const r of list ?? []) {
			if (r.blocked) blocked.push({ kind, text: recText(r), failed: r.failed ?? [] });
		}
	}
	const citations = j.citations ?? [];
	return {
		question: j.question ?? "",
		depth: j.depth ?? "",
		confidence: j.confidence ?? "",
		factChecked: j.depth !== "instant" && j.fact_check != null,
		verdicts: counts,
		unconfirmed,
		blocked,
		gaps: j.analysis?.gaps ?? [],
		sources: citations.length,
		cited: citations.filter((c) => c.cited).length,
		error: j.error || undefined,
	};
}

export interface Outcome {
	status: string;
	detail?: string;
	model?: string;
	report?: string;
	reportPath?: string;
	jsonPath?: string;
	artifacts: string[];
	checkpoint?: string;
	digest?: Digest;
}

// maxReport bounds the report sent to the model; the whole of it is on disk.
export const maxReport = 60_000;

const rules = [
	"Answer from this report and keep its citations.",
	"Say the confidence it gives.",
	"Say which claims the fact-check did not confirm, and do not present an unconfirmed claim or a blocked recommendation or conclusion as fact.",
	"Do not add facts the run did not establish; offer research_ask for a question its pages may answer, or a new run.",
	"Give the user the artifact paths.",
];

function digestLines(d: Digest): string[] {
	if (!d.factChecked) {
		return ["NOT FACT-CHECKED: this was an instant run, one search and one answer with no fact-check. Tell the user so."];
	}
	const out = [`Confidence: ${d.confidence || "not stated"}.`];
	const counts = Object.entries(d.verdicts).map(([k, n]) => `${n} ${k}`);
	out.push(`Fact-check: ${counts.length ? counts.join(", ") : "no claims were checked"}.`);
	for (const u of d.unconfirmed) {
		out.push(`- ${u.id} ${u.status}: ${u.claim}${u.reason ? ` (${u.reason})` : ""}`);
	}
	for (const b of d.blocked) {
		out.push(`- BLOCKED ${b.kind}, not established${b.failed.length ? ` (failed: ${b.failed.join(", ")})` : ""}: ${b.text}`);
	}
	if (d.gaps.length) {
		out.push(`Gaps the run could not close: ${d.gaps.join("; ")}`);
	}
	out.push(`Sources: ${d.sources} retrieved, ${d.cited} cited.`);
	return out;
}

function reportText(o: Outcome): string[] {
	if (!o.report) {
		return [];
	}
	if (o.report.length <= maxReport) {
		return ["", "Report:", "", o.report];
	}
	return ["", `Report (first ${maxReport} characters; the whole report is ${o.reportPath}):`, "", o.report.slice(0, maxReport)];
}

function recovery(o: Outcome): string[] {
	if (!o.checkpoint) {
		return o.status === "cancelled" ? ["It stopped before its plan was made, so there is no checkpoint: run it again."] : [];
	}
	const limited = /rate.?limit|challenge/i.test(o.detail ?? "");
	return [
		`Checkpoint: ${o.checkpoint}. Call deep_research with resume set to it to continue without repeating finished searches.` +
			(limited ? " The searches were rate-limited: offer to resume in about ten minutes, not right away." : ""),
	];
}

// content is the tool result's text: what the model reads.
export function content(o: Outcome): string {
	const head = [`deep-research run ${o.status}${o.model ? ` on ${o.model}` : ""}.`];
	if (o.detail) {
		head.push(`Detail: ${o.detail}`);
	}
	const lines = [...head];
	if (o.digest) {
		if (o.status === "incomplete") {
			lines.push("The report is incomplete; say so and why.");
		}
		lines.push(...digestLines(o.digest));
	}
	lines.push(...recovery(o));
	if (o.artifacts.length) {
		lines.push(`Artifacts: ${o.artifacts.join(", ")}`);
	}
	if (o.report) {
		lines.push("", "How to report it:", ...rules.map((r) => `- ${r}`));
	}
	lines.push(...reportText(o));
	return lines.join("\n");
}
