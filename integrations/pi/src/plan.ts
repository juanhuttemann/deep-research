// The plan review as text: its sub-topics are what a reader changes, one
// per line, and the rest of the plan (depth, budget, the clarifications)
// rides along unseen. The plan is the --plan-only output that --plan reads
// back (ui.ReadPlan), which needs only a name per sub-topic and enforces
// the count itself.

export interface SubTopic {
	name: string;
	[k: string]: unknown;
}

export interface Plan {
	question?: string;
	sub_topics?: SubTopic[];
	[k: string]: unknown;
}

export function subTopicNames(plan: Plan): string[] {
	return (plan.sub_topics ?? []).map((t) => t.name);
}

// withSubTopics is plan with its sub-topics replaced by lines, one name
// each. An unchanged line keeps its sub-topic, the planner's query and
// terms included; a changed or new one is a sub-topic of that name alone,
// whose query the run derives, since the old query searched for something
// else. A list marker a reader typed (1., -, •) is dropped, but not from a
// line that is a sub-topic's name as it stands: "3.5 vs 4 throughput" or
// "-O2 vs -O3 builds" left alone is that sub-topic, not a new "5 vs 4".
export function withSubTopics(plan: Plan, text: string): Plan {
	const old = new Map((plan.sub_topics ?? []).map((t) => [t.name.trim(), t]));
	const names = text
		.split("\n")
		.map((l) => l.trim())
		.map((l) => (old.has(l) ? l : l.replace(/^(?:\d+[.)]|[-*•])\s*/, "").trim()))
		.filter(Boolean);
	return { ...plan, sub_topics: names.map((n) => old.get(n) ?? { name: n }) };
}

// planLines is the plan as the review shows it.
export function planLines(plan: Plan): string[] {
	const names = subTopicNames(plan);
	return [
		`Plan: ${names.length} sub-topic${names.length === 1 ? "" : "s"}${plan.question ? ` for "${plan.question}"` : ""}`,
		...names.map((n, i) => `  ${i + 1}. ${n}`),
	];
}
