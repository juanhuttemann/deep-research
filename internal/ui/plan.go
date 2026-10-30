package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// DepthMode describes a research budget tier: a per-sub-agent source ceiling
// shown in the Research Brief.
type DepthMode struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// MaxSources is the per-sub-agent source ceiling.
	MaxSources int `json:"max_sources"`
	// SubTopics is how many sub-agents the tier plans for. A run's effort is
	// this times MaxSources, so leaving breadth to the model let a quick run
	// over six sub-topics outgrow a deep run over three.
	SubTopics int `json:"sub_topics"`
}

// DepthModes is the ordered set of supported depth tiers.
var DepthModes = []DepthMode{
	{"quick", "Quick", 3, 3},
	{"standard", "Standard", 4, 4},
	{"deep", "Deep", 5, 6},
}

// SubTopic is one branch of a research plan — the unit that becomes a
// parallel sub-agent in the supervisor tree. It reuses the agent package's
// type so the producer (Assistant) and consumer (UI) share one definition.
type SubTopic = agent.SubTopic

// Plan is the full research brief: the question, the depth tier, the derived
// source budget and the ordered sub-topics that will run as sub-agents.
type Plan struct {
	Question string `json:"question"`
	// Asked is the question as typed, kept when the planner fixed a typo in
	// it, so the brief can show what was changed before the run is launched.
	Asked      string     `json:"asked,omitempty"`
	Depth      DepthMode  `json:"depth"`
	MaxSources int        `json:"max_sources"`
	SubTopics  []SubTopic `json:"sub_topics"`
	// PinnedPerTopic is an explicit --sources request, carried so that editing
	// the sub-topic list re-derives the budget from what the reader asked for
	// rather than from the depth tier. Without it, adding one sub-topic to a
	// `--sources 9` run silently reverted every branch to the tier default.
	// Choosing a depth tier in the brief is a later, explicit choice and
	// clears it.
	PinnedPerTopic int `json:"pinned_per_topic,omitempty"`
	// Domains is the --domains source list the run is confined to, as asked:
	// each run of the plan checks them again, since a site that failed once
	// may answer the next time.
	Domains []string `json:"domains,omitempty"`
	// Scope is the planner's reading of the question (agent.ScopeNarrow, ...)
	// when it chose the breadth, "" otherwise: the brief and the page's
	// review show it beside the count it led to.
	Scope string `json:"scope,omitempty"`
	// NoFollowUps skips the follow-up round. It is a negative so that a plan
	// or trace written before the round could be turned off still runs it.
	NoFollowUps bool `json:"no_follow_ups,omitempty"`
	// Report is the shape the report is written in, the zero value for the
	// standard one: a plan written before reports had a shape still runs as
	// it did.
	Report ReportSpec `json:"report,omitzero"`
	// Questions are what the clarifier asked before planning, kept so the
	// brief and the page's review can ask them again; Clarifications are the
	// reader's answers, the reading the plan was made for. Neither ever
	// rewrites Question: the report is titled with what was asked.
	Questions      []Question      `json:"questions,omitempty"`
	Clarifications []Clarification `json:"clarifications,omitempty"`
}

// PerTopic is the source budget for a single sub-agent: the whole-
// run MaxSources divided equally among the sub-agents, floored to an
// integer and never below one. Flooring (rather than rounding up)
// guarantees the per-topic budgets sum to at most MaxSources.
func (p *Plan) PerTopic() int {
	n := len(p.SubTopics)
	if n < 1 {
		return max(p.MaxSources, 1)
	}
	return max(p.MaxSources/n, 1)
}

// WithSourcesPerTopic returns a copy of p budgeted at n sources per sub-agent,
// and reports whether the whole-run cap reduced the request. The reduction is
// reported rather than applied silently: asking for 20 sources across four
// sub-agents used to hand back 12 with no message at all.
func (p *Plan) WithSourcesPerTopic(n int) (*Plan, bool) {
	np := *p
	np.PinnedPerTopic = n
	np.MaxSources = capSources(n * max(len(p.SubTopics), 1))
	return &np, np.PerTopic() < n
}

