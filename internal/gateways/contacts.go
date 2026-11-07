// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/publicid"
)

// The settings gateway.contact_code and gateway.contact_code_requests (defaults.md, P-86).
const (
	// CodeLength, CodeTTL and MaxAttempts are gateway.contact_code: 6 digits, valid 10 minutes, at most 5 attempts.
	CodeLength  = 6
	CodeTTL     = 10 * time.Minute
	MaxAttempts = 5
	// RequestsPerUser and RequestsPerContact are gateway.contact_code_requests: the codes per User and per contact
	// text, whoever asks, within RequestWindow.
	RequestsPerUser    = 5
	RequestsPerContact = 3
	RequestWindow      = time.Hour
	// CodePruneAfter is how long after it expired a code is deleted, as account_link.request_prune_after is for the
	// Account link requests it is pruned with.
	CodePruneAfter = 24 * time.Hour
	// MaxContactLength is the longest contact, in characters, as the Bridge protocol takes it.
	MaxContactLength = 200
)

// The Audit log actions and resource of C-30.FR-3.
const (
	ActionCreated   = "gateway_contact.created"
	ActionConfirmed = "gateway_contact.confirmed"
	ActionDeleted   = "gateway_contact.deleted"
	ResourceContact = "gateway_contact"
)

// The reasons of gateway_contact.deleted that its person or an Admin did not ask for.
const (
	ReasonReplaced    = "replaced"
	ReasonUserDeleted = "user_deleted"
)

// The reasons of gateway_contact_code_rejected.
const (
	ReasonWrongCode    = "wrong_code"
	ReasonOtherSession = "other_session"
	ReasonUsed         = "used"
	ReasonVoid         = "void"
	ReasonExpired      = "expired"
	ReasonNoCode       = "no_code"
)

// The outcomes of gateway_contact_code_sent besides the error classes of the Bridge protocol.
const (
	OutcomeOK      = "ok"
	OutcomeLimited = "limited"
	OutcomeError   = "error"
)

// The error classes of the Bridge protocol that a refusal of the gateway has.
const (
	ClassTransient   = "transient"
	ClassRateLimited = "rate_limited"
	ClassGone        = "gone"
	ClassForbidden   = "forbidden"
	ClassInvalid     = "invalid"
)

// The codes of the field errors.
const (
	CodeUnknownID          = "unknown_id"
	CodeInvalidFormat      = "invalid_format"
	CodeContactCodeInvalid = "contact_code_invalid"
	pointerConnection      = "/connection_id"
	pointerContact         = "/contact"
	pointerCode            = "/code"
)

// textKey is the built-in text of the notification with a code.
const textKey = "gatewayContact.code"

// statusDeleted is the status of a deleted User.
const statusDeleted = "deleted"

var (
	// ErrNotFound is a contact that does not exist or is not the User's.
	ErrNotFound = errors.New("no such gateway contact")
	// ErrUserNotFound is a User an Admin names that does not exist.
	ErrUserNotFound = errors.New("no such user")
	// errNoSession is a code request without the web session that a code is tied to.
	errNoSession = errors.New("a contact code needs the web session that asks for it")
)

// randReader is crypto/rand, where codes come from by default.
var randReader = rand.Reader

// FieldError is a request field that is not valid; Detail never repeats the contact or the code. AttemptsRemaining is
// set on contact_code_invalid: the codes the contact still takes, 0 once it takes none.
type FieldError struct {
	Pointer           string
	Code              string
	Detail            string
	AttemptsRemaining *int
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// RateLimitedError refuses a code request beyond gateway.contact_code_requests until RetryAfter passed.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("too many contact code requests; retry after %s", e.RetryAfter)
}

// Seconds is the wait in whole seconds, rounded up, at least 1, for Retry-After.
func (e *RateLimitedError) Seconds() int {
	return max(int(math.Ceil(e.RetryAfter.Seconds())), 1)
}

// RefusedError is a notification with a code that the gateway refused: Class is how, as the Bridge protocol classifies
// it. It never carries the gateway's answer, which may repeat the contact and the code.
type RefusedError struct {
	Class string
}

func (e *RefusedError) Error() string {
	return "the notification gateway refused the code (" + e.Class + ")"
}

