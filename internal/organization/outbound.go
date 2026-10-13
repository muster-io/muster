// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/outbound"
)

// OutboundPolicyMaxAge is how long a replica keeps the outbound address policy it read before reading it again, the
// fallback when it missed the notification of a change.
const OutboundPolicyMaxAge = 10 * time.Second

// OutboundPolicyChannel is the LISTEN/NOTIFY channel of changes of the outbound address policy: on it every replica
// drops its cached policy and closes its idle outbound connections. The payload is the Organization's id.
const OutboundPolicyChannel = "muster_outbound_policy"

// The Audit log action and resource type of the outbound address policy.
const (
	ActionOutboundPolicyUpdated = "outbound_policy.updated"
	ResourceOutboundPolicy      = "outbound_policy"
)

// OutboundPolicyResource is the outbound address policy as the API shows it (C-20.FR-3).
type OutboundPolicyResource struct {
	Mode    OutboundMode
	Allowed []string
	Denied  []string
	// AlwaysBlocked are the rules no allowed entry opens, outbound.AlwaysBlocked.
	AlwaysBlocked []string
	Version       int64
}

// OutboundPolicyInput is an update of the outbound address policy.
type OutboundPolicyInput struct {
	Mode    OutboundMode
	Allowed []string
	Denied  []string
}

// OutboundPolicy reads the outbound address policy with the rules that always apply.
func (s *Service) OutboundPolicy(ctx context.Context) (OutboundPolicyResource, error) {
	return s.outboundPolicy(ctx, s.store)
}

func (s *Service) outboundPolicy(ctx context.Context, q SettingsQueries) (OutboundPolicyResource, error) {
	row, err := q.GetOutboundPolicySettings(ctx, s.orgID)
	if err != nil {
		return OutboundPolicyResource{}, fmt.Errorf("read the outbound address policy: %w", err)
	}
	return OutboundPolicyResource{Mode: OutboundMode(row.Policy), Allowed: nonNil(row.Allowed),
		Denied: nonNil(row.Denied), AlwaysBlocked: outbound.AlwaysBlocked(), Version: row.Version}, nil
}

// UpdateOutboundPolicy replaces the outbound address policy and its lists (C-20.FR-3, C-02.FR-21). version is the
// version of If-Match, nil for any; another one is ErrVersionMismatch. An entry that does not parse is a *FieldError
// with invalid_format, a repeated one with duplicate, at /allowed/<i> or /denied/<i>. A change is recorded as
// outbound_policy.updated with its diff, and sends NOTIFY on OutboundPolicyChannel and the organization hint in its
// transaction, so that every replica applies it at once; after the commit the hook of WithOutboundPolicyChanged
// applies it on this replica. An update that changes nothing writes nothing.
func (s *Service) UpdateOutboundPolicy(ctx context.Context, r audit.Actor, t audit.Transport, addr netip.Addr,
	version *int64, in OutboundPolicyInput) (OutboundPolicyResource, error) {
	after, fe := validateOutbound(in)
	if fe != nil {
		return OutboundPolicyResource{}, fe
	}
	var out OutboundPolicyResource
	var diff []audit.Change
	err := s.store.InTx(ctx, func(q SettingsQueries) error {
		current, err := q.LockOutboundPolicy(ctx, s.orgID)
		if err != nil {
			return fmt.Errorf("lock the outbound address policy: %w", err)
		}
		if version != nil && *version != current {
			return ErrVersionMismatch
		}
		before, err := s.outboundPolicy(ctx, q)
		if err != nil {
			return err
		}
		diff = audit.Diff(policyViewOf(before.Mode, before.Allowed, before.Denied),
			policyViewOf(after.Mode, after.Allowed, after.Denied))
		if len(diff) == 0 {
			out = before
			return nil
		}
		if _, err := q.UpdateOutboundPolicy(ctx, dbgen.UpdateOutboundPolicyParams{
			OrgID: s.orgID, Policy: string(after.Mode), Allowed: after.Allowed, Denied: after.Denied,
			UpdatedAt: s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("change the outbound address policy: %w", err)
		}
		if out, err = s.outboundPolicy(ctx, q); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r, Transport: t, Action: ActionOutboundPolicyUpdated,
			Resource: audit.Resource{Type: ResourceOutboundPolicy, Name: "Outbound address policy"},
			Diff:     diff, SourceAddress: addr,
		}); err != nil {
			return err
		}
		// The policy goes first: a replica handles its notifications in order, so once its live-updates streams have
		// the hint, it already applies the new policy.
		if err := q.NotifyOutboundPolicy(ctx, s.orgID); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: HintOrganization})
	})
	if err != nil {
		return OutboundPolicyResource{}, err
	}
	if len(diff) > 0 {
		if s.policyChanged != nil {
			s.policyChanged()
		}
		if s.log != nil {
			s.log.Log(ctx, logging.OutboundPolicyUpdated)
		}
	}
	return out, nil
}

