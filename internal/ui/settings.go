package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// Setting is one choice a run takes, described once for every surface that
// offers it: the CLI flag it becomes, the terminal's start screen (as a
// /command) and brief (as a key), and the serve page, which draws its
// controls from this list. Each surface used to carry its own copy of the
// tiers, their counts and the budget rules, and the copies drifted.
type Setting struct {
	// Key is the flag's name, the /command and the page's field.
	Key   string `json:"key"`
	Label string `json:"label"`
	Help  string `json:"help"`
	Value string `json:"value"`
	// Default is the value with nothing set, by which the start screen and
	// the page show only what was chosen.
	Default string   `json:"default"`
	Opts    []Option `json:"choices"`
	// Start settings are chosen before the plan is made; Review settings can
	// still change in the brief once it is on screen.
	Start  bool `json:"start"`
	Review bool `json:"review"`
	// BriefKey cycles the setting in the terminal brief; 0 for none.
	BriefKey Key `json:"-"`
	// count settings take any whole number from 0 (auto) to max (0 for no
	// bound), not only the choices offered: a config file's
	// sources_per_topic: 7 is a budget the CLI runs, and the page that
	// offered it as the value must be able to launch it.
	count bool
	max   int
}

// Option is one value a setting takes.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// RunSettings are a run's values for the settings, before its plan exists.
type RunSettings struct {
	Mode        string
	SubTopics   int
	Sources     int
	NoFollowUps bool
	Report      ReportSpec
	// NoClarify asks no clarifying questions before planning.
	NoClarify bool
}

// Setting keys, the flags they become.
const (
	SettingMode      = "mode"
	SettingTopics    = "topics"
	SettingSources   = "sources"
	SettingFollowUps = "follow-ups"
	SettingFormat    = "format"
	SettingWords     = "words"
	SettingTables    = "tables"
	SettingClarify   = "clarify"
)

// sourceChoices are the per-sub-agent budgets offered besides auto.
var sourceChoices = []int{2, 3, 4, 5, 6, 8, 10}

// wordChoices are the report lengths offered besides the format's own.
var wordChoices = []int{300, 600, 1000, 1500, 2500}

// Settings describes every setting at the values rs holds.
func Settings(rs RunSettings) []Setting {
	out, defaults := settingsAt(rs), settingsAt(RunSettings{})
	for i := range out {
		out[i].Default = defaults[i].Value
	}
	return out
}

func settingsAt(rs RunSettings) []Setting {
	out := []Setting{
		{Key: SettingMode, Label: "depth", Help: "How wide and deep the research goes: the most sub-topics, and the pages each reads.",
			Value: cmp.Or(rs.Mode, "standard"), Opts: modeOptions(), Start: true, Review: true, BriefKey: KeyD},
		{Key: SettingTopics, Label: "sub-topics", Help: "How many sub-topics the plan has. Auto lets the planner choose, up to the depth's count.",
			Value: strconv.Itoa(rs.SubTopics), Opts: countOptions(MaxSubTopics, nil), Start: true, count: true, max: MaxSubTopics},
		{Key: SettingSources, Label: "pages per topic", Help: "How many pages each sub-topic reads. Auto lets the depth decide.",
			Value: strconv.Itoa(rs.Sources), Opts: countOptions(0, sourceChoices), Start: true, count: true},
		{Key: SettingFollowUps, Label: "follow-ups", Help: "After the planned searches, up to three more for what the findings leave open.",
			Value: onOff(!rs.NoFollowUps), Opts: []Option{{Value: "on", Label: "on"}, {Value: "off", Label: "off"}},
			Start: true, Review: true, BriefKey: KeyF},
		{Key: SettingFormat, Label: "format", Help: "The report's shape. The answer, the fact-check and the citations are in every format.",
			Value: cmp.Or(rs.Report.Format, agent.ReportFormats[0]), Opts: formatOptions(), Start: true, Review: true, BriefKey: KeyT},
		// Words and tables are chosen with the question: on review, the
		// format alone keeps the brief's row to what its keys change.
		{Key: SettingWords, Label: "words", Help: "How long the explanation is, in words. Auto lets the format decide.",
			Value: strconv.Itoa(rs.Report.Words), Opts: countOptions(0, wordChoices), Start: true, count: true, max: agent.MaxReportWords},
		{Key: SettingTables, Label: "tables", Help: "How much of the report is in tables. Auto lets the format decide.",
			Value: cmp.Or(rs.Report.Tables, agent.ReportTables[0]), Opts: plainOptions(agent.ReportTables), Start: true},
		// Not a review setting: by the brief the questions were asked or not.
		{Key: SettingClarify, Label: "clarify", Help: "Before planning, ask up to three questions when the question leaves open what to research. Auto asks only when it does.",
			Value: clarifyValue(!rs.NoClarify), Opts: []Option{{Value: "auto", Label: "auto"}, {Value: "off", Label: "off"}}, Start: true},
	}
	for i := range out {
		out[i].Opts = withValue(out[i], out[i].Value)
	}
	return out
}