// Requester is who asks through the API: the User whose own contacts change, with the web session a code is tied to,
// or the Admin who removes a contact; the actor, the Transport and the client address of the Audit log entry.
type Requester struct {
	UserID       int64
	UserPublicID string
	SessionID    int64
	Actor        audit.Actor
	Transport    audit.Transport
	Address      netip.Addr
}

// Contact is a contact at a Notification gateway as the API shows it. ConfirmedAt is nil while it is unconfirmed;
// Replaces is the confirmed contact of the same person and gateway that it replaces once confirmed.
type Contact struct {
	PublicID           string
	ConnectionPublicID string
	ConnectionName     string
	Contact            string
	ConfirmedAt        *time.Time
	Replaces           *string
	CreatedAt          time.Time
}

// Confirmed reports whether the contact is confirmed, and so called.
func (c Contact) Confirmed() bool { return c.ConfirmedAt != nil }

// gatewayRef names the gateway of a contact.
type gatewayRef struct {
	id       int64
	publicID string
	name     string
}

// List lists the contacts of the User userID, by gateway and creation.
func (s *Service) List(ctx context.Context, userID int64) ([]Contact, error) {
	rows, err := s.cfg.Store.ListContactsOfUser(ctx, dbgen.ListContactsOfUserParams{OrgID: s.cfg.OrgID,
		UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list the gateway contacts: %w", err)
	}
	out := make([]Contact, len(rows))
	for i, r := range rows {
		out[i] = Contact{PublicID: r.PublicID, ConnectionPublicID: r.ConnectionPublicID,
			ConnectionName: r.ConnectionName, Contact: r.Contact, ConfirmedAt: timePtr(r.ConfirmedAt),
			Replaces: textPtr(r.Replaces), CreatedAt: r.CreatedAt.UTC()}
	}
	return out, nil
}

// ListOfUser lists the contacts of the User userPublicID, for an Admin; an unknown User is ErrUserNotFound, and a
// deleted one has none.
func (s *Service) ListOfUser(ctx context.Context, userPublicID string) ([]Contact, error) {
	u, err := s.user(ctx, userPublicID)
	if err != nil {
		return nil, err
	}
	return s.List(ctx, u.ID)
}

func (s *Service) user(ctx context.Context, publicID string) (dbgen.GetUserByPublicIDRow, error) {
	norm, err := publicid.Parse(publicid.User, publicID)
	if err != nil {
		return dbgen.GetUserByPublicIDRow{}, ErrUserNotFound
	}
	u, err := s.cfg.Store.GetUserByPublicID(ctx, dbgen.GetUserByPublicIDParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.GetUserByPublicIDRow{}, ErrUserNotFound
	}
	if err != nil {
		return dbgen.GetUserByPublicIDRow{}, fmt.Errorf("read the user: %w", err)
	}
	return u, nil
}

// Create adds an unconfirmed contact of the requesting User at the Notification gateway connectionPublicID and sends
// it a code (C-30.FR-3). The contact is trimmed and must be 1 to MaxContactLength characters without control
// characters (FieldError invalid_format at /contact); a Connection that is not a Notification gateway is a FieldError
// unknown_id at /connection_id. In one transaction, under the lock of the User: the limits of
// gateway.contact_code_requests (RateLimitedError), the replacement of the User's earlier unconfirmed contact at the
// gateway, recorded as gateway_contact.deleted with the reason replaced, the contact, recorded as
// gateway_contact.created, and its code. Then the code goes out as SendCode sends it; a failure there leaves the
// contact, unconfirmed, and is returned as SendCode returns it.
func (s *Service) Create(ctx context.Context, r Requester, connectionPublicID, contact string) (Contact, error) {
	text, err := normalizeContact(contact)
	if err != nil {
		return Contact{}, err
	}
	gw, err := s.gateway(ctx, connectionPublicID)
	if err != nil {
		return Contact{}, err
	}
	if r.SessionID == 0 {
		return Contact{}, errNoSession
	}
	code, err := newCode(s.cfg.Random)
	if err != nil {
		return Contact{}, err
	}
	now := s.now()
	var (
		created   Contact
		contactID int64
		codeID    int64
		lang      string
	)
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		u, err := s.lockUser(ctx, q, r.UserID)
		if err != nil {
			return err
		}
		lang = language(u.Language)
		if err := s.limits(ctx, q, u.ID, hashText(text), now); err != nil {
			return err
		}
		gone, err := q.DeleteUnconfirmedContacts(ctx, dbgen.DeleteUnconfirmedContactsParams{OrgID: s.cfg.OrgID,
			UserID: u.ID, ConnectionID: gw.id})
		if err != nil {
			return fmt.Errorf("delete the earlier unconfirmed contacts: %w", err)
		}
		for _, g := range gone {
			if err := s.record(ctx, q, r.Actor, r.Transport, r.Address, ActionDeleted, gw, g.PublicID,
				removed(gw, g.Contact), details(gw, u.PublicID, ReasonReplaced, "")); err != nil {
				return err
			}
		}
		created = Contact{PublicID: publicid.New(publicid.GatewayContact), ConnectionPublicID: gw.publicID,
			ConnectionName: gw.name, Contact: text, CreatedAt: now}
		contactID, err = q.InsertContact(ctx, dbgen.InsertContactParams{OrgID: s.cfg.OrgID,
			PublicID: created.PublicID, UserID: u.ID, ConnectionID: gw.id, Contact: text, CreatedAt: now})
		if err != nil {
			return fmt.Errorf("insert the gateway contact: %w", err)
		}
		row, err := q.LockContactOfUser(ctx, dbgen.LockContactOfUserParams{OrgID: s.cfg.OrgID, UserID: u.ID,
			PublicID: created.PublicID})
		if err != nil {
			return fmt.Errorf("read the gateway contact: %w", err)
		}
		created.Replaces = textPtr(row.Replaces)
		if err := s.record(ctx, q, r.Actor, r.Transport, r.Address, ActionCreated, gw, created.PublicID,
			added(gw, text), details(gw, u.PublicID, "", "")); err != nil {
			return err
		}
		codeID, err = s.insertCode(ctx, q, u.ID, contactID, created.PublicID, text, r.SessionID, code, now)
		return err
	})
	if err != nil {
		return Contact{}, err
	}
	if err := s.send(ctx, r, gw, contactID, created.PublicID, text, lang, code, codeID); err != nil {
		return Contact{}, err
	}
	return created, nil
}

