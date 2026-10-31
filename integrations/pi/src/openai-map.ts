// The OpenAI chat-completions requests drsh's client sends, read as a pi-ai
// context, and pi-ai's answer written back as the chat-completions response
// or stream drsh reads. Only what drsh uses is mapped: system, user,
// assistant and tool messages, function tools, JSON mode, reasoning effort,
// streamed reasoning (delta.reasoning, which drsh's reasoningSniffer reads)
// and usage.

import type {
	Api,
	AssistantMessage,
	AssistantMessageEvent,
	Context,
	ImageContent,
	Message,
	Model,
	TextContent,
	ThinkingLevel,
	Tool,
	ToolCall,
	Usage,
} from "@earendil-works/pi-ai";

export interface ChatToolCall {
	id: string;
	type?: string;
	function: { name: string; arguments: string };
}

type Part = { type: "text"; text: string } | { type: "image_url"; image_url: { url: string } } | { type: string };

export interface ChatMessage {
	role: "system" | "developer" | "user" | "assistant" | "tool";
	content?: string | Part[] | null;
	tool_calls?: ChatToolCall[];
	tool_call_id?: string;
}

export interface ChatRequest {
	model?: string;
	messages: ChatMessage[];
	tools?: { type: string; function: { name: string; description?: string; parameters?: unknown } }[];
	tool_choice?: unknown;
	response_format?: { type?: string };
	reasoning_effort?: string;
	stream?: boolean;
	stream_options?: { include_usage?: boolean };
	max_tokens?: number;
	max_completion_tokens?: number;
	temperature?: number;
}

export interface CallOptions {
	reasoning?: ThinkingLevel;
	toolChoice?: "auto" | "none";
	maxTokens?: number;
	temperature?: number;
	json: boolean;
}

function text(c: ChatMessage["content"]): string {
	if (typeof c === "string") return c;
	if (!Array.isArray(c)) return "";
	return c
		.filter((p): p is { type: "text"; text: string } => p.type === "text")
		.map((p) => p.text)
		.join("");
}

function userContent(c: ChatMessage["content"]): string | (TextContent | ImageContent)[] {
	if (!Array.isArray(c)) return text(c);
	const out: (TextContent | ImageContent)[] = [];
	for (const p of c) {
		if (p.type === "text") {
			out.push({ type: "text", text: (p as { text: string }).text });
		} else if (p.type === "image_url") {
			const m = /^data:([^;]+);base64,(.*)$/s.exec((p as { image_url: { url: string } }).image_url.url);
			if (m) out.push({ type: "image", mimeType: m[1], data: m[2] });
		}
	}
	return out;
}

function parseArgs(raw: string): ToolCall["arguments"] {
	try {
		const v = JSON.parse(raw || "{}");
		return v && typeof v === "object" && !Array.isArray(v) ? v : {};
	} catch {
		return {};
	}
}

const zeroUsage = (): Usage => ({
	input: 0,
	output: 0,
	cacheRead: 0,
	cacheWrite: 0,
	totalTokens: 0,
	cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
});

// reasoningLevel maps OpenAI's reasoning_effort to pi's thinking level. none
// is no reasoning; a level pi does not know is left to the model's default.
export function reasoningLevel(effort: string | undefined): ThinkingLevel | undefined {
	switch (effort) {
		case "minimal":
		case "low":
		case "medium":
		case "high":
		case "xhigh":
		case "max":
			return effort;
		default:
			return undefined;
	}
}

// toContext reads a chat-completions request as a pi-ai context and the
// call's options.
export function toContext(req: ChatRequest, model: Model<Api>): { context: Context; options: CallOptions } {
	const system: string[] = [];
	const messages: Message[] = [];
	const toolNames = new Map<string, string>();
	const now = Date.now();
	for (const m of req.messages ?? []) {
		switch (m.role) {
			case "system":
			case "developer":
				system.push(text(m.content));
				break;
			case "user":
				messages.push({ role: "user", content: userContent(m.content), timestamp: now });
				break;
			case "assistant": {
				const content: AssistantMessage["content"] = [];
				const t = text(m.content);
				if (t) content.push({ type: "text", text: t });
				for (const tc of m.tool_calls ?? []) {
					toolNames.set(tc.id, tc.function.name);
					content.push({ type: "toolCall", id: tc.id, name: tc.function.name, arguments: parseArgs(tc.function.arguments) });
				}
				messages.push({
					role: "assistant",
					content,
					api: model.api,
					provider: model.provider,
					model: model.id,
					usage: zeroUsage(),
					stopReason: m.tool_calls?.length ? "toolUse" : "stop",
					timestamp: now,
				});
				break;
			}
			case "tool":
				messages.push({
					role: "toolResult",
					toolCallId: m.tool_call_id ?? "",
					toolName: toolNames.get(m.tool_call_id ?? "") ?? "",
					content: [{ type: "text", text: text(m.content) }],
					isError: false,
					timestamp: now,
				});
				break;
		}
	}
	const tools: Tool[] = (req.tools ?? [])
		.filter((t) => t.type === "function")
		.map((t) => ({
			name: t.function.name,
			description: t.function.description ?? "",
			parameters: (t.function.parameters ?? { type: "object", properties: {} }) as Tool["parameters"],
		}));
	const context: Context = { messages };
	if (system.length) context.systemPrompt = system.join("\n\n");
	if (tools.length) context.tools = tools;
	return {
		context,
		options: {
			reasoning: reasoningLevel(req.reasoning_effort),
			toolChoice: req.tool_choice === "none" ? "none" : tools.length ? "auto" : undefined,
			maxTokens: req.max_completion_tokens ?? req.max_tokens,
			temperature: req.temperature,
			json: req.response_format?.type === "json_object" || req.response_format?.type === "json_schema",
		},
	};
}