// withValue is s's choices with v among them, in order, when v is a count s
// accepts but does not offer: shown as the value, it has to be selectable,
// or the page's list showed its first choice and the terminal row nothing.
func withValue(s Setting, v string) []Option {
	if _, ok := s.Option(v); ok || !s.accepts(v) {
		return s.Opts
	}
	n, _ := strconv.Atoi(v)
	i := slices.IndexFunc(s.Opts, func(o Option) bool {
		m, err := strconv.Atoi(o.Value)
		return err == nil && m > n
	})
	if i < 0 {
		i = len(s.Opts)
	}
	return slices.Insert(slices.Clone(s.Opts), i, Option{Value: v, Label: v})
}

// accepts reports whether v is a value s takes: one of its choices, or for
// a count any whole number in its range.
func (s Setting) accepts(v string) bool {
	if _, ok := s.Option(v); ok {
		return true
	}
	n, err := strconv.Atoi(v)
	return s.count && err == nil && n >= 0 && (s.max == 0 || n <= s.max)
}

func modeOptions() []Option {
	out := make([]Option, len(DepthModes))
	for i, m := range DepthModes {
		out[i] = Option{Value: m.Key, Label: m.Key, Note: fmt.Sprintf("≤%d", m.SubTopics)}
	}
	return out
}

func formatOptions() []Option {
	out := plainOptions(agent.ReportFormats)
	for i := range out {
		out[i].Note = reportPresets[out[i].Value].note
	}
	return out
}

// plainOptions are values offered as they are named.
func plainOptions(values []string) []Option {
	out := make([]Option, len(values))
	for i, v := range values {
		out[i] = Option{Value: v, Label: v}
	}
	return out
}

// countOptions is auto (0) then 1..upTo, or the values given.
func countOptions(upTo int, values []int) []Option {
	out := []Option{{Value: "0", Label: "auto"}}
	for n := 1; n <= upTo; n++ {
		values = append(values, n)
	}
	for _, n := range values {
		out = append(out, Option{Value: strconv.Itoa(n), Label: strconv.Itoa(n)})
	}
	return out
}

// Option is the setting's option with value v, and whether it has one.
func (s Setting) Option(v string) (Option, bool) {
	i := slices.IndexFunc(s.Opts, func(o Option) bool { return o.Value == v })
	if i < 0 {
		return Option{}, false
	}
	return s.Opts[i], true
}

// Next is the value after v, wrapping: what the brief's key moves to.
func (s Setting) Next(v string) string {
	i := slices.IndexFunc(s.Opts, func(o Option) bool { return o.Value == v })
	return s.Opts[(i+1)%len(s.Opts)].Value
}

// Flag is the command-line flag and value that set the setting to v. Every
// value is passed, auto and on included, so a choice made on a screen wins
// over the config file's.
func (s Setting) Flag(v string) (name, value string, err error) {
	if !s.accepts(v) {
		return "", "", fmt.Errorf("%s: %q is not one of its choices", s.Label, v)
	}
	if s.Key == SettingFollowUps {
		return "no-follow-ups", strconv.FormatBool(v == "off"), nil
	}
	return s.Key, v, nil
}

// SettingFor is the setting named key among settings.
func SettingFor(settings []Setting, key string) (Setting, bool) {
	i := slices.IndexFunc(settings, func(s Setting) bool { return s.Key == key })
	if i < 0 {
		return Setting{}, false
	}
	return settings[i], true
}

// ReviewSettings are the settings a plan on review can still change, at its
// values: the brief's row and the page review's controls.
func ReviewSettings(p *Plan) []Setting {
	var out []Setting
	for _, s := range Settings(RunSettings{}) {
		if s.Review {
			s.Value = PlanValue(p, s.Key)
			out = append(out, s)
		}
	}
	return out
}

// PlanValue is a review setting's value in plan.
func PlanValue(p *Plan, key string) string {
	switch key {
	case SettingMode:
		return p.Depth.Key
	case SettingFollowUps:
		return onOff(!p.NoFollowUps)
	case SettingFormat:
		return cmp.Or(p.Report.Format, agent.ReportFormats[0])
	}
	return ""
}

// ApplyToPlan sets a review setting on a plan the brief or the page's review
// is showing, and re-derives what depends on it: a depth chosen there
// replaces a pinned --sources budget.
func ApplyToPlan(p *Plan, key, value string) (*Plan, error) {
	s, ok := SettingFor(Settings(RunSettings{}), key)
	if !ok || !s.Review {
		return nil, fmt.Errorf("%q cannot be changed once the plan is made", key)
	}
	if _, ok := s.Option(value); !ok {
		return nil, fmt.Errorf("%s: %q is not one of its choices", s.Label, value)
	}
	switch key {
	case SettingMode:
		d, err := ParseDepthMode(value)
		if err != nil {
			return nil, err
		}
		return p.WithDepth(d), nil
	case SettingFormat:
		r, err := ReportSpec{Format: value, Words: p.Report.Words, Tables: p.Report.Tables}.Normalized()
		if err != nil {
			return nil, err
		}
		np := *p
		np.Report = r
		return &np, nil
	default: // SettingFollowUps
		np := *p
		np.NoFollowUps = value == "off"
		return &np, nil
	}
}

// clarifyValue is the clarify setting's value: auto asks only when the
// question leaves something open.
func clarifyValue(on bool) string {
	if on {
		return "auto"
	}
	return "off"
}
