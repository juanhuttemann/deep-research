import assert from "node:assert/strict";
import { execFile, spawnSync } from "node:child_process";
import { promisify } from "node:util";
import { existsSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { test } from "node:test";
import { checkAnswers, maxAnswerChars, maxAnswers, type Answer } from "../src/args.ts";
import { startBridge } from "../src/bridge.ts";
import type { AssistantMessageEvent } from "@earendil-works/pi-ai";
import { fakeStream, here, model, tempDir, usage, type Call } from "./helpers.ts";

// drsh must run asynchronously: the bridge answering it is in this process,
// and a synchronous exec would block the very loop that serves it.
const exec = async (file: string, args: string[], env: NodeJS.ProcessEnv) =>
	(await promisify(execFile)(file, args, { env, timeout: 60_000 })).stdout;

async function post(url: string, token: string, body: unknown) {
	return fetch(`${url}/chat/completions`, {
		method: "POST",
		headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
		body: JSON.stringify(body),
	});
}

test("the bridge answers only its own run", async () => {
	const b = await startBridge({ model, stream: fakeStream(() => ({ text: "hi" })) });
	try {
		const r = await fetch(`${b.baseURL}/models`, { headers: { authorization: "Bearer wrong" } });
		assert.equal(r.status, 401);
		const m = await fetch(`${b.baseURL}/models`, { headers: { authorization: `Bearer ${b.token}` } });
		assert.deepEqual(((await m.json()) as { data: { id: string }[] }).data.map((x: { id: string }) => x.id), ["testprov/test-model"]);
		assert.equal(b.env.OPENAI_MODEL, "testprov/test-model");
		assert.equal(b.env.OPENAI_BASE_URL, b.baseURL);
		assert.ok(b.baseURL.startsWith("http://127.0.0.1:"));
	} finally {
		await b.close();
	}
});

test("a call is answered whole or streamed, and its usage counted", async () => {
	const calls: Call[] = [];
	const b = await startBridge({ model, stream: fakeStream(() => ({ text: '{"ok":true}', thinking: "let me see" }), calls) });
	try {
		const whole: any = await (await post(b.baseURL, b.token, { messages: [{ role: "user", content: "q" }], response_format: { type: "json_object" } })).json();
		assert.equal(whole.choices[0].message.content, '{"ok":true}');
		assert.equal(whole.choices[0].message.reasoning, "let me see");
		assert.equal(whole.model, "testprov/test-model");
		assert.ok(calls[0].options?.onPayload, "JSON mode reaches the provider payload");

		const r = await post(b.baseURL, b.token, { messages: [{ role: "user", content: "q" }], stream: true, stream_options: { include_usage: true } });
		assert.equal(r.headers.get("content-type"), "text/event-stream");
		const lines = (await r.text()).split("\n\n").filter(Boolean);
		assert.equal(lines.at(-1), "data: [DONE]");
		const chunks = lines.slice(0, -1).map((l) => JSON.parse(l.slice("data: ".length)));
		assert.ok(chunks.some((c) => c.choices[0]?.delta?.reasoning === "let me see"), "reasoning streams as delta.reasoning");
		assert.ok(chunks.at(-1).usage, "usage comes last when asked for");
		assert.equal(b.calls, 2);
		assert.equal(b.usage.totalTokens, 240);
	} finally {
		await b.close();
	}
});

test("a refused call is answered with its status before anything streams", async () => {
	const b = await startBridge({ model, stream: fakeStream(() => ({ error: "429 Too Many Requests" })) });
	try {
		const r = await post(b.baseURL, b.token, { messages: [{ role: "user", content: "q" }], stream: true });
		assert.equal(r.status, 429);
		assert.match(((await r.json()) as { error: { message: string } }).error.message, /Too Many Requests/);
	} finally {
		await b.close();
	}
});

// drsh is the built binary at the repo root, or DRSH_BIN. These run it
// against the bridge with a scripted model: its openai-go client, JSON mode,
// streaming and tool calls end to end, with no network and no key.
const drsh = process.env.DRSH_BIN || resolve(here, "../../../drsh");
const haveDrsh = existsSync(drsh) && spawnSync(drsh, ["settings"], { env: isolated() }).status === 0;

function isolated(): NodeJS.ProcessEnv {
	const dir = tempDir("drsh-cfg-");
	return { ...process.env, DEEP_RESEARCH_CONFIG_DIR: dir, DEEP_RESEARCH_DATA_FILE: join(dir, "history.jsonl"), NO_COLOR: "1" };
}

const drshTest = haveDrsh ? test : test.skip;

drshTest("drsh's clarify and plan calls run on the bridge", async () => {
	const calls: Call[] = [];
	const b = await startBridge({
		model,
		stream: fakeStream((c) => {
			// The clarifier is asked "Question: ..." alone; the planner's
			// prompt is longer.
			const last = c.context.messages.at(-1);
			const prompt = last?.role === "user" && typeof last.content === "string" ? last.content : "";
			if (prompt.startsWith("Question:")) {
				return { text: JSON.stringify({ questions: [{ text: "Which workload?", options: ["web servers", "CLI tools"] }] }) };
			}
			return { thinking: "two angles", text: JSON.stringify({ subtopics: [{ name: "Benchmarks", query: "go rust benchmarks", terms: ["benchmark"] }] }) };
		}, calls),
	});
	try {
		const reports = tempDir("drsh-reports-");
		const env = { ...isolated(), ...b.env };
		const clarify = await exec(drsh, ["--jsonl", `--reports=${reports}`, "--prompt=Is Go faster than Rust?", "--clarify-only"], env);
		const asked = clarify.split("\n").filter(Boolean).map((l) => JSON.parse(l));
		assert.deepEqual(asked.find((e) => e.type === "clarify").questions.map((q: { text: string }) => q.text), ["Which workload?"]);
		assert.equal(asked.at(-1).status, "complete");

		const answers = JSON.stringify({ questions: [], answers: [{ question: "Which workload?", answer: "web servers" }] });
		const plan = await exec(drsh, ["--jsonl", `--reports=${reports}`, "--prompt=Is Go faster than Rust?", "--plan-only", `--answers=${answers}`], env);
		const planned = plan.split("\n").filter(Boolean).map((l) => JSON.parse(l));
		const p = planned.find((e) => e.type === "plan").plan;
		assert.deepEqual(p.sub_topics.map((t: { name: string }) => t.name), ["Benchmarks"]);
		assert.ok(JSON.stringify(calls.at(-1)?.context).includes("web servers"), "the answer reached the planner");
		assert.ok(b.usage.totalTokens > 0);
	} finally {
		await b.close();
	}
});

drshTest("drsh ask's tool calls run on the bridge", async () => {
	const dir = tempDir("drsh-ask-");
	const run = join(dir, "run.json");
	writeFileSync(
		run,
		JSON.stringify({
			question: "What does it cost?",
			timestamp: new Date().toISOString(),
			depth: "quick",
			report: "It costs $5 [1].",
			citations: [{ title: "Pricing", url: "https://example.com/pricing", status: "ok", cited: true }],
			pages: { "https://example.com/pricing": "The plan costs $5 a month." },
		}),
	);
	let turn = 0;
	const calls: Call[] = [];
	const b = await startBridge({
		model,
		stream: fakeStream((c) => {
			turn++;
			const tool = c.context.tools?.[0];
			if (turn === 1 && tool) {
				return { toolCalls: [{ type: "toolCall", id: "t1", name: tool.name, arguments: {} }] };
			}
			return { text: "The pricing page says $5 a month." };
		}, calls),
	});
	try {
		const out = await exec(drsh, ["ask", run, "-p", "Which source gives the price?"], { ...isolated(), ...b.env });
		assert.match(out, /\$5 a month/);
		assert.ok(calls.length >= 2, "the tool call's result went back for a second turn");
		assert.ok(calls[1].context.messages.some((m) => m.role === "toolResult"), "the second turn carries the tool's result");
	} finally {
		await b.close();
	}
});

// openai-go ends a stream that closes without [DONE] as a success: a provider
// failing mid-answer reached drsh as a complete, cut-off answer.
test("a call that fails mid-stream ends on an error event, not a clean close", async () => {
	const failing = async function* (): AsyncGenerator<AssistantMessageEvent> {
		const partial = { role: "assistant", content: [], api: model.api, provider: model.provider, model: model.id, usage: usage(0, 0), stopReason: "stop", timestamp: 0 } as const;
		yield { type: "start", partial: { ...partial, content: [] } };
		yield { type: "text_delta", contentIndex: 0, delta: "half an ans", partial: { ...partial, content: [] } };
		throw new Error("connection reset by provider");
	};
	const b = await startBridge({ model, stream: failing });
	try {
		const r = await post(b.baseURL, b.token, { messages: [{ role: "user", content: "q" }], stream: true });
		const events = (await r.text()).split("\n\n").filter(Boolean);
		assert.notEqual(events.at(-1), "data: [DONE]");
		assert.match(events.at(-1) ?? "", /"error":\{"message":"connection reset by provider"/);
	} finally {
		await b.close();
	}
});

// The extension holds answers to drsh's --answers bounds (args.ts), so that
// a long typed answer does not fail the next stage after the questions were
// answered. This pins those bounds to what the drsh built here accepts.
drshTest("drsh takes exactly the answers the extension lets through", async () => {
	const b = await startBridge({
		model,
		stream: fakeStream(() => ({ text: JSON.stringify({ subtopics: [{ name: "Benchmarks", query: "go rust benchmarks", terms: ["benchmark"] }] }) })),
	});
	try {
		const reports = tempDir("drsh-reports-");
		const env = { ...isolated(), ...b.env };
		const plan = async (answers: Answer[]) => {
			const args = ["--jsonl", `--reports=${reports}`, "--prompt=q", "--plan-only", `--answers=${JSON.stringify({ questions: [], answers })}`];
			const out = await promisify(execFile)(drsh, args, { env, timeout: 60_000 }).then((r) => r.stdout, (e) => e.stdout as string);
			return JSON.parse(out.trim().split("\n").at(-1)!) as { status: string; detail?: string };
		};
		const one = (i: number, n: number) => ({ question: `Q${i}?`, answer: "a".repeat(n) });
		const most = Array.from({ length: maxAnswers }, (_, i) => one(i, maxAnswerChars));
		assert.doesNotThrow(() => checkAnswers(most));
		assert.equal((await plan(most)).status, "complete", "the most the extension sends is taken");
		const long = [one(0, maxAnswerChars + 1)];
		assert.throws(() => checkAnswers(long));
		assert.match((await plan(long)).detail ?? "", new RegExp(`over ${maxAnswerChars}`), "one character more is refused");
		const many = Array.from({ length: maxAnswers + 1 }, (_, i) => one(i, 1));
		assert.throws(() => checkAnswers(many));
		assert.match((await plan(many)).detail ?? "", new RegExp(`at most ${maxAnswers}`), "one answer more is refused");
	} finally {
		await b.close();
	}
});

test("a call that arrives after the run was stopped reaches the provider stopped", async () => {
	// A signal does not replay its abort to a listener added after it.
	const stopped = new AbortController();
	stopped.abort();
	const calls: Call[] = [];
	const b = await startBridge({ model, stream: fakeStream(() => ({ text: "hi" }), calls), signal: stopped.signal });
	try {
		await (await post(b.baseURL, b.token, { messages: [{ role: "user", content: "q" }] })).text();
		assert.equal(calls[0]?.options?.signal?.aborted, true);
	} finally {
		await b.close();
	}
});

test("a client that stops reading holds the stream back instead of filling memory", async () => {
	const big = "x".repeat(64 * 1024);
	const total = 2000; // 128 MB, were it all queued
	let produced = 0;
	const stream = async function* (m: typeof model, _c: unknown, options?: { signal?: AbortSignal }): AsyncGenerator<AssistantMessageEvent> {
		const msg = { role: "assistant", content: [{ type: "text", text: "" }], api: m.api, provider: m.provider, model: m.id, usage: usage(1, 1), stopReason: "stop", timestamp: Date.now() } as any;
		yield { type: "start", partial: msg };
		for (let i = 0; i < total && !options?.signal?.aborted; i++) {
			produced++;
			yield { type: "text_delta", contentIndex: 0, delta: big, partial: msg };
		}
		yield { type: "done", reason: "stop", message: msg };
	};
	const b = await startBridge({ model, stream: stream as any });
	try {
		const { request } = await import("node:http");
		const url = new URL(`${b.baseURL}/chat/completions`);
		const req = request(url, { method: "POST", headers: { authorization: `Bearer ${b.token}`, "content-type": "application/json" } });
		const res = await new Promise<import("node:http").IncomingMessage>((ok) => {
			req.on("response", ok);
			req.end(JSON.stringify({ messages: [{ role: "user", content: "q" }], stream: true }));
		});
		res.pause(); // the client stops reading
		await new Promise((r) => setTimeout(r, 500));
		assert.ok(produced < total / 2, `the provider was read ${produced} of ${total} events ahead of a client that stopped reading`);
		req.destroy();
		res.destroy();
	} finally {
		await b.close();
	}
});