// SendCode sends a new code of gateway.contact_code for the contact contactPublicID of the requesting User through its
// gateway (C-30.FR-3): in one transaction, under the lock of the User, the limits of gateway.contact_code_requests
// (RateLimitedError), the earlier codes of the contact voided and the new one stored, its SHA-256 only, tied to the
// requesting web session; then, on the interactive path, sendNotification with the key of the code, the text
// "Your Muster code: …" in the User's language and no action, the code masked in the Request log. No limiter token
// within the budget is a *delivery.LimitedError and deletes the code, which was never sent; a refusal of the gateway
// is a *RefusedError and any other failure an error, both of which void the code, which still counts. A contact of
// another User is ErrNotFound; one whose gateway is no longer a Notification gateway a FieldError unknown_id.
func (s *Service) SendCode(ctx context.Context, r Requester, contactPublicID string) (Contact, error) {
	norm, err := publicid.Parse(publicid.GatewayContact, contactPublicID)
	if err != nil {
		return Contact{}, ErrNotFound
	}
	if r.SessionID == 0 {
		return Contact{}, errNoSession
	}
	code, err := newCode(s.cfg.Random)
	if err != nil {
		return Contact{}, err
	}
	now := s.now()
	var (
		c      dbgen.LockContactOfUserRow
		codeID int64
		lang   string
	)
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		u, err := s.lockUser(ctx, q, r.UserID)
		if err != nil {
			return err
		}
		lang = language(u.Language)
		c, err = q.LockContactOfUser(ctx, dbgen.LockContactOfUserParams{OrgID: s.cfg.OrgID, UserID: u.ID,
			PublicID: norm})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the gateway contact: %w", err)
		}
		if !c.Gateway {
			return notGateway()
		}
		if err := s.limits(ctx, q, u.ID, hashText(c.Contact), now); err != nil {
			return err
		}
		codeID, err = s.insertCode(ctx, q, u.ID, c.ID, c.PublicID, c.Contact, r.SessionID, code, now)
		return err
	})
	if err != nil {
		return Contact{}, err
	}
	gw := gatewayRef{id: c.ConnectionID, publicID: c.ConnectionPublicID, name: c.ConnectionName}
	if err := s.send(ctx, r, gw, c.ID, c.PublicID, c.Contact, lang, code, codeID); err != nil {
		return Contact{}, err
	}
	return contactOf(c), nil
}

