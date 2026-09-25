package ui

import (
	"context"
	"errors"

	"github.com/juanhuttemann/deep-research/internal/agent"
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
	Question   string     `json:"question"`
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

// Planner produces a research plan from a question using the assistant's Plan
// method. It falls back to heuristic sub-topics when the model returns
// nothing usable.
type Planner struct {
	Assistant agent.Assistant
	Depth     DepthMode
}

func NewPlanner(a agent.Assistant, depth DepthMode) *Planner {
	return &Planner{Assistant: a, Depth: depth}
}

// Plan generates the plan for question, applying the configured depth tier.
func (pl *Planner) Plan(ctx context.Context, question string) (*Plan, error) {
	topics, err := pl.Assistant.Plan(ctx, question, pl.Depth.SubTopics)
	if err != nil {
		return nil, err
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
	if n := pl.Depth.SubTopics; n > 0 && len(topics) > n {
		topics = topics[:n]
	}
	return &Plan{
		Question:   question,
		Depth:      pl.Depth,
		MaxSources: pl.MaxSourcesFor(len(topics)),
		SubTopics:  topics,
	}, nil
}

func (pl *Planner) MaxSourcesFor(n int) int { return maxSourcesFor(pl.Depth, n) }

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
