package ui

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
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
		note := "≤" + strconv.Itoa(m.SubTopics)
		if m.IsInstant() {
			note = "not fact-checked"
		}
		if o := s.Opts[i]; o.Value != m.Key || o.Note != note {
			t.Errorf("choice %d = %+v, want tier %s with %q", i, o, m.Key, note)
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

// The instant depth stands for every other setting at its least, as a
// value each one takes: an implied value a setting refused would launch a
// run its own flag rejects. It is the only choice that implies others.
func TestInstantImpliesAValueEachSettingTakes(t *testing.T) {
	all := Settings(RunSettings{})
	mode, _ := SettingFor(all, SettingMode)
	instant, ok := mode.Option(ModeInstant)
	if !ok || len(instant.Implies) == 0 {
		t.Fatalf("the instant depth implies nothing: %+v", mode.Opts)
	}
	for k, v := range instant.Implies {
		other, ok := SettingFor(all, k)
		if !ok || !other.accepts(v) {
			t.Errorf("instant implies %s=%q, which it does not take", k, v)
		}
	}
	for _, s := range all {
		for _, o := range s.Opts {
			if len(o.Implies) > 0 && (s.Key != SettingMode || o.Value != ModeInstant) {
				t.Errorf("%s %s implies other settings; only the instant depth does", s.Key, o.Value)
			}
		}
	}
	if name, val, err := mode.Flag(ModeInstant); err != nil || name != "mode" || val != ModeInstant {
		t.Errorf("instant is --%s=%s, %v; want --mode=instant", name, val, err)
	}
}

// The brief's d key and the page's review cycle into instant like any
// depth, and choosing it puts every option at its least: the question as
// the one sub-topic, two pages, no follow-ups, a brief report without
// tables. Another depth then applies that depth to the plan, checked.
func TestBriefCyclesIntoInstantAtItsLeast(t *testing.T) {
	p := newTestPlan("deep", []SubTopic{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}})
	p.Report = ReportSpec{Format: "detailed", Words: 900, Tables: "many"}
	brief, _ := briefSetting(KeyD)
	if next := brief.Next("deep"); next != ModeInstant {
		t.Fatalf("d after deep is %q, want instant", next)
	}
	if mode, _ := SettingFor(ReviewSettings(p), SettingMode); !slices.ContainsFunc(mode.Opts, func(o Option) bool { return o.Value == ModeInstant }) {
		t.Errorf("review does not offer instant: %+v", mode.Opts)
	}
	ip, err := ApplyToPlan(p, SettingMode, ModeInstant)
	if err != nil {
		t.Fatal(err)
	}
	want := ReportSpec{Format: "brief", Tables: "none"}
	if !ip.Instant || !ip.Depth.IsInstant() || len(ip.SubTopics) != 1 || ip.SubTopics[0].Query != p.Question ||
		ip.PerTopic() != 2 || !ip.NoFollowUps || ip.Report != want {
		t.Errorf("instant plan = %+v", ip)
	}
	checkLeavingInstant(t, ip)
}

// checkLeavingInstant checks that on the instant plan ip only the depth
// changes, and that another depth gives a checked plan at it.
func checkLeavingInstant(t *testing.T, ip *Plan) {
	t.Helper()
	for _, key := range []string{SettingFollowUps, SettingFormat} {
		if _, err := ApplyToPlan(ip, key, "on"); err == nil {
			t.Errorf("%s changed on an instant plan; only the depth does", key)
		}
	}
	back, err := ApplyToPlan(ip, SettingMode, "quick")
	if err != nil || back.Instant || back.Depth.Key != "quick" {
		t.Errorf("leaving instant = %+v, %v; want a checked quick plan", back, err)
	}
}

// Instant is every option at its least: over the config's values and over
// a setting given beside it alike.
func TestInstantHoldsEveryOptionAtItsLeast(t *testing.T) {
	rs := RunSettings{Mode: ModeInstant, Sources: 6, SubTopics: 4, Report: ReportSpec{Format: "detailed", Words: 900, Tables: "many"}}
	got, err := rs.WithImplied()
	if err != nil {
		t.Fatal(err)
	}
	want := RunSettings{Mode: ModeInstant, Sources: 2, SubTopics: 1, NoFollowUps: true, NoClarify: true,
		Report: ReportSpec{Format: "brief", Tables: "none"}}
	if got != want {
		t.Errorf("implied settings = %+v, want %+v", got, want)
	}
	for _, spelled := range []string{"INSTANT", " instant "} {
		if got, _ := (RunSettings{Mode: spelled}).WithImplied(); got.Mode != ModeInstant || got.SubTopics != 1 {
			t.Errorf("--mode %q: %+v, want the instant tier with its settings", spelled, got)
		}
	}
	for _, m := range []string{"", "quick", "standard", "deep"} {
		plain := RunSettings{Mode: m, Sources: 6}
		if got, _ := plain.WithImplied(); got != plain || got.Instant() {
			t.Errorf("at %q the settings changed: %+v", m, got)
		}
	}
}

// d cycles through instant, and leaving it puts back the plan it replaced:
// its sub-topics, follow-ups and report, as they were, at their own depth
// with the budget too, and at another depth re-budgeted as a depth choice
// always is. The page's review round-trips the plan as JSON, so the plan
// put back has to survive ReadPlan.
func TestLeavingInstantPutsThePlanBack(t *testing.T) {
	p := newTestPlan("deep", []SubTopic{{ID: "1", Name: "a", Query: "qa", Terms: []string{"x", "y"}}, {ID: "2", Name: "b"}})
	p.Report = ReportSpec{Format: "detailed", Tables: "many"}
	p, _ = p.WithSourcesPerTopic(6)
	ip, _ := ApplyToPlan(p, SettingMode, ModeInstant)
	back, _ := ApplyToPlan(ip, SettingMode, "deep")
	if !reflect.DeepEqual(back, p) {
		t.Errorf("deep → instant → deep = %+v, want the plan as it was %+v", back, p)
	}
	raw, _ := json.Marshal(ip)
	read, err := ReadPlan(bytes.NewReader(raw))
	if err != nil || read.Before == nil {
		t.Fatalf("the page's round trip lost the plan instant replaced: %+v, %v", read, err)
	}
	quick, _ := ApplyToPlan(read, SettingMode, "quick")
	if quick.Instant || quick.Depth.Key != "quick" || len(quick.SubTopics) != 2 || quick.NoFollowUps || quick.Report != p.Report ||
		quick.SubTopics[0].Query != "qa" || quick.Before != nil {
		t.Errorf("instant → quick after the round trip = %+v, want the plan back at quick", quick)
	}
}
