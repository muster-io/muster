// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The values of notification_reason that processing reads (Alertmanager v0.32 and later, F-050).
const (
	ReasonRepeat      = "repeat interval elapsed"
	ReasonAllResolved = "all alerts resolved"
)

// The statuses of a Snapshot and of its Alerts.
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
)

// Payload is an Alertmanager webhook as processing reads it (AlertmanagerWebhook in the API specification).
type Payload struct {
	GroupKey        string
	Status          string
	TruncatedAlerts int64
	// Reason is notification_reason; HasReason is false for an Alertmanager that does not send it.
	Reason    string
	HasReason bool
	Alerts    []PayloadAlert
}

// PayloadAlert is one Alert of a Payload. Fingerprint is the payload's, or computed from the labels as received.
type PayloadAlert struct {
	Fingerprint  string
	Status       string
	Labels       map[string]string
	Annotations  map[string]string
	StartsAt     time.Time
	EndsAt       *time.Time
	GeneratorURL string
}

// PayloadError is a body that is not an Alertmanager webhook; its text is the processing_error of the failed Stored
// Snapshot.
type PayloadError struct {
	msg string
}

func (e *PayloadError) Error() string { return e.msg }

func payloadError(format string, args ...any) error {
	return &PayloadError{msg: fmt.Sprintf(format, args...)}
}

type wireWebhook struct {
	GroupKey        *string           `json:"groupKey"`
	Status          *string           `json:"status"`
	TruncatedAlerts *int64            `json:"truncatedAlerts"`
	Reason          *string           `json:"notification_reason"`
	Alerts          []json.RawMessage `json:"alerts"`
}

type wireAlert struct {
	Status       *string         `json:"status"`
	Labels       json.RawMessage `json:"labels"`
	Annotations  json.RawMessage `json:"annotations"`
	StartsAt     *string         `json:"startsAt"`
	EndsAt       *string         `json:"endsAt"`
	GeneratorURL *string         `json:"generatorURL"`
	Fingerprint  *string         `json:"fingerprint"`
}

// ParsePayload reads an Alertmanager webhook. groupKey, status and alerts are required, and every Alert needs labels,
// status and startsAt; truncatedAlerts, notification_reason and fingerprint are optional (C-06.FR-20). Anything else
// is a *PayloadError that names what is wrong.
func ParsePayload(body []byte) (Payload, error) {
	if !json.Valid(body) {
		var v any
		err := json.Unmarshal(body, &v)
		return Payload{}, payloadError("the body is not valid JSON: %v", err)
	}
	var w wireWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		return Payload{}, payloadError("the body is not an Alertmanager webhook: %v", plainJSONError(err))
	}
	switch {
	case w.GroupKey == nil:
		return Payload{}, payloadError("the webhook has no groupKey")
	case w.Status == nil:
		return Payload{}, payloadError("the webhook has no status")
	case *w.Status != StatusFiring && *w.Status != StatusResolved:
		return Payload{}, payloadError("the status of the webhook is %s, not firing or resolved", strconv.Quote(*w.Status))
	case w.Alerts == nil:
		return Payload{}, payloadError("the webhook has no alerts")
	case w.TruncatedAlerts != nil && *w.TruncatedAlerts < 0:
		return Payload{}, payloadError("truncatedAlerts is negative")
	case hasNUL(*w.GroupKey) || (w.Reason != nil && hasNUL(*w.Reason)):
		return Payload{}, payloadError("the webhook has a NUL character, which PostgreSQL cannot store")
	}
	p := Payload{GroupKey: *w.GroupKey, Status: *w.Status, Alerts: make([]PayloadAlert, 0, len(w.Alerts))}
	if w.TruncatedAlerts != nil {
		p.TruncatedAlerts = *w.TruncatedAlerts
	}
	if w.Reason != nil {
		p.Reason, p.HasReason = *w.Reason, true
	}
	for i, raw := range w.Alerts {
		a, err := parseAlert(raw)
		if err != nil {
			return Payload{}, payloadError("alert %d: %v", i, err)
		}
		p.Alerts = append(p.Alerts, a)
	}
	return p, nil
}

