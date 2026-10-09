// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// The Audit log action of a Destination test (C-16.FR-5).
const ActionTested = "destination.tested"

// The kinds of the source of a test or a preview (TestSource).
const (
	SourceAlertGroup = "alert_group"
	SourceExample    = "example"
)

// The JSON pointers of the source of a test or a preview.
const (
	pointerSourceKind  = "/source/kind"
	pointerSourceGroup = "/source/alert_group_id"
)

// Source is the source of a test or a preview (TestSource): a recent Alert Group of one of the Destination's Routes,
// by its public_id, or the built-in example.
type Source struct {
	Kind         string
	AlertGroupID string
}

// Samples read the Alert Group a test or a preview renders, declared by their consumer; *messages.Renderer implements
// them. An unknown Alert Group is messages.ErrNotFound.
type Samples interface {
	TestSample(ctx context.Context, publicID string) (*messages.Source, error)
	TestExample(ctx context.Context) (*messages.Source, error)
}

// Tester runs the test of one Destination type through the interactive path and renders its preview without sending
// anything (C-16), declared by its consumer; *mattermost.Tester, *telegram.Tester and *webhooks.Tester implement it.
// An error is a failure of Muster itself, never of the Destination, whose failures are the steps' classes.
type Tester interface {
	Test(ctx context.Context, in delivery.TestInput) ([]delivery.TestStep, error)
	Preview(ctx context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error)
}

// TestResult is the result of testDestination: each step, and the health after the test.
type TestResult struct {
	Steps  []delivery.TestStep
	Health Health
}

// Test runs the Destination test of the Destination publicID from s (C-16.FR-1 to FR-3, FR-5, FR-7): one test message
// to a messenger, or the test event and the "create" request of an outgoing webhook, through the interactive path. A
// step that found no limiter token in time is limited and sent nothing; the operation still succeeds. When every step
// succeeded on a Broken Destination its Broken state ends, as after a successful probe; a failed test changes nothing.
// Every test is recorded in the Audit log as destination.tested, with the source and each step's class, and logged as
// destination_tested.
func (s *Service) Test(ctx context.Context, r Requester, publicID string, src Source) (TestResult, error) {
	d, tester, err := s.tester(ctx, publicID)
	if err != nil {
		return TestResult{}, err
	}
	in, err := s.input(ctx, d, src)
	if err != nil {
		return TestResult{}, err
	}
	in.Actor = groups.Actor{Kind: r.Actor.Kind, Transport: r.Transport, Person: r.Actor, Address: r.Address}
	steps, err := tester.Test(ctx, in)
	if err != nil {
		return TestResult{}, fmt.Errorf("test destination %s: %w", d.PublicID, err)
	}
	if err := s.recordTest(ctx, r, d, src, steps); err != nil {
		return TestResult{}, err
	}
	out := TestResult{Steps: steps, Health: d.Health}
	if passed(steps) && d.Health.State == healthBroken {
		if err := s.healthy(ctx, d.ID); err != nil {
			return TestResult{}, err
		}
		after, err := s.Get(ctx, publicID)
		if err != nil {
			return TestResult{}, err
		}
		out.Health = after.Health
	}
	return out, nil
}

// passed reports whether a test had steps and each succeeded.
func passed(steps []delivery.TestStep) bool {
	return len(steps) > 0 && !slices.ContainsFunc(steps, func(st delivery.TestStep) bool { return !st.OK() })
}

// tester reads the Destination publicID and the Tester of its type.
func (s *Service) tester(ctx context.Context, publicID string) (Destination, Tester, error) {
	d, err := s.Get(ctx, publicID)
	if err != nil {
		return Destination{}, nil, err
	}
	t := s.writer.Testers[d.Type]
	if t == nil || s.writer.Samples == nil {
		return Destination{}, nil, fmt.Errorf("no test of destinations of the type %s", d.Type)
	}
	return d, t, nil
}

// input is the test or preview of d from the source src: an Alert Group that is not of one of the Destination's
// Routes is unknown_id at /source/alert_group_id, as one that does not exist.
func (s *Service) input(ctx context.Context, d Destination, src Source) (delivery.TestInput, error) {
	in := delivery.TestInput{Destination: delivery.Destination{ID: d.ID, PublicID: d.PublicID, Name: d.Name,
		Type: d.Type}}
	var err error
	switch src.Kind {
	case SourceExample:
		in.Source, err = s.writer.Samples.TestExample(ctx)
	case SourceAlertGroup:
		id := strings.TrimSpace(src.AlertGroupID)
		if id == "" {
			return delivery.TestInput{}, &FieldError{Pointer: pointerSourceGroup, Code: CodeRequired,
				Detail: "The source alert_group needs an alert_group_id."}
		}
		in.Source, err = s.writer.Samples.TestSample(ctx, id)
		if errors.Is(err, messages.ErrNotFound) || (err == nil && !slices.ContainsFunc(d.Routes,
			func(r RouteRef) bool { return r.PublicID == in.Source.Route.PublicID })) {
			return delivery.TestInput{}, &FieldError{Pointer: pointerSourceGroup, Code: CodeUnknownID,
				Detail: "No such Alert Group of a Route of this Destination."}
		}
	default:
		return delivery.TestInput{}, &FieldError{Pointer: pointerSourceKind, Code: CodeInvalidFormat,
			Detail: "The source is alert_group or example."}
	}
	if err != nil {
		return delivery.TestInput{}, fmt.Errorf("read the source of the test: %w", err)
	}
	return in, nil
}

// recordTest writes the Audit log entry destination.tested of a test of d — the source and each step's name and
// class — and its log line.
func (s *Service) recordTest(ctx context.Context, r Requester, d Destination, src Source,
	steps []delivery.TestStep) error {
	source := map[string]any{"kind": src.Kind}
	if src.Kind == SourceAlertGroup {
		source["alert_group_id"] = strings.TrimSpace(src.AlertGroupID)
	}
	list := make([]map[string]any, 0, len(steps))
	names := make([]string, 0, len(steps))
	for _, st := range steps {
		list = append(list, map[string]any{"name": st.Name, "error_class": st.ErrorClass})
		names = append(names, st.Name+":"+st.ErrorClass)
	}
	err := s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		return s.writer.Audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionTested, Resource: audit.Resource{Type: ResourceDestination, PublicID: d.PublicID,
				Name: d.Name}, Details: map[string]any{"source": source, "steps": list}, SourceAddress: r.Address})
	})
	if err != nil {
		return fmt.Errorf("record the test of destination %s: %w", d.PublicID, err)
	}
	if s.writer.Log != nil {
		s.writer.Log.Log(ctx, logging.DestinationTested, logging.F("destination", d.PublicID),
			logging.F("steps", strings.Join(names, ",")))
	}
	return nil
}
