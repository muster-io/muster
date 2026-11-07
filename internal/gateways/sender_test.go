// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// signKeys sign with one fixed key, as an opened Keyring does.
type signKeys struct{}

func (signKeys) Sign(keyring.Purpose, []byte) ([]byte, string, error) {
	return bytes.Repeat([]byte{7}, 32), "k-0123456789abcdef", nil
}
func (signKeys) VerifyPrefix(keyring.Purpose, string, []byte, []byte) (bool, error) { return true, nil }
func (signKeys) KeyIDs() []string                                                   { return []string{"k-0123456789abcdef"} }

// failKeys cannot sign.
type failKeys struct{ signKeys }

func (failKeys) Sign(keyring.Purpose, []byte) ([]byte, string, error) {
	return nil, "", errors.New("the keyring is not open")
}

// sent is one call of the gateway.
type sent struct {
	call delivery.Call
	n    delivery.Notification
}

// notifier is the Notification gateway of the tests: it records each notification and answers with the next scripted
// outcome, ok with the id n<N> when none is left.
type notifier struct {
	mu       sync.Mutex
	sent     []sent
	outcomes []delivery.Outcome
}

func (g *notifier) Notify(_ context.Context, c delivery.Call, n delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, sent{call: c, n: n})
	if len(g.outcomes) > 0 {
		o := g.outcomes[0]
		g.outcomes = g.outcomes[1:]
		return o
	}
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n" + strconv.Itoa(len(g.sent))}
}

func (g *notifier) script(o ...delivery.Outcome) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.outcomes = append(g.outcomes, o...)
}

func (g *notifier) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sent)
}

func (g *notifier) last() sent {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sent[len(g.sent)-1]
}

// limiter is the limiter of the tests: a token is free unless deny, due at due then; holds records the holds.
type limiter struct {
	deny  bool
	due   time.Time
	holds []time.Time
	err   error
}

func (l *limiter) ConnectionToken(_ context.Context, _ DBTX, orgID, connection int64, _ time.Time) (bool, time.Time,
	error) {
	if orgID != 1 || connection == 0 {
		return false, time.Time{}, errors.New("the wrong connection")
	}
	return !l.deny, l.due, l.err
}

func (l *limiter) HoldConnection(_ context.Context, _ DBTX, _, _ int64, until time.Time) error {
	l.holds = append(l.holds, until)
	return l.err
}

// pages records page_not_delivered as the dispatcher would.
type pages struct {
	mu    sync.Mutex
	pages []recordedPage
	err   error
	ran   int
}

type recordedPage struct {
	ids  []int64
	page groups.Page
}

func (p *pages) PageNotDelivered(_ context.Context, _ groups.DBTX, ids []int64, page groups.Page) (
	func(context.Context), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	p.pages = append(p.pages, recordedPage{ids: slices.Clone(ids), page: page})
	return func(context.Context) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.ran++
	}, nil
}

// senderEnv is a sender of Organization 1 over the fake calls, with its queue, clocks and the log it wrote.
type senderEnv struct {
	s        *Sender
	q        *Calls
	f        *fakeCalls
	gw       *notifier
	lim      *limiter
	pages    *pages
	business *clock.Manual
	real     *clock.Manual
	log      *bytes.Buffer
}

func newSender(t *testing.T) *senderEnv {
	t.Helper()
	q, f, business := queue()
	realClock := clock.NewManual(c0)
	e := &senderEnv{q: q, f: f, gw: &notifier{}, lim: &limiter{}, pages: &pages{}, business: business,
		real: realClock,
		log:  &bytes.Buffer{}}
	e.s = &Sender{OrgID: 1, Store: f, Lease: db.Lease{Owner: "r1", Duration: SenderLease,
		Clocks: clock.Clocks{Business: business, Real: realClock}}, Gateway: e.gw, Limiter: e.lim, Keys: signKeys{},
		Pages: e.pages, Log: logging.New(e.log, logging.LevelInfo), Random: func() float64 { return 0 }}
	return e
}

