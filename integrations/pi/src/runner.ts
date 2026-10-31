// Running drsh. A research run streams --jsonl events on stdout; ask and
// doctor print text. pi.exec buffers a command's output until it exits, so
// the run is spawned here and read line by line as it goes.

import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { parseLine, type DrshEvent } from "./events.ts";

export interface RunOptions {
	env?: Record<string, string>;
	signal?: AbortSignal;
	cwd?: string;
}

export interface JsonlResult {
	// done is the run's last line; missing when the process was killed.
	done?: DrshEvent;
	plan?: unknown;
	questions?: DrshEvent["questions"];
	stderr: string;
	code: number | null;
	aborted: boolean;
}

export interface PlainResult {
	stdout: string;
	stderr: string;
	code: number | null;
	aborted: boolean;
}

// stderrKept bounds the stderr kept for an error message: the tail is what
// says why a run failed.
const stderrKept = 8_000;

// killGrace is how long a run has to end on SIGINT, writing its done line and
// checkpoint, before it is killed outright.
const killGrace = 15_000;

function tail(s: string): string {
	return s.length > stderrKept ? s.slice(-stderrKept) : s;
}

function start(bin: string, args: string[], o: RunOptions) {
	const child = spawn(bin, args, {
		cwd: o.cwd,
		env: { ...process.env, ...o.env, NO_COLOR: "1" },
		stdio: ["ignore", "pipe", "pipe"],
	});
	let stderr = "";
	child.stderr.setEncoding("utf8");
	child.stderr.on("data", (d: string) => {
		stderr = tail(stderr + d);
	});
	let aborted = false;
	const onAbort = () => {
		aborted = true;
		// SIGINT lets a headless run end as a cancellation, with its done
		// line and checkpoint; a run that does not stop is killed.
		child.kill("SIGINT");
		setTimeout(() => child.kill("SIGKILL"), killGrace).unref();
	};
	o.signal?.addEventListener("abort", onAbort, { once: true });
	const exited = new Promise<number | null>((resolve, reject) => {
		child.on("error", reject);
		child.on("close", (code) => {
			o.signal?.removeEventListener("abort", onAbort);
			resolve(code);
		});
	});
	return { child, exited, stderr: () => stderr, aborted: () => aborted };
}

// runJsonl runs drsh with --jsonl among args, handing each event to onEvent.
export async function runJsonl(
	bin: string,
	args: string[],
	o: RunOptions & { onEvent?: (e: DrshEvent) => void },
): Promise<JsonlResult> {
	if (o.signal?.aborted) {
		return { stderr: "", code: null, aborted: true };
	}
	const p = start(bin, args, o);
	const out: JsonlResult = { stderr: "", code: null, aborted: false };
	const lines = createInterface({ input: p.child.stdout, crlfDelay: Infinity });
	lines.on("line", (line) => {
		const e = parseLine(line);
		if (!e) return;
		if (e.type === "done") out.done = e;
		if (e.type === "plan") out.plan = e.plan;
		if (e.type === "clarify") out.questions = e.questions ?? [];
		o.onEvent?.(e);
	});
	const closed = new Promise<void>((resolve) => lines.on("close", resolve));
	out.code = await p.exited;
	await closed;
	out.stderr = p.stderr();
	out.aborted = p.aborted();
	return out;
}

// runPlain runs a drsh command that prints text: ask, doctor, settings.
export async function runPlain(bin: string, args: string[], o: RunOptions = {}): Promise<PlainResult> {
	if (o.signal?.aborted) {
		return { stdout: "", stderr: "", code: null, aborted: true };
	}
	const p = start(bin, args, o);
	let stdout = "";
	p.child.stdout.setEncoding("utf8");
	p.child.stdout.on("data", (d: string) => {
		stdout += d;
	});
	const code = await p.exited;
	return { stdout, stderr: p.stderr(), code, aborted: p.aborted() };
}
