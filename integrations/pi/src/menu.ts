// The extension's lists: Pi's own select dialog, drawn the same way
// (ExtensionSelectorComponent), plus a warning line under the options.
// ctx.ui.select has no place for one, so a warning went to ctx.ui.notify
// and showed at the top of the screen, far from the list it was about. The
// settings screen's list adds ←/→ to step a setting through its choices in
// place; Enter still hands the row back for the full option list.

import type { ExtensionContext, Theme } from "@earendil-works/pi-coding-agent";
import { Container, type KeybindingsManager, matchesKey, Spacer, Text } from "@earendil-works/pi-tui";

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