// round runs one round of the sender.
func (e *senderEnv) round(t *testing.T) time.Duration {
	t.Helper()
	next, err := e.s.Round(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return next
}

// advance moves both clocks.
func (e *senderEnv) advance(d time.Duration) {
	e.business.Advance(d)
	e.real.Advance(d)
}

func (e *senderEnv) page(t *testing.T, user, gateway, group int64) {
	t.Helper()
	page(t, e.q, user, gateway, group)
}

// counted is the value of muster_gateway_calls_total for the gateway 10 and outcome.
func counted(outcome string) uint64 {
	return metrics.GatewayCalls.With("CN00000000GW10", outcome).Get()
}

// TestSenderCombining is C-30.AC-3 and FR-2, FR-5 on a manual clock: of three Pages to one person within
// gateway.combine_window, the first reaches the gateway at once, under the key calls/<public_id> in the delivery
// client class, with the person's confirmed contact, a text that names the Alert Group by number, title and Severity
// level and says how to acknowledge, and action data that names the call, signed, within 64 bytes; the other two wait
// as one call until the window after the first one ended has passed, and when the second one is acknowledged
// meanwhile, that call is about the third only. Accepted attempts count as accepted.
func TestSenderCombining(t *testing.T) {
	e := newSender(t)
	before := counted("accepted")
	e.page(t, 1, 10, 1)
	e.round(t)
	if e.gw.count() != 1 {
		t.Fatalf("%d notifications", e.gw.count())
	}
	first := e.gw.last()
	call := e.f.calls[0]
	if first.call.Class != outbound.ClassDelivery || first.call.Key != "calls/"+call.publicID ||
		first.n.Connection != 10 || first.n.Contact != "+10000000001" || first.n.Language != "en" ||
		first.n.Text != "Muster: 1 Alert Group needs you. #1 Disk1, warning. Press 1 to acknowledge." ||
		first.n.Action == nil || first.n.Action.Label != "Press 1 to acknowledge." ||
		len(first.n.Action.Data) > buttons.MaxLen {
		t.Fatalf("the first call %+v %+v", first.call, first.n)
	}
	a, err := buttons.Verify(signKeys{}, first.n.Action.Data, "")
	if err != nil || a.Subject != buttons.SubjectCall || a.PublicID != call.publicID ||
		a.Command != buttons.CommandAcknowledge {
		t.Errorf("the action data names %+v, %v", a, err)
	}
	if call.state != CallAccepted || call.notification.String != "n1" || call.attempts != 1 ||
		call.contactID.Int64 != 501 || !call.ended.Time.Equal(c0) || call.owner != "" {
		t.Errorf("the first call %+v", call)
	}
	if got := counted("accepted"); got != before+1 {
		t.Errorf("accepted %d, want %d", got, before+1)
	}

	e.advance(10 * time.Second)
	e.page(t, 1, 10, 2)
	e.page(t, 1, 10, 3)
	e.s.MaxWait = 5 * time.Minute
	if next := e.round(t); e.gw.count() != 1 || next != CombineWindow-10*time.Second {
		t.Fatalf("within the window: %d notifications, next round in %v", e.gw.count(), next)
	}
	e.f.setStatus(2, "acknowledged")
	e.advance(51 * time.Second)
	e.round(t)
	if e.gw.count() != 2 {
		t.Fatalf("%d notifications after the window", e.gw.count())
	}
	second := e.gw.last()
	if second.n.Text != "Muster: 1 Alert Group needs you. #3 Disk3, warning. Press 1 to acknowledge." ||
		len(second.n.Action.Data) > buttons.MaxLen || second.call.Key == first.call.Key {
		t.Errorf("the second call %+v %+v", second.call, second.n)
	}
	if got := e.f.itemsOf(e.f.calls[1].id); !slices.Equal(got, []string{"+Disk3", "-Disk2"}) {
		t.Errorf("the second call is about %v", got)
	}
	if strings.Contains(e.log.String(), "+10000000001") || strings.Contains(e.log.String(), "Disk") {
		t.Errorf("the log carries the contact or the text:\n%s", e.log)
	}
}

// TestSenderText is C-30.FR-5 and C-12.FR-3: the text of a call about several Alert Groups names each by number,
// title and Severity level, in the language of the person's profile, Russian with its plurals.
func TestSenderText(t *testing.T) {
	en := []string{"Muster: 2 Alert Groups need you. #1 Disk1, warning. #4 Down, critical. Press 1 to acknowledge."}
	e := newSender(t)
	e.page(t, 1, 10, 1)
	e.page(t, 1, 10, 4)
	e.round(t)
	if got := e.gw.last().n.Text; got != en[0] {
		t.Errorf("English: %q", got)
	}
	for n, want := range map[int]string{1: "Muster: вас ждёт 1 группа алертов.", 2: "Muster: вас ждут 2 группы алертов.",
		4: "Muster: вас ждут 4 группы алертов."} {
		e := newSender(t)
		for id := range int64(n) {
			e.f.group(10+id, "G", "warning")
			e.page(t, 2, 10, 10+id)
		}
		e.round(t)
		got := e.gw.last()
		if !strings.HasPrefix(got.n.Text, want+" #10 G, уровень warning.") || got.n.Language != "ru" ||
			!strings.HasSuffix(got.n.Text, "Нажмите 1, чтобы подтвердить.") ||
			got.n.Action.Label != "Нажмите 1, чтобы подтвердить." {
			t.Errorf("%d in Russian: %+v", n, got.n)
		}
	}
	if got := callText("ru", make([]dbgen.ListCallGroupsRow, 5)); !strings.HasPrefix(got, "Muster: вас ждут 5 групп алертов.") {
		t.Errorf("5 in Russian: %q", got)
	}
}

// TestSenderRetries is C-30.AC-3 and C-11.FR-2 for calls: an attempt the gateway answers transient is retried after
// delivery.transient_backoff with the same Idempotency-Key, contact and text until it is accepted, counting each
// attempt; the Page that comes due meanwhile does not join the call being retried but waits for the next one, due
// gateway.combine_window after the retried call ended; a replayed answer is accepted.
func TestSenderRetries(t *testing.T) {
	e := newSender(t)
	transient := counted("transient")
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeTransient, Error: "bridge down"},
		delivery.Outcome{Kind: delivery.OutcomeTransient, Error: "bridge down"},
		delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n1", Replayed: true})
	e.page(t, 1, 10, 1)
	e.round(t)
	call := e.f.calls[0]
	if call.state != CallSending || call.attempts != 1 || call.errorClass.String != "transient" ||
		!call.nextAttempt.Time.Equal(c0.Add(delivery.TransientFirstStep)) || call.owner != "" {
		t.Fatalf("after the first attempt %+v", call)
	}
	e.page(t, 1, 10, 2)
	e.round(t)
	if e.gw.count() != 1 {
		t.Fatalf("a retry before its backoff: %d", e.gw.count())
	}
	for range 2 {
		e.advance(delivery.TransientFirstStep)
		e.round(t)
	}
	if e.gw.count() != 3 {
		t.Fatalf("%d attempts", e.gw.count())
	}
	for _, s := range e.gw.sent {
		if s.call.Key != e.gw.sent[0].call.Key || s.n.Text != e.gw.sent[0].n.Text || s.n.Contact != "+10000000001" {
			t.Errorf("an attempt %+v differs from the first %+v", s, e.gw.sent[0])
		}
	}
	if !strings.Contains(e.gw.sent[0].n.Text, "#1 Disk1") || strings.Contains(e.gw.sent[2].n.Text, "Disk2") {
		t.Errorf("the call took an Alert Group after its first attempt: %q", e.gw.sent[2].n.Text)
	}
	call = e.f.calls[0]
	if call.state != CallAccepted || call.attempts != 3 {
		t.Errorf("the retried call %+v", call)
	}
	if got := counted("transient"); got != transient+2 {
		t.Errorf("transient %d, want %d", got, transient+2)
	}
	if !strings.Contains(e.log.String(), `"replayed":true`) {
		t.Errorf("the replayed answer was not logged:\n%s", e.log)
	}
	waiting := e.f.calls[1]
	if waiting.state != CallWaiting || !waiting.notBefore.Time.Equal(e.business.Now().Add(CombineWindow)) ||
		!slices.Equal(e.f.itemsOf(waiting.id), []string{"+Disk2"}) {
		t.Errorf("the call waiting behind it %+v %v", waiting, e.f.itemsOf(waiting.id))
	}
}

