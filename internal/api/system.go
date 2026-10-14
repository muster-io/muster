// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/systemstatus"
)

// SystemStatus is the System status of internal/systemstatus.
type SystemStatus interface {
	Get(ctx context.Context) (systemstatus.Status, error)
}

// permissionSystemStatus is the Permission that shows the notices for Admins.
const permissionSystemStatus auth.Permission = "system-status:read"

// ListSystemNotices is listSystemNotices: the active Organization-wide notices (C-03.FR-18, C-02.FR-24) — "recovering
// after downtime" for every signed-in identity, "no replica is leading" only for one that holds system-status:read.
func (s *Server) ListSystemNotices(ctx context.Context, _ gen.ListSystemNoticesRequestObject) (
	gen.ListSystemNoticesResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	notices, err := s.notices.Visible(ctx, id.Can(permissionSystemStatus))
	if err != nil {
		return nil, err
	}
	out := gen.SystemNoticeList{Items: make([]gen.SystemNotice, 0, len(notices))}
	for _, n := range notices {
		item := gen.SystemNotice{Kind: gen.SystemNoticeKind(n.Kind), Audience: gen.SystemNoticeAudience(n.Audience)}
		if n.Since.IsZero() {
			item.Since.SetNull()
		} else {
			item.Since.Set(n.Since.UTC())
		}
		if n.Until.IsZero() {
			item.Until.SetNull()
		} else {
			item.Until.Set(n.Until.UTC())
		}
		out.Items = append(out.Items, item)
	}
	return gen.ListSystemNotices200JSONResponse(out), nil
}

// GetSystemStatus is getSystemStatus (C-19.FR-10, C-19.AC-6): the replicas with the Leader and their key ids, the
// recovery state, the delivery queues, the Broken Destinations, the Integrations that need attention, the template
// errors, the active Storms and the last outgoing heartbeat, for an identity that holds system-status:read, which the
// permission middleware checks. The resource has no operation that changes anything.
func (s *Server) GetSystemStatus(ctx context.Context, _ gen.GetSystemStatusRequestObject) (
	gen.GetSystemStatusResponseObject, error) {
	st, err := s.systemStatus.Get(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetSystemStatus200JSONResponse(systemStatusOf(st)), nil
}

// systemStatusOf is the API form of the System status: every list present, every nullable time and result set or
// null.
func systemStatusOf(st systemstatus.Status) gen.SystemStatus {
	out := gen.SystemStatus{
		Version:               st.Version,
		Replicas:              make([]gen.ReplicaStatus, 0, len(st.Replicas)),
		DeliveryQueues:        make([]gen.QueueDepth, 0, len(st.DeliveryQueues)),
		BrokenDestinations:    make([]gen.BrokenDestination, 0, len(st.BrokenDestinations)),
		IntegrationsAttention: make([]gen.IntegrationAttention, 0, len(st.IntegrationsAttention)),
		TemplateErrors:        make([]gen.TemplateErrorEntry, 0, len(st.TemplateErrors)),
		ActiveStorms:          make([]gen.ActiveStorm, 0, len(st.ActiveStorms)),
	}
	if st.Commit != "" {
		commit := st.Commit
		out.Commit = &commit
	}
	for _, r := range st.Replicas {
		out.Replicas = append(out.Replicas, gen.ReplicaStatus{Id: r.ID, Leader: r.Leader, KeyIds: r.KeyIDs,
			RefreshedAt: r.RefreshedAt.UTC()})
	}
	out.Recovery.Active = st.Recovery.Active
	out.Recovery.PeriodFrom = nullableTime(st.Recovery.PeriodFrom)
	out.Recovery.PeriodTo = nullableTime(st.Recovery.PeriodTo)
	out.Recovery.Until = nullableTime(st.Recovery.Until)
	for _, q := range st.DeliveryQueues {
		out.DeliveryQueues = append(out.DeliveryQueues, gen.QueueDepth{Destination: entityRefOf(q.Destination),
			Queued: int(q.Queued)})
	}
	for _, b := range st.BrokenDestinations {
		out.BrokenDestinations = append(out.BrokenDestinations, gen.BrokenDestination{
			Destination: entityRefOf(b.Destination), Since: b.Since.UTC(), Reason: b.Reason})
	}
	for _, a := range st.IntegrationsAttention {
		out.IntegrationsAttention = append(out.IntegrationsAttention, gen.IntegrationAttention{
			Integration: entityRefOf(a.Integration), Issue: gen.IntegrationAttentionIssue(a.Issue),
			Since: nullableTime(a.Since)})
	}
	for _, e := range st.TemplateErrors {
		out.TemplateErrors = append(out.TemplateErrors, gen.TemplateErrorEntry{
			SubjectKind: gen.TemplateErrorEntrySubjectKind(e.SubjectKind), Subject: entityRefOf(e.Subject),
			Template: gen.TemplateKind(e.Template), Since: e.Since.UTC(), Error: e.Error})
	}
	for _, storm := range st.ActiveStorms {
		out.ActiveStorms = append(out.ActiveStorms, gen.ActiveStorm{Route: entityRefOf(storm.Route),
			Since: storm.Since.UTC(), AlertGroupCount: int(storm.AlertGroupCount)})
	}
	hb := st.OutgoingHeartbeat
	out.OutgoingHeartbeat = gen.OutgoingHeartbeatResult{Configured: hb.Configured,
		LastAttemptAt: nullableTime(hb.LastAttemptAt), LastOk: nullableBool(hb.LastOK), LastError: nullableString(hb.LastError)}
	if hb.LastErrorClass != "" {
		out.OutgoingHeartbeat.LastErrorClass.Set(gen.NullableOutgoingHeartbeatErrorClass(hb.LastErrorClass))
	} else {
		out.OutgoingHeartbeat.LastErrorClass.SetNull()
	}
	if hb.LastStatus != 0 {
		out.OutgoingHeartbeat.LastStatus.Set(hb.LastStatus)
	} else {
		out.OutgoingHeartbeat.LastStatus.SetNull()
	}
	return out
}

func entityRefOf(e systemstatus.Entity) gen.EntityRef {
	return gen.EntityRef{Id: e.ID, Name: e.Name}
}

// nullableBool is v as a nullable field: null when it is nil.
func nullableBool(v *bool) nullable.Nullable[bool] {
	if v == nil {
		return nullable.NewNullNullable[bool]()
	}
	return nullable.NewNullableWithValue(*v)
}
