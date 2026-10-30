package ui

import (
	"strconv"
	"strings"
	"testing"
)

// The tiers' sub-topic counts were copied by hand into the serve page and
// drifted from DepthModes; the list every surface draws is built from them.
func TestModeChoicesComeFromTheTiers(t *testing.T) {
	s, _ := SettingFor(Settings(RunSettings{}), SettingMode)
	if len(s.Opts) != len(DepthModes) {
		t.Fatalf("mode offers %d choices, want the %d tiers", len(s.Opts), len(DepthModes))
	}
	for i, m := range DepthModes {
		if o := s.Opts[i]; o.Value != m.Key || o.Note != "≤"+strconv.Itoa(m.SubTopics) {
			t.Errorf("choice %d = %+v, want tier %s with ≤%d", i, o, m.Key, m.SubTopics)
		}
	}
	topics, _ := SettingFor(Settings(RunSettings{}), SettingTopics)
	if last := topics.Opts[len(topics.Opts)-1].Value; last != strconv.Itoa(MaxSubTopics) {
		t.Errorf("sub-topics go up to %s, want MaxSubTopics %d", last, MaxSubTopics)
	}
}

// A review edit, from the brief's key or the page's control, goes through
// ApplyToPlan: a depth chosen there re-budgets and drops a pinned --sources,
// follow-ups flips the plan's round, and a start-only setting is refused.
func TestApplyToPlan(t *testing.T) {
	p := PlanFor("q", depthMode("quick"), []SubTopic{{Name: "a"}, {Name: "b"}}, 9)
	deep, err := ApplyToPlan(p, SettingMode, "deep")
	if err != nil {
		t.Fatal(err)
	}
	if deep.Depth.Key != "deep" || deep.PinnedPerTopic != 0 || deep.PerTopic() != depthMode("deep").MaxSources {
		t.Errorf("deep plan %+v (%d per sub-agent)", deep, deep.PerTopic())
	}
	off, err := ApplyToPlan(p, SettingFollowUps, "off")
	if err != nil || !off.NoFollowUps || p.NoFollowUps {
		t.Errorf("follow-ups off: %+v, %v (and the original changed: %v)", off, err, p.NoFollowUps)
	}
	if PlanValue(off, SettingFollowUps) != "off" || PlanValue(deep, SettingMode) != "deep" {
		t.Error("PlanValue does not read back what ApplyToPlan set")
	}
	// The format is stored normalized, "standard" as the zero spec, and the
	// words chosen with the question stay.
	p.Report = ReportSpec{Format: "brief", Words: 400}
	std, err := ApplyToPlan(p, SettingFormat, "standard")
	if err != nil || std.Report != (ReportSpec{Words: 400}) || PlanValue(std, SettingFormat) != "standard" {
		t.Errorf("format standard: %+v, %v", std, err)
	}
	for _, bad := range [][2]string{{SettingTopics, "2"}, {SettingMode, "huge"}, {"colour", "red"},
		{SettingWords, "300"}, {SettingFormat, "essay"}} {
		if _, err := ApplyToPlan(p, bad[0], bad[1]); err == nil {
			t.Errorf("ApplyToPlan(%s, %s) was accepted", bad[0], bad[1])
		}
	}
}

// The brief's keys, hints and settings row come from the list: every review
// setting with a key is offered, at the plan's value.
func TestBriefOffersEveryReviewSetting(t *testing.T) {
	r := &Renderer{T: Theme{}, width: 200}
	r.plan = PlanFor("q", depthMode("deep"), []SubTopic{{Name: "a"}}, 0)
	r.plan.NoFollowUps = true
	row, foot := r.briefSettingsRow(), strings.Join(r.briefFoot(), "\n")
	for _, s := range ReviewSettings(r.plan) {
		o, _ := s.Option(s.Value)
		if !strings.Contains(row, s.Label+"  "+o.Label) {
			t.Errorf("brief row %q lacks %s %s", row, s.Label, o.Label)
		}
		if s.BriefKey != 0 && !strings.Contains(foot, "["+string(rune(s.BriefKey))+"] "+s.Label) {
			t.Errorf("brief hints %q lack %s's key", foot, s.Label)
		}
	}
}

// A config's sources_per_topic: 7 is a budget the CLI runs, but the page
// offered "7" as the value and then refused it as "not one of its choices",
// so every launch from the page failed, and the terminal row showed nothing.
// A count setting takes any whole number in its range and lists the one it
// holds.
func TestCountSettingsTakeTheirConfiguredValue(t *testing.T) {
	sources, _ := SettingFor(Settings(RunSettings{Sources: 7}), SettingSources)
	if o, ok := sources.Option("7"); !ok || o.Label != "7" {
		t.Fatalf("sources at 7 does not offer 7: %+v", sources.Opts)
	}
	var order []string
	for _, o := range sources.Opts {
		order = append(order, o.Value)
	}
	if got := strings.Join(order, " "); got != "0 2 3 4 5 6 7 8 10" {
		t.Errorf("choices out of order: %s", got)
	}
	if name, val, err := sources.Flag("7"); err != nil || name != "sources" || val != "7" {
		t.Errorf("Flag(7) = %s %s %v", name, val, err)
	}
	topics, _ := SettingFor(Settings(RunSettings{}), SettingTopics)
	mode, _ := SettingFor(Settings(RunSettings{}), SettingMode)
	for _, bad := range []struct {
		s Setting
		v string
	}{{topics, "9"}, {topics, "-1"}, {sources, "x"}, {mode, "7"}} {
		if _, _, err := bad.s.Flag(bad.v); err == nil {
			t.Errorf("%s took %q", bad.s.Key, bad.v)
		}
	}
}

// Each setting says its default, the value with nothing set, which the
// start screen and the page compare against to show only what was chosen.
func TestSettingsCarryTheirDefaults(t *testing.T) {
	for _, s := range Settings(RunSettings{Mode: "deep", Report: ReportSpec{Words: 1000}}) {
		want := map[string]string{SettingMode: "standard", SettingWords: "0", SettingFollowUps: "on"}[s.Key]
		if want != "" && s.Default != want {
			t.Errorf("%s default = %q, want %q", s.Key, s.Default, want)
		}
		if changed := s.Value != s.Default; changed != (s.Key == SettingMode || s.Key == SettingWords) {
			t.Errorf("%s: value %q, default %q", s.Key, s.Value, s.Default)
		}
	}
}