// policyView is what the Audit log diff of the outbound address policy shows.
type policyView struct {
	Policy  string   `json:"policy"`
	Allowed []string `json:"allowed"`
	Denied  []string `json:"denied"`
}

func policyViewOf(mode OutboundMode, allowed, denied []string) policyView {
	return policyView{Policy: string(mode), Allowed: nonNil(allowed), Denied: nonNil(denied)}
}

// validateOutbound returns the update with its entries trimmed, or the first field that is not valid: an entry must
// parse as the policy itself parses it (outbound.ParseEntry), once per list (entryKey).
func validateOutbound(in OutboundPolicyInput) (OutboundPolicyInput, *FieldError) {
	if in.Mode != OutboundStandard && in.Mode != OutboundStrict {
		return OutboundPolicyInput{}, &FieldError{Pointer: "/policy", Code: CodeInvalidFormat,
			Detail: "The policy is not standard or strict."}
	}
	out := OutboundPolicyInput{Mode: in.Mode}
	for _, list := range []struct {
		name    string
		entries []string
		into    *[]string
	}{{"allowed", in.Allowed, &out.Allowed}, {"denied", in.Denied, &out.Denied}} {
		*list.into = make([]string, 0, len(list.entries))
		seen := map[string]bool{}
		for i, raw := range list.entries {
			pointer := fmt.Sprintf("/%s/%d", list.name, i)
			entry := strings.TrimSpace(raw)
			if _, err := outbound.ParseEntry(entry); err != nil {
				return OutboundPolicyInput{}, &FieldError{Pointer: pointer, Code: CodeInvalidFormat,
					Detail: "The entry is not a network in CIDR form, an address, a host name or a domain such as " +
						"*.example.org."}
			}
			key := entryKey(entry)
			if seen[key] {
				return OutboundPolicyInput{}, &FieldError{Pointer: pointer, Code: CodeDuplicate,
					Detail: "The entry appears twice in the list."}
			}
			seen[key] = true
			*list.into = append(*list.into, entry)
		}
	}
	return out, nil
}

