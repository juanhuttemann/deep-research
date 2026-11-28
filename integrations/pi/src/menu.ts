// The extension's lists: Pi's own select dialog, drawn the same way
// (ExtensionSelectorComponent), plus a warning line under the options.
// ctx.ui.select has no place for one, so a warning went to ctx.ui.notify
// and showed at the top of the screen, far from the list it was about. The
// settings screen's list adds ←/→ to step a setting through its choices in
// place; Enter still hands the row back for the full option list.

import type { ExtensionContext, Theme } from "@earendil-works/pi-coding-agent";
import { Container, decodeKittyPrintable, type KeybindingsManager, matchesKey, Spacer, Text } from "@earendil-works/pi-tui";

// Menu is a list of lines with a cursor and an optional warning under them.
// Enter picks the line under the cursor, the cancel key closes it.
export class Menu extends Container {
	protected selected: number;
	// notice is the warning under the list, until the next key that moves
	// or changes something.
	protected notice: string | undefined;
	private readonly list = new Container();
	protected readonly theme: Theme;
	protected readonly kb: KeybindingsManager;
	private readonly lines: () => string[];
	private readonly close: (line: number | undefined) => void;

	constructor(
		theme: Theme,
		kb: KeybindingsManager,
		o: { title: string; lines: () => string[]; selected?: number; notice?: string; arrows?: boolean },
		close: (line: number | undefined) => void,
	) {
		super();
		this.theme = theme;
		this.kb = kb;
		this.lines = o.lines;
		this.close = close;
		this.selected = o.selected ?? 0;
		this.notice = o.notice;
		this.addChild(new Border(theme));
		this.addChild(new Spacer(1));
		this.addChild(new Text(theme.fg("accent", theme.bold(o.title)), 1, 0));
		this.addChild(new Spacer(1));
		this.addChild(this.list);
		this.addChild(new Spacer(1));
		const hint = (k: string, d: string) => theme.fg("dim", k) + theme.fg("muted", ` ${d}`);
		// The keys Pi's own dialog names, from the user's keybindings.
		const keys = (k: "tui.select.confirm" | "tui.select.cancel") => kb.getKeys(k).join("/");
		const line = [
			hint("↑↓", "navigate"),
			...(o.arrows ? [hint("←→", "change")] : []),
			hint(keys("tui.select.confirm"), "select"),
			hint(keys("tui.select.cancel"), "cancel"),
		];
		this.addChild(new Text(line.join("  "), 1, 0));
		this.addChild(new Spacer(1));
		this.addChild(new Border(theme));
		this.draw();
	}

	// selectedLine is where the cursor is.
	get selectedLine(): number {
		return this.selected;
	}

	protected draw(): void {
		const lines = this.lines();
		this.selected = Math.min(this.selected, lines.length - 1);
		this.list.clear();
		lines.forEach((l, i) => {
			const text = i === this.selected ? this.theme.fg("accent", `→ ${l}`) : `  ${this.theme.fg("text", l)}`;
			this.list.addChild(new Text(text, 1, 0));
		});
		const about = this.notice ? undefined : this.about();
		if (this.notice) {
			this.list.addChild(new Spacer(1));
			this.list.addChild(new Text(this.theme.fg("warning", this.notice), 1, 0));
		} else if (about) {
			this.list.addChild(new Spacer(1));
			this.list.addChild(new Text(this.theme.fg("muted", about), 1, 0));
		}
	}

	// about is a line on the line under the cursor, shown under the list
	// when there is no warning; none by default.
	protected about(): string | undefined {
		return undefined;
	}

	// warn shows why under the list, until the next move.
	protected warn(why: string): void {
		this.notice = why;
		this.draw();
	}

	// key handles a key a subclass adds; true when it changed something to
	// redraw.
	protected key(_data: string): boolean {
		return false;
	}

	// confirm is Enter on the line under the cursor.
	protected confirm(): void {
		this.close(this.selected);
	}

	handleInput(data: string): void {
		// Pi's own bindings first: a user who bound ← or → to confirm or
		// cancel keeps that.
		if (this.kb.matches(data, "tui.select.confirm") || data === "\n") {
			this.confirm();
			return;
		}
		if (this.kb.matches(data, "tui.select.cancel")) {
			this.close(undefined);
			return;
		}
		if (this.kb.matches(data, "tui.select.up") || data === "k") {
			this.selected = Math.max(0, this.selected - 1);
		} else if (this.kb.matches(data, "tui.select.down") || data === "j") {
			this.selected = Math.min(this.lines().length - 1, this.selected + 1);
		} else if (!this.key(data)) {
			return;
		}
		this.notice = undefined;
		this.draw();
	}
}

