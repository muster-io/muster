// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

const (
	gwID    = "CN00000000GW10"
	contact = "+10000000001"
)

var codeRe = regexp.MustCompile(`code: ([0-9]{6})\.`)

// env is a Service over the in-memory store, the fake gateway, the interactive path without limits and a manual
// business clock at t0; log holds its log lines.
type env struct {
	t     *testing.T
	store *fakeStore
	gw    *fakeGatewaySender
	clock *clock.Manual
	log   *bytes.Buffer
	svc   *Service
}

var t0 = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, store: newFakeStore(), gw: &fakeGatewaySender{}, clock: clock.NewManual(t0), log: &bytes.Buffer{}}
	logger := logging.New(e.log, logging.LevelInfo)
	e.svc = New(Config{OrgID: 1, Store: e.store, Audit: audit.NewWriter(logger, e.clock), Business: e.clock,
		Log: logger, Gateway: e.gw, Path: deliverytest.Unlimited(1, clock.Clocks{Business: e.clock, Real: clock.Real{}})})
	return e
}

// person is the requester User 1 in the web session 1, or the User and the session given.
func person(user, session int64) Requester {
	pub := "SR0000000000P1"
	if user == 2 {
		pub = "SR0000000000P2"
	}
	return Requester{UserID: user, UserPublicID: pub, SessionID: session, Actor: audit.User(user, pub),
		Transport: audit.TransportUI}
}

// sentCode is the code of the last notification the gateway got.
func (e *env) sentCode() string {
	e.t.Helper()
	m := codeRe.FindStringSubmatch(e.gw.last().Text)
	if m == nil {
		e.t.Fatalf("no code in %q", e.gw.last().Text)
	}
	return m[1]
}

func (e *env) create(r Requester, text string) Contact {
	e.t.Helper()
	c, err := e.svc.Create(e.t.Context(), r, gwID, text)
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	return c
}

// left is the attempts_remaining of a contact_code_invalid refusal, -1 for any other error.
func left(err error) int {
	f, ok := errors.AsType[*FieldError](err)
	if !ok || f.Code != CodeContactCodeInvalid || f.Pointer != pointerCode || f.AttemptsRemaining == nil {
		return -1
	}
	return *f.AttemptsRemaining
}

func (e *env) actions() []string {
	var out []string
	for _, a := range e.store.audits {
		out = append(out, a.Action)
	}
	return out
}

// noLeak fails when the log carries the contact or one of the codes.
func (e *env) noLeak(codes ...string) {
	e.t.Helper()
	if strings.Contains(e.log.String(), contact) {
		e.t.Errorf("the contact reached the log: %s", e.log.String())
	}
	for _, c := range codes {
		if regexp.MustCompile(`(^|[^0-9])` + c + `([^0-9]|$)`).MatchString(e.log.String()) {
			e.t.Errorf("the code %s reached the log", c)
		}
	}
}

// TestCreateSendsCode is C-30.FR-3 and AC-2: a new contact is unconfirmed, the gateway gets its code through
// sendNotification without action data in the caller's language, the code is stored as its hash only and tied to the
// session, and the creation is in the Audit log with /contact in its diff; the log has neither the contact nor the
// code.
func TestCreateSendsCode(t *testing.T) {
	e := newEnv(t)
	c := e.create(person(1, 7), "  "+contact+" ")
	if c.Contact != contact || c.Confirmed() || c.Replaces != nil || c.ConnectionPublicID != gwID ||
		c.ConnectionName != "Gateway" || !strings.HasPrefix(c.PublicID, "GC") || !c.CreatedAt.Equal(t0) {
		t.Errorf("created %+v", c)
	}
	n := e.gw.last()
	code := e.sentCode()
	want := "Your Muster code: " + code + ". It is valid for 10 minutes. Do not share it."
	if n.Contact != contact || n.Action != nil || n.Language != "en" || n.Connection != 10 || n.Text != want {
		t.Errorf("notification %+v", n)
	}
	if len(e.gw.keys) != 1 || e.gw.keys[0] != "contact-code-"+c.PublicID+"-"+strconv.FormatInt(e.store.codes[0].id, 10) {
		t.Errorf("keys %v", e.gw.keys)
	}
	if len(e.store.codes) != 1 {
		t.Fatalf("%d codes", len(e.store.codes))
	}
	k := e.store.codes[0]
	if k.sessionID != 7 || bytes.Contains(k.codeHash, []byte(code)) || !bytes.Equal(k.codeHash, codeHash(c.PublicID,
		code)) || !k.expiresAt.Equal(t0.Add(10*time.Minute)) || !bytes.Equal(k.contactHash, hashText(contact)) {
		t.Errorf("code row %+v", k)
	}
	if got := e.actions(); len(got) != 1 || got[0] != ActionCreated {
		t.Fatalf("audit %v", got)
	}
	a := e.store.audits[0]
	if a.ResourceType.String != ResourceContact || a.ResourcePublicID.String != c.PublicID ||
		!strings.Contains(string(a.Diff), `"pointer":"/contact","after":"`+contact+`"`) ||
		strings.Contains(string(a.Details), contact) || !strings.Contains(string(a.Details), `"user":"SR0000000000P1"`) {
		t.Errorf("audit entry %s %s", a.Diff, a.Details)
	}
	if !strings.Contains(e.log.String(), `"event":"gateway_contact_code_sent"`) ||
		!strings.Contains(e.log.String(), `"outcome":"ok"`) || !strings.Contains(e.log.String(), `[redacted]`) {
		t.Errorf("log %s", e.log.String())
	}
	e.noLeak(code)
}