// confirmOutcome is how a confirmation ended in its transaction: the contact, or the reason the code was refused with
// the attempts the contact has left.
type confirmOutcome struct {
	contact Contact
	gateway string
	reason  string
	left    int
}

// Confirm confirms the contact contactPublicID of the requesting User with code (C-30.FR-3, AC-2). Under the lock of
// the User and of the contact's newest code: a code that is used or void, whose web session ended, that expired, or
// that the contact lacks, and a wrong code or one asked for in another web session — both of which count an attempt,
// the one that reaches gateway.contact_code's limit voiding the code — are a FieldError contact_code_invalid at /code,
// never saying which, with the attempts left, logged as gateway_contact_code_rejected. A right code from the session
// that asked for it is used, compared in constant time, and confirms the contact: the User's earlier confirmed contact
// at the gateway is deleted, recorded as gateway_contact.deleted with the reason replaced, and the confirmation is
// recorded as gateway_contact.confirmed, in one transaction. A contact that is already confirmed stays as it is. A
// contact of another User is ErrNotFound. It calls no gateway.
func (s *Service) Confirm(ctx context.Context, r Requester, contactPublicID, code string) (Contact, error) {
	norm, err := publicid.Parse(publicid.GatewayContact, contactPublicID)
	if err != nil {
		return Contact{}, ErrNotFound
	}
	var out confirmOutcome
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		out = confirmOutcome{}
		return s.confirm(ctx, q, r, norm, code, &out)
	})
	if err != nil {
		return Contact{}, err
	}
	if out.reason != "" {
		s.cfg.Log.Log(ctx, logging.GatewayContactCodeRejected, logging.F("user", r.UserPublicID),
			logging.F("connection", out.gateway), logging.F("contact", norm), logging.F("reason", out.reason),
			logging.F("attempts_remaining", out.left))
		left := out.left
		return Contact{}, &FieldError{Pointer: pointerCode, Code: CodeContactCodeInvalid, AttemptsRemaining: &left,
			Detail: "The code is wrong, used or expired; request a new one after five wrong codes."}
	}
	return out.contact, nil
}

