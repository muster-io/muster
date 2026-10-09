// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// fakeSamples are the sources of tests: the example, and Alert Groups by public_id.
type fakeSamples struct {
	groups map[string]*messages.Source
	err    error
}

func (f *fakeSamples) TestSample(_ context.Context, id string) (*messages.Source, error) {
	if f.err != nil {
		return nil, f.err
	}
	src, ok := f.groups[id]
	if !ok {
		return nil, messages.ErrNotFound
	}
	return src, nil
}

func (f *fakeSamples) TestExample(context.Context) (*messages.Source, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &messages.Source{Number: 1, Title: "example"}, nil
}

// fakeTester answers each test with its steps and each preview with its items, and records what it was given.
type fakeTester struct {
	steps []delivery.TestStep
	items []delivery.PreviewItem
	err   error
	ins   []delivery.TestInput
}

func (f *fakeTester) Test(_ context.Context, in delivery.TestInput) ([]delivery.TestStep, error) {
	f.ins = append(f.ins, in)
	return f.steps, f.err
}

func (f *fakeTester) Preview(_ context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error) {
	f.ins = append(f.ins, in)
	return f.items, f.err
}

type testEnv struct {
	s       *Service
	store   *fakeStore
	w       *saveWriter
	healed  *[]int64
	samples *fakeSamples
	tester  *fakeTester
	log     *bytes.Buffer
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	s, store, w, _, healed := newSaver(t)
	e := &testEnv{s: s, store: store, w: w, healed: healed, log: &bytes.Buffer{},
		samples: &fakeSamples{groups: map[string]*messages.Source{
			"AGAAAAAAAAAA21": {Number: 21, Route: messages.RouteRef{PublicID: "RTAAAAAAAAAAA1"}},
			"AGAAAAAAAAAA22": {Number: 22, Route: messages.RouteRef{PublicID: "RTAAAAAAAAAAA9"}}}},
		tester: &fakeTester{steps: []delivery.TestStep{{Name: "message", ErrorClass: "none"},
			{Name: "press", ErrorClass: "none"}}}}
	s.writer.Samples, s.writer.Log = e.samples, logging.New(e.log, logging.LevelInfo)
	s.writer.Testers = map[string]Tester{TypeMattermost: e.tester, TypeTelegram: e.tester, TypeWebhook: e.tester}
	return e
}

// TestDestinationTest is C-16.FR-1, FR-5 and AC-1 at the service: the example source reaches the Tester of the type,
// with the Destination and the person; the result keeps every step and the health; the test is recorded once as
// destination.tested with the source and each step's class, and logged.
func TestDestinationTest(t *testing.T) {
	e := newTestEnv(t)
	res, err := e.s.Test(t.Context(), saver, "DSAAAAAAAAAAA1", Source{Kind: SourceExample})
	if err != nil || len(res.Steps) != 2 || res.Health.State != "healthy" || len(*e.healed) != 0 {
		t.Fatalf("test %+v %v", res, err)
	}
	in := e.tester.ins[0]
	if in.Destination.ID != 1 || in.Destination.Type != TypeMattermost || in.Source.Title != "example" ||
		in.Actor.Person.PublicID != "USAAAAAAAAAAA1" || in.Actor.Transport != saver.Transport {
		t.Errorf("input %+v", in)
	}
	if len(e.w.audit) != 1 || e.w.audit[0].Action != ActionTested || e.w.audit[0].ResourcePublicID.String !=
		"DSAAAAAAAAAAA1" {
		t.Fatalf("audit %+v", e.w.audit)
	}
	var details map[string]any
	_ = json.Unmarshal(e.w.audit[0].Details, &details)
	if b, _ := json.Marshal(details); string(b) != `{"source":{"kind":"example"},"steps":[{"error_class":"none",`+
		`"name":"message"},{"error_class":"none","name":"press"}]}` {
		t.Errorf("details %s", b)
	}
	if !strings.Contains(e.log.String(), `"event":"destination_tested"`) ||
		!strings.Contains(e.log.String(), `"steps":"message:none,press:none"`) {
		t.Errorf("log %s", e.log)
	}
	// A recent Alert Group of a Route of the Destination.
	if _, err := e.s.Test(t.Context(), saver, "DSAAAAAAAAAAA1", Source{Kind: SourceAlertGroup,
		AlertGroupID: " AGAAAAAAAAAA21 "}); err != nil || e.tester.ins[1].Source.Number != 21 {
		t.Fatalf("alert group %v", err)
	}
	_ = json.Unmarshal(e.w.audit[1].Details, &details)
	if src := details["source"].(map[string]any); src["alert_group_id"] != "AGAAAAAAAAAA21" {
		t.Errorf("source %v", src)
	}
}