// TestCodeInRussian: the text follows the language of the caller's profile.
func TestCodeInRussian(t *testing.T) {
	e := newEnv(t)
	e.create(person(2, 8), contact)
	n := e.gw.last()
	if n.Language != "ru" || !strings.HasPrefix(n.Text, "Ваш код Muster: ") ||
		!strings.Contains(n.Text, "действителен 10 минут.") {
		t.Errorf("notification %+v", n)
	}
}

// TestConfirm is C-30.AC-2: a wrong code is refused with the attempts left, the right one confirms the contact with
// the Audit log entry, and the same code again is refused with none left.
func TestConfirm(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	code := e.sentCode()
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, wrong); left(err) != 4 {
		t.Errorf("a wrong code: %v (%d left)", err, left(err))
	}
	got, err := e.svc.Confirm(t.Context(), r, c.PublicID, " "+code+" ")
	if err != nil || !got.Confirmed() || !got.ConfirmedAt.Equal(t0) || got.Contact != contact {
		t.Fatalf("Confirm = %+v, %v", got, err)
	}
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, code); left(err) != 0 {
		t.Errorf("the code again: %v", err)
	}
	if want := []string{ActionCreated, ActionConfirmed}; strings.Join(e.actions(), " ") != strings.Join(want, " ") {
		t.Errorf("audit %v", e.actions())
	}
	if d := string(e.store.audits[1].Diff); !strings.Contains(d, `{"pointer":"/contact","after":"`+contact+`"}`) ||
		!strings.Contains(d, `{"pointer":"/confirmed","before":false,"after":true}`) {
		t.Errorf("confirmed diff %s", d)
	}
	if contact, ok, err := e.svc.ConfirmedContact(t.Context(), 1, 10); err != nil || !ok || contact != "+10000000001" {
		t.Errorf("ConfirmedContact = %q, %v, %v", contact, ok, err)
	}
	if n := strings.Count(e.log.String(), `"event":"gateway_contact_code_rejected"`); n != 2 ||
		!strings.Contains(e.log.String(), `"reason":"wrong_code"`) || !strings.Contains(e.log.String(), `"reason":"used"`) {
		t.Errorf("rejections in %s", e.log.String())
	}
	e.noLeak(code)
}

// TestAttempts: after five wrong codes the code is void, and the right one is refused.
func TestAttempts(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	code := e.sentCode()
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for want := 4; want >= 0; want-- {
		if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, wrong); left(err) != want {
			t.Errorf("attempt with %d left: %v", want, err)
		}
	}
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, code); left(err) != 0 {
		t.Errorf("the right code after the last attempt: %v", err)
	}
	if !strings.Contains(e.log.String(), `"reason":"void"`) {
		t.Errorf("log %s", e.log.String())
	}
}

// TestExpiry: a code is valid gateway.contact_code on the business clock.
func TestExpiry(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	code := e.sentCode()
	e.clock.Advance(CodeTTL)
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, code); left(err) != 0 {
		t.Errorf("an expired code: %v", err)
	}
	if !strings.Contains(e.log.String(), `"reason":"expired"`) {
		t.Errorf("log %s", e.log.String())
	}
}

