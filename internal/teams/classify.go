// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"context"
	"fmt"
	"sync"

	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/teams/dbgen"
)

// The rules by which an Alert belongs to a Team (C-25.FR-5): the value of the Team label equal to the Team's slug, or
// one of the Team's Team matchers.
const (
	RuleLabel   = "label"
	RuleMatcher = "matcher"
)

// ClassifyQueries are the queries of the classification, run in the Snapshot's transaction.
type ClassifyQueries interface {
	ListClassifiedTeams(ctx context.Context, orgID int64) ([]dbgen.ListClassifiedTeamsRow, error)
	ListClassifiedMatchers(ctx context.Context, orgID int64) ([]dbgen.ListClassifiedMatchersRow, error)
}

// Membership is a Team an Alert belongs to, with the rule that decided it and, for the rule matcher, the Team matcher
// in the Alertmanager syntax.
type Membership struct {
	TeamID  int64
	Rule    string
	Matcher string
}

// Classification is what the classification answers for the labels of one Alert: the Teams it belongs to, ordered by
// Team, and the value of the Team label that names no Team, empty when the label is missing, empty or names a Team.
type Classification struct {
	Teams        []Membership
	UnknownValue string
}

// Stamp identifies a state of the Teams and of the Team label: organizations.teams_version, which every change of a
// Team or of the Team label raises, and the Team label itself.
type Stamp struct {
	Label   string
	Version int64
}

// classified is a Team that is not deleted with its Team matchers compiled once.
type classified struct {
	id       int64
	matchers []matchers.Matcher
}

// Classes are the Teams that are not deleted as of a Stamp, ready to classify Alerts.
type Classes struct {
	stamp  Stamp
	teams  []classified
	bySlug map[string]int64
}

// Classify answers, for the labels of an Alert with the Static labels of its Integration applied, the Teams it belongs
// to (C-25.FR-5): the Team whose slug equals the value of the Team label exactly, by the rule label, and every Team one
// of whose Team matchers picks it, a missing label counting as the empty value, by the rule matcher with the first
// such matcher; a Team both pick belongs by the label. A value of the Team label that names no Team is UnknownValue.
func (c *Classes) Classify(labels map[string]string) Classification {
	var out Classification
	value := labels[c.stamp.Label]
	byLabel, named := c.bySlug[value]
	if value != "" && !named {
		out.UnknownValue = value
	}
	for _, t := range c.teams {
		if named && t.id == byLabel {
			out.Teams = append(out.Teams, Membership{TeamID: t.id, Rule: RuleLabel})
			continue
		}
		for _, m := range t.matchers {
			if m.Matches(labels) {
				out.Teams = append(out.Teams, Membership{TeamID: t.id, Rule: RuleMatcher, Matcher: m.String()})
				break
			}
		}
	}
	return out
}

// Classifier classifies the Alerts that start firing by their Teams (C-25.FR-5) for the Router. The Teams that are not
// deleted, their slugs and their compiled Team matchers are cached per replica: each Snapshot reads the Stamp in its
// own transaction, as the Routes' stamp, and a Stamp that differs from the cached one reads the Teams again in that
// transaction, so that a change made on any replica applies to the next Snapshot. The cache holds one state only.
type Classifier struct {
	orgID int64

	mu     sync.Mutex
	cached *Classes
}

// NewClassifier returns the Classifier of the Organization orgID.
func NewClassifier(orgID int64) *Classifier {
	return &Classifier{orgID: orgID}
}

// At returns the Teams as of the Stamp st: the cached ones when their Stamp is the same, otherwise those q reads,
// which then replace the cache.
func (c *Classifier) At(ctx context.Context, q ClassifyQueries, st Stamp) (*Classes, error) {
	c.mu.Lock()
	cached := c.cached
	c.mu.Unlock()
	if cached != nil && cached.stamp == st {
		return cached, nil
	}
	out, err := c.load(ctx, q, st)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cached = out
	c.mu.Unlock()
	return out, nil
}

// load reads the Teams that are not deleted with their Team matchers and compiles the matchers once.
func (c *Classifier) load(ctx context.Context, q ClassifyQueries, st Stamp) (*Classes, error) {
	rows, err := q.ListClassifiedTeams(ctx, c.orgID)
	if err != nil {
		return nil, fmt.Errorf("list the teams to classify alerts by: %w", err)
	}
	out := &Classes{stamp: st, teams: make([]classified, len(rows)), bySlug: make(map[string]int64, len(rows))}
	index := make(map[int64]int, len(rows))
	for i, r := range rows {
		out.teams[i] = classified{id: r.ID}
		out.bySlug[r.Slug], index[r.ID] = r.ID, i
	}
	ms, err := q.ListClassifiedMatchers(ctx, c.orgID)
	if err != nil {
		return nil, fmt.Errorf("list the team matchers to classify alerts by: %w", err)
	}
	for _, m := range ms {
		i, ok := index[m.TeamID]
		if !ok {
			continue
		}
		compiled, err := matchers.New(m.Label, matchers.Op(m.Op), m.Value)
		if err != nil {
			return nil, fmt.Errorf("compile a team matcher of the team %d: %w", m.TeamID, err)
		}
		out.teams[i].matchers = append(out.teams[i].matchers, compiled)
	}
	return out, nil
}
