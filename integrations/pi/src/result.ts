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

// Figure is a computation the program ran over the claims' values: its
// result when every input passed the fact-check, or why it did not.
export interface Figure {
	id: string;
	label: string;
	value?: string;
	formula?: string;
	reason?: string;
}

export interface Digest {
	question: string;
	// title is the report's headline, absent for a run with none (instant
	// and older runs): the question titles it then.
	title?: string;
	depth: string;
	confidence: string;
	// instant is a run that answers in one call with no fact-check at all.
	instant: boolean;
	// factChecked is false for an instant run, and for any run that ended
	// before its fact-check did (a late phase failed: incomplete).
	factChecked: boolean;
	verdicts: Record<string, number>;
	unconfirmed: Unconfirmed[];
	blocked: Blocked[];
	figures: Figure[];
	gaps: string[];
	sources: number;
	cited: number;
	error?: string;
}

interface RunJSON {
	question?: string;
	title?: string;
	depth?: string;
	confidence?: string;
	error?: string;
	analysis?: {
		claims?: { id: string; claim: string; status?: string; note?: string; computed?: string; qualifier?: string }[];
		computations?: { id: string; label?: string; result?: string; unit?: string; formula?: string; valid?: boolean; reason?: string }[];
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

// decided is each claim as the run concluded it: the claim's own status,
// which drsh sets after the fact-check, its governance included (a quote the
// checker gave that is not in the page makes a supported verdict
// insufficient; a computed claim is decided by the program). The verdicts
// are the checker's answers before that: counted, they called claims drsh
// had made insufficient supported, beside the conclusions they had failed. A claim with no status (an older run) takes its verdict's.
function decided(j: RunJSON): Unconfirmed[] {
	const verdicts = j.fact_check?.verdicts ?? [];
	const byId = new Map(verdicts.map((v) => [v.id, v]));
	const claims = j.analysis?.claims ?? [];
	if (!claims.length) {
		return verdicts.map((v) => ({ id: v.id, claim: v.claim ?? "", status: v.status, reason: v.reason }));
	}
	const out: Unconfirmed[] = [];
	for (const c of claims) {
		const v = byId.get(c.id);
		const status = c.status || v?.status;
		if (status) out.push({ id: c.id, claim: c.claim || v?.claim || "", status, reason: c.note || v?.reason });
	}
	return out;
}

const statusOrder = ["supported", "partial", "disputed", "contradicted", "insufficient", "unsourced"];

export function digest(raw: unknown): Digest {
	const j = (raw ?? {}) as RunJSON;
	const all = decided(j);
	// In one order, supported first, so the count reads the same each run.
	const counts: Record<string, number> = {};
	const rank = (s: string) => (statusOrder.indexOf(s) + statusOrder.length + 1) % (statusOrder.length + 1);
	for (const c of [...all].sort((a, b) => rank(a.status) - rank(b.status))) {
		counts[c.status] = (counts[c.status] ?? 0) + 1;
	}
	const unconfirmed = all.filter((c) => c.status !== "supported");
	const figures = (j.analysis?.computations ?? []).map((m) =>
		m.valid
			? { id: m.id, label: m.label ?? "", value: [m.result, m.unit].filter(Boolean).join(" "), formula: m.formula }
			: { id: m.id, label: m.label ?? "", reason: m.reason || "not computed" },
	);
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
		title: j.title || undefined,
		depth: j.depth ?? "",
		confidence: j.confidence ?? "",
		instant: j.depth === "instant",
		factChecked: j.depth !== "instant" && j.fact_check != null,
		verdicts: counts,
		unconfirmed,
		blocked,
		figures,
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

const followUps = "I can also answer follow-ups from this run's saved files, though those answers aren't fact-checked.";

// closingOffer is what the agent offers after a report, in the Pi user's
// terms: the command for another run, and follow-ups from the saved files,
// which are not checked. The other run is what this one was not: a checked
// one after an instant lookup, a broader one after a quick or standard run.
// It once offered a deep run as the one that "includes a fact-check step",
// after a standard run that had been fact-checked.
export function closingOffer(d?: Digest): string {
	if (d?.instant) {
		return `For a fact-checked answer, I can run a standard research (\`/research --mode standard\`); ${followUps}`;
	}
	if (d?.depth === "deep") {
		return `${followUps.replace(/^I can also/, "I can")} A new question is a new run (\`/research\`).`;
	}
	return `For a broader look, I can run a deep research (\`/research --mode deep\`), which plans more sub-topics and reads more pages; ${followUps}`;
}

function rules(d?: Digest): string[] {
	return [
		"Answer from this report and keep its citations.",
		"Say the confidence it gives.",
		"Say which claims the fact-check did not confirm, and do not present an unconfirmed claim or a blocked recommendation or conclusion as fact.",
		"State a figure only as the report or a COMPUTED line gives it; do no arithmetic of your own.",
		// Said to a Pi user, "a new deep_research" and "via research_ask"
		// name the agent's tools, which the user cannot call: a new run is
		// the /research command, and a follow-up is answered from the run's
		// files, which the agent can read. Told what not to say, the model
		// still offered "a fact-checked version", a check a run can end
		// without; given the sentence, it uses it.
		"Do not add facts the run did not establish.",
		// The paths share a long directory and name: listed in full, the
		// three of them took three wrapped lines of the answer.
		"Give the user the folder the run's files are in, once, then the file names (the .md report first), not each full path.",
		`End with this sentence, as written: "${closingOffer(d)}"`,
	];
}

function digestLines(d: Digest): string[] {
	if (d.instant) {
		return ["NOT FACT-CHECKED: this was an instant run, one search and one answer with no fact-check. Tell the user so."];
	}
	const out = [`Confidence: ${d.confidence || "not stated"}.`];
	if (d.factChecked) {
		const counts = Object.entries(d.verdicts).map(([k, n]) => `${n} ${k}`);
		out.push(`Claims, as the run concluded them after the fact-check: ${counts.length ? counts.join(", ") : "none were checked"}.`);
	} else {
		// A standard or deep run whose analysis or fact-check failed: it is
		// not an instant lookup, and what it did find is still worth saying.
		out.push(`NOT FACT-CHECKED: the run (${d.depth || "not instant"}) ended before its fact-check finished, so none of its claims were checked. Tell the user so.`);
	}
	for (const u of d.unconfirmed) {
		out.push(`- ${u.id} ${u.status}: ${u.claim}${u.reason ? ` (${u.reason})` : ""}`);
	}
	for (const b of d.blocked) {
		out.push(`- BLOCKED ${b.kind}, not established${b.failed.length ? ` (failed: ${b.failed.join(", ")})` : ""}: ${b.text}`);
	}
	for (const f of d.figures) {
		const name = f.label ? `${f.id} (${f.label})` : f.id;
		out.push(
			f.value
				? `- COMPUTED by the program: ${name} = ${f.value}, from ${f.formula}`
				: `- NOT COMPUTED, not established: ${name}: ${f.reason}`,
		);
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
		// No checkpoint is either a run stopped before its plan, or one whose
		// checkpoint could not be saved or read back: which, drsh does not say.
		return o.status === "cancelled"
			? ["There is no checkpoint to resume from (the run stopped before its plan was made, or its checkpoint could not be saved): run it again."]
			: [];
	}
	const limited = /rate.?limit|challenge/i.test(o.detail ?? "");
	return [
		`Checkpoint: ${o.checkpoint}. Call deep_research with resume set to it (or the user runs /research-resume) to continue without repeating finished searches.` +
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
		lines.push("", "How to report it:", ...rules(o.digest).map((r) => `- ${r}`));
	}
	lines.push(...reportText(o));
	return lines.join("\n");
}
