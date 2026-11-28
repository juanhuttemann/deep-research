import assert from "node:assert/strict";
import { test } from "node:test";
import type { ExtensionContext, Theme } from "@earendil-works/pi-coding-agent";
import { getKeybindings, KeybindingsManager, TUI_KEYBINDINGS } from "@earendil-works/pi-tui";
import { type MenuResult, pick, SettingsMenu } from "../src/menu.ts";

// The settings screen's keys, on a stand-in for Pi's theme that leaves text
// uncoloured, so the drawn lines read as plain text.

const plain = { fg: (_c: string, s: string) => s, bold: (s: string) => s } as unknown as Theme;
const up = "\x1b[A";
const down = "\x1b[B";
const right = "\x1b[C";
const left = "\x1b[D";

function menu(
	selected = 0,
	kb: KeybindingsManager = getKeybindings(),
	held?: (row: number) => string | undefined,
	describe?: (row: number) => string | undefined,
) {
	const choices = ["quick", "standard", "deep"];
	let depth = 1;
	const steps: Array<[number, number]> = [];
	const results: MenuResult[] = [];
	const m = new SettingsMenu(
		plain,
		kb,
		{
			title: "settings",
			rows: () => [`depth: ${choices[depth]}`, "follow-ups: on"],
			step: (row, dir) => {
				steps.push([row, dir]);
				if (row === 0) depth = (depth + dir + choices.length) % choices.length;
			},
			held,
			describe,
			selected,
		},
		(r) => results.push(r),
	);
	const shown = () => m.render(80).map((l) => l.trimEnd());
	return { m, steps, results, shown };
}

test("left and right step the setting under the cursor and redraw it in place", () => {
	const t = menu();
	t.m.handleInput(down);
	t.m.handleInput(right);
	assert.deepEqual(t.steps, [[0, 1]]);
	assert.ok(t.shown().some((l) => l.includes("→ depth: deep")), t.shown().join("\n"));
	t.m.handleInput(left);
	t.m.handleInput(left);
	assert.deepEqual(t.steps, [[0, 1], [0, -1], [0, -1]]);
	assert.ok(t.shown().some((l) => l.includes("→ depth: quick")));
	assert.deepEqual(t.results, [], "stepping does not close the menu");
	assert.equal(t.m.selectedLine, 1);
});

test("left and right on Start do nothing", () => {
	const t = menu();
	t.m.handleInput(right);
	t.m.handleInput(left);
	assert.deepEqual(t.steps, []);
	assert.deepEqual(t.results, []);
});

test("enter still opens a row's option list, and on Start starts", () => {
	const t = menu(2);
	t.m.handleInput("\r");
	t.m.handleInput(up);
	t.m.handleInput(up);
	t.m.handleInput("\r");
	assert.deepEqual(t.results, [{ kind: "edit", row: 1 }, { kind: "start" }]);
});

test("escape cancels and the hint names the arrows", () => {
	const t = menu();
	assert.ok(t.shown().some((l) => l.includes("←→ change")), t.shown().join("\n"));
	t.m.handleInput("\x1b");
	assert.deepEqual(t.results, [{ kind: "cancel" }]);
});

test("a user who bound left to cancel keeps it", () => {
	const t = menu(1, new KeybindingsManager(TUI_KEYBINDINGS, { "tui.select.cancel": ["left"] }));
	t.m.handleInput(left);
	assert.deepEqual(t.steps, []);
	assert.deepEqual(t.results, [{ kind: "cancel" }]);
});

test("a held row shows why in the warning colour under the list, until the cursor moves", () => {
	const coloured = { fg: (c: string, t: string) => `<${c}>${t}`, bold: (t: string) => t } as unknown as Theme;
	const results: MenuResult[] = [];
	let steps = 0;
	const m = new SettingsMenu(
		coloured,
		getKeybindings(),
		{ title: "settings", rows: () => ["depth: instant", "tables: none"], step: () => steps++, held: (row) => (row === 1 ? "held: pick another depth" : undefined), selected: 2 },
		(r) => results.push(r),
	);
	const lines = () => m.render(80).map((l) => l.trim());
	for (const k of [right, left, "\r"]) {
		m.handleInput(k);
		const at = lines().indexOf("<warning>held: pick another depth");
		assert.ok(at > lines().findIndex((l) => l.includes("tables: none")), lines().join("\n"));
	}
	assert.equal(steps, 0);
	assert.deepEqual(results, [], "Enter on a held row closed the menu");
	m.handleInput(up);
	assert.ok(!lines().some((l) => l.includes("held:")), "the warning outlived the move");
});

// A warning for a list (the plan needs a sub-topic) went to a notice at the
// top of the screen, far from the list it was about.
test("pick shows its warning under the options, and an abort closes it", async () => {
	const screens: string[][] = [];
	const ctx = {
		mode: "tui",
		ui: {
			notify: () => assert.fail("the warning went to a notice"),
			custom: <T>(factory: (tui: unknown, theme: Theme, kb: unknown, done: (r: T) => void) => SettingsMenu) =>
				new Promise<T>((resolve) => {
					const m = factory(undefined, plain, getKeybindings(), resolve);
					screens.push(m.render(80).map((l) => l.trim()));
					m.handleInput(down);
					screens.push(m.render(80).map((l) => l.trim()));
				}),
		},
	} as unknown as ExtensionContext;
	const abort = new AbortController();
	const got = pick(ctx, "Review the plan", ["▶ Run this plan", "Edit sub-topics", "Cancel"], { notice: "A plan needs at least one sub-topic", signal: abort.signal });
	await new Promise((r) => setImmediate(r));
	const [first, moved] = screens;
	const at = first.indexOf("A plan needs at least one sub-topic");
	assert.ok(at > first.indexOf("Cancel"), first.join("\n"));
	assert.ok(!moved.some((l) => l.includes("needs at least")), "the warning outlived the move");
	abort.abort();
	assert.equal(await got, undefined);
});

test("pick outside the terminal is the plain select, the warning a notice", async () => {
	const notes: string[] = [];
	const ctx = {
		mode: "rpc",
		ui: { notify: (m: string) => notes.push(m), select: async (_t: string, o: string[]) => o[2] },
	} as unknown as ExtensionContext;
	assert.equal(await pick(ctx, "t", ["a", "b", "c"], { notice: "why" }), "c");
	assert.deepEqual(notes, ["why"]);
});

// The screen showed "words: auto" with nothing on what it does; the help was
// only in the list Enter opens.
test("the setting under the cursor is described under the list; a warning takes its place", () => {
	const t = menu(0, getKeybindings(), (row) => (row === 1 ? "held by the depth" : undefined), (row) => `about row ${row}`);
	assert.ok(!t.shown().some((l) => l.includes("about row")), "nothing on Start");
	t.m.handleInput(down);
	assert.ok(t.shown().includes(" about row 0"), t.shown().join("\n"));
	t.m.handleInput(down);
	t.m.handleInput(right);
	assert.ok(t.shown().includes(" held by the depth"), t.shown().join("\n"));
	assert.ok(!t.shown().some((l) => l.includes("about row")), "the warning replaces the description");
});

test("a row starting under the cursor is described from the first draw", () => {
	const t = menu(1, getKeybindings(), undefined, (row) => `about row ${row}`);
	assert.ok(t.shown().includes(" about row 0"), t.shown().join("\n"));
});
