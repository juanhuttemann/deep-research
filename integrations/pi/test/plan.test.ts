import assert from "node:assert/strict";
import { test } from "node:test";
import { planLines, subTopicNames, withSubTopics, type Plan } from "../src/plan.ts";

const plan: Plan = {
	question: "What is the meaning of life?",
	depth: { key: "standard" },
	clarifications: [{ question: "Which sense?", answer: "Religious" }],
	sub_topics: [
		{ id: "1", name: "Abrahamic traditions", query: "meaning of life judaism christianity islam", terms: ["purpose"] },
		{ id: "2", name: "Buddhism", query: "buddhism meaning of life", terms: ["dukkha"] },
	],
};

test("the review shows sub-topics, not the plan's JSON", () => {
	const lines = planLines(plan);
	assert.equal(lines[0], 'Plan: 2 sub-topics for "What is the meaning of life?"');
	assert.deepEqual(lines.slice(1), ["  1. Abrahamic traditions", "  2. Buddhism"]);
	assert.ok(!lines.join("\n").includes("{"), "no JSON on screen");
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

test("deleting every line leaves no sub-topics, which the review refuses", () => {
	assert.equal(withSubTopics(plan, "\n  \n").sub_topics?.length, 0);
});
