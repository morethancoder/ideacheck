package rubric

import (
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"

	"github.com/morethancoder/ideacheck/internal/judge"
)

// Verdict labels, best first. Rank orders them so a gate can cap a verdict.
const (
	Build     = "build"
	Explore   = "explore"
	Park      = "park"
	Kill      = "kill"
	Uncertain = "uncertain"
)

var rank = map[string]int{Kill: 0, Park: 1, Explore: 2, Build: 3}

// Rank orders verdicts (kill lowest); ok is false for unknown labels.
func Rank(verdict string) (int, bool) {
	r, ok := rank[verdict]
	return r, ok
}

// Gate is a hard cap: when When holds, the verdict can be no better than Max.
// When is an expr-lang expression over question id → normalized value in [0,1].
type Gate struct {
	When   string `yaml:"when" json:"when"`
	Max    string `yaml:"max" json:"max"`
	Reason string `yaml:"reason" json:"reason"`

	program *vm.Program
	needs   []string
}

func (v *Verdict) validate(qs []judge.Question) error {
	t := v.Thresholds
	if !(t.Build > t.Explore && t.Explore > t.Park && t.Park > 0 && t.Build <= 1) {
		return fmt.Errorf("thresholds must satisfy 1 >= build > explore > park > 0, got %+v", t)
	}
	if v.MinConfidence < 0 || v.MinConfidence > 1 {
		return fmt.Errorf("min_confidence %v must be within [0,1]", v.MinConfidence)
	}
	env := make(map[string]float64, len(qs))
	for _, q := range qs {
		env[q.ID] = 0
	}
	for i := range v.Gates {
		if err := v.Gates[i].compile(env); err != nil {
			return fmt.Errorf("gate %d: %w", i, err)
		}
	}
	return nil
}

// compile type-checks When against the rubric's question ids, so a misspelled
// id fails at load time instead of silently never firing.
func (g *Gate) compile(env map[string]float64) error {
	if _, ok := rank[g.Max]; !ok {
		return fmt.Errorf("max %q is not one of build, explore, park, kill", g.Max)
	}
	if g.Reason == "" {
		return fmt.Errorf("missing reason")
	}
	for id := range identifiers(g.When) {
		if _, ok := env[id]; !ok {
			return fmt.Errorf("when %q references unknown question %q", g.When, id)
		}
	}
	p, err := expr.Compile(g.When, expr.Env(env), expr.AsBool())
	if err != nil {
		return fmt.Errorf("when %q: %w", g.When, err)
	}
	g.program = p
	g.needs = nil
	for id := range identifiers(g.When) {
		g.needs = append(g.needs, id)
	}
	return nil
}

type identCollector map[string]bool

func (c identCollector) Visit(n *ast.Node) {
	if id, ok := (*n).(*ast.IdentifierNode); ok {
		c[id.Value] = true
	}
}

// identifiers returns the variable names an expression reads; nil if it does not parse.
func identifiers(src string) identCollector {
	p, err := expr.Compile(src, expr.AllowUndefinedVariables())
	if err != nil {
		return nil
	}
	c := identCollector{}
	node := p.Node()
	ast.Walk(&node, c)
	return c
}

// Fires reports whether the gate applies. A gate is skipped when any question it
// reads is unanswered: a missing value must never count as 0 and trip `x < 0.3`.
func (g *Gate) Fires(values map[string]float64) (bool, error) {
	for _, id := range g.needs {
		if _, ok := values[id]; !ok {
			return false, nil
		}
	}
	out, err := expr.Run(g.program, values)
	if err != nil {
		return false, fmt.Errorf("gate %q: %w", g.When, err)
	}
	return out.(bool), nil
}