// TestSessionTied is ADR-0013 for codes: the right code from another session of the same person is refused and counts
// as an attempt; a session that ended voids its codes.
func TestSessionTied(t *testing.T) {
	e := newEnv(t)
	c := e.create(person(1, 7), contact)
	code := e.sentCode()
	if _, err := e.svc.Confirm(t.Context(), person(1, 9), c.PublicID, code); left(err) != 4 {
		t.Errorf("another session: %v", err)
	}
	if !strings.Contains(e.log.String(), `"reason":"other_session"`) {
		t.Errorf("log %s", e.log.String())
	}
	e.store.ended[7] = true
	if _, err := e.svc.Confirm(t.Context(), person(1, 7), c.PublicID, code); left(err) != 0 {
		t.Errorf("a code of a session that ended: %v", err)
	}
}

// TestNewCodeVoidsEarlier: sendMyGatewayContactCode voids the earlier codes; the new one confirms.
func TestNewCodeVoidsEarlier(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	first := e.sentCode()
	e.clock.Advance(time.Second)
	got, err := e.svc.SendCode(t.Context(), r, c.PublicID)
	if err != nil || got.PublicID != c.PublicID || got.Contact != contact {
		t.Fatalf("SendCode = %+v, %v", got, err)
	}
	second := e.sentCode()
	if !e.store.codes[0].voidedAt.Valid {
		t.Error("the first code is not void")
	}
	if first != second {
		if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, first); left(err) != 4 {
			t.Errorf("the first code: %v", err)
		}
	}
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, second); err != nil {
		t.Errorf("the second code: %v", err)
	}
}

