import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { closingOffer, content, digest, maxReport } from "../src/result.ts";
import { fixture } from "./helpers.ts";

const load = (name: string) => JSON.parse(readFileSync(fixture(name), "utf8"));

test("the digest lists what the fact-check did not confirm, and what was blocked", () => {
	const d = digest(load("run.json"));
	assert.equal(d.confidence, "medium");
	assert.equal(d.title, "Go vs Rust Web Server Speed");
	assert.ok(d.factChecked);
	assert.deepEqual(Object.entries(d.verdicts), [["supported", 2], ["partial", 1], ["contradicted", 1], ["insufficient", 1]]);
	assert.deepEqual(d.unconfirmed.map((u) => [u.id, u.status]), [["c2", "partial"], ["c3", "insufficient"], ["c5", "contradicted"]]);
	assert.equal(d.unconfirmed[0].claim, "Go uses twice the memory of Rust for the same service.", "a verdict with no claim text takes the claim's");
	assert.deepEqual(d.blocked.map((b) => b.kind), ["recommendation", "conclusion"]);
	assert.equal(d.sources, 2);
	assert.equal(d.cited, 1);
});

// The card listed every blocked unit in the .json, where the repair pass
// keeps the originals its approved revisions replaced: a run whose report
// named two conclusions as not established showed eight blocked rows.
test("the digest leaves out blocked units an approved revision replaced, and blocked revisions", () => {
	const j = load("run.json");
	j.analysis.recommendations = [
		{ choose: "Alpha", when: "you want everything", blocked: "it names no claim to rest on" },
		{ choose: "Beta", when: "you want the fastest", blocked: "it does not follow from its claims" },
		{ choose: "Alpha", when: "you want search and fetch", claims: ["c1"], revises: "r1" },
		{ choose: "Beta", when: "you want speed", claims: ["c2"], revises: "r2", blocked: "it does not follow from its claims" },
	];
	j.analysis.conclusions = [
		{ statement: "Alpha is the most adopted.", claims: ["c1"], blocked: "it does not follow from its claims" },
		{ statement: "Gamma is the lighter option.", claims: ["c3"], blocked: "c3 insufficient", failed: ["c3"] },
		{ statement: "Alpha is widely used.", claims: ["c1"], revises: "k1" },
	];
	const d = digest(j);
	assert.deepEqual(
		d.blocked.map((b) => [b.kind, b.text]),
		[
			["recommendation", "Beta when you want the fastest"],
			["conclusion", "Gamma is the lighter option."],
		],
	);
	assert.deepEqual(d.blocked[1].failed, ["c3"]);
});

// The card and the agent counted the checker's verdicts: a run whose claims
// drsh made insufficient after the check (a quote not in its page) counted
// them supported, beside the conclusions those claims had failed.
test("the digest counts claims as the run concluded them, not the checker's verdicts", () => {
	const j = load("run.json");
	j.analysis.claims[0].status = "insufficient";
	j.analysis.claims[0].note = "quote_not_located: a quoted passage is not in the page it cites; …";
	const d = digest(j);
	assert.equal(j.fact_check.verdicts[0].status, "supported", "the checker supported it");
	assert.deepEqual(d.verdicts, { supported: 1, partial: 1, insufficient: 2, contradicted: 1 });
	const c1 = d.unconfirmed.find((u) => u.id === "c1");
	assert.equal(c1?.status, "insufficient");
	assert.match(c1?.reason ?? "", /^quote_not_located/);
	const text = content({ status: "complete", artifacts: [], digest: d, report: "# R" });
	assert.match(text, /Claims, as the run concluded them after the fact-check: 1 supported, 1 partial, 1 contradicted, 2 insufficient\./);
});

test("a run whose claims carry no status is counted by its verdicts", () => {
	const j = load("run.json");
	for (const c of j.analysis.claims) delete c.status;
	assert.deepEqual(digest(j).verdicts, { supported: 2, partial: 1, insufficient: 1 });
	delete j.analysis.claims;
	assert.deepEqual(digest(j).verdicts, { supported: 2, partial: 1, insufficient: 1 });
});