// pick is ctx.ui.select with a warning under the options, closed when the
// signal aborts as select is. Outside the terminal (Pi's RPC mode) custom
// components are not drawn: there it is the plain select, the warning a
// notice.
export async function pick(
	ctx: ExtensionContext,
	title: string,
	options: string[],
	o: { notice?: string; signal?: AbortSignal } = {},
): Promise<string | undefined> {
	if (ctx.mode !== "tui") {
		if (o.notice) ctx.ui.notify(o.notice, "warning");
		return ctx.ui.select(title, options, { signal: o.signal });
	}
	if (o.signal?.aborted) return undefined;
	let onAbort = () => {};
	const line = await ctx.ui.custom<number | undefined>((_tui, theme, kb, done) => {
		onAbort = () => done(undefined);
		o.signal?.addEventListener("abort", onAbort, { once: true });
		return new Menu(theme, kb, { title, lines: () => options, notice: o.notice }, done);
	});
	o.signal?.removeEventListener("abort", onAbort);
	return line === undefined ? undefined : options[line];
}

// MenuResult is how the settings menu closed: on Start, on a setting row
// (Enter, to pick from its option list) or cancelled.
export type MenuResult = { kind: "start" } | { kind: "edit"; row: number } | { kind: "cancel" };

export interface MenuOptions {
	title: string;
	// rows draws the setting rows at their current values; it is called again
	// after every step.
	rows: () => string[];
	// step moves setting row `row` (0-based, Start not counted) one choice
	// forward (1) or back (-1).
	step: (row: number, dir: 1 | -1) => void;
	// held says why setting row `row` cannot change now (the depth holds
	// it), or undefined when it can. A held row's ←/→ and Enter show that
	// under the list instead of changing it.
	held?: (row: number) => string | undefined;
	// describe says what setting row `row` does, at its current value,
	// shown under the list while the cursor is on it.
	describe?: (row: number) => string | undefined;
	// selected is the line the cursor starts on: 0 is Start, 1 the first row.
	selected?: number;
}

export const startLabel = "▶ Start";

// SettingsMenu is the start screen's list: Start, then a row per setting.
export class SettingsMenu extends Menu {
	private readonly opts: MenuOptions;

	constructor(theme: Theme, kb: KeybindingsManager, opts: MenuOptions, done: (r: MenuResult) => void) {
		super(theme, kb, { title: opts.title, lines: () => [startLabel, ...opts.rows()], selected: opts.selected, arrows: true }, (line) =>
			done(line === undefined ? { kind: "cancel" } : line === 0 ? { kind: "start" } : { kind: "edit", row: line - 1 }),
		);
		this.opts = opts;
		// The first draw ran in Menu's constructor, before opts was set.
		this.draw();
	}

	// A setting's help was only in the list Enter opens: the screen showed
	// "words: auto" and "clarify: auto" with nothing on what they do.
	protected override about(): string | undefined {
		return this.selected > 0 ? this.opts?.describe?.(this.selected - 1) : undefined;
	}

	// refused shows why the row under the cursor is held, if it is.
	private refused(): boolean {
		const why = this.selected > 0 ? this.opts.held?.(this.selected - 1) : undefined;
		if (why === undefined) return false;
		this.warn(why);
		return true;
	}

	protected override confirm(): void {
		if (!this.refused()) super.confirm();
	}

	// ←/→ step the row under the cursor. On Start there is nothing to step;
	// a held row says why, and its warning stays.
	protected override key(data: string): boolean {
		if (!matchesKey(data, "left") && !matchesKey(data, "right")) return false;
		if (this.selected === 0 || this.refused()) return false;
		this.opts.step(this.selected - 1, matchesKey(data, "right") ? 1 : -1);
		return true;
	}
}

// matching is the indices of the options holding every word of filter, in
// their order, case aside: drsh's own picker's rule (matching in
// internal/ui/pick.go), so "fable 5" finds "anthropic/claude-fable-5.2".
export function matching(options: string[], filter: string): number[] {
	const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
	const out: number[] = [];
	options.forEach((o, i) => {
		const l = o.toLowerCase();
		if (words.every((w) => l.includes(w))) out.push(i);
	});
	return out;
}

const control = (ch: string) => {
	const c = ch.charCodeAt(0);
	return c < 32 || c === 0x7f || (c >= 0x80 && c <= 0x9f);
};

// pasteStart and pasteEnd wrap a bracketed paste, which Pi hands on as it
// came: rejected for its escape bytes, a pasted model name was dropped.
const pasteStart = "\x1b[200~";
const pasteEnd = "\x1b[201~";

// typed is the text a key types, or undefined for a key that types none:
// a Kitty CSI-u sequence decoded, a bracketed paste (its line breaks and
// other control characters as spaces), or plain text with no control
// character.
function typed(data: string): string | undefined {
	const kitty = decodeKittyPrintable(data);
	if (kitty !== undefined) return kitty;
	if (data.startsWith(pasteStart)) {
		const body = data.slice(pasteStart.length).replace(pasteEnd, "");
		return [...body].map((ch) => (control(ch) ? " " : ch)).join("");
	}
	return [...data].some(control) ? undefined : data;
}

// maxShown is how many rows the filtered list shows at once, as Pi's own
// select does.
const maxShown = 10;