// TestDestinationTestBroken is C-16.FR-7 and AC-5: a test whose every step succeeded ends the Broken state and answers
// the health after it; a failed test changes nothing, and both are recorded.
func TestDestinationTestBroken(t *testing.T) {
	e := newTestEnv(t)
	e.tester.steps = []delivery.TestStep{{Name: "message", ErrorClass: "fatal", Error: "kicked"}}
	res, err := e.s.Test(t.Context(), saver, "DSAAAAAAAAAAA2", Source{Kind: SourceExample})
	if err != nil || res.Health.State != "broken" || len(*e.healed) != 0 || len(e.w.audit) != 1 {
		t.Fatalf("failed %+v %v %v", res, err, *e.healed)
	}
	e.tester.steps = []delivery.TestStep{{Name: "message", ErrorClass: "none"}}
	res, err = e.s.Test(t.Context(), saver, "DSAAAAAAAAAAA2", Source{Kind: SourceExample})
	if err != nil || res.Health.State != "healthy" || len(*e.healed) != 1 || (*e.healed)[0] != 2 ||
		len(e.w.audit) != 2 {
		t.Fatalf("passed %+v %v %v", res, err, *e.healed)
	}
	// No steps is no success.
	e.store.rows[1].Health = "broken"
	e.tester.steps = nil
	if res, _ := e.s.Test(t.Context(), saver, "DSAAAAAAAAAAA2", Source{Kind: SourceExample}); res.Health.State !=
		"broken" {
		t.Errorf("no steps %+v", res)
	}
}

// TestDestinationTestRefusals: a source that is not an Alert Group of a Route of the Destination is unknown_id, one
// without its id required, another kind invalid_format; an unknown Destination is ErrNotFound; and failures of the
// sources, the Tester, the end of the Broken state and the Audit log are errors, with nothing recorded before.
func TestDestinationTestRefusals(t *testing.T) {
	e := newTestEnv(t)
	ctx := t.Context()
	for _, c := range []struct {
		src     Source
		pointer string
		code    string
	}{
		{Source{Kind: SourceAlertGroup, AlertGroupID: "AGAAAAAAAAAA22"}, "/source/alert_group_id", CodeUnknownID},
		{Source{Kind: SourceAlertGroup, AlertGroupID: "AGAAAAAAAAAA99"}, "/source/alert_group_id", CodeUnknownID},
		{Source{Kind: SourceAlertGroup}, "/source/alert_group_id", CodeRequired},
		{Source{Kind: "other"}, "/source/kind", CodeInvalidFormat},
	} {
		_, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA1", c.src)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("%+v: %v", c.src, err)
		}
		if _, err := e.s.Preview(ctx, "DSAAAAAAAAAAA1", c.src); !errors.As(err, &fe) {
			t.Errorf("preview %+v: %v", c.src, err)
		}
	}
	if _, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA9", Source{Kind: SourceExample}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown %v", err)
	}
	e.samples.err = errBoom
	if _, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA1", Source{Kind: SourceExample}); !errors.Is(err, errBoom) {
		t.Errorf("samples %v", err)
	}
	e.samples.err = nil
	e.tester.err = errBoom
	if _, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA1", Source{Kind: SourceExample}); !errors.Is(err, errBoom) {
		t.Errorf("tester %v", err)
	}
	if _, err := e.s.Preview(ctx, "DSAAAAAAAAAAA1", Source{Kind: SourceExample}); !errors.Is(err, errBoom) {
		t.Errorf("preview tester %v", err)
	}
	e.tester.err = nil
	if len(e.w.audit) != 0 {
		t.Errorf("recorded %+v", e.w.audit)
	}
	e.w.auditErr = errBoom
	if _, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA1", Source{Kind: SourceExample}); !errors.Is(err, errBoom) {
		t.Errorf("audit %v", err)
	}
	e.w.auditErr = nil
	e.tester.steps = []delivery.TestStep{{Name: "message", ErrorClass: "none"}}
	e.store.rows[1].ID = 99 // the end of its Broken state fails
	if _, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA2", Source{Kind: SourceExample}); !errors.Is(err, errBoom) {
		t.Errorf("healthy %v", err)
	}
	e.store.rows[1].ID = 2
	e.store.fail["ListDestinationRoutes"] = nil
	calls := 0
	e.s.writer.Healthy = func(context.Context, int64) error {
		calls++
		e.store.fail["GetDestination"] = errBoom
		return nil
	}
	if _, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA2", Source{Kind: SourceExample}); !errors.Is(err, errBoom) ||
		calls != 1 {
		t.Errorf("read after %v", err)
	}
	delete(e.store.fail, "GetDestination")
	delete(e.s.writer.Testers, TypeMattermost)
	if _, err := e.s.Test(ctx, saver, "DSAAAAAAAAAAA1", Source{Kind: SourceExample}); err == nil {
		t.Error("a type without a tester")
	}
}

