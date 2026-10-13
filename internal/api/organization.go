// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// GetOrganization is getOrganization: the organization resource for every signed-in identity, its Secrets shown only
// as set or not (C-03.FR-20, FR-21).
func (s *Server) GetOrganization(ctx context.Context, _ gen.GetOrganizationRequestObject) (
	gen.GetOrganizationResponseObject, error) {
	o, err := s.organization.Get(ctx)
	if err != nil {
		return nil, err
	}
	tag := etag(o.Version)
	return gen.GetOrganization200JSONResponse{Body: organizationOf(o),
		Headers: gen.GetOrganization200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateOrganization is updateOrganization with If-Match: it changes every setting of the Organization (C-20.FR-1,
// FR-8, C-04.FR-8), the TOTP policy (C-03.FR-20) and the outgoing heartbeat, its write-only URL (null clears it) and its
// proxy (C-19.FR-7); an invalid field is 422 at its full JSON pointer.
func (s *Server) UpdateOrganization(ctx context.Context, req gen.UpdateOrganizationRequestObject) (
	gen.UpdateOrganizationResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	o, err := s.organization.Update(ctx, r.Actor, r.Transport, r.Address, version, inputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(o.Version)
	return gen.UpdateOrganization200JSONResponse{Body: organizationOf(o),
		Headers: gen.UpdateOrganization200ResponseHeaders{ETag: &tag}}, nil
}

// GetKeyring is getKeyring: every key held by this replica or a live replica, which is active, which live replicas
// hold or lack it, what still depends on it and its state (C-20.FR-6).
func (s *Server) GetKeyring(ctx context.Context, _ gen.GetKeyringRequestObject) (gen.GetKeyringResponseObject, error) {
	st, err := s.keyring.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.Keyring{Keys: make([]gen.KeyringKey, 0, len(st.Keys))}
	for _, k := range st.Keys {
		out.Keys = append(out.Keys, gen.KeyringKey{Id: k.ID, Active: k.Active, HeldBy: k.HeldBy,
			MissingOn: k.MissingOn, Needed: k.Needed, Removable: k.Removable, State: gen.KeyringKeyState(k.State)})
	}
	return gen.GetKeyring200JSONResponse(out), nil
}

// organizationOf is the API form of the organization resource.
func organizationOf(o organization.Organization) gen.Organization {
	tag := etag(o.Version)
	out := gen.Organization{
		Id: o.PublicID, Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
		CriticalIsUrgent: o.CriticalIsUrgent, InstanceLabels: o.InstanceLabels,
		SeverityMapping: make([]gen.SeverityMappingEntry, 0, len(o.SeverityMapping)),
		SeverityStyles:  make([]gen.SeverityLevelStyle, 0, len(o.SeverityStyles)),
		Retention: gen.RetentionSettings{
			StoredSnapshotsDays: int(o.Retention.StoredSnapshotsDays), AlertDetailsDays: int(o.Retention.AlertDetailsDays),
			AlertGroupSummariesDays: int(o.Retention.AlertGroupSummariesDays),
			AuditLogDays:            int(o.Retention.AuditLogDays),
		},
		TotpRequired: gen.TotpPolicy(o.TOTPRequired), OidcTokenGraceSeconds: int(o.OIDCTokenGrace / time.Second),
		Etag: &tag,
	}
	for _, m := range o.SeverityMapping {
		out.SeverityMapping = append(out.SeverityMapping, gen.SeverityMappingEntry{Value: m.Value,
			Level: gen.SeverityLevel(m.Level)})
	}
	for _, st := range o.SeverityStyles {
		out.SeverityStyles = append(out.SeverityStyles, gen.SeverityLevelStyle{Level: gen.SeverityLevel(st.Level),
			Emoji: st.Emoji, Color: st.Color})
	}
	hb := o.OutgoingHeartbeat
	out.OutgoingHeartbeat.UrlStatus = secretStatusOf(hb.URL)
	password := secretStatusOf(hb.ProxyPassword)
	out.OutgoingHeartbeat.Proxy = gen.ProxyConfig{Enabled: hb.Proxy.Enabled, PasswordStatus: &password}
	if hb.Proxy.Type != "" {
		t := gen.ProxyType(hb.Proxy.Type)
		out.OutgoingHeartbeat.Proxy.Type = &t
	}
	if hb.Proxy.Address != "" {
		addr := hb.Proxy.Address
		out.OutgoingHeartbeat.Proxy.Address = &addr
	}
	if hb.Proxy.Username != nil {
		out.OutgoingHeartbeat.Proxy.Username.Set(*hb.Proxy.Username)
	}
	return out
}

func secretStatusOf(st organization.SecretState) gen.SecretStatus {
	out := gen.SecretStatus{Set: st.Set}
	if st.UpdatedAt != nil {
		out.UpdatedAt.Set(st.UpdatedAt.UTC())
	} else {
		out.UpdatedAt.SetNull()
	}
	return out
}

// inputOf is an update of the organization resource as internal/organization takes it.
func inputOf(b gen.UpdateOrganizationJSONRequestBody) organization.Input {
	in := organization.Input{
		Name: b.Name, TimeZone: b.TimeZone, SeverityLabel: b.SeverityLabel, CriticalIsUrgent: b.CriticalIsUrgent,
		InstanceLabels: b.InstanceLabels, TOTPRequired: organization.TOTPPolicy(b.TotpRequired),
		OIDCTokenGraceSeconds: int64(b.OidcTokenGraceSeconds),
		Retention: organization.Retention{
			StoredSnapshotsDays: int64(b.Retention.StoredSnapshotsDays), AlertDetailsDays: int64(b.Retention.AlertDetailsDays),
			AlertGroupSummariesDays: int64(b.Retention.AlertGroupSummariesDays),
			AuditLogDays:            int64(b.Retention.AuditLogDays),
		},
		HeartbeatURL: secretInputOf(b.OutgoingHeartbeat.Url),
	}
	if in.InstanceLabels == nil {
		in.InstanceLabels = []string{}
	}
	for _, m := range b.SeverityMapping {
		in.SeverityMapping = append(in.SeverityMapping, organization.SeverityMapping{Value: m.Value,
			Level: organization.SeverityLevel(m.Level)})
	}
	for _, st := range b.SeverityStyles {
		in.SeverityStyles = append(in.SeverityStyles, organization.SeverityStyle{
			Level: organization.SeverityLevel(st.Level), Emoji: st.Emoji, Color: st.Color})
	}
	p := b.OutgoingHeartbeat.Proxy
	in.HeartbeatProxy = organization.ProxyInput{Enabled: p.Enabled, Address: p.Address,
		UsernameSet: p.Username.IsSpecified(), Password: secretInputOf(p.Password)}
	if p.Type != nil {
		t := string(*p.Type)
		in.HeartbeatProxy.Type = &t
	}
	if p.Username.IsSpecified() && !p.Username.IsNull() {
		u := p.Username.MustGet()
		in.HeartbeatProxy.Username = &u
	}
	return in
}

// secretInputOf is a write-only Secret field of the request body: omitted keeps it, null clears it.
func secretInputOf(v nullable.Nullable[string]) organization.SecretInput {
	switch {
	case !v.IsSpecified():
		return keyring.Keep
	case v.IsNull():
		return keyring.Clear
	}
	return keyring.Replace(logging.Secret(v.MustGet()))
}

// GetOutboundPolicy is getOutboundPolicy: the outbound address policy with the rules that always apply (C-20.FR-3).
func (s *Server) GetOutboundPolicy(ctx context.Context, _ gen.GetOutboundPolicyRequestObject) (
	gen.GetOutboundPolicyResponseObject, error) {
	p, err := s.organization.OutboundPolicy(ctx)
	if err != nil {
		return nil, err
	}
	tag := etag(p.Version)
	return gen.GetOutboundPolicy200JSONResponse{Body: outboundPolicyOf(p),
		Headers: gen.GetOutboundPolicy200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateOutboundPolicy is updateOutboundPolicy with If-Match: it replaces the policy and its lists, effective at once on
// every replica (C-20.FR-3, C-02.FR-21); always_blocked and etag in the body are ignored.
func (s *Server) UpdateOutboundPolicy(ctx context.Context, req gen.UpdateOutboundPolicyRequestObject) (
	gen.UpdateOutboundPolicyResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	p, err := s.organization.UpdateOutboundPolicy(ctx, r.Actor, r.Transport, r.Address, version,
		organization.OutboundPolicyInput{Mode: organization.OutboundMode(req.Body.Policy), Allowed: req.Body.Allowed,
			Denied: req.Body.Denied})
	if err != nil {
		return nil, err
	}
	tag := etag(p.Version)
	return gen.UpdateOutboundPolicy200JSONResponse{Body: outboundPolicyOf(p),
		Headers: gen.UpdateOutboundPolicy200ResponseHeaders{ETag: &tag}}, nil
}

// outboundPolicyOf is the API form of the outbound address policy.
func outboundPolicyOf(p organization.OutboundPolicyResource) gen.OutboundPolicy {
	tag := etag(p.Version)
	blocked := append([]string{}, p.AlwaysBlocked...)
	return gen.OutboundPolicy{Policy: gen.OutboundPolicyPolicy(p.Mode), Allowed: append([]string{}, p.Allowed...),
		Denied: append([]string{}, p.Denied...), AlwaysBlocked: &blocked, Etag: &tag}
}

// ListSeverityValues is listSeverityValues: the values of the severity label seen within
// organization.severity_values_window, with their counts and levels (C-20.FR-1, AC-4).
func (s *Server) ListSeverityValues(ctx context.Context, _ gen.ListSeverityValuesRequestObject) (
	gen.ListSeverityValuesResponseObject, error) {
	v, err := s.organization.SeverityValues(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListSeverityValues200JSONResponse{Items: make([]gen.SeverityValue, 0, len(v.Items)),
		WindowDays: v.WindowDays}
	for _, i := range v.Items {
		out.Items = append(out.Items, gen.SeverityValue{Value: i.Value, Count: int(i.Count),
			Level: gen.SeverityLevel(i.Level), Mapped: i.Mapped})
	}
	return out, nil
}