// TestLimits are gateway.contact_code_requests: five codes per User within an hour across contacts, three per contact
// text whoever asks, with the wait until the oldest counted is an hour old; removing a contact keeps its codes counted.
func TestLimits(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	a := e.create(r, "+10000000001")
	for range 2 {
		e.clock.Advance(time.Minute)
		if _, err := e.svc.SendCode(t.Context(), r, a.PublicID); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Advance(time.Minute)
	_, err := e.svc.SendCode(t.Context(), r, a.PublicID)
	l, ok := errors.AsType[*RateLimitedError](err)
	if !ok || l.RetryAfter != 57*time.Minute || l.Seconds() != 57*60 {
		t.Fatalf("the fourth code for one contact: %v", err)
	}
	// The same contact text by another User counts too.
	if _, err := e.svc.Create(t.Context(), person(2, 8), gwID, "+10000000001"); !isLimited(err) {
		t.Errorf("another User for the same contact: %v", err)
	}
	if err := e.svc.Delete(t.Context(), r, a.PublicID); err != nil {
		t.Fatal(err)
	}
	e.create(r, "+10000000002")
	// The second unconfirmed contact replaces the first; its code still counts.
	b := e.create(r, "+10000000003")
	_, err = e.svc.SendCode(t.Context(), r, b.PublicID)
	if !isLimited(err) {
		t.Errorf("the sixth code of the User: %v", err)
	}
	if n := len(e.gw.sent); n != 5 {
		t.Errorf("%d codes sent", n)
	}
	e.clock.Advance(time.Hour)
	if _, err := e.svc.SendCode(t.Context(), r, b.PublicID); err != nil {
		t.Errorf("an hour later: %v", err)
	}
	if e.store.locks == 0 {
		t.Error("the contact text was never locked")
	}
}

func isLimited(err error) bool {
	_, ok := errors.AsType[*RateLimitedError](err)
	return ok
}

// TestValidation: the contact, the gateway, the contact's owner and the session.
func TestValidation(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	for name, text := range map[string]string{"empty": " ", "too long": strings.Repeat("1", 201),
		"control": "+1000\n0001", "invalid utf-8": "\xff"} {
		if _, err := e.svc.Create(t.Context(), r, gwID, text); !fieldErr(err, pointerContact, CodeInvalidFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.svc.Create(t.Context(), r, gwID, strings.Repeat("я", 200)); err != nil {
		t.Errorf("200 characters: %v", err)
	}
	for _, id := range []string{"CN00000000BR11", "CN00000000XX99", "not an id", "DS00000000GW10"} {
		if _, err := e.svc.Create(t.Context(), r, id, contact); !fieldErr(err, pointerConnection, CodeUnknownID) {
			t.Errorf("%s: %v", id, err)
		}
	}
	if _, err := e.svc.Create(t.Context(), person(1, 0), gwID, contact); !errors.Is(err, errNoSession) {
		t.Errorf("without a session: %v", err)
	}
	if _, err := e.svc.SendCode(t.Context(), person(1, 0), "GC0000000000A1"); !errors.Is(err, errNoSession) {
		t.Errorf("a code without a session: %v", err)
	}
	c := e.create(r, contact)
	for name, f := range map[string]func() error{
		"send for another": func() error { _, err := e.svc.SendCode(t.Context(), person(2, 8), c.PublicID); return err },
		"confirm for another": func() error {
			_, err := e.svc.Confirm(t.Context(), person(2, 8), c.PublicID, "1")
			return err
		},
		"delete for another": func() error { return e.svc.Delete(t.Context(), person(2, 8), c.PublicID) },
		"bad id":             func() error { _, err := e.svc.SendCode(t.Context(), r, "nope"); return err },
		"bad id to confirm":  func() error { _, err := e.svc.Confirm(t.Context(), r, "nope", "1"); return err },
		"bad id to delete":   func() error { return e.svc.Delete(t.Context(), r, "nope") },
	} {
		if err := f(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e.store.gateways[10] = fakeGateway{publicID: gwID, name: "Gateway"}
	if _, err := e.svc.SendCode(t.Context(), r, c.PublicID); !fieldErr(err, pointerConnection, CodeUnknownID) {
		t.Errorf("a gateway that is no longer one: %v", err)
	}
	if _, ok, err := e.svc.ConfirmedContact(t.Context(), 1, 10); ok || err != nil {
		t.Errorf("ConfirmedContact = %v, %v", ok, err)
	}
}

func fieldErr(err error, pointer, code string) bool {
	f, ok := errors.AsType[*FieldError](err)
	return ok && f.Pointer == pointer && f.Code == code && f.Error() != ""
}

// TestNoCode: a contact whose codes were all pruned has none to check.
func TestNoCode(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	e.store.codes = nil
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, "123456"); left(err) != 0 {
		t.Errorf("no code: %v", err)
	}
	if !strings.Contains(e.log.String(), `"reason":"no_code"`) {
		t.Errorf("log %s", e.log.String())
	}
}

// TestSendFailures: no limiter token deletes the code, which never went; a refusal voids it and answers the class;
// another failure voids it too. The contact stays, unconfirmed.
func TestSendFailures(t *testing.T) {
	e := newEnv(t)
	e.svc.cfg.Path = fakePath{err: &delivery.LimitedError{RetryAfter: 2 * time.Second}}
	r := person(1, 7)
	if _, err := e.svc.Create(t.Context(), r, gwID, contact); !isDeliveryLimited(err) {
		t.Fatalf("limited: %v", err)
	}
	if len(e.store.codes) != 0 || len(e.store.contacts) != 1 || !strings.Contains(e.log.String(), `"outcome":"limited"`) {
		t.Errorf("after limited: %d codes, %d contacts", len(e.store.codes), len(e.store.contacts))
	}
	c := e.store.contacts[0].publicID
	e.svc.cfg.Path = fakePath{err: errors.New("database gone")}
	if _, err := e.svc.SendCode(t.Context(), r, c); err == nil || !strings.Contains(err.Error(), "send the code") {
		t.Errorf("an error: %v", err)
	}
	if len(e.store.codes) != 1 || !e.store.codes[0].voidedAt.Valid {
		t.Errorf("the code after an error: %+v", e.store.codes)
	}
	e.svc.cfg.Path = deliverytest.Unlimited(1, clock.Clocks{Business: e.clock, Real: clock.Real{}})
	for kind, class := range map[delivery.OutcomeKind]string{delivery.OutcomeFatal: ClassForbidden,
		delivery.OutcomeGone: ClassGone, delivery.OutcomeUnknown: ClassInvalid, delivery.OutcomeTransient: ClassTransient,
		delivery.OutcomeRetryAfter: ClassRateLimited} {
		if class != classOf(kind) {
			t.Errorf("classOf(%s) = %s", kind, classOf(kind))
		}
	}
	e.gw.outcome = delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "echo " + contact}
	e.clock.Advance(time.Hour)
	_, err := e.svc.SendCode(t.Context(), r, c)
	refused, ok := errors.AsType[*RefusedError](err)
	if !ok || refused.Class != ClassForbidden || strings.Contains(err.Error(), contact) {
		t.Fatalf("refused: %v", err)
	}
	if k := e.store.codes[len(e.store.codes)-1]; !k.voidedAt.Valid {
		t.Error("the refused code is not void")
	}
	if !strings.Contains(e.log.String(), `"outcome":"forbidden"`) {
		t.Errorf("log %s", e.log.String())
	}
	e.store.fail["VoidCode"] = errors.New("void failed")
	if _, err := e.svc.SendCode(t.Context(), r, c); err == nil || !strings.Contains(err.Error(), "give up the code") {
		t.Errorf("a void that fails: %v", err)
	}
	e.noLeak()
}

func isDeliveryLimited(err error) bool {
	_, ok := errors.AsType[*delivery.LimitedError](err)
	return ok
}

// TestReplacement is the settled open question 1: a new contact for a gateway with a confirmed one replaces it only
// once it is confirmed, until then both are listed; a newer unconfirmed contact replaces an earlier unconfirmed one at
// once.
func TestReplacement(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	a := e.create(r, "+10000000001")
	if _, err := e.svc.Confirm(t.Context(), r, a.PublicID, e.sentCode()); err != nil {
		t.Fatal(err)
	}
	b := e.create(r, "+10000000002")
	if b.Replaces == nil || *b.Replaces != a.PublicID {
		t.Errorf("replaces %v", b.Replaces)
	}
	list, err := e.svc.List(t.Context(), 1)
	if err != nil || len(list) != 2 || !list[0].Confirmed() || list[1].Confirmed() || *list[1].Replaces != a.PublicID {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if got, _, _ := e.svc.ConfirmedContact(t.Context(), 1, 10); got != "+10000000001" {
		t.Errorf("called %q before the new one is confirmed", got)
	}
	cNew := e.create(r, "+10000000003")
	if list, _ := e.svc.List(t.Context(), 1); len(list) != 2 || list[1].PublicID != cNew.PublicID {
		t.Errorf("after a second unconfirmed contact: %+v", list)
	}
	got, err := e.svc.Confirm(t.Context(), r, cNew.PublicID, e.sentCode())
	if err != nil || got.Replaces != nil || !got.Confirmed() {
		t.Fatalf("Confirm = %+v, %v", got, err)
	}
	if list, _ := e.svc.List(t.Context(), 1); len(list) != 1 || list[0].PublicID != cNew.PublicID {
		t.Errorf("after the confirmation: %+v", list)
	}
	if got, _, _ := e.svc.ConfirmedContact(t.Context(), 1, 10); got != "+10000000003" {
		t.Errorf("called %q", got)
	}
	want := []string{ActionCreated, ActionConfirmed, ActionCreated, ActionDeleted, ActionCreated, ActionDeleted,
		ActionConfirmed}
	if strings.Join(e.actions(), " ") != strings.Join(want, " ") {
		t.Errorf("audit %v", e.actions())
	}
	for _, i := range []int{3, 5} {
		if !strings.Contains(string(e.store.audits[i].Details), `"reason":"replaced"`) {
			t.Errorf("entry %d details %s", i, e.store.audits[i].Details)
		}
	}
	if !strings.Contains(string(e.store.audits[6].Details), `"replaced":"`+a.PublicID+`"`) {
		t.Errorf("confirmation details %s", e.store.audits[6].Details)
	}
	// Confirming an already confirmed contact with a new code changes nothing.
	if _, err := e.svc.SendCode(t.Context(), r, cNew.PublicID); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.Confirm(t.Context(), r, cNew.PublicID, e.sentCode()); err != nil || !got.Confirmed() ||
		len(e.store.audits) != len(want) {
		t.Errorf("a confirmed contact again: %+v, %v, %d entries", got, err, len(e.store.audits))
	}
}

// TestAdmin: an Admin lists and removes a User's contacts; an unknown User is ErrUserNotFound.
func TestAdmin(t *testing.T) {
	e := newEnv(t)
	c := e.create(person(1, 7), contact)
	admin := Requester{UserID: 2, UserPublicID: "SR0000000000P2", Actor: audit.User(2, "SR0000000000P2"),
		Transport: audit.TransportAPI}
	list, err := e.svc.ListOfUser(t.Context(), "sr0000000000p1")
	if err != nil || len(list) != 1 || list[0].Contact != contact {
		t.Fatalf("ListOfUser = %+v, %v", list, err)
	}
	for _, id := range []string{"SR0000000000ZZ", "nope"} {
		if _, err := e.svc.ListOfUser(t.Context(), id); !errors.Is(err, ErrUserNotFound) {
			t.Errorf("ListOfUser(%s) = %v", id, err)
		}
		if err := e.svc.DeleteOfUser(t.Context(), admin, id, c.PublicID); !errors.Is(err, ErrUserNotFound) {
			t.Errorf("DeleteOfUser(%s) = %v", id, err)
		}
	}
	if err := e.svc.DeleteOfUser(t.Context(), admin, "SR0000000000P1", c.PublicID); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.List(t.Context(), 1); len(list) != 0 {
		t.Errorf("after the removal: %+v", list)
	}
	last := e.store.audits[len(e.store.audits)-1]
	if last.Action != ActionDeleted || !last.ActorUserID.Valid || last.ActorUserID.Int64 != 2 ||
		!strings.Contains(string(last.Diff), `"before":"`+contact+`"`) ||
		!strings.Contains(string(last.Details), `"user":"SR0000000000P1"`) {
		t.Errorf("entry %+v %s %s", last, last.Diff, last.Details)
	}
	if err := e.svc.DeleteOfUser(t.Context(), admin, "SR0000000000P1", c.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("again: %v", err)
	}
	e.noLeak()
}

// TestRemoveUserContacts is C-03.FR-13 revised: deleting a User erases their contacts and codes, each recorded as
// gateway_contact.deleted with the reason user_deleted, the other Users' kept.
func TestRemoveUserContacts(t *testing.T) {
	e := newEnv(t)
	e.create(person(1, 7), "+10000000001")
	e.create(person(2, 8), "+10000000002")
	admin := audit.User(2, "SR0000000000P2")
	if err := e.svc.removeUserContacts(t.Context(), e.store, 1, admin, audit.TransportAPI, noAddr); err != nil {
		t.Fatal(err)
	}
	if len(e.store.contacts) != 1 || e.store.contacts[0].userID != 2 || len(e.store.codes) != 1 ||
		e.store.codes[0].userID != 2 {
		t.Errorf("left %+v %+v", e.store.contacts, e.store.codes)
	}
	last := e.store.audits[len(e.store.audits)-1]
	if last.Action != ActionDeleted || !strings.Contains(string(last.Details), `"reason":"user_deleted"`) {
		t.Errorf("entry %s %s", last.Action, last.Details)
	}
	for _, q := range []string{"LockContactUser", "DeleteUserCodes", "DeleteUserContacts"} {
		e.store.fail = map[string]error{q: errors.New("boom")}
		if err := e.svc.removeUserContacts(t.Context(), e.store, 2, admin, audit.TransportAPI, noAddr); err == nil {
			t.Errorf("%s failing: nil", q)
		}
	}
}

// TestGateways lists the Notification gateways only.
func TestGateways(t *testing.T) {
	e := newEnv(t)
	got, err := e.svc.Gateways(t.Context())
	if err != nil || len(got) != 1 || got[0] != (Gateway{ID: 10, PublicID: gwID, Name: "Gateway"}) {
		t.Errorf("Gateways = %+v, %v", got, err)
	}
	e.store.fail["ListGateways"] = errors.New("boom")
	if _, err := e.svc.Gateways(t.Context()); err == nil {
		t.Error("a failing list")
	}
	e.store.fail["ConfirmedContact"] = errors.New("boom")
	if _, _, err := e.svc.ConfirmedContact(t.Context(), 1, 10); err == nil {
		t.Error("a failing read")
	}
}

// TestQueryFailures: every query that fails fails its operation and leaves nothing behind.
func TestQueryFailures(t *testing.T) {
	ops := map[string]func(e *env, c Contact) error{
		"create": func(e *env, _ Contact) error {
			_, err := e.svc.Create(e.t.Context(), person(1, 7), gwID, "+10000000009")
			return err
		},
		"send": func(e *env, c Contact) error {
			_, err := e.svc.SendCode(e.t.Context(), person(1, 7), c.PublicID)
			return err
		},
		"confirm": func(e *env, c Contact) error {
			_, err := e.svc.Confirm(e.t.Context(), person(1, 7), c.PublicID, "000000")
			return err
		},
		"confirm right": func(e *env, c Contact) error {
			_, err := e.svc.Confirm(e.t.Context(), person(1, 7), c.PublicID, e.sentCode())
			return err
		},
		"delete": func(e *env, c Contact) error { return e.svc.Delete(e.t.Context(), person(1, 7), c.PublicID) },
		"list":   func(e *env, _ Contact) error { _, err := e.svc.List(e.t.Context(), 1); return err },
		"list of user": func(e *env, _ Contact) error {
			_, err := e.svc.ListOfUser(e.t.Context(), "SR0000000000P1")
			return err
		},
	}
	queries := []string{"GetGateway", "LockContactUser", "LockContactText", "CountUserCodes", "CountContactCodes",
		"DeleteUnconfirmedContacts", "InsertContact", "LockContactOfUser", "InsertAuditEntry", "InsertCode",
		"MarkCodeSent", "VoidEarlierCodes", "LockNewestCode", "RecordWrongCode", "UseCode", "DeleteReplacedContacts", "ConfirmContact",
		"DeleteContact", "ListContactsOfUser", "GetUserByPublicID"}
	failed := map[string]bool{}
	for name, op := range ops {
		for _, q := range queries {
			e := newEnv(t)
			c := e.create(person(1, 7), contact)
			if name == "confirm right" {
				// A confirmed contact of the gateway that the confirmation replaces.
				e.store.contacts = append(e.store.contacts, fakeContact{id: 1, userID: 1, connID: 10,
					publicID: "GC0000000000Z1", contact: "+10000000000", createdAt: t0,
					confirmedAt: pgtype.Timestamptz{Time: t0, Valid: true}})
			}
			before := len(e.store.audits)
			e.store.fail[q] = errors.New("boom " + q)
			if err := op(e, c); err != nil && strings.Contains(err.Error(), "boom "+q) {
				failed[q] = true
				// After a send the gateway accepted, the contact and its entries are committed.
				if len(e.store.audits) != before && q != "MarkCodeSent" && q != "VoidEarlierCodes" {
					t.Errorf("%s with %s failing recorded %d entries", name, q, len(e.store.audits)-before)
				}
			}
		}
	}
	for _, q := range queries {
		if !failed[q] {
			t.Errorf("no operation failed on %s", q)
		}
	}
}

// TestNewCode: six digits, values above the largest multiple of a million drawn again, and a reader that fails.
func TestNewCode(t *testing.T) {
	high := []byte{0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0x30, 0x39}
	if got, err := newCode(bytes.NewReader(high)); err != nil || got != "012345" {
		t.Errorf("newCode = %q, %v", got, err)
	}
	if _, err := newCode(bytes.NewReader([]byte{1})); err == nil {
		t.Error("a short reader")
	}
	e := newEnv(t)
	e.svc.cfg.Random = bytes.NewReader(nil)
	if _, err := e.svc.Create(t.Context(), person(1, 7), gwID, contact); err == nil {
		t.Error("Create without randomness")
	}
	if _, err := e.svc.SendCode(t.Context(), person(1, 7), "GC0000000000A1"); err == nil {
		t.Error("SendCode without randomness")
	}
	if New(Config{}).cfg.Random == nil {
		t.Error("no default randomness")
	}
}

// TestErrors are the texts and the Retry-After of the errors.
func TestErrors(t *testing.T) {
	if s := (&RateLimitedError{RetryAfter: 100 * time.Millisecond}).Seconds(); s != 1 {
		t.Errorf("Seconds = %d", s)
	}
	if !strings.Contains((&RateLimitedError{RetryAfter: time.Minute}).Error(), "1m0s") {
		t.Error("RateLimitedError text")
	}
	if (&RefusedError{Class: "gone"}).Error() != "the notification gateway refused the code (gone)" {
		t.Error("RefusedError text")
	}
	if language(pgtype.Text{String: "de", Valid: true}) != "en" {
		t.Error("an unknown language")
	}
}

// TestPruner deletes the codes that expired a day ago.
func TestPruner(t *testing.T) {
	q := &fakePrune{}
	p := Pruner{q: q}
	if n, err := p.Codes(t.Context(), 1, t0, 50); err != nil || n != 3 || q.arg.Before != t0.Add(-CodePruneAfter) ||
		q.arg.BatchSize != 50 || q.arg.OrgID != 1 {
		t.Errorf("Codes = %d, %v, %+v", n, err, q.arg)
	}
	q.err = errors.New("boom")
	if _, err := p.Codes(t.Context(), 1, t0, 50); err == nil {
		t.Error("a failing prune")
	}
	if NewPruner(nil).q == nil {
		t.Error("NewPruner")
	}
}

type fakePrune struct {
	arg dbgen.PruneCodesParams
	err error
}

func (f *fakePrune) PruneCodes(_ context.Context, a dbgen.PruneCodesParams) (int64, error) {
	f.arg = a
	return 3, f.err
}

var noAddr netip.Addr

// TestFailedSendKeepsEarlierCode: a new code that was never sent — no limiter token — or that the gateway refused
// leaves the contact's earlier code valid; only a code the gateway accepted voids the earlier ones.
func TestFailedSendKeepsEarlierCode(t *testing.T) {
	for name, path := range map[string]Path{
		"limited": fakePath{err: &delivery.LimitedError{RetryAfter: time.Second}},
		"error":   fakePath{err: errors.New("database gone")},
		"refused": nil,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			r := person(1, 7)
			c := e.create(r, contact)
			first := e.sentCode()
			if path != nil {
				e.svc.cfg.Path = path
			} else {
				e.gw.outcome = delivery.Outcome{Kind: delivery.OutcomeUnknown}
			}
			if _, err := e.svc.SendCode(t.Context(), r, c.PublicID); err == nil {
				t.Fatal("the failed code succeeded")
			}
			if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, first); err != nil {
				t.Errorf("the earlier code after a failed new one: %v", err)
			}
		})
	}
}

// TestDeletedUser: a User whose deletion committed while a change waited for the lock adds, sends, confirms and
// removes nothing (C-03.FR-13).
func TestDeletedUser(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	code := e.sentCode()
	u := e.store.users[1]
	u.status = statusDeleted
	e.store.users[1] = u
	contacts, codes := len(e.store.contacts), len(e.store.codes)
	if _, err := e.svc.Create(t.Context(), r, gwID, "+10000000009"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Create = %v", err)
	}
	if _, err := e.svc.SendCode(t.Context(), r, c.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("SendCode = %v", err)
	}
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, code); !errors.Is(err, ErrNotFound) {
		t.Errorf("Confirm = %v", err)
	}
	if err := e.svc.Delete(t.Context(), r, c.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete = %v", err)
	}
	if len(e.store.contacts) != contacts || len(e.store.codes) != codes || len(e.gw.sent) != 1 {
		t.Errorf("changed: %d contacts, %d codes, %d sent", len(e.store.contacts), len(e.store.codes), len(e.gw.sent))
	}
}

