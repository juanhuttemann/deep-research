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
	// errors counts the errors drsh sent for this sub-agent: a failed search
	// is an error event naming it, and the sub-agent still ends "done".
	errors?: number;
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
	// A phase change clears it: the line belongs to the call that sent it,
	// and the fact-check showed the analysis's last line under its own
	// header for minutes.
	status: string;
	// checked is what the fact-check has said of its own progress, in
	// drsh's words: how it split the check, the tally once its verdicts are
	// applied ("11 of 12 claims supported…"), the revisions it approved.
	checked: string[];
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
		checked: [],
		errors: [],
		domains: [],
	};
}

// maxErrors bounds the non-fatal errors kept for the view; the full list is
// in the run's timeline.
const maxErrors = 5;

// agentLog is a line of the agent's own log, which drsh sends as an info
// stamped with the phase it is in: the call it makes, in a column
// ("fact-check  provider/model · effort"), or indented. Kept as the
// fact-check's notes, the model's name pushed out the tally.
const agentLog = /^(\s|\S+ {2,})/;

// maxChecked bounds the fact-check's notes.
const maxChecked = 3;

// tally is the fact-check's count of what it supported: of its notes the one
// to keep. A retry's notes (no verdicts, asking again) pushed it out.
const tally = /^\d+ of \d+ claims supported/;

// note adds a fact-check note, dropping the oldest past maxChecked, the
// tally last.
function note(notes: string[], n: string): string[] {
	const out = [...notes, n];
	while (out.length > maxChecked) {
		const i = out.findIndex((x) => !tally.test(x));
		out.splice(i < 0 ? 0 : i, 1);
	}
	return out;
}

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
			s.status = "";
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
			s.status = "";
			if (e.sources) s.sources = e.sources;
			break;
		case "citation":
		case "token":
			if (e.sources) s.sources = e.sources;
			if (e.tokens) s.tokens = e.tokens;
			break;
		case "info":
			if (e.transient && e.detail) s.status = e.detail.split("\n")[0];
			else if (e.phase === "Fact-Check" && e.detail && !agentLog.test(e.detail)) s.checked = note(s.checked, e.detail.split("\n")[0]);
			break;
		case "error": {
			if (e.detail) s.errors = [...s.errors, e.detail].slice(-maxErrors);
			const a = e.sub_id ? s.subagents.find((x) => x.id === e.sub_id) : undefined;
			if (a) a.errors = (a.errors ?? 0) + 1;
			break;
		}
		case "source":
			if (e.check) s.domains.push(e.check);
			break;
	}
	return s;
}
