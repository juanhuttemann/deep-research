// The run settings, as `drsh settings` prints them: ui.Settings in
// internal/ui/settings.go, the list the terminal and the serve page draw
// from. Nothing here knows a key, a tier, a count or what instant implies;
// a setting added there shows up in Pi as a tool parameter, a /research
// flag and a row on the settings screen without a change to this package.

export interface SettingOption {
	value: string;
	label: string;
	note?: string;
	implies?: Record<string, string>;
	arg?: string;
}

export interface Setting {
	key: string;
	label: string;
	help: string;
	value: string;
	default: string;
	choices: SettingOption[];
	start: boolean;
	review: boolean;
	count?: boolean;
	max?: number;
	flag: string;
}

export function parseSettings(json: string): Setting[] {
	const raw = JSON.parse(json) as { settings?: Setting[] };
	if (!Array.isArray(raw.settings) || raw.settings.length === 0) {
		throw new Error("drsh settings printed no settings");
	}
	return raw.settings;
}

// paramName is the tool parameter for a setting: keys like follow-ups become
// follow_ups, the spelling every provider's function schema accepts.
export function paramName(key: string): string {
	return key.replaceAll("-", "_");
}

// accepts reports whether v is a value the setting takes: one of its
// choices, or for a count any whole number from 0 to max (0 for no bound),
// as Setting.accepts does in Go.
export function accepts(s: Setting, v: string): boolean {
	if (s.choices.some((o) => o.value === v)) {
		return true;
	}
	if (!s.count || !/^\d+$/.test(v)) {
		return false;
	}
	const n = Number(v);
	return !s.max || n <= s.max;
}

// settingArg is the command-line argument that sets s to v: the option's own
// arg, or for a count not among the choices, --flag=N.
export function settingArg(s: Setting, v: string): string {
	const o = s.choices.find((c) => c.value === v);
	if (o?.arg) {
		return o.arg;
	}
	if (!accepts(s, v)) {
		throw new Error(`${s.label}: ${JSON.stringify(v)} is not one of ${s.choices.map((c) => c.value).join(", ")}`);
	}
	return `--${s.flag}=${v}`;
}

// valueLabel is a value as the settings screen shows it: auto for a count's
// 0, the option's label otherwise.
export function valueLabel(s: Setting, v: string): string {
	return s.choices.find((o) => o.value === v)?.label ?? v;
}

// implied is what the chosen values imply for the other settings, by key:
// what instant holds at its least.
export function implied(settings: Setting[], values: Record<string, string>): Record<string, string> {
	const out: Record<string, string> = {};
	for (const s of settings) {
		const o = s.choices.find((c) => c.value === (values[s.key] ?? s.value));
		Object.assign(out, o?.implies ?? {});
	}
	return out;
}

// effective is each setting's value for a run: implied over chosen over the
// configured default.
export function effective(settings: Setting[], values: Record<string, string>): Record<string, string> {
	const out: Record<string, string> = {};
	for (const s of settings) {
		out[s.key] = values[s.key] ?? s.value;
	}
	return { ...out, ...implied(settings, values) };
}

// fromParams reads the settings out of a tool call's parameters, as strings:
// a count arrives as a number.
export function fromParams(settings: Setting[], params: Record<string, unknown>): Record<string, string> {
	const out: Record<string, string> = {};
	for (const s of settings) {
		const v = params[paramName(s.key)];
		if (v === undefined || v === null || v === "") {
			continue;
		}
		const str = String(v);
		if (!accepts(s, str)) {
			throw new Error(`${paramName(s.key)}: ${JSON.stringify(v)} is not one of ${s.choices.map((c) => c.value).join(", ")}`);
		}
		out[s.key] = str;
	}
	return out;
}

// describe is a setting's parameter description: its help, then each choice
// with its note, so instant's "not fact-checked" reaches the model.
export function describe(s: Setting): string {
	const choices = s.choices
		.map((o) => {
			const label = o.label !== o.value ? ` (${o.label})` : "";
			return `${o.value}${label}${o.note ? `: ${o.note}` : ""}`;
		})
		.join("; ");
	const range = s.count ? ` Any whole number from 0${s.max ? ` to ${s.max}` : ""}; 0 is auto.` : "";
	return `${s.help} Choices: ${choices}.${range} Default here: ${s.value}.`;
}

export interface CommandArgs {
	values: Record<string, string>;
	domains: string[];
	question: string;
}

// parseCommandArgs reads `/research --topics 3 --follow-ups off --domains a,b
// question words`. A flag is a setting key or its CLI flag; the rest is the
// question.
export function parseCommandArgs(settings: Setting[], input: string): CommandArgs {
	const words = input.trim().split(/\s+/).filter(Boolean);
	const out: CommandArgs = { values: {}, domains: [], question: "" };
	const rest: string[] = [];
	for (let i = 0; i < words.length; i++) {
		const w = words[i];
		if (!w.startsWith("--") || rest.length > 0) {
			rest.push(w);
			continue;
		}
		let [name, v] = w.slice(2).split("=", 2) as [string, string | undefined];
		if (v === undefined) {
			v = words[++i];
			if (v === undefined) {
				throw new Error(`--${name} needs a value`);
			}
		}
		if (name === "domains") {
			out.domains.push(...v.split(",").map((d) => d.trim()).filter(Boolean));
			continue;
		}
		const s = settings.find((x) => x.key === name || x.flag === name);
		if (!s) {
			throw new Error(`unknown option --${name}; the options are ${settings.map((x) => `--${x.key}`).join(", ")}, --domains`);
		}
		const value = s.choices.find((o) => o.label === v && o.value !== v)?.value ?? v;
		if (!accepts(s, value)) {
			throw new Error(`--${name}: ${JSON.stringify(v)} is not one of ${s.choices.map((c) => c.label).join(", ")}`);
		}
		out.values[s.key] = value;
	}
	out.question = rest.join(" ");
	return out;
}

export interface Completion {
	value: string;
	label: string;
	description?: string;
}

// completions offers the option names, then a named option's choices.
export function completions(settings: Setting[], prefix: string): Completion[] {
	const words = prefix.split(/\s+/);
	const last = words[words.length - 1] ?? "";
	const before = words.slice(0, -1).join(" ");
	const head = before ? `${before} ` : "";
	const prev = words[words.length - 2] ?? "";
	if (prev.startsWith("--")) {
		const s = settings.find((x) => x.key === prev.slice(2));
		if (s) {
			return s.choices
				.filter((o) => o.label.startsWith(last))
				.map((o) => ({ value: `${head}${o.label} `, label: o.label, description: o.note }));
		}
	}
	if (last.startsWith("-") || last === "") {
		const opts = settings.map((s) => ({ value: `${head}--${s.key} `, label: `--${s.key}`, description: s.label }));
		opts.push({ value: `${head}--domains `, label: "--domains", description: "research only these sites (comma-separated)" });
		return opts.filter((o) => o.label.startsWith(last || "--"));
	}
	return [];
}
