import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Api, AssistantMessage, AssistantMessageEvent, Context, Model, SimpleStreamOptions, ToolCall, Usage } from "@earendil-works/pi-ai";
import { parseLine, type DrshEvent } from "../src/events.ts";
import { parseSettings } from "../src/settings.ts";

export const here = dirname(fileURLToPath(import.meta.url));
export const fixture = (name: string) => join(here, "fixtures", name);
export const fakeDrsh = join(here, "fake-drsh.mjs");

export function events(name: string): DrshEvent[] {
	return readFileSync(fixture(name), "utf8")
		.split("\n")
		.map(parseLine)
		.filter((e): e is DrshEvent => e !== undefined);
}

export const settings = () => parseSettings(readFileSync(fixture("settings.json"), "utf8"));

export const model = {
	id: "test-model",
	name: "Test model",
	api: "openai-completions",
	provider: "testprov",
	baseUrl: "https://example.invalid/v1",
	input: ["text"],
	cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
	reasoning: true,
	contextWindow: 100_000,
	maxTokens: 8_000,
} as unknown as Model<Api>;

export function usage(input: number, output: number): Usage {
	return {
		input,
		output,
		cacheRead: 0,
		cacheWrite: 0,
		totalTokens: input + output,
		cost: { input: 0.001, output: 0.002, cacheRead: 0, cacheWrite: 0, total: 0.003 },
	};
}

export interface Answer {
	text?: string;
	thinking?: string;
	toolCalls?: ToolCall[];
	error?: string;
}

export interface Call {
	context: Context;
	options?: SimpleStreamOptions;
}

// fakeStream is ctx.modelRegistry.streamSimple answering from answer(), and
// recording each call.
export function fakeStream(answer: (c: Call) => Answer, calls: Call[] = []) {
	return async function* (m: Model<Api>, context: Context, options?: SimpleStreamOptions): AsyncGenerator<AssistantMessageEvent> {
		const c = { context, options };
		calls.push(c);
		const a = answer(c);
		const msg: AssistantMessage = {
			role: "assistant",
			content: [],
			api: m.api,
			provider: m.provider,
			model: m.id,
			usage: usage(100, 20),
			stopReason: a.toolCalls?.length ? "toolUse" : "stop",
			timestamp: Date.now(),
		};
		if (a.error) {
			yield { type: "error", reason: "error", error: { ...msg, stopReason: "error", errorMessage: a.error } };
			return;
		}
		yield { type: "start", partial: msg };
		if (a.thinking) {
			msg.content.push({ type: "thinking", thinking: a.thinking });
			yield { type: "thinking_delta", contentIndex: 0, delta: a.thinking, partial: msg };
		}
		if (a.text) {
			msg.content.push({ type: "text", text: a.text });
			yield { type: "text_delta", contentIndex: msg.content.length - 1, delta: a.text, partial: msg };
		}
		for (const tc of a.toolCalls ?? []) {
			msg.content.push(tc);
			yield { type: "toolcall_end", contentIndex: msg.content.length - 1, toolCall: tc, partial: msg };
		}
		yield { type: "done", reason: a.toolCalls?.length ? "toolUse" : "stop", message: msg };
	};
}

const made: string[] = [];
process.on("exit", () => {
	for (const d of made) rmSync(d, { recursive: true, force: true });
});

// tempDir is a fresh directory under the system's, removed when the test
// file's process exits: every `make verify` runs these, and the reports,
// configs and copied runs they write piled up in /tmp.
export function tempDir(prefix: string): string {
	const d = mkdtempSync(join(tmpdir(), prefix));
	made.push(d);
	return d;
}