// WithDepth returns a copy of p re-budgeted at depth d. The sub-topic
// decomposition does not depend on the depth tier — only the source ceiling
// does — so cycling depth in the brief is a local recalculation and never a
// second planning call to the model.
func (p *Plan) WithDepth(d DepthMode) *Plan {
	np := *p
	np.Depth = d
	// Picking a tier in the brief is the reader overriding whatever budget
	// they passed on the command line, so the pin does not survive it.
	np.PinnedPerTopic = 0
	np.MaxSources = maxSourcesFor(d, len(p.SubTopics))
	return &np
}

// ReadPlan decodes a plan a caller wrote or edited (the --plan-only output)
// and makes it safe to run: the depth tier is looked up by its key rather
// than trusted from the file, IDs are renumbered in order, and the source
// budget is re-derived from the tier or the pinned --sources request, so a
// hand edit cannot ask for more than the run budget allows.
func ReadPlan(r io.Reader) (*Plan, error) {
	var p Plan
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	if p.Question = strings.TrimSpace(p.Question); p.Question == "" {
		return nil, errors.New("read plan: no question")
	}
	depth, err := ParseDepthMode(p.Depth.Key)
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	topics := p.SubTopics[:0]
	for _, t := range p.SubTopics {
		// Not cut to the brief's 60 runes: the planner writes longer names,
		// and a fallback search sends the name, so a cut one would search for
		// a word ending in "…".
		if t.Name = strings.TrimSpace(t.Name); t.Name != "" {
			topics = append(topics, t)
		}
	}
	if len(topics) == 0 {
		return nil, errors.New("read plan: no sub-topics to research")
	}
	// An edited list is input like any other: a private host typed into it
	// is refused here as it is on the command line.
	domains, err := tools.ParseDomains(p.Domains)
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	plan := PlanFor(p.Question, depth, topics, p.PinnedPerTopic)
	plan.Domains = domains
	// The browser hands back the plan it reviewed, typed question included,
	// so a run of it still reports what the planner corrected.
	plan.Asked = strings.TrimSpace(p.Asked)
	plan.NoFollowUps = p.NoFollowUps
	plan.Scope = p.Scope
	if plan.Report, err = p.Report.Normalized(); err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	c, err := Clarified{Questions: p.Questions, Answers: p.Clarifications}.Checked()
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	plan.Questions, plan.Clarifications = c.Questions, c.Answers
	return plan, nil
}

// PlanFor builds a runnable plan from parts kept elsewhere (an edited plan,
// a trace): numbered in order, and budgeted from the pinned per-sub-agent
// request when there is one, else from the tier.
func PlanFor(question string, depth DepthMode, topics []SubTopic, pin int) *Plan {
	p := &Plan{Question: question, Depth: depth, SubTopics: topics, PinnedPerTopic: max(pin, 0)}
	return renumbered(p)
}

// Planner produces a research plan from a question using the assistant's Plan
// method. It falls back to heuristic sub-topics when the model returns
// nothing usable.
type Planner struct {
	Assistant agent.Assistant
	Depth     DepthMode
	// SubTopics, when positive, asks for exactly that many sub-topics;
	// otherwise the planner chooses up to the tier's count.
	SubTopics int
	// Reading is what the reader clarified about the question.
	Reading []Clarification
}

func NewPlanner(a agent.Assistant, depth DepthMode) *Planner {
	return &Planner{Assistant: a, Depth: depth}
}

// Plan generates the plan for question, applying the configured depth tier.
func (pl *Planner) Plan(ctx context.Context, question string) (*Plan, error) {
	// A question pasted over two lines kept its line break into the report's
	// title.
	question = strings.Join(strings.Fields(question), " ")
	b := pl.Breadth()
	out, err := pl.Assistant.Plan(ctx, question, b, pl.Reading)
	if err != nil {
		return nil, err
	}
	topics, asked := out.SubTopics, ""
	if out.Question != "" {
		question, asked = out.Question, question
	}
	// Nothing to research: running on would still spend three model calls on
	// an empty findings list and write a report about nothing.
	if len(topics) == 0 {
		return nil, errors.New("the plan has no sub-topics to research")
	}
	// The tier's breadth is a budget, not a request: the source allowance below
	// is sized for this many sub-agents, so an assistant that returns more
	// would spend more than the tier allows. Asked-for counts are honoured by
	// models only approximately.
	scope := ""
	if !b.Exact {
		scope = out.Scope
	}
	if n := scopeCap(scope, b.Max); n > 0 && len(topics) > n {
		topics = topics[:n]
	}
	trimSharedSubject(question, topics)
	return &Plan{
		Question:   question,
		Asked:      asked,
		Scope:      scope,
		Depth:      pl.Depth,
		MaxSources: pl.MaxSourcesFor(len(topics)),
		SubTopics:  topics,
	}, nil
}