// TestSenderRetriesEnd: once every Alert Group of a call being retried has stopped, its retries end and it ends
// dropped, which starts the window of the call waiting behind it; while one still fires, it is retried.
func TestSenderRetriesEnd(t *testing.T) {
	e := newSender(t)
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeTransient}, delivery.Outcome{Kind: delivery.OutcomeTransient})
	e.page(t, 1, 10, 1)
	e.page(t, 1, 10, 2)
	e.round(t)
	e.page(t, 1, 10, 3)
	e.f.setStatus(1, "snoozed")
	e.advance(delivery.TransientFirstStep)
	e.round(t)
	if e.gw.count() != 2 || e.f.calls[0].state != CallSending {
		t.Fatalf("one Alert Group still fires: %d attempts, %+v", e.gw.count(), e.f.calls[0])
	}
	e.f.setStatus(2, "resolved")
	e.advance(delivery.TransientFirstStep)
	e.round(t)
	call := e.f.calls[0]
	if e.gw.count() != 2 || call.state != CallDropped || !call.ended.Valid || call.attempts != 2 {
		t.Fatalf("after all stopped: %d attempts, %+v", e.gw.count(), call)
	}
	if !strings.Contains(e.log.String(), `"event":"gateway_call_dropped"`) || e.pages.ran != 0 {
		t.Errorf("no gateway_call_dropped, or a page_not_delivered:\n%s", e.log)
	}
	if w := e.f.calls[1]; !w.notBefore.Time.Equal(e.business.Now().Add(CombineWindow)) {
		t.Errorf("the waiting call %+v", w)
	}
}

