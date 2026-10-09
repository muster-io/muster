// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/outbound"
)

func (f *fakeDB) GetTestDestination(_ context.Context, arg dbgen.GetTestDestinationParams) (
	dbgen.GetTestDestinationRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetTestDestination"); err != nil {
		return dbgen.GetTestDestinationRow{}, err
	}
	for _, ds := range f.dests {
		if ds.typ == delivery.TypeMattermost && !ds.deleted && ds.connection != nil &&
			*ds.connection == arg.ConnectionID.Int64 && ds.publicID == arg.PublicID && ds.channel == arg.ChannelID {
			return dbgen.GetTestDestinationRow{ID: ds.id, PublicID: ds.publicID, Name: ds.name}, nil
		}
	}
	return dbgen.GetTestDestinationRow{}, pgx.ErrNoRows
}

func (f *fakeDB) ProbeBrokenNow(_ context.Context, arg dbgen.ProbeBrokenNowParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ProbeBrokenNow"); err != nil {
		return 0, err
	}
	ds := f.dests[arg.ID]
	if ds == nil || ds.health != "broken" || ds.deleted {
		return 0, nil
	}
	ds.nextProbe, ds.probeOnNext = at(arg.Now), false
	return 1, nil
}

// TestProbeNow is the probe at once after a person saved a Broken Destination (C-11.FR-9, S-047 note): the probe is due
// now, the workers are woken, and the next round probes it; a healthy Destination is left as it is.
func TestProbeNow(t *testing.T) {
	e := newEnv(t)
	e.w.Adapters[delivery.TypeMattermost] = adapterOnly{rec: e.rec}
	e.w.MaxWait = time.Hour
	if err := e.svc.ProbeNow(t.Context(), destMM); err != nil || e.db.notified != 0 ||
		e.db.dests[destMM].nextProbe != nil {
		t.Fatalf("a healthy destination %v %d %+v", err, e.db.notified, e.db.dests[destMM])
	}
	e.breakDest(destMM, "HTTP 404")
	if err := e.svc.ProbeNow(t.Context(), destMM); err != nil || e.db.notified != 1 ||
		!e.db.dests[destMM].nextProbe.Equal(e.business.Now()) {
		t.Fatalf("a broken destination %v %d %+v", err, e.db.notified, e.db.dests[destMM])
	}
	e.round(t)
	if ds := e.db.dests[destMM]; !ds.probeOnNext {
		t.Errorf("the probe did not run at once: %+v", ds)
	}
	for _, q := range []string{"ProbeBrokenNow", "NotifyDelivery"} {
		e.breakDest(destMM, "HTTP 404")
		e.db.fail[q] = errBoom
		if err := e.svc.ProbeNow(t.Context(), destMM); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
		delete(e.db.fail, q)
	}
}

// TestTestDestination is the Destination of a press on a test message (C-16.FR-3): a Mattermost Destination of the
// Connection in the channel of the press, not deleted.
func TestTestDestination(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].channel = "ch-ops"
	d, ok, err := e.svc.TestDestination(t.Context(), connID, "DSAAAAAAAAAA11", "ch-ops")
	if err != nil || !ok || d.ID != destMM || d.Type != delivery.TypeMattermost || *d.Connection != connID {
		t.Fatalf("found %+v %v %v", d, ok, err)
	}
	for _, c := range []struct {
		conn            int64
		id, channel     string
		deleteBeforeRun bool
	}{{connID, "DSAAAAAAAAAA11", "other", false}, {connID + 1, "DSAAAAAAAAAA11", "ch-ops", false},
		{connID, "DSAAAAAAAAAA12", "ch-ops", false}, {connID, "DSAAAAAAAAAA11", "ch-ops", true}} {
		e.db.dests[destMM].deleted = c.deleteBeforeRun
		if _, ok, err := e.svc.TestDestination(t.Context(), c.conn, c.id, c.channel); ok || err != nil {
			t.Errorf("%+v found %v %v", c, ok, err)
		}
	}
	e.db.fail["GetTestDestination"] = errBoom
	if _, _, err := e.svc.TestDestination(t.Context(), connID, "DSAAAAAAAAAA11", "ch-ops"); !errors.Is(err, errBoom) {
		t.Errorf("failed read %v", err)
	}
}