// TestConfirmAtFormerGateway: a contact whose gateway was deleted or lost the role gateway is not confirmed.
func TestConfirmAtFormerGateway(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	e.store.gateways[10] = fakeGateway{publicID: gwID, name: "Gateway"}
	if _, err := e.svc.Confirm(t.Context(), r, c.PublicID, e.sentCode()); !fieldErr(err, pointerConnection,
		CodeUnknownID) {
		t.Errorf("Confirm = %v", err)
	}
	if e.store.contacts[0].confirmedAt.Valid {
		t.Error("confirmed")
	}
}

// TestUserDeletedDuringSend: a User deleted while their code was being sent has nothing marked sent, and the request
// fails on nothing (C-03.FR-13).
func TestUserDeletedDuringSend(t *testing.T) {
	e := newEnv(t)
	r := person(1, 7)
	c := e.create(r, contact)
	e.gw.during = func() {
		u := e.store.users[1]
		u.status = statusDeleted
		e.store.users[1] = u
	}
	if _, err := e.svc.SendCode(t.Context(), r, c.PublicID); err != nil {
		t.Errorf("SendCode = %v", err)
	}
	if k := e.store.codes[len(e.store.codes)-1]; k.sentAt.Valid || e.store.codes[0].voidedAt.Valid {
		t.Errorf("codes changed for a deleted User: %+v", e.store.codes)
	}
}