// TestSenderDropped: a call whose Alert Groups were all acknowledged before its first attempt is not sent and ends
// dropped; it starts no window, so the next call goes at once.
func TestSenderDropped(t *testing.T) {
	e := newSender(t)
	e.page(t, 1, 10, 1)
	e.f.setStatus(1, "acknowledged")
	e.round(t)
	if e.gw.count() != 0 || e.f.calls[0].state != CallDropped || e.f.calls[0].attempts != 0 ||
		!slices.Equal(e.f.itemsOf(e.f.calls[0].id), []string{"-Disk1"}) {
		t.Fatalf("%d sent, %+v", e.gw.count(), e.f.calls[0])
	}
	e.page(t, 1, 10, 2)
	e.round(t)
	if e.gw.count() != 1 {
		t.Errorf("the next call: %d sent", e.gw.count())
	}
}

// TestSenderNotDelivered is C-30.FR-7 and AC-7: a call the gateway refuses with the class invalid, or fatally, ends
// Not delivered with call_refused; an answer Muster cannot classify, call_unknown; a used-up budget, call_budget_used
// — by attempts or by time; a person without a confirmed contact, no_confirmed_contact without any call; each records
// page_not_delivered for every Alert Group of the call, naming the person and the gateway, and starts the window.
func TestSenderNotDelivered(t *testing.T) {
	cases := []struct {
		name    string
		out     []delivery.Outcome
		reason  string
		class   string
		prepare func(e *senderEnv)
	}{
		{"invalid", []delivery.Outcome{{Kind: delivery.OutcomeUnknown, Status: 400, Error: "Bad number"}},
			groups.PageCallRefused, "unknown", nil},
		{"fatal", []delivery.Outcome{{Kind: delivery.OutcomeFatal, Status: 401}}, groups.PageCallRefused, "fatal", nil},
		{"unknown", []delivery.Outcome{{Kind: delivery.OutcomeUnknown, Status: 200}}, groups.PageCallUnknown,
			"unknown", nil},
		{"unanswered", []delivery.Outcome{{Kind: delivery.OutcomeUnknown, Status: 400, Unanswered: true}},
			groups.PageCallUnknown, "unknown", nil},
		{"gone", []delivery.Outcome{{Kind: delivery.OutcomeGone}}, groups.PageCallUnknown, "unknown", nil},
		{"budget by attempts", []delivery.Outcome{{Kind: delivery.OutcomeTransient}}, groups.PageCallBudgetUsed,
			"transient", func(e *senderEnv) {
				e.f.calls[0].state, e.f.calls[0].attempts = CallSending, delivery.TransientBudgetAttempts-1
				e.f.calls[0].firstAttempt = pgtype.Timestamptz{Time: c0, Valid: true}
				e.f.calls[0].language = pgtype.Text{String: "en", Valid: true}
				e.f.calls[0].text = pgtype.Text{String: "the text", Valid: true}
				e.f.calls[0].contactID = pgtype.Int8{Int64: 501, Valid: true}
			}},
		{"budget by time", []delivery.Outcome{{Kind: delivery.OutcomeTransient}}, groups.PageCallBudgetUsed,
			"transient", func(e *senderEnv) {
				e.f.calls[0].state, e.f.calls[0].attempts = CallSending, 2
				e.f.calls[0].firstAttempt = pgtype.Timestamptz{Time: c0, Valid: true}
				e.f.calls[0].firstFailed = pgtype.Timestamptz{Time: c0.Add(-delivery.TransientBudgetTime - 1),
					Valid: true}
				e.f.calls[0].language = pgtype.Text{String: "en", Valid: true}
				e.f.calls[0].text = pgtype.Text{String: "the text", Valid: true}
				e.f.calls[0].contactID = pgtype.Int8{Int64: 501, Valid: true}
			}},
		{"no contact", nil, groups.PageNoConfirmedContact, "", func(e *senderEnv) {
			delete(e.f.contacts, [2]int64{1, 10})
		}},
		{"contact removed while retried", nil, groups.PageNoConfirmedContact, "", func(e *senderEnv) {
			e.f.calls[0].state = CallSending
			e.f.calls[0].firstAttempt = pgtype.Timestamptz{Time: c0, Valid: true}
			e.f.calls[0].language = pgtype.Text{String: "en", Valid: true}
			e.f.calls[0].text = pgtype.Text{String: "the text", Valid: true}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSender(t)
			e.gw.script(c.out...)
			e.page(t, 1, 10, 1)
			e.page(t, 1, 10, 4)
			if c.prepare != nil {
				c.prepare(e)
			}
			e.round(t)
			call := e.f.calls[0]
			if call.state != CallNotDelivered || call.reason.String != c.reason || call.errorClass.String != c.class {
				t.Fatalf("the call %+v", call)
			}
			if want := len(c.out); e.gw.count() != want {
				t.Errorf("%d calls, want %d", e.gw.count(), want)
			}
			if len(e.pages.pages) != 1 || e.pages.ran != 1 || !slices.Equal(e.pages.pages[0].ids, []int64{1, 4}) {
				t.Fatalf("page_not_delivered %+v, ran %d", e.pages.pages, e.pages.ran)
			}
			p := e.pages.pages[0].page
			if p.Reason != c.reason || len(p.Users) != 1 || p.Users[0] != (groups.PagedUser{ID: 1,
				PublicID: "SR0000000000P1", Name: "Petr", Login: "petr"}) || p.Gateway == nil ||
				*p.Gateway != (groups.PageGateway{ID: 10, PublicID: "CN00000000GW10", Name: "Gateway"}) {
				t.Errorf("the page %+v", p)
			}
			if !strings.Contains(e.log.String(), `"event":"gateway_call_not_delivered"`) ||
				!strings.Contains(e.log.String(), `"reason":"`+c.reason+`"`) {
				t.Errorf("the log:\n%s", e.log)
			}
			e.page(t, 1, 10, 2)
			if w := e.f.calls[1]; !w.notBefore.Time.Equal(e.business.Now().Add(CombineWindow)) {
				t.Errorf("the next call is due at %v", w.notBefore)
			}
		})
	}
}