// TestTestOp: a request of a test other than its message goes through the interactive path in the interactive class.
func TestTestOp(t *testing.T) {
	e := newEnv(t)
	conn := int64(connID)
	dest := delivery.Destination{ID: destMM, PublicID: "DSAAAAAAAAAA11", Type: delivery.TypeMattermost,
		Connection: &conn}
	var class outbound.Class
	out, err := e.interactive().Do(t.Context(), delivery.Subject{Destination: &dest}, delivery.TestOp(
		func(_ context.Context, c delivery.Call) delivery.Outcome {
			class = c.Class
			return delivery.Outcome{Kind: delivery.OutcomeOK, Status: 200}
		}))
	if err != nil || out.Status != 200 || class != outbound.ClassInteractive {
		t.Errorf("test op %+v %v %s", out, err, class)
	}
}

// TestStepClasses is C-16.FR-2: each outcome's error class, limited for no token within the budget, and other errors
// of the interactive path returned.
func TestStepClasses(t *testing.T) {
	for k, want := range map[delivery.OutcomeKind]string{delivery.OutcomeOK: "none",
		delivery.OutcomeRetryAfter: "retry_after", delivery.OutcomeTransient: "transient",
		delivery.OutcomeFatal: "fatal", delivery.OutcomeUnknown: "unknown",
		delivery.OutcomeTemplateError: "template_error", delivery.OutcomeMarkupRejected: "unknown",
		delivery.OutcomeGone: "unknown"} {
		if got := delivery.ErrorClass(k); got != want {
			t.Errorf("%s = %s", k, got)
		}
	}
	st, err := delivery.Step("message", delivery.Outcome{Kind: delivery.OutcomeOK}, nil, time.Second)
	if err != nil || !st.OK() || st.Error != "" || st.Duration != time.Second || st.Name != "message" {
		t.Errorf("ok %+v %v", st, err)
	}
	st, _ = delivery.Step("event", delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "answered 404"}, nil, 0)
	if st.OK() || st.ErrorClass != "fatal" || st.Error != "answered 404" {
		t.Errorf("fatal %+v", st)
	}
	st, _ = delivery.Step("event", delivery.Outcome{Kind: delivery.OutcomeTransient}, nil, 0)
	if st.Error != "the request failed: transient" {
		t.Errorf("without text %+v", st)
	}
	for _, e := range []error{&delivery.LimitedError{RetryAfter: time.Second},
		fmt.Errorf("wait: %w", context.DeadlineExceeded)} {
		st, err = delivery.Step("create", delivery.Outcome{}, e, 0)
		if err != nil || st.ErrorClass != "limited" || st.Error == "" {
			t.Errorf("%v: %+v %v", e, st, err)
		}
	}
	// The end of ctx after the call was made is no limited step: something was sent.
	if _, err := delivery.Step("create", delivery.Outcome{Kind: delivery.OutcomeRetryAfter}, context.DeadlineExceeded,
		0); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("after the call %v", err)
	}
	if _, err := delivery.Step("create", delivery.Outcome{}, errBoom, 0); !errors.Is(err, errBoom) {
		t.Errorf("other error %v", err)
	}
}

// TestResponseText is C-16.FR-2: the start of a response body, masked before it is cut, at most 4 KB on a rune
// boundary, invalid UTF-8 replaced.
func TestResponseText(t *testing.T) {
	if delivery.ResponseText(nil, nil) != nil {
		t.Error("an empty body")
	}
	mask := func(s string) string { return strings.ReplaceAll(s, "s3cret", "[redacted]") }
	if got := *delivery.ResponseText([]byte("ok s3cret \xff"), mask); got != "ok [redacted] �" {
		t.Errorf("masked %q", got)
	}
	long := strings.Repeat("a", delivery.MaxResponseBody-3) + "s3cret" + "é"
	got := *delivery.ResponseText([]byte(long), mask)
	if strings.Contains(got, "s3c") || len(got) > delivery.MaxResponseBody || !strings.HasSuffix(got, "[re") {
		t.Errorf("cut %q", got[len(got)-10:])
	}
	got = *delivery.ResponseText([]byte(strings.Repeat("a", delivery.MaxResponseBody-1)+"é"), nil)
	if len(got) != delivery.MaxResponseBody-1 {
		t.Errorf("rune boundary %d", len(got))
	}
}
