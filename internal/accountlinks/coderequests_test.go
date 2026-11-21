// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
)

// codeStarted is what the start of a code link answers, whatever its messenger.
type codeStarted struct {
	requestID string
	expiresAt time.Time
	attempts  int
}

// codeMessenger is a messenger whose links are confirmed by a code that its bot sends: how to ask for a code, how to
// confirm one, and the codes its bot sent to an account.
type codeMessenger struct {
	name     string
	handle   string
	external string
	others   []string
	start    func(t *testing.T, e *env, r Requester, handle string) (codeStarted, error)
	confirm  func(t *testing.T, e *env, r Requester, requestID, code string) (Link, error)
	codes    func(e *env, external string) []string
}

var codeMessengers = []codeMessenger{
	{
		name: MessengerMattermost, handle: "dana", external: "u-dana", others: []string{"bob", "alice"},
		start: func(t *testing.T, e *env, r Requester, handle string) (codeStarted, error) {
			st, err := e.s.StartMattermost(t.Context(), r, mmPID, handle)
			return codeStarted{st.RequestPublicID, st.ExpiresAt, st.AttemptsRemaining}, err
		},
		confirm: func(t *testing.T, e *env, r Requester, id, code string) (Link, error) {
			return e.s.ConfirmMattermost(t.Context(), r, id, code)
		},
		codes: func(e *env, external string) []string {
			var out []string
			for _, m := range e.mm.sent {
				if c := codeRe.FindString(m.text); m.user == external && c != "" {
					out = append(out, c)
				}
			}
			return out
		},
	},
	{
		name: MessengerBridge, handle: "dana@example.org", external: "u-dana",
		others: []string{"bob@example.org", "alice@example.org"},
		start: func(t *testing.T, e *env, r Requester, handle string) (codeStarted, error) {
			st, err := e.s.StartBridgeCode(t.Context(), r, brPID, handle)
			return codeStarted{st.RequestPublicID, st.ExpiresAt, st.AttemptsRemaining}, err
		},
		confirm: func(t *testing.T, e *env, r Requester, id, code string) (Link, error) {
			return e.s.ConfirmBridge(t.Context(), r, id, code)
		},
		codes: func(e *env, external string) []string {
			var out []string
			for _, m := range e.br.sent {
				if c := codeRe.FindString(m.text); m.user == external && c != "" {
					out = append(out, c)
				}
			}
			return out
		},
	},
	{
		name: MessengerSlack, handle: "dana", external: "U0DANA", others: []string{"bob", "alice@example.org"},
		start: func(t *testing.T, e *env, r Requester, handle string) (codeStarted, error) {
			st, err := e.s.StartSlack(t.Context(), r, slPID, handle)
			return codeStarted{st.RequestPublicID, st.ExpiresAt, st.AttemptsRemaining}, err
		},
		confirm: func(t *testing.T, e *env, r Requester, id, code string) (Link, error) {
			return e.s.ConfirmSlack(t.Context(), r, id, code)
		},
		codes: func(e *env, external string) []string {
			var out []string
			for _, m := range e.sl.sent {
				if m.user == external && m.code != "" {
					out = append(out, string(m.code))
				}
			}
			return out
		},
	},
}

// moreUsers adds n active Users to the store, as requesters.
func (e *env) moreUsers(n int) []Requester {
	out := make([]Requester, 0, n)
	for i := range n {
		id := int64(50 + i)
		pid := fmt.Sprintf("SRAAAAAAAAAB%02d", i)
		e.store.users = append(e.store.users, fUser{id, pid, fmt.Sprintf("user%d", i), fmt.Sprintf("User %d", i),
			"responder", StatusActive, ""})
		out = append(out, Requester{UserID: id, UserPublicID: pid, Actor: audit.User(id, pid),
			Transport: audit.TransportUI, Address: netip.MustParseAddr("192.0.2.9")})
	}
	return out
}

