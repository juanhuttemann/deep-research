import assert from "node:assert/strict";
import { test } from "node:test";
import type { ExtensionContext, Theme } from "@earendil-works/pi-coding-agent";
import { getKeybindings, KeybindingsManager, TUI_KEYBINDINGS } from "@earendil-works/pi-tui";
import { FilterMenu, matching, type MenuResult, pick, SettingsMenu } from "../src/menu.ts";

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

// The model list ran to hundreds of rows with no way to search them, where
// drsh's own picker filters as you type.
function filterMenu(options: string[]) {
	const picked: Array<number | undefined> = [];
	const m = new FilterMenu(plain, getKeybindings(), { title: "Model", options }, (i) => picked.push(i));
	const shown = () => m.render(80).map((l) => l.trimEnd());
	const type = (s: string) => [...s].forEach((c) => m.handleInput(c));
	return { m, picked, shown, type };
}

const models = ["Pi's current model", "prov/claude-fable-5", "prov/claude-fable-5.2", "prov/claude-haiku-4.5", "other/fable-mini"];

test("the filter keeps the rows holding every word typed, in any order and case", () => {
	assert.deepEqual(matching(models, "FABLE 5"), [1, 2]);
	assert.deepEqual(matching(models, "5.2 fable"), [2]);
	assert.deepEqual(matching(models, ""), [0, 1, 2, 3, 4]);
	assert.deepEqual(matching(models, "gpt"), []);
});

test("typing filters the list, the arrows move in it, and enter picks from it", () => {
	const t = filterMenu(models);
	assert.ok(t.shown().some((l) => l.includes("type to filter")));
	t.type("fable 5");
	assert.ok(t.shown().includes(" > fable 5"), t.shown().join("\n"));
	assert.ok(!t.shown().some((l) => l.includes("haiku") || l.includes("fable-mini")), t.shown().join("\n"));
	t.m.handleInput("\x1b[B");
	t.m.handleInput("\r");
	assert.deepEqual(t.picked, [2], "the index is the option's, not the filtered row's");
});

test("backspace widens the filter; a filter matching nothing picks nothing", () => {
	const t = filterMenu(models);
	t.type("gptx");
	assert.ok(t.shown().some((l) => l.includes("nothing matches")));
	t.m.handleInput("\r");
	assert.deepEqual(t.picked, []);
	for (let i = 0; i < 4; i++) t.m.handleInput("\x7f");
	t.m.handleInput("\x7f");
	assert.ok(t.shown().some((l) => l.includes("→ Pi's current model")));
	t.m.handleInput("\x1b");
	assert.deepEqual(t.picked, [undefined]);
});

test("a long list shows ten rows around the cursor and where it is", () => {
	const t = filterMenu(Array.from({ length: 40 }, (_, i) => `model-${i}`));
	for (let i = 0; i < 20; i++) t.m.handleInput("\x1b[B");
	const rows = t.shown().filter((l) => /model-\d+$/.test(l));
	assert.equal(rows.length, 10);
	assert.ok(rows.some((l) => l.includes("→ model-20")));
	assert.ok(t.shown().includes("  (21/40)"), t.shown().join("\n"));
});

test("a bracketed paste filters; a letter bound to a key still types", () => {
	const t = filterMenu(models);
	t.m.handleInput("\x1b[200~haiku\x1b[201~");
	assert.ok(t.shown().includes(" > haiku"), t.shown().join("\n"));
	t.m.handleInput("\r");
	assert.deepEqual(t.picked, [3]);
	const kb = new KeybindingsManager(TUI_KEYBINDINGS, { "tui.select.confirm": ["enter", "f"], "tui.editor.deleteCharBackward": ["backspace", "ctrl+g"] });
	const picked: Array<number | undefined> = [];
	const m = new FilterMenu(plain, kb, { title: "Model", options: models }, (i) => picked.push(i));
	for (const c of "fable") m.handleInput(c);
	assert.deepEqual(picked, [], "typing f did not pick a row");
	m.handleInput("\x07");
	assert.ok(m.render(80).some((l) => l.trimEnd() === " > fabl"), "ctrl+g deletes as Pi's input does");
});
