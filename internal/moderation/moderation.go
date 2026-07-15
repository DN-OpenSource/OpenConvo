// Package moderation implements the automod pipeline (SPEC.md §11): an
// ordered middleware chain on message create/update — blocklist matcher,
// then an optional webhook classifier — producing an allow/flag/block/
// shadow decision. AI/ML classifiers are v2; the chain interface is
// designed so those adapters slot in without API changes.
package moderation

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/openstream/openstream/internal/domain"
)

// Verdict is the pipeline outcome for a message.
type Verdict string

// Pipeline verdicts.
const (
	VerdictAllow  Verdict = "allow"
	VerdictFlag   Verdict = "flag"
	VerdictBlock  Verdict = "block"
	VerdictShadow Verdict = "shadow"
)

// Decision carries the verdict and its origin for the audit log.
type Decision struct {
	Verdict Verdict
	Reason  string
}

// Middleware is one stage of the automod chain.
type Middleware interface {
	// Check inspects the message; returning a non-allow verdict stops the
	// chain (first decision wins).
	Check(ctx context.Context, appID string, msg *domain.Message) (Decision, error)
}

// Pipeline runs middlewares in order.
type Pipeline struct {
	stages []Middleware
}

// NewPipeline builds a pipeline from stages.
func NewPipeline(stages ...Middleware) *Pipeline {
	return &Pipeline{stages: stages}
}

// Check runs the chain; middleware errors fail open (a broken classifier
// must not take chat down — SPEC.md §13 fail-open default).
func (p *Pipeline) Check(ctx context.Context, appID string, msg *domain.Message) Decision {
	for _, stage := range p.stages {
		decision, err := stage.Check(ctx, appID, msg)
		if err != nil {
			continue
		}
		if decision.Verdict != VerdictAllow {
			return decision
		}
	}
	return Decision{Verdict: VerdictAllow}
}

// BlocklistMatcher checks message text against a named word list
// (SPEC.md §11.1). Modes: exact (word match), wildcard (* expands), regex.
type BlocklistMatcher struct {
	list *domain.Blocklist

	once     sync.Once
	patterns []*regexp.Regexp
}

// NewBlocklistMatcher wraps a blocklist.
func NewBlocklistMatcher(list *domain.Blocklist) *BlocklistMatcher {
	return &BlocklistMatcher{list: list}
}

func (m *BlocklistMatcher) compile() {
	for _, word := range m.list.Words {
		var expr string
		switch m.list.Mode {
		case "regex":
			expr = word
		case "wildcard":
			expr = `(?i)\b` + strings.ReplaceAll(regexp.QuoteMeta(word), `\*`, `\w*`) + `\b`
		default: // exact
			expr = `(?i)\b` + regexp.QuoteMeta(word) + `\b`
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			continue // invalid patterns are skipped, not fatal
		}
		m.patterns = append(m.patterns, re)
	}
}

// Matches reports whether text trips the list.
func (m *BlocklistMatcher) Matches(text string) bool {
	m.once.Do(m.compile)
	for _, re := range m.patterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// Check implements Middleware.
func (m *BlocklistMatcher) Check(_ context.Context, _ string, msg *domain.Message) (Decision, error) {
	if !m.Matches(msg.Text) {
		return Decision{Verdict: VerdictAllow}, nil
	}
	reason := "blocklist:" + m.list.Name
	switch m.list.Behavior {
	case domain.AutomodBehaviorBlock:
		return Decision{Verdict: VerdictBlock, Reason: reason}, nil
	case domain.AutomodBehaviorShadowBlock:
		return Decision{Verdict: VerdictShadow, Reason: reason}, nil
	default:
		return Decision{Verdict: VerdictFlag, Reason: reason}, nil
	}
}

// DefaultProfanityList is a small embedded starter list (operators replace
// it with their own; SPEC.md ships a fuller default).
func DefaultProfanityList() *domain.Blocklist {
	return &domain.Blocklist{
		Name:     "profanity_en_v1",
		Mode:     "exact",
		Behavior: domain.AutomodBehaviorFlag,
		Words: []string{
			"asshole", "bastard", "bitch", "bullshit", "cunt", "dickhead",
			"fuck", "fucker", "fucking", "motherfucker", "shit", "slut",
			"twat", "wanker", "whore",
		},
	}
}