// TestDestinationPreview is C-16.FR-4 at the service: the items of the Tester of the type, nothing recorded.
func TestDestinationPreview(t *testing.T) {
	e := newTestEnv(t)
	text := "#1 example"
	e.tester.items = []delivery.PreviewItem{{Name: "message", Format: "markdown", Text: &text}}
	items, err := e.s.Preview(t.Context(), "DSAAAAAAAAAAA3", Source{Kind: SourceExample})
	if err != nil || len(items) != 1 || *items[0].Text != text || len(e.w.audit) != 0 ||
		e.tester.ins[0].Destination.Type != TypeWebhook {
		t.Fatalf("preview %+v %v", items, err)
	}
	if _, err := e.s.Preview(t.Context(), "DSAAAAAAAAAAA9", Source{Kind: SourceExample}); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("unknown %v", err)
	}
}

// TestSaveBrokenWebhookProbes is the note of S-047 (C-11.FR-9): saving a Broken outgoing webhook, changed or not, makes
// its probe due at once; a healthy one is not probed; a probe that cannot be scheduled fails the save's answer.
func TestSaveBrokenWebhookProbes(t *testing.T) {
	e := newTestEnv(t)
	var probed []int64
	e.s.writer.Probe = func(_ context.Context, id int64) error {
		probed = append(probed, id)
		if id == 99 {
			return errBoom
		}
		return nil
	}
	ctx := t.Context()
	in := webhookInput("hook", "https://example.org/v2")
	if _, err := e.s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); err != nil || len(probed) != 0 {
		t.Fatalf("healthy %v %v", err, probed)
	}
	e.store.rows[2].Health = "broken"
	if _, err := e.s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); err != nil || len(probed) != 1 || probed[0] != 3 {
		t.Fatalf("changed %v %v", err, probed)
	}
	set, _ := json.Marshal(in.Mentions)
	e.store.rows[2].Mentions, e.store.rows[2].LimiterLimit, e.store.rows[2].LimiterPerSeconds = set, 5, 1
	e.store.rows[2].WebhookEventsConfig = in.Webhook.Events.JSON()
	e.store.rows[2].Proxy, e.store.rows[2].ProxyPasswordSet = []byte(`{"enabled":false}`), false
	sets := len(e.w.hookSets)
	if _, err := e.s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); err != nil || len(probed) != 2 ||
		len(e.w.hookSets) != sets {
		t.Fatalf("unchanged %v %v %d", err, probed, len(e.w.hookSets)-sets)
	}
	e.store.rows[2].ID = 99
	if _, err := e.s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); !errors.Is(err, errBoom) {
		t.Errorf("failed probe %v", err)
	}
	e.s.writer.Probe = nil
	if _, err := e.s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); err != nil {
		t.Errorf("without a probe %v", err)
	}
}