// TestSenderRetryAfter: a RetryAfter waits exactly as asked plus the token margin, holds the gateway's limiter until
// then and counts no attempt; without a free limiter token a call waits until one is due, and a waiting call still
// takes Pages.
func TestSenderRetryAfter(t *testing.T) {
	e := newSender(t)
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 30 * time.Second,
		Scope: delivery.ScopeDestination})
	e.page(t, 1, 10, 1)
	e.round(t)
	call := e.f.calls[0]
	if call.attempts != 0 || call.errorClass.String != "retry_after" ||
		!call.nextAttempt.Time.Equal(c0.Add(30*time.Second+SenderTokenMargin)) ||
		!slices.Equal(e.lim.holds, []time.Time{c0.Add(30 * time.Second)}) {
		t.Fatalf("after a RetryAfter %+v, holds %v", call, e.lim.holds)
	}

	e = newSender(t)
	e.lim.deny, e.lim.due = true, c0.Add(time.Second)
	e.page(t, 1, 10, 1)
	e.round(t)
	if e.gw.count() != 0 || e.f.calls[0].state != CallWaiting ||
		!e.f.calls[0].nextAttempt.Time.Equal(c0.Add(time.Second+SenderTokenMargin)) {
		t.Fatalf("without a token: %d sent, %+v", e.gw.count(), e.f.calls[0])
	}
	e.page(t, 1, 10, 2)
	if len(e.f.calls) != 1 || len(e.f.itemsOf(e.f.calls[0].id)) != 2 {
		t.Errorf("a Page did not join the call that waits for a token: %+v", e.f.calls)
	}
	e.lim.deny = false
	e.advance(time.Second + SenderTokenMargin)
	e.round(t)
	if e.gw.count() != 1 || !strings.Contains(e.gw.last().n.Text, "2 Alert Groups") {
		t.Errorf("with a token: %+v", e.gw.sent)
	}
}