// entryKey is what two entries that mean the same have in common: a network masked to its prefix, a bare address
// unmapped as the network of one address, a host name or a domain in lower case without its final dot.
func entryKey(entry string) string {
	if p, err := netip.ParsePrefix(entry); err == nil {
		return p.Masked().String()
	}
	if a, err := netip.ParseAddr(entry); err == nil {
		a = a.WithZone("").Unmap()
		return netip.PrefixFrom(a, a.BitLen()).String()
	}
	return strings.TrimSuffix(strings.ToLower(entry), ".")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// OutboundPolicies is the Organization's outbound address policy as the outbound package reads it: read from
// outbound_policies and cached per replica. A replica drops the cached policy at once on a notification on
// OutboundPolicyChannel (Invalidate); OutboundPolicyMaxAge of the real clock bounds its age when a notification was
// missed.
type OutboundPolicies struct {
	store Store
	orgID int64
	clock clock.Clock
	// closeIdle closes the idle outbound connections of this replica; tests replace it.
	closeIdle func()

	mu     sync.Mutex
	policy outbound.Policy
	// row is the policy as stored, to tell a re-read that changed it.
	row    dbgen.GetOutboundPolicyRow
	readAt time.Time
	cached bool
	// loaded is whether a policy was ever read, so that the first read closes nothing.
	loaded bool
	// epoch moves with each Invalidate; a read that started in an older one is not cached.
	epoch uint64
	// invalidated is whether Invalidate closed the connections since the last read, so that read closes nothing more.
	invalidated bool
	// read is the read in progress, which other callers wait for instead of reading too.
	read *policyRead
}

type policyRead struct {
	done   chan struct{}
	policy outbound.Policy
	err    error
}

// NewOutboundPolicies reads the policy of the Organization orgID through s; c is the real clock.
func NewOutboundPolicies(s Store, orgID int64, c clock.Clock) *OutboundPolicies {
	return &OutboundPolicies{store: s, orgID: orgID, clock: c, closeIdle: outbound.CloseIdleConnections}
}

// Invalidate drops the cached policy at once — on a notification on OutboundPolicyChannel, or a LISTEN that is back
// after a loss and may have missed one — and closes the idle outbound connections, so that the next request reads the
// policy again, dials, and is checked against it. A read in progress is not waited for: callers after Invalidate start
// a new one.
func (o *OutboundPolicies) Invalidate() {
	o.mu.Lock()
	o.epoch++
	o.cached, o.read, o.invalidated = false, nil, true
	o.mu.Unlock()
	o.closeIdle()
}

// OutboundPolicy returns the cached policy, or reads it again when there is none or the cached one is older than
// OutboundPolicyMaxAge. One caller reads while the others wait for its result, each within its own context, outside
// the lock. An error leaves the cache empty, so the requests it serves fail and the next one reads again. A re-read
// that finds another policy than the cached one closes the idle outbound connections, as Invalidate would have; the
// read that follows an Invalidate does not close them again.
func (o *OutboundPolicies) OutboundPolicy(ctx context.Context) (outbound.Policy, error) {
	o.mu.Lock()
	now := o.clock.Now()
	if o.cached && now.Sub(o.readAt) < OutboundPolicyMaxAge && !now.Before(o.readAt) {
		p := o.policy
		o.mu.Unlock()
		return p, nil
	}
	if r := o.read; r != nil {
		o.mu.Unlock()
		select {
		case <-r.done:
			return r.policy, r.err
		case <-ctx.Done():
			return outbound.Policy{}, ctx.Err()
		}
	}
	r := &policyRead{done: make(chan struct{})}
	o.read, o.cached = r, false
	epoch := o.epoch
	o.mu.Unlock()

	var row dbgen.GetOutboundPolicyRow
	row, r.policy, r.err = o.load(ctx)
	o.mu.Lock()
	changed := false
	if r.err == nil && o.epoch == epoch {
		changed = o.loaded && !o.invalidated && !sameRow(o.row, row)
		o.policy, o.row, o.readAt, o.cached, o.loaded, o.invalidated = r.policy, row, now, true, true, false
	}
	if o.read == r {
		o.read = nil
	}
	o.mu.Unlock()
	close(r.done)
	if changed {
		o.closeIdle()
	}
	return r.policy, r.err
}

func sameRow(a, b dbgen.GetOutboundPolicyRow) bool {
	return a.Policy == b.Policy && slices.Equal(a.Allowed, b.Allowed) && slices.Equal(a.Denied, b.Denied)
}

func (o *OutboundPolicies) load(ctx context.Context) (dbgen.GetOutboundPolicyRow, outbound.Policy, error) {
	row, err := o.store.GetOutboundPolicy(ctx, o.orgID)
	if err != nil {
		return row, outbound.Policy{}, fmt.Errorf("read the outbound address policy: %w", err)
	}
	p, err := outbound.ParsePolicy(row.Policy, row.Allowed, row.Denied)
	if err != nil {
		return row, outbound.Policy{}, fmt.Errorf("the stored outbound address policy: %w", err)
	}
	return row, p, nil
}
