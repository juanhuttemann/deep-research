// The plan review as text: its sub-topics are what a reader changes, one
// per line, and the rest of the plan (depth, budget, the clarifications)
// rides along unseen. The plan is the --plan-only output that --plan reads
// back (ui.ReadPlan), which needs only a name per sub-topic and enforces
// the count itself.

export interface SubTopic {
	name: string;
	query?: string;
	[k: string]: unknown;
}

export interface Plan {
	question?: string;
	depth?: { key?: string; label?: string };
	max_sources?: number;
	no_follow_ups?: boolean;
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
// else. A list marker a reader typed ("1. ", "- ", "•") is dropped, but not
// from a line that is a sub-topic's name as it stands; and a number or a
// dash is a marker only with a space after it: "3.5 vs 4 throughput" or
// "-O2 builds", typed new, is that sub-topic, not "5 vs 4" or "O2 builds".
export function withSubTopics(plan: Plan, text: string): Plan {
	const old = new Map((plan.sub_topics ?? []).map((t) => [t.name.trim(), t]));
	const names = text
		.split("\n")
		.map((l) => l.trim())
		.map((l) => (old.has(l) ? l : l.replace(/^(?:(?:\d+[.)]|[-*])\s+|•\s*)/, "").trim()))
		.filter(Boolean);
	return { ...plan, sub_topics: names.map((n) => old.get(n) ?? { name: n }) };
}

// tooMany is why an edited plan cannot run, or undefined when it can: drsh
// refuses a plan of more sub-topics than its topics setting goes to
// (ReadPlan), and said so only once the run had started. max is that
// setting's own maximum, from drsh settings, not a copy of the number.
export function tooMany(plan: Plan, max: number | undefined): string | undefined {
	const n = subTopicNames(plan).length;
	return max && n > max ? `A plan has at most ${max} sub-topics; this one has ${n}` : undefined;
}

// planLines is the plan as the review shows it: what it runs at (the depth,
// the pages each sub-topic reads, the follow-up round), then each sub-topic
// with the search it starts from. Names alone left what the plan would do
// unsaid. planned is the count the planner gave: an edit that changes it
// changes the budget, which drsh derives again when the run starts.
export function planLines(plan: Plan, planned?: number): string[] {
	const names = subTopicNames(plan);
	const n = names.length;
	const budget: string[] = [];
	if (plan.depth?.label) budget.push(`${plan.depth.label.toLowerCase()} depth`);
	if (planned !== undefined && planned !== n) {
		budget.push("pages per topic recalculated when the run starts");
	} else if (plan.max_sources && n) {
		const per = Math.max(Math.floor(plan.max_sources / n), 1);
		budget.push(`${per} page${per === 1 ? "" : "s"} per topic`);
	}
	budget.push(`follow-ups ${plan.no_follow_ups ? "off" : "on"}`);
	const lines = [`Plan: ${n} sub-topic${n === 1 ? "" : "s"}${plan.question ? ` for "${plan.question}"` : ""}`, `  ${budget.join(" · ")}`];
	(plan.sub_topics ?? []).forEach((t, i) => {
		lines.push(`  ${i + 1}. ${t.name}`);
		lines.push(`     ${t.query ? `search: ${t.query}` : "search worded when the run starts"}`);
	});
	return lines;
}