test("an instant run is said to be not fact-checked", () => {
	const d = digest(load("run-instant.json"));
	assert.equal(d.factChecked, false);
	assert.equal(d.title, undefined, "an instant run has no title");
	const text = content({ status: "complete", artifacts: [], digest: d, report: "Go 1.26" });
	assert.match(text, /NOT FACT-CHECKED/);
	assert.doesNotMatch(text, /Confidence:/);
});

// A standard run whose fact-check failed has no fact_check in its .json,
// and the model was told it was an instant run, one search and one answer,
// with its confidence and gaps dropped.
test("a run whose fact-check did not finish is not called instant", () => {
	const j = load("run.json");
	delete j.fact_check;
	const d = digest(j);
	assert.equal(d.factChecked, false);
	assert.equal(d.instant, false);
	const text = content({ status: "incomplete", detail: "fact-check failed", artifacts: [], digest: d, report: "# Report" });
	assert.match(text, /NOT FACT-CHECKED: the run \(quick\) ended before its fact-check/);
	assert.doesNotMatch(text, /instant/);
	assert.match(text, /Confidence: medium/);
	assert.match(text, /BLOCKED recommendation/);
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

// Pi closed a report offering "a new deep_research" and follow-ups "via
// research_ask": the agent's tool names, which mean nothing to its user.
test("the offers after a report are in the Pi user's terms", () => {
	const text = content({ status: "complete", artifacts: [], digest: digest(load("run.json")), report: "# Report" });
	const d = digest(load("run.json"));
	assert.ok(text.includes(`End with this sentence, as written: "${closingOffer(d)}"`));
	for (const offer of [closingOffer(d), closingOffer({ ...d, instant: true }), closingOffer({ ...d, depth: "deep" })]) {
		assert.doesNotMatch(offer, /deep_research|research_ask/);
	}
	assert.doesNotMatch(text, /offer research_ask/);
});

// A standard run, fact-checked, closed offering a deep run as the one that
// "includes a fact-check step".
test("the closing offer is the run this one was not", () => {
	const d = digest(load("run.json"));
	assert.match(closingOffer(d), /\/research --mode deep\`\), which plans more sub-topics and reads more pages/);
	assert.doesNotMatch(closingOffer(d), /fact-check step/);
	assert.match(closingOffer({ ...d, instant: true }), /fact-checked answer.*\/research --mode standard/);
	const deep = closingOffer({ ...d, depth: "deep" });
	assert.doesNotMatch(deep, /--mode deep/);
	assert.match(deep, /^I can answer follow-ups/);
});

test("the model is told to name the run's folder once, not every full path", () => {
	const text = content({ status: "complete", artifacts: ["/r/a.md"], digest: digest(load("run.json")), report: "# R" });
	assert.match(text, /folder the run's files are in, once, then the file names/);
});

test("a stopped run says how to resume, or that it cannot", () => {
	const limited = content({ status: "incomplete", detail: "3 searches rate-limited", artifacts: [], checkpoint: "/r/x.partial.json" });
	assert.match(limited, /resume set to it/);
	assert.match(limited, /about ten minutes/);
	// With no checkpoint the run may have searched and lost its progress
	// (the save failed), so it is not said to have stopped before its plan.
	const none = content({ status: "cancelled", artifacts: [] });
	assert.match(none, /no checkpoint to resume from/);
	assert.match(none, /or its checkpoint could not be saved/);
	assert.match(none, /run it again/);
});

test("a long report is cut, and the model is told where the rest is", () => {
	const text = content({ status: "complete", artifacts: [], report: "x".repeat(maxReport + 10), reportPath: "/r/run.md" });
	assert.match(text, /whole report is \/r\/run.md/);
	assert.ok(text.length < maxReport + 2_000);
});

test("the digest gives the program's figures, and the ones it could not compute", () => {
	const d = digest(load("run.json"));
	assert.deepEqual(d.figures, [
		{ id: "m1", label: "two-node throughput", value: "122400 req/s", formula: "c4 + c4" },
		{ id: "m2", label: "memory ratio", reason: "c2 is partial" },
	]);
	const text = content({ status: "complete", artifacts: [], digest: d, report: "# r" });
	assert.match(text, /COMPUTED by the program: m1 \(two-node throughput\) = 122400 req\/s, from c4 \+ c4/);
	assert.match(text, /NOT COMPUTED, not established: m2 \(memory ratio\): c2 is partial/);
	assert.match(text, /c5 contradicted: .*122,400/);
});