func parseAlert(raw json.RawMessage) (PayloadAlert, error) {
	var w wireAlert
	if err := json.Unmarshal(raw, &w); err != nil {
		return PayloadAlert{}, fmt.Errorf("not an alert: %v", plainJSONError(err))
	}
	if w.Status == nil {
		return PayloadAlert{}, errors.New("no status")
	}
	if *w.Status != StatusFiring && *w.Status != StatusResolved {
		return PayloadAlert{}, fmt.Errorf("the status is %s, not firing or resolved", strconv.Quote(*w.Status))
	}
	labels, err := stringMap(w.Labels, true)
	if err != nil {
		return PayloadAlert{}, fmt.Errorf("labels %w", err)
	}
	annotations, err := stringMap(w.Annotations, false)
	if err != nil {
		return PayloadAlert{}, fmt.Errorf("annotations %w", err)
	}
	if w.StartsAt == nil {
		return PayloadAlert{}, errors.New("no startsAt")
	}
	starts, err := time.Parse(time.RFC3339Nano, *w.StartsAt)
	if err != nil {
		return PayloadAlert{}, errors.New("startsAt is not an RFC 3339 time")
	}
	// Times are kept to the microsecond, as PostgreSQL stores them, so that a startsAt read back from an Alert equals
	// the same startsAt received again.
	a := PayloadAlert{Status: *w.Status, Labels: labels, Annotations: annotations,
		StartsAt: starts.UTC().Truncate(time.Microsecond)}
	if w.EndsAt != nil {
		ends, err := time.Parse(time.RFC3339Nano, *w.EndsAt)
		if err != nil {
			return PayloadAlert{}, errors.New("endsAt is not an RFC 3339 time")
		}
		// Alertmanager sends the zero time for an Alert without an end.
		if ends.Year() > 1 {
			e := ends.UTC().Truncate(time.Microsecond)
			a.EndsAt = &e
		}
	}
	if w.GeneratorURL != nil {
		a.GeneratorURL = *w.GeneratorURL
	}
	if w.Fingerprint != nil && *w.Fingerprint != "" {
		a.Fingerprint = *w.Fingerprint
	} else {
		a.Fingerprint = Fingerprint(labels)
	}
	if hasNUL(a.Fingerprint, a.GeneratorURL) || mapHasNUL(labels) || mapHasNUL(annotations) {
		return PayloadAlert{}, errors.New("a NUL character, which PostgreSQL cannot store")
	}
	return a, nil
}

// hasNUL reports a NUL character, which PostgreSQL text and jsonb refuse, in any of the strings.
func hasNUL(values ...string) bool {
	for _, v := range values {
		if strings.IndexByte(v, 0) >= 0 {
			return true
		}
	}
	return false
}

func mapHasNUL(m map[string]string) bool {
	for k, v := range m {
		if hasNUL(k, v) {
			return true
		}
	}
	return false
}

// stringMap reads an object of strings; a missing or null object is empty unless required.
func stringMap(raw json.RawMessage, required bool) (map[string]string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		if required {
			return nil, errors.New("are missing")
		}
		return map[string]string{}, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("are not an object of strings")
	}
	return m, nil
}

// plainJSONError keeps the reason of a decoding error and drops the Go type names it may carry.
func plainJSONError(err error) string {
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		if te.Field != "" {
			return fmt.Sprintf("%s has the wrong type (%s)", te.Field, te.Value)
		}
		return fmt.Sprintf("the value has the wrong type (%s)", te.Value)
	}
	return err.Error()
}

// Fingerprint is the fingerprint Alertmanager gives a label set: FNV-1a over the label names in order, each name and
// value followed by the byte 0xff, written as 16 hexadecimal digits.
func Fingerprint(labels map[string]string) string {
	h := fnv.New64a()
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0xff})
		_, _ = h.Write([]byte(labels[name]))
		_, _ = h.Write([]byte{0xff})
	}
	return fmt.Sprintf("%016x", h.Sum64())
}
