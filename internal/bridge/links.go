// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// Linker links the account behind an account_link event; *accountlinks.Service implements it.
type Linker interface {
	LinkBridge(ctx context.Context, conn accountlinks.BridgeConnection, token string, from accountlinks.BridgeAccount) (
		accountlinks.BridgeLinked, error)
}

// link handles the account_link event ev of the Connection c inside its claim (C-22.FR-15): the Linker links the
// sender's account — its external id, display name and handle — or rejects the token, in a transaction of its own, and
// says what to answer. Handle answers once the claim committed and then has the Linker log the link or the rejection,
// with the answer's error stripped of the token, since a bridge may repeat what it was sent. A failure of the Linker
// fails the event, so that it comes again.
func (e *Events) link(ctx context.Context, c Conn, ev Event) (handled, error) {
	out, err := e.Linker.LinkBridge(ctx, accountlinks.BridgeConnection{ID: c.ID, PublicID: c.PublicID}, ev.Token,
		accountlinks.BridgeAccount{ExternalID: ev.User.ExternalId, DisplayName: ev.User.DisplayName,
			Handle: ev.User.Handle.GetOrEmpty()})
	if err != nil {
		return handled{}, err
	}
	h := handled{answer: out.Answer}
	if out.Log != nil {
		h.after = func(ctx context.Context, answerErr error) { out.Log(ctx, withoutSecret(answerErr, ev.Token)) }
	}
	return h, nil
}

// withoutSecret is err with secret replaced by [redacted]; a token is base64url, which no text escapes.
func withoutSecret(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
}

// LinkConnections find the Bridge Connection of an Account link by code and read its info, declared by their
// consumer; *connections.Service implements them.
type LinkConnections interface {
	// BridgeConn is the Bridge Connection publicID with its client, kept until the Connection changes; one that is
	// unknown, deleted or not a Bridge Connection is ErrNoConnection.
	BridgeConn(ctx context.Context, publicID string) (Conn, error)
	// BridgeInfo is Muster's copy of the info of conn, read again through its client in class when it is older than
	// bridge.info_max_age; a failed read returns the stale copy, if there is one, with the failed Result.
	BridgeInfo(ctx context.Context, conn Conn, class outbound.Class) (Info, Result)
}

// LinkBots run the calls of an Account link by code through the bot of a Bridge Connection (C-22.FR-15, C-18.FR-11):
// the lookup of the account by its handle and the direct message with the code, on the interactive path, never as a
// delivery. They implement accountlinks.BridgeBots.
type LinkBots struct {
	Conns LinkConnections
	Path  Path
}

// Session takes one limiter token of the Connection publicID on the interactive path and runs f with its bot, whose
// offer comes from the Connection's info, read again first when Muster's copy is older than bridge.info_max_age
// (C-22.FR-2), the stale copy when that read fails; a RetryAfter that the read or a call of f gets holds the
// Connection. A Connection that is unknown, deleted or not a
// Bridge one is accountlinks.ErrNoConnection; no token within the budget is a *delivery.LimitedError and runs nothing.
func (l LinkBots) Session(ctx context.Context, publicID string,
	f func(ctx context.Context, b accountlinks.BridgeBot) error) error {
	conn, err := l.Conns.BridgeConn(ctx, publicID)
	if errors.Is(err, ErrNoConnection) {
		return accountlinks.ErrNoConnection
	}
	if err != nil {
		return err
	}
	var ferr error
	id := conn.ID
	if _, err := l.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.DirectOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			info, r := l.Conns.BridgeInfo(ctx, conn, call.Class)
			b := &linkBot{conn: conn, class: call.Class, offer: offerOf(info),
				last: delivery.Outcome{Kind: delivery.OutcomeOK}}
			if o := r.Outcome(); o.Kind == delivery.OutcomeRetryAfter {
				o.Scope = delivery.ScopeConnection
				b.last = o
			}
			ferr = f(ctx, b)
			return b.last
		})); err != nil {
		return err
	}
	return ferr
}

// offerOf is what info offers for Account links; a zero info, which Muster never read, offers no code.
func offerOf(i Info) accountlinks.BridgeOffer {
	o := accountlinks.BridgeOffer{Read: i.ProtocolVersion != "", UserLookup: string(i.UserLookup)}
	if dm := i.Capabilities.DirectMessages; dm != nil {
		o.BotCanStart = dm.BotCanStart
	}
	return o
}

// linkBot is the bot of a Bridge Connection within a Session.
type linkBot struct {
	conn  Conn
	class outbound.Class
	offer accountlinks.BridgeOffer
	// last is the outcome of the last call that failed, which the Session hands to the interactive path.
	last delivery.Outcome
}

// Offer is what the Connection's info offers for Account links.
func (b *linkBot) Offer() accountlinks.BridgeOffer { return b.offer }

// FindUser finds the account with handle through findUsers; none, and a handle the bridge cannot search for — a 400
// of the class invalid — are accountlinks.ErrMessengerUserNotFound.
func (b *linkBot) FindUser(ctx context.Context, handle string) (accountlinks.BridgeAccount, error) {
	p, found, r := b.conn.Client.FindUsers(ctx, b.class, handle)
	if (r.OK() && !found) || (r.Classification.Class == ClassInvalid && r.Status == http.StatusBadRequest) {
		return accountlinks.BridgeAccount{}, accountlinks.ErrMessengerUserNotFound
	}
	if err := b.failed("findUsers", r); err != nil {
		return accountlinks.BridgeAccount{}, err
	}
	return accountlinks.BridgeAccount{ExternalID: p.ExternalId, DisplayName: p.DisplayName,
		Handle: p.Handle.GetOrEmpty()}, nil
}

// SendDirectMessage sends text to the account externalID through sendDirectMessage with the Idempotency-Key key; an
// account the bot cannot write to — an answer of the class forbidden, other than the 401 of a refused token — is
// accountlinks.ErrDirectMessageRefused, which leaves the Connection as it is.
func (b *linkBot) SendDirectMessage(ctx context.Context, key, externalID, text string) error {
	_, r := b.conn.Client.SendDirectMessage(ctx, b.class, key, externalID, text)
	if r.Classification.Class == ClassForbidden && r.Status != 0 && r.Status != http.StatusUnauthorized {
		return accountlinks.ErrDirectMessageRefused
	}
	return b.failed("sendDirectMessage", r)
}

// failed is the error of a call that did not succeed: a RetryAfter is a *delivery.LimitedError and holds the
// Connection, anything else an *accountlinks.MessengerError that names the call and how it ended, never the bridge's
// text, which may repeat the direct message and so the code.
func (b *linkBot) failed(call string, r Result) error {
	if r.OK() {
		return nil
	}
	o := r.Outcome()
	if o.Kind == delivery.OutcomeRetryAfter {
		o.Scope = delivery.ScopeConnection
		b.last = o
		return &delivery.LimitedError{RetryAfter: o.RetryAfter}
	}
	b.last = o
	return &accountlinks.MessengerError{Call: call, Outcome: string(r.Classification.Class), Status: r.Status}
}