// TestSenderCriticalFirst: of the due calls, those about critical Alert Groups are claimed and sent first, then by
// when they came due.
func TestSenderCriticalFirst(t *testing.T) {
	e := newSender(t)
	e.page(t, 1, 10, 1)
	e.advance(time.Second)
	e.page(t, 2, 10, 2)
	e.advance(time.Second)
	e.page(t, 1, 11, 4)
	e.round(t)
	var order []string
	for _, s := range e.gw.sent {
		order = append(order, s.n.Contact)
	}
	if !slices.Equal(order, []string{"+10000000003", "+10000000001", "+10000000002"}) {
		t.Errorf("sent to %v", order)
	}
}

// TestSenderLease: a call whose lease ran out before its preparation, or went to another replica before its outcome
// was recorded, changes nothing: the replica that holds the lease sends it again with the same key, and
// page_not_delivered is not recorded twice.
func TestSenderLease(t *testing.T) {
	e := newSender(t)
	e.page(t, 1, 10, 1)
	rows, err := e.f.ClaimDueCalls(t.Context(), claimParams(e.s))
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	e.real.Advance(SenderLease)
	e.s.attempt(t.Context(), rows[0])
	if e.gw.count() != 0 || e.f.calls[0].state != CallWaiting {
		t.Errorf("an expired lease: %d sent, %+v", e.gw.count(), e.f.calls[0])
	}

	e = newSender(t)
	e.page(t, 1, 10, 1)
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeUnknown, Status: 400})
	rows, _ = e.f.ClaimDueCalls(t.Context(), claimParams(e.s))
	c, _, err := e.s.prepare(t.Context(), rows[0])
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	e.f.calls[0].owner = "r2"
	if err := e.s.send(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if e.f.calls[0].state != CallSending || e.pages.ran != 0 {
		t.Errorf("another replica's call %+v, page_not_delivered ran %d", e.f.calls[0], e.pages.ran)
	}
	e.f.calls[0].owner = "r1"
	if err := e.s.record(t.Context(), c, delivery.Outcome{Kind: delivery.OutcomeOK}); err != nil ||
		e.f.calls[0].state != CallAccepted {
		t.Errorf("the accepted call %+v, %v", e.f.calls[0], err)
	}
	for _, out := range []delivery.Outcome{{Kind: delivery.OutcomeOK}, {Kind: delivery.OutcomeTransient},
		{Kind: delivery.OutcomeRetryAfter}} {
		if err := e.s.record(t.Context(), c, out); err != nil {
			t.Errorf("%s of an ended call: %v", out.Kind, err)
		}
	}
}