// confirm is the transaction of Confirm; out says how it ended.
func (s *Service) confirm(ctx context.Context, q Queries, r Requester, contactPublicID, code string,
	out *confirmOutcome) error {
	u, err := s.lockUser(ctx, q, r.UserID)
	if err != nil {
		return err
	}
	c, err := q.LockContactOfUser(ctx, dbgen.LockContactOfUserParams{OrgID: s.cfg.OrgID, UserID: u.ID,
		PublicID: contactPublicID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock the gateway contact: %w", err)
	}
	if !c.Gateway {
		return notGateway()
	}
	out.gateway = c.ConnectionPublicID
	k, err := q.LockNewestCode(ctx, dbgen.LockNewestCodeParams{OrgID: s.cfg.OrgID,
		ContactID: pgtype.Int8{Int64: c.ID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		out.reason = ReasonNoCode
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock the code: %w", err)
	}
	now := s.now()
	switch {
	case k.UsedAt.Valid:
		out.reason = ReasonUsed
	case k.VoidedAt.Valid || k.SessionEnded:
		out.reason = ReasonVoid
	case !k.ExpiresAt.After(now):
		out.reason = ReasonExpired
	}
	if out.reason != "" {
		return nil
	}
	right := subtle.ConstantTimeCompare(codeHash(c.PublicID, normalizeCode(code)), k.CodeHash) == 1
	if !right || k.SessionID != r.SessionID {
		out.reason = ReasonWrongCode
		if right {
			out.reason = ReasonOtherSession
		}
		attempts, err := q.RecordWrongCode(ctx, dbgen.RecordWrongCodeParams{MaxAttempts: MaxAttempts, Now: now,
			OrgID: s.cfg.OrgID, ID: k.ID})
		if err != nil {
			return fmt.Errorf("count the wrong code: %w", err)
		}
		out.left = max(MaxAttempts-int(attempts), 0)
		return nil
	}
	if _, err := q.UseCode(ctx, dbgen.UseCodeParams{Now: now, OrgID: s.cfg.OrgID, ID: k.ID}); err != nil {
		return fmt.Errorf("use the code: %w", err)
	}
	gw := gatewayRef{id: c.ConnectionID, publicID: c.ConnectionPublicID, name: c.ConnectionName}
	if c.ConfirmedAt.Valid {
		out.contact = contactOf(c)
		return nil
	}
	gone, err := q.DeleteReplacedContacts(ctx, dbgen.DeleteReplacedContactsParams{OrgID: s.cfg.OrgID, UserID: u.ID,
		ConnectionID: c.ConnectionID, Keep: c.ID})
	if err != nil {
		return fmt.Errorf("delete the replaced contact: %w", err)
	}
	replaced := ""
	for _, g := range gone {
		if err := s.record(ctx, q, r.Actor, r.Transport, r.Address, ActionDeleted, gw, g.PublicID,
			removed(gw, g.Contact), details(gw, u.PublicID, ReasonReplaced, "")); err != nil {
			return err
		}
		replaced = g.PublicID
	}
	if err := q.ConfirmContact(ctx, dbgen.ConfirmContactParams{Now: now, OrgID: s.cfg.OrgID, ID: c.ID}); err != nil {
		return fmt.Errorf("confirm the gateway contact: %w", err)
	}
	diff := []audit.Change{{Pointer: audit.ContactPointer, After: c.Contact},
		{Pointer: "/confirmed", Before: false, After: true}}
	if err := s.record(ctx, q, r.Actor, r.Transport, r.Address, ActionConfirmed, gw, c.PublicID, diff,
		details(gw, u.PublicID, "", replaced)); err != nil {
		return err
	}
	out.contact = contactOf(c)
	out.contact.ConfirmedAt, out.contact.Replaces = &now, nil
	return nil
}

// Delete removes the contact contactPublicID of the requesting User, from the web session.
func (s *Service) Delete(ctx context.Context, r Requester, contactPublicID string) error {
	return s.delete(ctx, r, r.UserID, contactPublicID)
}

// DeleteOfUser removes the contact contactPublicID of the User userPublicID, for an Admin (C-18.FR-6 revised).
func (s *Service) DeleteOfUser(ctx context.Context, r Requester, userPublicID, contactPublicID string) error {
	u, err := s.user(ctx, userPublicID)
	if err != nil {
		return err
	}
	return s.delete(ctx, r, u.ID, contactPublicID)
}

// delete deletes the contact of the owner with the Audit log entry gateway_contact.deleted, by r; its codes stay,
// without it, and count toward the limits until they are pruned.
func (s *Service) delete(ctx context.Context, r Requester, ownerID int64, contactPublicID string) error {
	norm, err := publicid.Parse(publicid.GatewayContact, contactPublicID)
	if err != nil {
		return ErrNotFound
	}
	return s.cfg.Store.InTx(ctx, func(q Queries) error {
		u, err := s.lockUser(ctx, q, ownerID)
		if err != nil {
			return err
		}
		c, err := q.LockContactOfUser(ctx, dbgen.LockContactOfUserParams{OrgID: s.cfg.OrgID, UserID: u.ID,
			PublicID: norm})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the gateway contact: %w", err)
		}
		if _, err := q.DeleteContact(ctx, dbgen.DeleteContactParams{OrgID: s.cfg.OrgID, ID: c.ID}); err != nil {
			return fmt.Errorf("delete the gateway contact %s: %w", c.PublicID, err)
		}
		gw := gatewayRef{id: c.ConnectionID, publicID: c.ConnectionPublicID, name: c.ConnectionName}
		return s.record(ctx, q, r.Actor, r.Transport, r.Address, ActionDeleted, gw, c.PublicID,
			removed(gw, c.Contact), details(gw, u.PublicID, "", ""))
	})
}

// gateway reads the Notification gateway publicID; any other Connection is a FieldError unknown_id.
func (s *Service) gateway(ctx context.Context, publicID string) (gatewayRef, error) {
	norm, err := publicid.Parse(publicid.Connection, publicID)
	if err != nil {
		return gatewayRef{}, notGateway()
	}
	g, err := s.cfg.Store.GetGateway(ctx, dbgen.GetGatewayParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) {
		return gatewayRef{}, notGateway()
	}
	if err != nil {
		return gatewayRef{}, fmt.Errorf("read the notification gateway: %w", err)
	}
	return gatewayRef{id: g.ID, publicID: g.PublicID, name: g.Name}, nil
}

func notGateway() error {
	return &FieldError{Pointer: pointerConnection, Code: CodeUnknownID,
		Detail: "No Connection in the role gateway has this id."}
}

// limits takes the lock of the requests for the contact text of hash, then refuses a request beyond
// gateway.contact_code_requests within the hour before now — of the User, then for the contact whoever asked — with
// a *RateLimitedError until the oldest one counted is an hour old. The caller holds the lock of the User.
func (s *Service) limits(ctx context.Context, q Queries, userID int64, hash []byte, now time.Time) error {
	if err := q.LockContactText(ctx, dbgen.LockContactTextParams{LockClass: db.GatewayContactLockClass,
		OrgID: s.cfg.OrgID, ContactHash: hash}); err != nil {
		return fmt.Errorf("lock the code requests of the contact: %w", err)
	}
	since := now.Add(-RequestWindow)
	n, err := q.CountUserCodes(ctx, dbgen.CountUserCodesParams{Since: since, OrgID: s.cfg.OrgID, UserID: userID})
	if err != nil {
		return fmt.Errorf("count the code requests of the user: %w", err)
	}
	if n.Requests >= RequestsPerUser {
		return &RateLimitedError{RetryAfter: n.Oldest.Add(RequestWindow).Sub(now)}
	}
	m, err := q.CountContactCodes(ctx, dbgen.CountContactCodesParams{Since: since, OrgID: s.cfg.OrgID,
		ContactHash: hash})
	if err != nil {
		return fmt.Errorf("count the code requests of the contact: %w", err)
	}
	if m.Requests >= RequestsPerContact {
		return &RateLimitedError{RetryAfter: m.Oldest.Add(RequestWindow).Sub(now)}
	}
	return nil
}

// insertCode stores the code of the contact contactID, its SHA-256 bound to the contact only, tied to the web session
// sessionID, expiring CodeTTL after now.
func (s *Service) insertCode(ctx context.Context, q Queries, userID, contactID int64, contactPublicID, text string,
	sessionID int64, code string, now time.Time) (int64, error) {
	id, err := q.InsertCode(ctx, dbgen.InsertCodeParams{OrgID: s.cfg.OrgID, UserID: userID,
		ContactID: pgtype.Int8{Int64: contactID, Valid: true}, ContactHash: hashText(text), SessionID: sessionID,
		CodeHash: codeHash(contactPublicID, code), CreatedAt: now, ExpiresAt: now.Add(CodeTTL)})
	if err != nil {
		return 0, fmt.Errorf("store the code: %w", err)
	}
	return id, nil
}

// send sends the code codeID of the contact contactID through the gateway gw on the interactive path, with the code
// registered as a secret of the request, so that the Request log and an answer that echoes it carry neither, and logs
// gateway_contact_code_sent. Once the gateway accepted it, the code is marked sent, which makes it the one checked, and
// the contact's earlier codes are voided, in one transaction. The code is deleted when no limiter token was free,
// since nothing was sent, and voided when the call failed otherwise; either way the earlier codes stay as they were.
func (s *Service) send(ctx context.Context, r Requester, gw gatewayRef, contactID int64, contactPublicID, text, lang,
	code string, codeID int64) error {
	n := delivery.Notification{Connection: gw.id, Contact: text, Language: lang,
		Text: messages.N(lang, textKey, int64(CodeTTL/time.Minute),
			messages.Args{"code": code, "minutes": strconv.Itoa(int(CodeTTL / time.Minute))})}
	id := gw.id
	o, err := s.cfg.Path.Do(outbound.WithSecrets(ctx, logging.Secret(code)), delivery.Subject{Connection: &id},
		delivery.NotifyOp(s.cfg.Gateway, codeKey(contactPublicID, codeID), n))
	outcome, failed := OutcomeOK, err
	keep := context.WithoutCancel(ctx)
	var cleanup error
	switch _, limited := errors.AsType[*delivery.LimitedError](err); {
	case limited:
		outcome = OutcomeLimited
		cleanup = s.cfg.Store.DeleteCode(keep, dbgen.DeleteCodeParams{OrgID: s.cfg.OrgID, ID: codeID})
	case err != nil:
		outcome, failed = OutcomeError, fmt.Errorf("send the code: %w", err)
	case o.Kind != delivery.OutcomeOK:
		outcome = classOf(o.Kind)
		failed = &RefusedError{Class: outcome}
	}
	switch {
	case failed == nil:
		failed = s.sent(keep, r.UserID, contactID, codeID)
	case outcome != OutcomeLimited:
		cleanup = s.cfg.Store.VoidCode(keep, dbgen.VoidCodeParams{Now: s.now(), OrgID: s.cfg.OrgID, ID: codeID})
	}
	s.cfg.Log.Log(ctx, logging.GatewayContactCodeSent, logging.F("user", r.UserPublicID),
		logging.F("connection", gw.publicID), logging.F("contact", contactPublicID), logging.F("outcome", outcome))
	if cleanup != nil {
		return errors.Join(failed, fmt.Errorf("give up the code: %w", cleanup))
	}
	return failed
}

// sent marks the code codeID sent and voids the earlier codes of the contact contactID, which it replaces, under the
// lock of the User userID, as every other change of the User's codes, so that concurrent sends and confirmations
// serialize instead of locking the code rows in different orders. A User deleted meanwhile has no codes left: nothing
// is marked, and that is no error.
func (s *Service) sent(ctx context.Context, userID, contactID, codeID int64) error {
	now := s.now()
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		_, err := s.lockUser(ctx, q, userID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.MarkCodeSent(ctx, dbgen.MarkCodeSentParams{Now: now, OrgID: s.cfg.OrgID, ID: codeID}); err != nil {
			return fmt.Errorf("mark the code sent: %w", err)
		}
		if _, err := q.VoidEarlierCodes(ctx, dbgen.VoidEarlierCodesParams{Now: now, OrgID: s.cfg.OrgID,
			ContactID: pgtype.Int8{Int64: contactID, Valid: true}, Keep: codeID}); err != nil {
			return fmt.Errorf("void the earlier codes: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("the code was sent, but %w", err)
	}
	return nil
}

// lockUser locks the User userID whose contacts change; a deleted User is ErrNotFound, so that nothing is added for
// a User whose deletion committed while this waited for the lock (C-03.FR-13).
func (s *Service) lockUser(ctx context.Context, q Queries, userID int64) (dbgen.LockContactUserRow, error) {
	u, err := q.LockContactUser(ctx, dbgen.LockContactUserParams{OrgID: s.cfg.OrgID, ID: userID})
	if err != nil {
		return dbgen.LockContactUserRow{}, fmt.Errorf("lock the user: %w", err)
	}
	if u.Status == statusDeleted {
		return dbgen.LockContactUserRow{}, ErrNotFound
	}
	return u, nil
}

// codeKey is the Idempotency-Key of the code codeID of a contact: contact-code-<contact public_id>-<code id>, unique
// even for a gateway that outlives a restore of the database, where a code id alone could come again and get the
// answer of an earlier code, replayed, without its notification.
func codeKey(contactPublicID string, codeID int64) string {
	return "contact-code-" + contactPublicID + "-" + strconv.FormatInt(codeID, 10)
}

// classOf is the error class of the Bridge protocol behind the outcome kind of a refused notification.
func classOf(k delivery.OutcomeKind) string {
	switch k {
	case delivery.OutcomeRetryAfter:
		return ClassRateLimited
	case delivery.OutcomeGone:
		return ClassGone
	case delivery.OutcomeFatal:
		return ClassForbidden
	case delivery.OutcomeUnknown:
		return ClassInvalid
	default:
		return ClassTransient
	}
}

func (s *Service) record(ctx context.Context, q Queries, actor audit.Actor, t audit.Transport, addr netip.Addr,
	action string, gw gatewayRef, contactPublicID string, diff []audit.Change, d map[string]any) error {
	return s.cfg.Audit.Record(ctx, q, audit.Entry{OrgID: s.cfg.OrgID, Actor: actor, Transport: t, Action: action,
		Resource: audit.Resource{Type: ResourceContact, PublicID: contactPublicID, Name: gw.name}, Diff: diff,
		Details: d, SourceAddress: addr, Personal: []string{audit.ContactPointer}})
}

func (s *Service) now() time.Time { return s.cfg.Business.Now().UTC() }

// added is the diff of a new contact, removed that of one that went.
func added(gw gatewayRef, contact string) []audit.Change {
	return []audit.Change{{Pointer: "/connection", After: gw.publicID}, {Pointer: audit.ContactPointer, After: contact}}
}

func removed(gw gatewayRef, contact string) []audit.Change {
	return []audit.Change{{Pointer: "/connection", Before: gw.publicID},
		{Pointer: audit.ContactPointer, Before: contact}}
}

// details are the details of an entry: the gateway, the User whose contact it is — which the Audit log reads to show
// the contact as [erased] once the User is deleted — and the reason and the replaced contact when there are. They
// never carry the contact.
func details(gw gatewayRef, userPublicID, reason, replaced string) map[string]any {
	d := map[string]any{"connection": gw.publicID, "user": userPublicID}
	if reason != "" {
		d["reason"] = reason
	}
	if replaced != "" {
		d["replaced"] = replaced
	}
	return d
}

func contactOf(c dbgen.LockContactOfUserRow) Contact {
	return Contact{PublicID: c.PublicID, ConnectionPublicID: c.ConnectionPublicID, ConnectionName: c.ConnectionName,
		Contact: c.Contact, ConfirmedAt: timePtr(c.ConfirmedAt), Replaces: textPtr(c.Replaces),
		CreatedAt: c.CreatedAt.UTC()}
}

// normalizeContact is contact trimmed; one that is empty, longer than MaxContactLength characters or holds a control
// character is a FieldError invalid_format.
func normalizeContact(contact string) (string, error) {
	text := strings.TrimSpace(contact)
	n := utf8.RuneCountInString(text)
	if n == 0 || n > MaxContactLength || !utf8.ValidString(text) || strings.ContainsFunc(text, unicode.IsControl) {
		return "", &FieldError{Pointer: pointerContact, Code: CodeInvalidFormat,
			Detail: "The contact is 1 to 200 characters of text without control characters."}
	}
	return text, nil
}

// newCode is a code of CodeLength decimal digits, each value equally likely: a 32-bit number below the largest
// multiple of 10^CodeLength is taken, any other drawn again.
func newCode(random io.Reader) (string, error) {
	const space = 1_000_000
	const limit = math.MaxUint32 / space * space
	var b [4]byte
	for {
		if _, err := io.ReadFull(random, b[:]); err != nil {
			return "", fmt.Errorf("generate the contact code: %w", err)
		}
		if v := binary.BigEndian.Uint32(b[:]); v < limit {
			return fmt.Sprintf("%0*d", CodeLength, v%space), nil
		}
	}
}

// normalizeCode is a code as typed, without its spaces.
func normalizeCode(code string) string {
	return strings.Join(strings.Fields(code), "")
}

// hashText is the SHA-256 of a contact text, which the limits per contact count by.
func hashText(text string) []byte {
	h := sha256.Sum256([]byte(text))
	return h[:]
}

// codeHash is the SHA-256 of a code bound to its contact, as it is stored.
func codeHash(contactPublicID, code string) []byte {
	h := sha256.Sum256([]byte(contactPublicID + ":" + code))
	return h[:]
}

// language is the language of the person's notifications: their profile's, else English.
func language(l pgtype.Text) string {
	if l.Valid && l.String == messages.LanguageRussian {
		return messages.LanguageRussian
	}
	return messages.LanguageEnglish
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}
