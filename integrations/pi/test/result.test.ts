import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { content, digest, maxReport } from "../src/result.ts";
import { fixture } from "./helpers.ts";

const load = (name: string) => JSON.parse(readFileSync(fixture(name), "utf8"));

test("the digest lists what the fact-check did not confirm, and what was blocked", () => {
	const d = digest(load("run.json"));
	assert.equal(d.confidence, "medium");
	assert.ok(d.factChecked);
	assert.deepEqual(d.verdicts, { supported: 1, partial: 1, insufficient: 1 });
	assert.deepEqual(d.unconfirmed.map((u) => [u.id, u.status]), [["c2", "partial"], ["c3", "insufficient"]]);
	assert.equal(d.unconfirmed[0].claim, "Go uses twice the memory of Rust for the same service.", "a verdict with no claim text takes the claim's");
	assert.deepEqual(d.blocked.map((b) => b.kind), ["recommendation", "conclusion"]);
	assert.equal(d.sources, 2);
	assert.equal(d.cited, 1);
});

test("an instant run is said to be not fact-checked", () => {
	const d = digest(load("run-instant.json"));
	assert.equal(d.factChecked, false);
	const text = content({ status: "complete", artifacts: [], digest: d, report: "Go 1.26" });
	assert.match(text, /NOT FACT-CHECKED/);
	assert.doesNotMatch(text, /Confidence:/);
});

test("the model is told the rules, the unconfirmed claims and where the files are", () => {
	const text = content({
		status: "complete",
		model: "testprov/test-model",
		artifacts: ["/r/run.md", "/r/run.json"],
		digest: digest(load("run.json")),
		report: "# Report",
	});
	assert.match(text, /on testprov\/test-model/);
	assert.match(text, /c3 insufficient: Go's GC pauses/);
	assert.match(text, /BLOCKED conclusion, not established/);
	assert.match(text, /do not present an unconfirmed claim/);
	assert.match(text, /Artifacts: \/r\/run.md, \/r\/run.json/);
});

test("a stopped run says how to resume, or that it cannot", () => {
	const limited = content({ status: "incomplete", detail: "3 searches rate-limited", artifacts: [], checkpoint: "/r/x.partial.json" });
	assert.match(limited, /resume set to it/);
	assert.match(limited, /about ten minutes/);
	assert.match(content({ status: "cancelled", artifacts: [] }), /no checkpoint: run it again/);
});

test("a long report is cut, and the model is told where the rest is", () => {
	const text = content({ status: "complete", artifacts: [], report: "x".repeat(maxReport + 10), reportPath: "/r/run.md" });
	assert.match(text, /whole report is \/r\/run.md/);
	assert.ok(text.length < maxReport + 2_000);
});
