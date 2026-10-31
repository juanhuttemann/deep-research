// The --jsonl event stream drsh writes, and the run state the progress view
// is drawn from. The field names mirror ui.Event in internal/ui/events.go; a
// Go test decodes test/fixtures/*.jsonl into that struct with unknown fields
// refused, so a renamed field fails there before it silently reads as empty
// here.

export interface Question {
	id: string;
	text: string;
	options?: string[];
}

export interface DomainCheck {
	domain: string;
	status: string;
	pages?: number;
	detail?: string;
}

export interface DrshEvent {
	type: string;
	time?: string;
	phase?: string;
	detail?: string;
	query?: string;
	terms?: string[];
	url?: string;
	domain?: string;
	source_title?: string;
	source_url?: string;
	status?: string;
	code?: string;
	tokens?: number;
	sources?: number;
	sub_id?: string;
	sub_name?: string;
	sub_state?: string;
	progress?: number;
	line?: string;
	part?: string;
	transient?: boolean;
	artifacts?: string[];
	checkpoint?: string;
	plan?: unknown;
	questions?: Question[];
	check?: DomainCheck;
}

export interface SubAgent {
	id: string;
	name: string;
	state: string;
	progress: number;
	line: string;
}

// RunState is what the live view shows. It is a tool result's details, so it
// holds plain JSON only.
export interface RunState {
	phase: string;
	phaseDetail: string;
	subagents: SubAgent[];
	searches: number;
	reads: number;
	sources: number;
	tokens: number;
	// status is the latest transient line: which model is asked, a retry.
	status: string;
	// thinking is the tail of the reasoning streaming now, for a model that
	// thinks for minutes before its first word.
	thinking: string;
	errors: string[];
	domains: DomainCheck[];
}

export function initialState(): RunState {
	return {
		phase: "",
		phaseDetail: "",
		subagents: [],
		searches: 0,
		reads: 0,
		sources: 0,
		tokens: 0,
		status: "",
		thinking: "",
		errors: [],
		domains: [],
	};
}

// thinkingTail bounds the reasoning kept: the view shows a line or two of it.
const thinkingTail = 240;

// maxErrors bounds the non-fatal errors kept for the view; the full list is
// in the run's timeline.
const maxErrors = 5;

export function parseLine(line: string): DrshEvent | undefined {
	const s = line.trim();
	if (!s.startsWith("{")) {
		return undefined;
	}
	try {
		const e = JSON.parse(s) as DrshEvent;
		return typeof e.type === "string" ? e : undefined;
	} catch {
		return undefined;
	}
}

function subagent(s: RunState, e: DrshEvent): void {
	if (!e.sub_id) {
		return;
	}
	let a = s.subagents.find((x) => x.id === e.sub_id);
	if (!a) {
		a = { id: e.sub_id, name: e.sub_name ?? e.sub_id, state: "queued", progress: 0, line: "" };
		s.subagents.push(a);
	}
	if (e.sub_name) a.name = e.sub_name;
	if (e.sub_state) a.state = e.sub_state;
	if (e.progress) a.progress = e.progress;
	if (e.line) a.line = e.line;
}

// reduce folds one event into the state, in place, and returns it.
export function reduce(s: RunState, e: DrshEvent): RunState {
	switch (e.type) {
		case "phase":
			s.phase = e.phase ?? s.phase;
			s.phaseDetail = e.detail ?? "";
			s.thinking = "";
			break;
		case "subagent":
			subagent(s, e);
			break;
		case "search":
			s.searches++;
			subagent(s, e);
			break;
		case "read":
			s.reads++;
			break;
		case "report":
			s.phase = e.phase || "Report";
			s.phaseDetail = e.detail ?? "";
			s.thinking = "";
			if (e.sources) s.sources = e.sources;
			break;
		case "citation":
		case "token":
			if (e.sources) s.sources = e.sources;
			if (e.tokens) s.tokens = e.tokens;
			break;
		case "info":
			if (e.transient && e.detail) s.status = e.detail.split("\n")[0];
			break;
		case "thinking":
			s.thinking = (e.status === "start" ? "" : s.thinking) + (e.detail ?? "");
			s.thinking = s.thinking.slice(-thinkingTail);
			break;
		case "error":
			if (e.detail) s.errors = [...s.errors, e.detail].slice(-maxErrors);
			break;
		case "source":
			if (e.check) s.domains.push(e.check);
			break;
	}
	return s;
}