// TestCodeRequestsOfOthers is account_link.mattermost_code_requests, account_link.bridge_code_requests and
// account_link.slack_code_requests across Users: another User's requests for an account never refuse its owner's, who
// is answered as when nobody asked, gets the code and links; a User's own requests are refused with 429 at the fourth
// for one account and the sixth in all within the hour, until their own oldest is an hour old.
func TestCodeRequestsOfOthers(t *testing.T) {
	for _, m := range codeMessengers {
		t.Run(m.name, func(t *testing.T) {
			base := newEnv(t)
			want, err := m.start(t, base, alice, m.handle)
			if err != nil {
				t.Fatal(err)
			}
			e := newEnv(t)
			for range 3 {
				if _, err := m.start(t, e, bob, m.handle); err != nil {
					t.Fatal(err)
				}
			}
			got, err := m.start(t, e, alice, m.handle)
			if err != nil || got.requestID == "" || !got.expiresAt.Equal(want.expiresAt) || got.attempts != want.attempts {
				t.Fatalf("the owner's request after three of another User = %+v, %v; when nobody asked %+v", got, err,
					want)
			}
			codes := m.codes(e, m.external)
			if len(codes) != 4 {
				t.Fatalf("the account received %d codes, want 4", len(codes))
			}
			l, err := m.confirm(t, e, alice, got.requestID, codes[3])
			if err != nil || l.ExternalID != m.external {
				t.Fatalf("the owner's confirmation = %+v, %v", l, err)
			}

			// A User's own limits: the fourth request for one account, then the sixth in all.
			f := newEnv(t)
			if _, err := m.start(t, f, bob, m.handle); err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(10 * time.Minute)
			for range 3 {
				if _, err := m.start(t, f, alice, m.handle); err != nil {
					t.Fatal(err)
				}
				f.clock.Advance(time.Minute)
			}
			sent := len(m.codes(f, m.external))
			var rl *RateLimitedError
			if _, err := m.start(t, f, alice, m.handle); !errors.As(err, &rl) || rl.RetryAfter != 57*time.Minute ||
				len(m.codes(f, m.external)) != sent {
				t.Fatalf("the User's fourth request for one account = %v", err)
			}
			for _, h := range m.others {
				if _, err := m.start(t, f, alice, h); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.start(t, f, bob, m.handle); err != nil {
				t.Errorf("another User's request for the account = %v", err)
			}
			if _, err := m.start(t, f, alice, m.others[0]); !errors.As(err, &rl) || rl.RetryAfter != 57*time.Minute {
				t.Errorf("the User's sixth request = %v", err)
			}
		})
	}
}

// TestCodeRequestsWithheld is the cross-User cap of account_link.*_code_requests: an account receives at most ten codes
// within the hour, whoever asks; a request beyond them is answered as one whose code was sent, voids the requester's
// earlier request as a sent code does, stores a request no code confirms, which counts toward the limits, sends
// nothing and is logged as account_link_code_withheld.
func TestCodeRequestsWithheld(t *testing.T) {
	type asked struct {
		r    Requester
		st   codeStarted
		code string
	}
	for _, m := range codeMessengers {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t)
			users := append([]Requester{alice, bob}, e.moreUsers(2)...)
			var (
				all          []asked
				drawn, ninth string
			)
			for round := range 3 {
				for _, r := range users {
					// The code the tenth request draws, which it sends, and the eleventh, which it never sends.
					if len(all) == 9 || len(all) == 10 {
						next := *e.s.cfg.Random.(*counter)
						c, err := newCode(&next)
						if err != nil {
							t.Fatal(err)
						}
						ninth, drawn = drawn, c
					}
					st, err := m.start(t, e, r, m.handle)
					if err != nil {
						t.Fatalf("round %d of %s = %v", round, r.UserPublicID, err)
					}
					if st.requestID == "" || !st.expiresAt.Equal(t0.Add(10*time.Minute)) || st.attempts != 5 {
						t.Fatalf("round %d of %s answered %+v", round, r.UserPublicID, st)
					}
					a := asked{r: r, st: st}
					if codes := m.codes(e, m.external); len(codes) == len(all)+1 {
						a.code = codes[len(codes)-1]
					}
					all = append(all, a)
				}
			}
			if n := len(m.codes(e, m.external)); n != 10 || len(e.store.reqs) != 12 {
				t.Fatalf("the account received %d codes of %d requests, want 10 of 12", n, len(e.store.reqs))
			}
			if got := strings.Count(e.log.String(),
				`"event":"account_link_code_withheld","messenger":"`+m.name+`"`); got != 2 {
				t.Errorf("%d withheld codes logged: %s", got, e.log)
			}
			withheld, earlier := all[10], all[6]
			if withheld.code != "" || earlier.code == "" || earlier.r.UserID != withheld.r.UserID {
				t.Fatalf("the eleventh request %+v, the earlier one of its User %+v", withheld, earlier)
			}
			if _, err := m.confirm(t, e, earlier.r, earlier.st.requestID, earlier.code); !linkCodeInvalid(err) ||
				attemptsLeft(err) != 0 {
				t.Errorf("the earlier request after a withheld one = %v", err)
			}
			if ninth != all[9].code {
				t.Fatalf("the code drawn for the tenth request %q, sent %q", ninth, all[9].code)
			}
			i := slices.IndexFunc(e.store.reqs, func(r fReq) bool { return r.publicID == withheld.st.requestID })
			if i < 0 || len(e.store.reqs[i].hash) != sha256.Size || bytes.Equal(e.store.reqs[i].hash, hash(drawn)) {
				t.Fatalf("the withheld request stored the hash of its code %q", drawn)
			}
			for _, c := range []string{drawn, m.codes(e, m.external)[0], "22222222"} {
				if _, err := m.confirm(t, e, withheld.r, withheld.st.requestID, c); !linkCodeInvalid(err) {
					t.Fatalf("a withheld request confirmed with %q = %v", c, err)
				}
			}
			if _, err := m.start(t, e, withheld.r, m.handle); !errors.As(err, new(*RateLimitedError)) {
				t.Errorf("a fourth request after a withheld one = %v", err)
			}
			// Another User's request whose code was sent, the tenth, still confirms after the withheld ones.
			tenth := all[9]
			if l, err := m.confirm(t, e, tenth.r, tenth.st.requestID, tenth.code); err != nil ||
				l.ExternalID != m.external {
				t.Errorf("the tenth request after the withheld ones = %+v, %v", l, err)
			}
		})
	}
}
