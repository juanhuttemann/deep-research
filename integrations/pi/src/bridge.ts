// The local endpoint drsh calls instead of a provider: OpenAI's chat
// completions over HTTP on 127.0.0.1, answered by Pi's own model layer with
// Pi's sign-in. drsh needs no model setup of its own, any provider Pi
// reaches works (OAuth subscriptions included), and the run's usage is
// counted in Pi's session.
//
// One bridge serves one drsh process. It listens on a random loopback port
// and answers only requests carrying its random token, which drsh is given
// as its API key.

import { randomBytes } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import type { Api, AssistantMessageEvent, Context, Model, SimpleStreamOptions, Usage } from "@earendil-works/pi-ai";
import {
	addUsage,
	completion,
	errorBody,
	errorStatus,
	streamChunks,
	toContext,
	withJSONMode,
	zeroUsage,
	type ChatRequest,
	type StreamState,
} from "./openai-map.ts";

// StreamFn is ctx.modelRegistry.streamSimple: a provider-neutral call with
// auth resolved by Pi.
export type StreamFn = (model: Model<Api>, context: Context, options?: SimpleStreamOptions) => AsyncIterable<AssistantMessageEvent>;

export interface Bridge {
	// baseURL is what drsh takes as OPENAI_BASE_URL.
	baseURL: string;
	token: string;
	// modelName is what drsh takes as OPENAI_MODEL and what each answer
	// names as the model that served it.
	modelName: string;
	usage: Usage;
	calls: number;
	// env is drsh's environment for the bridge: the endpoint, the token as
	// its key, the model, and the OpenAI-compatible transport over whatever
	// provider the user saved with drsh setup.
	env: Record<string, string>;
	close(): Promise<void>;
}

// maxBody bounds a request body. drsh's largest prompt (the fact-check with
// every quoted page) is a few MB.
const maxBody = 64 << 20;

function sendJSON(res: ServerResponse, status: number, body: unknown): void {
	res.writeHead(status, { "content-type": "application/json" });
	res.end(JSON.stringify(body));
}

// fail reports a failed call: with its status before anything streamed, else
// as an error event. openai-go ends a stream that closes without [DONE] as
// a success, so an answer cut off by a provider failure would otherwise
// reach drsh as a complete, truncated one.
function fail(res: ServerResponse, message: string, status: number): void {
	if (!res.headersSent) {
		sendJSON(res, status, errorBody(message, status));
		return;
	}
	res.end(`data: ${JSON.stringify(errorBody(message, status))}\n\n`);
}

async function readBody(req: IncomingMessage): Promise<string> {
	const parts: Buffer[] = [];
	let n = 0;
	for await (const c of req) {
		n += (c as Buffer).length;
		if (n > maxBody) throw new Error("request body too large");
		parts.push(c as Buffer);
	}
	return Buffer.concat(parts).toString("utf8");
}

