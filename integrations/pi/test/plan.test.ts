import assert from "node:assert/strict";
import { test } from "node:test";
import { planLines, subTopicNames, tooMany, withSubTopics, type Plan } from "../src/plan.ts";

const plan: Plan = {
	question: "What is the meaning of life?",
	depth: { key: "standard" },
	clarifications: [{ question: "Which sense?", answer: "Religious" }],
	sub_topics: [
		{ id: "1", name: "Abrahamic traditions", query: "meaning of life judaism christianity islam", terms: ["purpose"] },
		{ id: "2", name: "Buddhism", query: "buddhism meaning of life", terms: ["dukkha"] },
	],
};

// The review showed the names alone: not the depth, the pages each topic
// reads, the follow-up round, or what each would search for.
test("the review shows what the plan runs at and each sub-topic's search, not its JSON", () => {
	const lines = planLines({ ...plan, depth: { key: "standard", label: "Standard" }, max_sources: 9 });
	assert.equal(lines[0], 'Plan: 2 sub-topics for "What is the meaning of life?"');
	assert.deepEqual(lines.slice(1), [
		"  standard depth · 4 pages per topic · follow-ups on",
		"  1. Abrahamic traditions",
		"     search: meaning of life judaism christianity islam",
		"  2. Buddhism",
		"     search: buddhism meaning of life",
	]);
	assert.ok(!lines.join("\n").includes("{"), "no JSON on screen");
});

test("an edit that changes the count says the budget is worked out again; a new topic has no search yet", () => {
	const edited = withSubTopics({ ...plan, max_sources: 8, no_follow_ups: true }, "Buddhism\nStoicism\nAbsurdism");
	const lines = planLines(edited, 2);
	assert.equal(lines[1], "  pages per topic recalculated when the run starts · follow-ups off");
	assert.ok(lines.includes("     search worded when the run starts"), lines.join("\n"));
});

// drsh refuses a plan past its topics setting's maximum, and said so only
// after the review, once the run had started.
test("a plan past drsh's sub-topic limit is refused in the review", () => {
	const nine = withSubTopics(plan, Array.from({ length: 9 }, (_, i) => `Topic ${i}`).join("\n"));
	assert.equal(tooMany(nine, 8), "A plan has at most 8 sub-topics; this one has 9");
	assert.equal(tooMany(plan, 8), undefined);
	assert.equal(tooMany(nine, undefined), undefined, "no limit known, none applied");
});

test("an unchanged line keeps its planned query; a new or changed one is a name alone", () => {
	const edited = withSubTopics(plan, "Buddhism\n2. Hinduism\n\n- Abrahamic religions\n");
	assert.deepEqual(subTopicNames(edited), ["Buddhism", "Hinduism", "Abrahamic religions"]);
	assert.equal(edited.sub_topics?.[0].query, "buddhism meaning of life");
	assert.deepEqual(edited.sub_topics?.[1], { name: "Hinduism" });
	assert.deepEqual(edited.sub_topics?.[2], { name: "Abrahamic religions" }, "a renamed topic drops the old query");
	assert.deepEqual(edited.clarifications, plan.clarifications, "the rest of the plan rides along");
	assert.deepEqual(edited.depth, plan.depth);
});

// The list markers a reader may type were stripped from every line, so an
// untouched "3.5 vs 4 throughput" came back as "5 vs 4 throughput" and lost
// the planner's query.
test("an unchanged name that starts like a list marker keeps its sub-topic", () => {
	const p: Plan = {
		sub_topics: [
			{ name: "3.5 vs 4 throughput", query: "q1" },
			{ name: "-O2 vs -O3 builds", query: "q2" },
		],
	};
	const edited = withSubTopics(p, "3.5 vs 4 throughput\n-O2 vs -O3 builds\n1. New one");
	assert.deepEqual(edited.sub_topics, [{ name: "3.5 vs 4 throughput", query: "q1" }, { name: "-O2 vs -O3 builds", query: "q2" }, { name: "New one" }]);
});

// The exception above covered only names left as they were: a new or
// renamed "3.5 vs 4 throughput" still lost its "3." and "-O2 builds" its
// dash.
test("a new name that starts like a list marker keeps its prefix; a real marker goes", () => {
	const edited = withSubTopics(plan, "3.5 vs 4 throughput\n-O2 builds\n- Hinduism\n2) Jainism\n•Sikhism\n* Taoism");
	assert.deepEqual(subTopicNames(edited), ["3.5 vs 4 throughput", "-O2 builds", "Hinduism", "Jainism", "Sikhism", "Taoism"]);
});

test("deleting every line leaves no sub-topics, which the review refuses", () => {
	assert.equal(withSubTopics(plan, "\n  \n").sub_topics?.length, 0);
});
