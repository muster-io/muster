// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/schedules"
)

// IcalFeeds is what the API needs of the personal iCal feeds of internal/schedules (C-27.FR-10).
type IcalFeeds interface {
	Get(ctx context.Context, userID int64) (schedules.Feed, error)
	Issue(ctx context.Context, r schedules.Requester, u schedules.FeedUser) (schedules.IssuedFeed, error)
	Revoke(ctx context.Context, r schedules.Requester, u schedules.FeedUser) error
	RevokeOfUser(ctx context.Context, r schedules.Requester, userPublicID string) error
	Lookup(ctx context.Context, value string) (schedules.FeedUser, error)
	Calendar(ctx context.Context, u schedules.FeedUser) ([]byte, error)
}

// IcalMaskedPath is the path of every iCal feed as log lines name it: its token is a credential (L2.md NFR-6).
const IcalMaskedPath = schedules.FeedBasePath + "[redacted]" + schedules.FeedSuffix

// contentTypeCalendar is the media type of an iCal feed.
const contentTypeCalendar = "text/calendar; charset=utf-8"

// IcalHandler serves the iCal feeds at /ical/{token}.ics on the app listener, without a session (getIcalCalendar).
// It logs nothing on success, ical_feed_not_found for a 404 and ical_feed_failed for a failure, each with the path
// masked as IcalMaskedPath: the real path, which carries the token, never reaches a log line, an error or an answer.
type IcalHandler struct {
	Feeds IcalFeeds
	Log   *logging.Logger
}

// NewIcalHandler is the IcalHandler over feeds and log, with the series of muster_ical_feed_requests_total at zero from
// the start, so that increase() sees the first fetch of each outcome.
func NewIcalHandler(feeds IcalFeeds, log *logging.Logger) IcalHandler {
	for _, outcome := range metrics.IcalFeedOutcomes {
		metrics.IcalFeedRequests.With(outcome)
	}
	return IcalHandler{Feeds: feeds, Log: log}
}

func (h IcalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", "GET, HEAD")
		writeIcalProblem(w, http.StatusMethodNotAllowed, typeMethodNotAllowed,
			"The iCal feed takes GET and HEAD only.")
		return
	}
	value, ok := strings.CutPrefix(r.URL.Path, schedules.FeedBasePath)
	if ok {
		value, ok = strings.CutSuffix(value, schedules.FeedSuffix)
	}
	user, err := schedules.FeedUser{}, schedules.ErrFeedNotFound
	if ok {
		user, err = h.Feeds.Lookup(r.Context(), value)
	}
	var body []byte
	if err == nil {
		body, err = h.Feeds.Calendar(r.Context(), user)
	}
	switch {
	case errors.Is(err, schedules.ErrFeedNotFound):
		metrics.IcalFeedRequests.With("not_found").Inc()
		h.Log.Log(r.Context(), logging.IcalFeedNotFound, h.fields(user)...)
		writeIcalProblem(w, http.StatusNotFound, typeNotFound, "No such iCal feed.")
		return
	case err != nil:
		metrics.IcalFeedRequests.With("failed").Inc()
		h.Log.Log(r.Context(), logging.IcalFeedFailed, append(h.fields(user), logging.F("error", err.Error()))...)
		writeIcalProblem(w, http.StatusInternalServerError, typeInternal, "The iCal feed cannot be read now.")
		return
	}
	metrics.IcalFeedRequests.With("served").Inc()
	header.Set("Content-Type", contentTypeCalendar)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:gosec // G705: an iCal document Muster wrote with its values escaped as TEXT, served as text/calendar
}

// fields are the fields of the log line of a request for a feed: the masked path and the User where it is known.
func (IcalHandler) fields(user schedules.FeedUser) []logging.Field {
	fields := []logging.Field{logging.F("path", IcalMaskedPath)}
	if user.PublicID != "" {
		fields = append(fields, logging.F("user", user.PublicID))
	}
	return fields
}

// writeIcalProblem writes an RFC 9457 problem without an instance and without anything about the token, its User or
// the Organization: the path of the request carries the token.
func writeIcalProblem(w http.ResponseWriter, status int, typ, detail string) {
	w.Header().Set("Content-Type", contentTypeProblem)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(gen.Problem{Type: problemBase + typ, Title: titles[typ], Status: status,
		Detail: &detail})
}
