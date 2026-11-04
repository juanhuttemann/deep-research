import assert from "node:assert/strict";
import { test } from "node:test";
import {
	accepts,
	completions,
	describe,
	effective,
	fromParams,
	implied,
	paramName,
	parseCommandArgs,
	settingArg,
} from "../src/settings.ts";
import { settings } from "./helpers.ts";

const s = settings();
const get = (key: string) => {
	const x = s.find((y) => y.key === key);
	assert.ok(x, `no setting ${key} in drsh settings`);
	return x;
};

test("an option's arg is what drsh printed, follow-ups off included", () => {
	assert.equal(settingArg(get("follow-ups"), "off"), "--no-follow-ups=true");
	assert.equal(settingArg(get("mode"), "deep"), "--mode=deep");
});

test("a count takes any whole number in range, not only the choices", () => {
	const topics = get("topics");
	assert.equal(settingArg(topics, String(topics.max)), `--topics=${topics.max}`);
	assert.ok(accepts(get("sources"), "7"), "sources has no bound but takes 7");
	assert.throws(() => settingArg(topics, String((topics.max ?? 0) + 1)));
	assert.throws(() => settingArg(get("format"), "poem"));
});

test("instant holds the others at their least, and that is shown", () => {
	const held = implied(s, { mode: "instant" });
	assert.equal(held["follow-ups"], "off");
	assert.equal(effective(s, { mode: "instant", topics: "5" }).topics, held.topics, "instant wins over a chosen count");
	assert.match(describe(get("mode")), /not fact-checked/);
});

test("tool parameters map back to setting values", () => {
	const v = fromParams(s, { [paramName("follow-ups")]: "off", topics: 3, mode: undefined });
	assert.deepEqual(v, { "follow-ups": "off", topics: "3" });
	assert.throws(() => fromParams(s, { format: "poem" }), /format/);
});

test("/research flags are the setting keys, labels as values, then the question", () => {
	const a = parseCommandArgs(s, "--topics 3 --follow-ups=off --domains go.dev,pkg.go.dev --mode deep Is Go fast?");
	assert.deepEqual(a.values, { topics: "3", "follow-ups": "off", mode: "deep" });
	assert.deepEqual(a.domains, ["go.dev", "pkg.go.dev"]);
	assert.equal(a.question, "Is Go fast?");
	assert.equal(parseCommandArgs(s, "--topics auto q").values.topics, "0", "auto is the label of 0");
	assert.equal(parseCommandArgs(s, "what does --mode mean").question, "what does --mode mean", "flags after the question are words");
	assert.throws(() => parseCommandArgs(s, "--colour red q"), /unknown option/);
	assert.throws(() => parseCommandArgs(s, "--mode"), /needs a value/);
});

test("completion offers the options, then a named option's choices", () => {
	assert.ok(completions(s, "--to").some((c) => c.label === "--topics"));
	const modes = completions(s, "--mode ").map((c) => c.label);
	assert.deepEqual(modes, get("mode").choices.map((o) => o.label));
	const byFlag = completions(s, "--no-follow-ups ").map((c) => c.label);
	assert.ok(!byFlag.includes("on") && !byFlag.includes("off"), "a setting named by its flag offers no on/off, which would read inverted");
});

// `--no-follow-ups on` read the value as the follow-ups setting's own, so
// it turned follow-ups on. Named by its CLI flag, a setting now means what
// the flag means to drsh.
test("/research reads a setting named by its CLI flag as drsh reads the flag", () => {
	const fu = (input: string) => parseCommandArgs(s, input).values["follow-ups"];
	assert.equal(fu("--no-follow-ups q"), "off", "alone, the flag is true");
	assert.equal(parseCommandArgs(s, "--no-follow-ups Is Go fast?").question, "Is Go fast?", "alone, it takes no word of the question");
	assert.equal(fu("--no-follow-ups=true q"), "off");
	assert.equal(fu("--no-follow-ups=false q"), "on");
	assert.equal(fu("--no-follow-ups=0 q"), "on", "bools are spelled as Go's flags take them");
	assert.throws(() => parseCommandArgs(s, "--no-follow-ups on q"), /takes no value; use --follow-ups/);
	assert.throws(() => parseCommandArgs(s, "--no-follow-ups=off q"), /not true or false/);
	assert.equal(fu("--follow-ups on q"), "on", "the setting's own name keeps on/off");
});
