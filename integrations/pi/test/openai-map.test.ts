import assert from "node:assert/strict";
import { test } from "node:test";
import { errorStatus, reasoningLevel, streamChunks, toContext, withJSONMode, type StreamState } from "../src/openai-map.ts";
import { model, usage } from "./helpers.ts";

test("a chat request reads as a pi context: system prompt, turns, tool calls and results", () => {
	const { context, options } = toContext(
		{
			messages: [
				{ role: "system", content: "You answer from the run." },
				{ role: "user", content: [{ type: "text", text: "Which source gives the price?" }] },
				{ role: "assistant", content: null, tool_calls: [{ id: "t1", type: "function", function: { name: "search_pages", arguments: '{"q":"price"}' } }] },
				{ role: "tool", tool_call_id: "t1", content: "page 3 says $5" },
			],
			tools: [{ type: "function", function: { name: "search_pages", description: "search", parameters: { type: "object" } } }],
			reasoning_effort: "high",
			response_format: { type: "json_object" },
			max_completion_tokens: 900,
		},
		model,
	);
	assert.equal(context.systemPrompt, "You answer from the run.");
	assert.deepEqual(context.messages.map((m) => m.role), ["user", "assistant", "toolResult"]);
	const call = context.messages[1];
	assert.ok(call.role === "assistant" && call.content[0].type === "toolCall" && call.content[0].arguments.q === "price");
	const result = context.messages[2];
	assert.ok(result.role === "toolResult" && result.toolName === "search_pages", "a tool result names the tool its call did");
	assert.equal(context.tools?.[0].name, "search_pages");
	assert.equal(options.reasoning, "high");
	assert.equal(options.json, true);
	assert.equal(options.maxTokens, 900);
	assert.equal(options.toolChoice, "auto");
});

test("reasoning none sends no level, and an unknown one is left to the model", () => {
	assert.equal(reasoningLevel("none"), undefined);
	assert.equal(reasoningLevel("max"), "max");
	assert.equal(reasoningLevel("turbo"), undefined);
});

test("JSON mode is asked of an OpenAI-compatible provider only", () => {
	assert.deepEqual(withJSONMode({ model: "x" }, model), { model: "x", response_format: { type: "json_object" } });
	assert.equal(withJSONMode({ model: "x" }, { ...model, api: "anthropic-messages" }), undefined);
});

test("stream events become chunks drsh reads: reasoning, text, tool calls, finish, usage", () => {
	const s: StreamState = { id: "c", model: "p/m", created: 1, toolIndex: 0, includeUsage: true };
	const partial = { role: "assistant", content: [], api: "openai-completions", provider: "p", model: "m", usage: usage(0, 0), stopReason: "stop", timestamp: 0 } as const;
	const reasoning = streamChunks(s, { type: "thinking_delta", contentIndex: 0, delta: "hmm", partial: { ...partial, content: [] } });
	assert.deepEqual((reasoning[0] as { choices: { delta: unknown }[] }).choices[0].delta, { reasoning: "hmm" });
	const call = streamChunks(s, {
		type: "toolcall_end",
		contentIndex: 0,
		toolCall: { type: "toolCall", id: "t1", name: "f", arguments: { a: 1 } },
		partial: { ...partial, content: [] },
	});
	assert.deepEqual((call[0] as { choices: { delta: { tool_calls: unknown[] } }[] }).choices[0].delta.tool_calls[0], {
		index: 0,
		id: "t1",
		type: "function",
		function: { name: "f", arguments: '{"a":1}' },
	});
	const done = streamChunks(s, { type: "done", reason: "toolUse", message: { ...partial, content: [], usage: usage(10, 5) } });
	assert.equal((done[0] as { choices: { finish_reason: string }[] }).choices[0].finish_reason, "tool_calls");
	assert.equal((done[1] as { usage: { total_tokens: number } }).usage.total_tokens, 15);
});

test("a provider failure keeps the status drsh's retries read", () => {
	assert.equal(errorStatus("401 Unauthorized"), 401);
	assert.equal(errorStatus("Rate limit exceeded, retry later"), 429);
	assert.equal(errorStatus("status 503: upstream down"), 503);
	assert.equal(errorStatus("reasoning_effort is not supported by this model"), 400);
	assert.equal(errorStatus("request was 500 tokens over the limit of the deployment"), 502, "a bare number is not a status");
	assert.equal(errorStatus("socket hang up"), 502);
});