// FilterMenu is a list under a filter box, drawn like Pi's select: typing
// keeps the rows holding every word typed, the arrows move, Enter picks,
// the cancel key closes. Pi's select has no filter, and the model list it
// showed ran to hundreds of rows, read ten at a time with the arrows.
export class FilterMenu extends Container {
	private filter = "";
	private shown: number[];
	private selected = 0;
	private readonly box = new Text("", 1, 0);
	private readonly list = new Container();
	private readonly theme: Theme;
	private readonly kb: KeybindingsManager;
	private readonly options: string[];
	private readonly close: (index: number | undefined) => void;

	constructor(theme: Theme, kb: KeybindingsManager, o: { title: string; options: string[] }, close: (index: number | undefined) => void) {
		super();
		this.theme = theme;
		this.kb = kb;
		this.options = o.options;
		this.shown = o.options.map((_, i) => i);
		this.close = close;
		this.addChild(new Border(theme));
		this.addChild(new Spacer(1));
		this.addChild(new Text(theme.fg("accent", theme.bold(o.title)), 1, 0));
		this.addChild(new Spacer(1));
		this.addChild(this.box);
		this.addChild(new Spacer(1));
		this.addChild(this.list);
		this.addChild(new Spacer(1));
		const hint = (k: string, d: string) => theme.fg("dim", k) + theme.fg("muted", ` ${d}`);
		const keys = (k: "tui.select.confirm" | "tui.select.cancel") => kb.getKeys(k).join("/");
		this.addChild(new Text([hint("type", "filter"), hint("↑↓", "navigate"), hint(keys("tui.select.confirm"), "select"), hint(keys("tui.select.cancel"), "cancel")].join("  "), 1, 0));
		this.addChild(new Spacer(1));
		this.addChild(new Border(theme));
		this.draw();
	}

	private draw(): void {
		const t = this.theme;
		this.box.setText(`${t.fg("accent", "> ")}${this.filter ? t.fg("text", this.filter) : t.fg("dim", "type to filter")}`);
		this.list.clear();
		if (!this.shown.length) {
			this.list.addChild(new Text(t.fg("dim", "  nothing matches"), 1, 0));
			return;
		}
		// The window keeps the cursor in view, centred where it can be.
		const start = Math.max(0, Math.min(this.selected - Math.floor(maxShown / 2), this.shown.length - maxShown));
		this.shown.slice(start, start + maxShown).forEach((i, n) => {
			const l = this.options[i];
			const text = start + n === this.selected ? t.fg("accent", `→ ${l}`) : `  ${t.fg("text", l)}`;
			this.list.addChild(new Text(text, 1, 0));
		});
		if (this.shown.length > maxShown) {
			this.list.addChild(new Text(t.fg("dim", ` (${this.selected + 1}/${this.shown.length})`), 1, 0));
		}
	}

	private refilter(text: string): void {
		this.filter = text;
		this.shown = matching(this.options, text);
		this.selected = 0;
	}

	handleInput(data: string): void {
		const kb = this.kb;
		// Text typed goes in the filter before any binding is read: a letter
		// bound to confirm picked the highlighted row at the first letter
		// of a name.
		const s = typed(data);
		if (s !== undefined) {
			this.refilter(this.filter + s);
			this.draw();
			return;
		}
		if (kb.matches(data, "tui.select.confirm") || data === "\n") {
			// Enter on a filter that matches nothing does nothing.
			if (this.shown.length) this.close(this.shown[this.selected]);
			return;
		}
		if (kb.matches(data, "tui.select.cancel")) {
			this.close(undefined);
			return;
		}
		if (kb.matches(data, "tui.select.up")) {
			this.selected = Math.max(0, this.selected - 1);
		} else if (kb.matches(data, "tui.select.down")) {
			this.selected = Math.min(this.shown.length - 1, this.selected + 1);
		} else if (matchesKey(data, "backspace") || kb.matches(data, "tui.editor.deleteCharBackward")) {
			if (!this.filter) return;
			this.refilter([...this.filter].slice(0, -1).join(""));
		} else {
			return;
		}
		this.draw();
	}
}

// pickFiltered is ctx.ui.select with a filter box, for a long list. Outside
// the terminal it is the plain select.
export async function pickFiltered(ctx: ExtensionContext, title: string, options: string[]): Promise<string | undefined> {
	if (ctx.mode !== "tui") return ctx.ui.select(title, options);
	const i = await ctx.ui.custom<number | undefined>((_tui, theme, kb, done) => new FilterMenu(theme, kb, { title, options }, done));
	return i === undefined ? undefined : options[i];
}

// Border is DynamicBorder drawn with the theme Pi hands the component: Pi's
// own reads a global theme that an extension's module copy may not have set.
class Border {
	private readonly theme: Theme;
	constructor(theme: Theme) {
		this.theme = theme;
	}
	invalidate(): void {}
	render(width: number): string[] {
		return [this.theme.fg("border", "─".repeat(Math.max(1, width)))];
	}
}
