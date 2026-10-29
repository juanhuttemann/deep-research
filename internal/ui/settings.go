package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

// Setting is one choice a run takes, described once for every surface that
// offers it: the CLI flag it becomes, the terminal's start screen (as a
// /command) and brief (as a key), and the serve page, which draws its
// controls from this list. Each surface used to carry its own copy of the
// tiers, their counts and the budget rules, and the copies drifted.
type Setting struct {
	// Key is the flag's name, the /command and the page's field.
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Help  string   `json:"help"`
	Value string   `json:"value"`
	Opts  []Option `json:"choices"`
	// Start settings are chosen before the plan is made; Review settings can
	// still change in the brief once it is on screen.
	Start  bool `json:"start"`
	Review bool `json:"review"`
	// BriefKey cycles the setting in the terminal brief; 0 for none.
	BriefKey Key `json:"-"`
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
}

// Setting keys, the flags they become.
const (
	SettingMode      = "mode"
	SettingTopics    = "topics"
	SettingSources   = "sources"
	SettingFollowUps = "follow-ups"
)

// sourceChoices are the per-sub-agent budgets offered besides auto.
var sourceChoices = []int{2, 3, 4, 5, 6, 8, 10}

// Settings describes every setting at the values rs holds.
func Settings(rs RunSettings) []Setting {
	return []Setting{
		{Key: SettingMode, Label: "depth", Help: "How wide and deep the research goes: the most sub-topics, and the pages each reads.",
			Value: cmp.Or(rs.Mode, "standard"), Opts: modeOptions(), Start: true, Review: true, BriefKey: KeyD},
		{Key: SettingTopics, Label: "sub-topics", Help: "How many sub-topics the plan has. Auto lets the planner choose, up to the depth's count.",
			Value: strconv.Itoa(rs.SubTopics), Opts: countOptions(MaxSubTopics, nil), Start: true},
		{Key: SettingSources, Label: "pages per topic", Help: "How many pages each sub-topic reads. Auto lets the depth decide.",
			Value: strconv.Itoa(rs.Sources), Opts: countOptions(0, sourceChoices), Start: true},
		{Key: SettingFollowUps, Label: "follow-ups", Help: "After the planned searches, up to three more for what the findings leave open.",
			Value: onOff(!rs.NoFollowUps), Opts: []Option{{Value: "on", Label: "on"}, {Value: "off", Label: "off"}},
			Start: true, Review: true, BriefKey: KeyF},
	}
}

func modeOptions() []Option {
	out := make([]Option, len(DepthModes))
	for i, m := range DepthModes {
		out[i] = Option{Value: m.Key, Label: m.Key, Note: fmt.Sprintf("≤%d", m.SubTopics)}
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
	if _, ok := s.Option(v); !ok {
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
	default: // SettingFollowUps
		np := *p
		np.NoFollowUps = value == "off"
		return &np, nil
	}
}
