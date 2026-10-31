// The drsh command lines a research call runs. The mutual exclusions are the
// CLI's (internal/cli/cli.go): --resume and --plan carry their own question
// and settings, so they take none of the others.

import { settingArg, type Setting } from "./settings.ts";
import type { Question } from "./events.ts";

export interface Answer {
	question: string;
	answer: string;
}

// Clarified is the --answers JSON: the questions asked and the reader's
// answers, as ui.ParseAnswers reads it.
export interface Clarified {
	questions: Question[];
	answers: Answer[];
}

export interface NewRun {
	question: string;
	values: Record<string, string>;
	domains?: string[];
	answers?: Clarified;
	trace?: boolean;
}

export type Stage = "clarify" | "plan" | "run";

function common(reports: string): string[] {
	return ["--jsonl", `--reports=${reports}`];
}

function settingArgs(settings: Setting[], values: Record<string, string>): string[] {
	const out: string[] = [];
	for (const s of settings) {
		const v = values[s.key];
		if (v !== undefined) {
			out.push(settingArg(s, v));
		}
	}
	for (const k of Object.keys(values)) {
		if (!settings.some((s) => s.key === k)) {
			throw new Error(`unknown setting ${JSON.stringify(k)}`);
		}
	}
	return out;
}

// newRunArgs is a question's command line at one stage: the clarifying
// questions only, the plan only, or the whole run.
export function newRunArgs(settings: Setting[], r: NewRun, reports: string, stage: Stage): string[] {
	// --prompt=: a question that starts with "-" is still the value.
	const args = [...common(reports), `--prompt=${r.question}`, ...settingArgs(settings, r.values)];
	if (r.domains?.length) {
		args.push(`--domains=${r.domains.join(",")}`);
	}
	if (stage === "clarify") {
		args.push("--clarify-only");
		return args;
	}
	if (r.answers) {
		args.push(`--answers=${JSON.stringify(r.answers)}`);
	}
	if (stage === "plan") {
		args.push("--plan-only");
	} else if (r.trace) {
		args.push("--trace");
	}
	return args;
}

// planRunArgs runs a plan written by --plan-only, edited or not.
export function planRunArgs(planFile: string, reports: string, trace?: boolean): string[] {
	return [...common(reports), `--plan=${planFile}`, ...(trace ? ["--trace"] : [])];
}

// resumeArgs continues a run from its checkpoint.
export function resumeArgs(checkpoint: string, reports: string): string[] {
	return [...common(reports), `--resume=${checkpoint}`];
}