export async function startBridge(o: { model: Model<Api>; stream: StreamFn; signal?: AbortSignal }): Promise<Bridge> {
	const token = randomBytes(24).toString("hex");
	const modelName = `${o.model.provider}/${o.model.id}`;
	const usage = zeroUsage();
	const bridge = { token, modelName, usage, calls: 0 } as Bridge;

	const chat = async (req: IncomingMessage, res: ServerResponse) => {
		let body: ChatRequest;
		try {
			body = JSON.parse(await readBody(req)) as ChatRequest;
		} catch (err) {
			sendJSON(res, 400, errorBody(`bad request: ${(err as Error).message}`, 400));
			return;
		}
		const { context, options } = toContext(body, o.model);
		const abort = new AbortController();
		// The client hanging up (drsh's per-call timeout, or the run
		// cancelled) ends the provider call too.
		res.on("close", () => {
			if (!res.writableFinished) abort.abort();
		});
		const outer = () => abort.abort();
		o.signal?.addEventListener("abort", outer, { once: true });
		// A signal does not replay an abort to a listener added after it:
		// a call that arrived once the run was stopped went to the provider.
		if (o.signal?.aborted) abort.abort();
		bridge.calls++;
		const stream = o.stream(o.model, context, {
			signal: abort.signal,
			reasoning: options.reasoning,
			toolChoice: options.toolChoice,
			maxTokens: options.maxTokens,
			temperature: options.temperature,
			onPayload: options.json ? (p, m) => withJSONMode(p, m) : undefined,
		});
		const state: StreamState = {
			id: `chatcmpl-${randomBytes(8).toString("hex")}`,
			model: modelName,
			created: Math.floor(Date.now() / 1000),
			toolIndex: 0,
			includeUsage: body.stream_options?.include_usage === true,
		};
		try {
			const final = body.stream ? await streamTo(res, stream, state) : await collect(stream);
			if (final?.type === "error") {
				const msg = final.error.errorMessage || final.reason;
				fail(res, msg, errorStatus(msg));
				return;
			}
			if (final?.type === "done") {
				addUsage(usage, final.message.usage);
				if (!body.stream) sendJSON(res, 200, completion(final.message, state.id, modelName));
			}
			// A stream that stopped with neither an answer nor a failure
			// still has to end, or drsh waits out its timeout on it.
			if (!res.writableEnded) fail(res, "the model ended without an answer", 502);
		} finally {
			o.signal?.removeEventListener("abort", outer);
		}
	};

	const server = createServer((req, res) => {
		if (req.headers.authorization !== `Bearer ${token}`) {
			sendJSON(res, 401, errorBody("this endpoint serves only the drsh run it was started for", 401));
			return;
		}
		const path = (req.url ?? "").split("?")[0].replace(/\/+$/, "");
		if (req.method === "GET" && (path === "/v1/models" || path === "/models")) {
			sendJSON(res, 200, { object: "list", data: [{ id: modelName, object: "model", owned_by: o.model.provider }] });
			return;
		}
		if (req.method === "POST" && (path === "/v1/chat/completions" || path === "/chat/completions")) {
			chat(req, res).catch((err: Error) => {
				if (!res.writableEnded) fail(res, err.message, errorStatus(err.message));
			});
			return;
		}
		sendJSON(res, 404, errorBody(`no route ${req.method} ${path}`, 404));
	});
	await new Promise<void>((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, "127.0.0.1", () => resolve());
	});
	const { port } = server.address() as AddressInfo;
	bridge.baseURL = `http://127.0.0.1:${port}/v1`;
	bridge.env = {
		OPENAI_BASE_URL: bridge.baseURL,
		OPENAI_API_KEY: token,
		OPENAI_MODEL: modelName,
		DEEP_RESEARCH_PROVIDER: "openai",
	};
	bridge.close = () =>
		new Promise<void>((resolve) => {
			server.closeAllConnections();
			server.close(() => resolve());
		});
	return bridge;
}

type Final = Extract<AssistantMessageEvent, { type: "done" } | { type: "error" }>;

async function collect(stream: AsyncIterable<AssistantMessageEvent>): Promise<Final | undefined> {
	let final: Final | undefined;
	for await (const e of stream) {
		if (e.type === "done" || e.type === "error") final = e;
	}
	return final;
}

// send writes data and, when the socket's buffer is full, waits for it to
// drain before the stream is read on. Written regardless, a client reading
// slowly had the whole answer queued in memory as fast as the provider sent
// it. A client that hangs up ends the wait too: drain would never come.
async function send(res: ServerResponse, data: string): Promise<void> {
	if (res.write(data) || res.destroyed) return;
	await new Promise<void>((resolve) => {
		const done = () => {
			res.off("drain", done);
			res.off("close", done);
			resolve();
		};
		res.on("drain", done);
		res.on("close", done);
	});
}

// streamTo writes the answer as server-sent events. Headers wait for the
// first event that is not a failure: a provider that refuses the call
// (a bad key, a rate limit) does so before anything streams, and is then
// answered with the status drsh's retry logic reads.
async function streamTo(res: ServerResponse, stream: AsyncIterable<AssistantMessageEvent>, s: StreamState): Promise<Final | undefined> {
	let final: Final | undefined;
	for await (const e of stream) {
		if (e.type === "error") {
			final = e;
			break;
		}
		if (!res.headersSent) {
			res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
		}
		for (const c of streamChunks(s, e)) {
			await send(res, `data: ${JSON.stringify(c)}\n\n`);
		}
		if (e.type === "done") {
			final = e;
			res.end("data: [DONE]\n\n");
		}
	}
	return final;
}