// narrowCap is the most sub-topics a question the planner judged narrow
// runs: one thing about one subject is a search or two, and the planner,
// told so, still wrote four for it.
const narrowCap = 2

// scopeCap is the most sub-topics a plan of scope runs under the ceiling
// max: a narrow question's few, else the ceiling.
func scopeCap(scope string, max int) int {
	if scope == agent.ScopeNarrow && (max <= 0 || max > narrowCap) {
		return narrowCap
	}
	return max
}

// MaxSubTopics bounds an exact sub-topic request. The run's source budget is
// capped anyway (maxRunSources), so more branches would only thin each one.
const MaxSubTopics = 8

// Breadth is what the planner is asked for: exactly SubTopics when set,
// else up to the tier's count.
func (pl *Planner) Breadth() agent.Breadth {
	if pl.SubTopics > 0 {
		return agent.Breadth{Max: min(pl.SubTopics, MaxSubTopics), Exact: true}
	}
	return agent.Breadth{Max: pl.Depth.SubTopics}
}

func (pl *Planner) MaxSourcesFor(n int) int { return maxSourcesFor(pl.Depth, n) }

// trimSharedSubject drops the words every sub-topic name opens with while they
// only restate the question. A planner that titled each sub-topic "Intel Arc
// Pro B70 vs AMD Radeon AI Pro R9700 …" left rows that all read the same,
// cut off before the part that differs, and anchored searches that sent the
// subject twice and clipped the facet off the end.
//
// ponytail: a word prefix only; a subject restated at the end of each name,
// or a shared prefix that leaves a stray connector ("vs AMD …"), stays as is.
func trimSharedSubject(question string, topics []SubTopic) {
	if len(topics) < 2 {
		return
	}
	names := make([][]string, len(topics))
	for i, t := range topics {
		names[i] = strings.Fields(t.Name)
	}
	n := sharedSubjectWords(question, names)
	if n == 0 {
		return
	}
	for i := range topics {
		rest := names[i][n:]
		for len(rest) > 1 && bareWord(rest[0]) == "" {
			rest = rest[1:] // a separator left behind: "… R9700 — Pricing"
		}
		topics[i].Name = upperFirst(strings.Join(rest, " "))
	}
}

// sharedSubjectWords counts the leading words that every name shares and the
// question contains. Every name keeps at least one word of its own.
func sharedSubjectWords(question string, names [][]string) int {
	asked := map[string]bool{}
	for _, w := range strings.Fields(question) {
		asked[bareWord(w)] = true
	}
	for n := 0; ; n++ {
		for _, ws := range names {
			if n >= len(ws)-1 || bareWord(ws[n]) != bareWord(names[0][n]) || !asked[bareWord(ws[n])] {
				return n
			}
		}
	}
}

// bareWord is w lowercased without the punctuation around it, so "R9700:"
// and "r9700" are the same word.
func bareWord(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// maxSourcesFor is the whole-run source budget for depth d across n
// sub-agents. DepthMode.MaxSources is a per-sub-agent number, so the tiers
// differ by n sources per step — which is what makes changing depth in the
// brief visibly change the plan.
func maxSourcesFor(d DepthMode, n int) int {
	maxPer := d.MaxSources
	if maxPer <= 0 {
		maxPer = 4
	}
	return max(capSources(maxPer*n), maxPer)
}

// maxRunSources bounds a whole run's source budget, whatever the depth tier
// and the per-sub-agent request add up to.
const maxRunSources = 50

// capSources bounds a whole-run source budget.
func capSources(total int) int { return min(total, maxRunSources) }