// withJSONMode asks an OpenAI-compatible provider for a JSON object, as drsh
// does when it calls the provider itself. Other APIs have no such switch;
// drsh's prompts ask for JSON, and its parser takes it from the text.
export function withJSONMode(payload: unknown, model: Model<Api>): unknown {
	if (model.api !== "openai-completions" || !payload || typeof payload !== "object") {
		return undefined;
	}
	const p = payload as Record<string, unknown>;
	if ("response_format" in p) return undefined;
	return { ...p, response_format: { type: "json_object" } };
}

export function finishReason(stop: string): string {
	switch (stop) {
		case "length":
			return "length";
		case "toolUse":
			return "tool_calls";
		default:
			return "stop";
	}
}

export function chatUsage(u: Usage) {
	return {
		prompt_tokens: u.input + u.cacheRead + u.cacheWrite,
		completion_tokens: u.output,
		total_tokens: u.totalTokens || u.input + u.cacheRead + u.cacheWrite + u.output,
		prompt_tokens_details: { cached_tokens: u.cacheRead },
		completion_tokens_details: { reasoning_tokens: u.reasoning ?? 0 },
	};
}

function toolCallsOf(m: AssistantMessage): ChatToolCall[] {
	return m.content
		.filter((c): c is ToolCall => c.type === "toolCall")
		.map((c) => ({ id: c.id, type: "function", function: { name: c.name, arguments: JSON.stringify(c.arguments) } }));
}

// completion is a finished answer as a non-streamed chat completion.
export function completion(m: AssistantMessage, id: string, modelName: string) {
	const content = m.content
		.filter((c): c is TextContent => c.type === "text")
		.map((c) => c.text)
		.join("");
	const reasoning = m.content
		.filter((c) => c.type === "thinking")
		.map((c) => (c as { thinking: string }).thinking)
		.join("");
	const calls = toolCallsOf(m);
	return {
		id,
		object: "chat.completion",
		created: Math.floor(m.timestamp / 1000) || Math.floor(Date.now() / 1000),
		model: modelName,
		choices: [
			{
				index: 0,
				message: {
					role: "assistant",
					content,
					...(reasoning ? { reasoning } : {}),
					...(calls.length ? { tool_calls: calls } : {}),
				},
				finish_reason: finishReason(m.stopReason),
			},
		],
		usage: chatUsage(m.usage),
	};
}

// StreamState carries what one streamed answer's chunks share.
export interface StreamState {
	id: string;
	model: string;
	created: number;
	toolIndex: number;
	includeUsage: boolean;
}

function chunk(s: StreamState, delta: Record<string, unknown>, finish: string | null = null) {
	return {
		id: s.id,
		object: "chat.completion.chunk",
		created: s.created,
		model: s.model,
		choices: [{ index: 0, delta, finish_reason: finish }],
	};
}

// streamChunks is the chat-completion chunks one pi-ai stream event becomes.
// A tool call is sent whole when it ends: the client joins the pieces of an
// index, and one piece is a valid joining.
export function streamChunks(s: StreamState, e: AssistantMessageEvent): object[] {
	switch (e.type) {
		case "start":
			return [chunk(s, { role: "assistant", content: "" })];
		case "text_delta":
			return [chunk(s, { content: e.delta })];
		case "thinking_delta":
			return [chunk(s, { reasoning: e.delta })];
		case "toolcall_end": {
			const c = e.toolCall;
			const delta = {
				tool_calls: [
					{ index: s.toolIndex++, id: c.id, type: "function", function: { name: c.name, arguments: JSON.stringify(c.arguments) } },
				],
			};
			return [chunk(s, delta)];
		}
		case "done": {
			const out: object[] = [chunk(s, {}, finishReason(e.reason))];
			if (s.includeUsage) {
				out.push({ ...chunk(s, {}), choices: [], usage: chatUsage(e.message.usage) });
			}
			return out;
		}
		default:
			return [];
	}
}

// errorStatus is the HTTP status a provider failure is reported with. drsh
// does not retry a 401, retries a 429 or a 5xx, and on a 400 that names
// reasoning drops its reasoning setting and asks again (effortFallback), so
// the status has to say which failure it was.
export function errorStatus(message: string): number {
	const m = message.toLowerCase();
	if (/unauthori[sz]ed|invalid api key|authentication|no api key|not logged in|credential/.test(m)) return 401;
	if (/rate.?limit|too many requests|quota|overloaded/.test(m)) return 429;
	// A status the provider's message leads with, or names as one: a bare
	// number elsewhere ("500 tokens over") is not a status.
	const code = /(?:^|status(?: code)?:? |http |error:? )([45]\d\d)\b/.exec(m);
	if (code) return Number(code[1]);
	if (/reasoning|thinking|effort|context length|context window|too long|invalid/.test(m)) return 400;
	return 502;
}

export function errorBody(message: string, status: number) {
	return { error: { message, type: status === 401 ? "authentication_error" : "api_error", code: status } };
}

// addUsage adds u into total, in place.
export function addUsage(total: Usage, u: Usage): void {
	total.input += u.input;
	total.output += u.output;
	total.cacheRead += u.cacheRead;
	total.cacheWrite += u.cacheWrite;
	total.totalTokens += u.totalTokens;
	if (u.reasoning) total.reasoning = (total.reasoning ?? 0) + u.reasoning;
	total.cost.input += u.cost.input;
	total.cost.output += u.cost.output;
	total.cost.cacheRead += u.cost.cacheRead;
	total.cost.cacheWrite += u.cost.cacheWrite;
	total.cost.total += u.cost.total;
}

export { zeroUsage };