func claimParams(s *Sender) dbgen.ClaimDueCallsParams {
	p := s.Lease.Params(s.batch())
	return dbgen.ClaimDueCallsParams{OrgID: s.OrgID, Owner: p.Owner, LeaseUntil: p.LeaseUntil, Due: p.Due, Now: p.Now, Lim: p.Limit}
}

// TestSenderFailures: a failed query or signature fails the attempt, logged as gateway_call_failed without the
// contact or the text, and leaves the call to be attempted again; a failed claim or read of the next work fails the
// round.
func TestSenderFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, q := range []string{"LockCallPerson", "GetLeasedCall", "ListCallGroups", "DropCallItems", "CallContact",
		"RescheduleCall", "StartCall", "EndCall", "ReleaseWaitingCall", "RenewCallLease", "RecordCallRetry",
		"limiter", "hold", "pages", "keys"} {
		t.Run(q, func(t *testing.T) {
			e := newSender(t)
			e.page(t, 1, 10, 1)
			switch q {
			case "limiter":
				e.lim.err = boom
			case "hold":
				e.lim.err = boom
				e.gw.script(delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: time.Second})
				e.lim.deny = false
				e.s.Limiter = &holdFails{limiter: e.lim}
			case "pages":
				e.pages.err = boom
				e.gw.script(delivery.Outcome{Kind: delivery.OutcomeFatal})
			case "keys":
				e.s.Keys = failKeys{}
			case "DropCallItems":
				e.f.setStatus(1, "acknowledged")
				e.f.fail[q] = boom
			case "RescheduleCall":
				e.lim.deny = true
				e.f.fail[q] = boom
			case "ReleaseWaitingCall", "RenewCallLease":
				e.gw.script(delivery.Outcome{Kind: delivery.OutcomeTransient})
				e.round(t)
				e.advance(delivery.TransientFirstStep)
				e.f.fail[q] = boom
			case "RecordCallRetry":
				e.gw.script(delivery.Outcome{Kind: delivery.OutcomeTransient})
				e.f.fail[q] = boom
			default:
				e.f.fail[q] = boom
			}
			if _, err := e.s.Round(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(e.log.String(), `"event":"gateway_call_failed"`) ||
				strings.Contains(e.log.String(), "+10000000001") {
				t.Errorf("the log:\n%s", e.log)
			}
		})
	}
	for _, q := range []string{"ClaimDueCalls", "NextCallWork"} {
		e := newSender(t)
		e.f.fail[q] = boom
		if _, err := e.s.Round(t.Context()); !errors.Is(err, boom) {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// holdFails is a limiter whose tokens are free but whose holds fail.
type holdFails struct{ *limiter }

func (h *holdFails) ConnectionToken(context.Context, DBTX, int64, int64, time.Time) (bool, time.Time, error) {
	return true, time.Time{}, nil
}

// TestSenderRun: the sender runs rounds until its context ends, waking at once on Wake, waiting at least
// SenderMinWait and at most MaxWait, and backs off after a failed round.
func TestSenderRun(t *testing.T) {
	e := newSender(t)
	e.page(t, 1, 10, 1)
	ctx, cancel := context.WithCancel(t.Context())
	var waits []time.Duration
	e.s.MaxWait = 10 * time.Second
	e.s.Wait = func(_ context.Context, d time.Duration, wake <-chan struct{}) {
		waits = append(waits, d)
		switch len(waits) {
		case 1:
			e.f.fail["ClaimDueCalls"] = errors.New("boom")
		case 2:
			delete(e.f.fail, "ClaimDueCalls")
			e.s.Wake()
			e.s.Wake()
			<-wake
		default:
			cancel()
		}
	}
	e.s.Run(ctx)
	if e.gw.count() != 1 || len(waits) != 3 || waits[0] != 10*time.Second || waits[1] < SenderFailureBackoff/2 {
		t.Errorf("%d sent, waits %v", e.gw.count(), waits)
	}
	if got := callOutcome(delivery.OutcomeMarkupRejected); got != "unknown" {
		t.Errorf("markup_rejected counts as %s", got)
	}
	if (&Sender{}).limiter() == nil || (&Sender{}).batch() != SenderBatch || (&Sender{}).maxWait() != SenderMaxWait {
		t.Error("the defaults")
	}
	woken := make(chan struct{})
	close(woken)
	waitReal(t.Context(), time.Hour, woken)
	waitReal(t.Context(), time.Millisecond, nil)
	ended, stop := context.WithCancel(t.Context())
	stop()
	waitReal(ended, time.Hour, nil)
}

// TestSenderRepageAfterDrop: an Alert Group dropped from a waiting call whose preparation then found no limiter token
// is not lost when it is Paged again before the call goes: the Page queues it again, and the call is about it.
func TestSenderRepageAfterDrop(t *testing.T) {
	e := newSender(t)
	e.page(t, 1, 10, 1)
	e.page(t, 1, 10, 2)
	e.f.setStatus(2, "acknowledged")
	e.lim.deny, e.lim.due = true, c0.Add(time.Second)
	e.round(t)
	if got := e.f.itemsOf(e.f.calls[0].id); e.gw.count() != 0 || e.f.calls[0].state != CallWaiting ||
		!slices.Equal(got, []string{"+Disk1", "-Disk2"}) {
		t.Fatalf("without a token: %d sent, %+v, %v", e.gw.count(), e.f.calls[0], got)
	}
	e.f.setStatus(2, "firing")
	e.page(t, 1, 10, 2)
	if got := e.f.itemsOf(e.f.calls[0].id); len(e.f.calls) != 1 || !slices.Equal(got, []string{"+Disk1", "+Disk2"}) {
		t.Fatalf("the Page again: %+v, %v", e.f.calls, got)
	}
	e.lim.deny = false
	e.advance(time.Second + SenderTokenMargin)
	e.round(t)
	if e.gw.count() != 1 || !strings.Contains(e.gw.last().n.Text, "2 Alert Groups") ||
		!strings.Contains(e.gw.last().n.Text, "#2 Disk2") {
		t.Errorf("the call %+v", e.gw.sent)
	}
}
