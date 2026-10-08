// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakemattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The reasons a press cannot be made.
var (
	ErrUnknownPost   = errors.New("unknown or deleted post")
	ErrUnknownAction = errors.New("unknown action")
	ErrUnknownUser   = errors.New("unknown user")
)

const (
	resolveTimeout = 2 * time.Second
	maxAnswer      = 1 << 20
)

// Press is a button press as GET /_fake/presses lists it: Request is the JSON sent to the integration URL, null when
// none was sent, and Status and Answer are the integration's answer, 0 and null when there was none. Error is
// ActionIntegrationError when the person got the 400.
type Press struct {
	AtMs     int64           `json:"at_ms"`
	PostID   string          `json:"post_id"`
	ActionID string          `json:"action_id"`
	UserID   string          `json:"user_id"`
	URL      string          `json:"url"`
	Request  json.RawMessage `json:"request"`
	Status   int             `json:"status"`
	Answer   json.RawMessage `json:"answer"`
	Error    string          `json:"error,omitempty"`
}

// PressResult is the answer of POST /_fake/press: the integration's status and JSON answer, and the status the
// person got, 200 or 400 with ActionIntegrationError.
type PressResult struct {
	Status       int             `json:"status"`
	Answer       json.RawMessage `json:"answer"`
	Error        string          `json:"error,omitempty"`
	PersonStatus int             `json:"person_status"`

	triggerID string
}

// pressRequest is what the server posts to the integration URL (F-023).
type pressRequest struct {
	UserID      string          `json:"user_id"`
	UserName    string          `json:"user_name"`
	ChannelID   string          `json:"channel_id"`
	ChannelName string          `json:"channel_name"`
	TeamID      string          `json:"team_id"`
	TeamDomain  string          `json:"team_domain"`
	PostID      string          `json:"post_id"`
	TriggerID   string          `json:"trigger_id"`
	Type        string          `json:"type"`
	DataSource  string          `json:"data_source"`
	Context     json.RawMessage `json:"context,omitempty"`
}

// pressAnswer is the part of an integration's answer the server uses; any other field is ignored (F-024).
type pressAnswer struct {
	Update *struct {
		Message string          `json:"message"`
		Props   json.RawMessage `json:"props"`
	} `json:"update"`
	EphemeralText string `json:"ephemeral_text"`
}

// Presses returns the presses in the order they were made.
func (f *Fake) Presses() []Press {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Press{}, f.st.presses...)
}

// Press presses the button actionID of a live post as the user userID, root post or Thread reply alike (F-055): the
// address check of F-022, the request of F-023 with a wait of PressTimeout (F-032), and the answer's update and
// ephemeral_text (F-023 to F-025). An error means no press was made.
func (f *Fake) Press(ctx context.Context, postID, actionID, userID string) (PressResult, error) {
	req, target, err := f.preparePress(postID, actionID, userID)
	if err != nil {
		return PressResult{}, err
	}
	res := PressResult{PersonStatus: http.StatusOK, triggerID: req.TriggerID}
	rec := Press{AtMs: f.nowMs(), PostID: postID, ActionID: actionID, UserID: userID, URL: target}
	if reason := f.forbiddenAddress(ctx, target); reason != "" {
		f.failPress(&res, reason)
	} else {
		rec.Request, _ = json.Marshal(req)
		f.callIntegration(ctx, &res, target, rec.Request, req)
	}
	rec.Status, rec.Answer, rec.Error = res.Status, res.Answer, res.Error
	f.mu.Lock()
	f.st.presses = append(f.st.presses, rec)
	f.mu.Unlock()
	return res, nil
}

func (f *Fake) preparePress(postID, actionID, userID string) (pressRequest, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.st.posts[postID]
	if !ok || p.DeleteAt != 0 {
		return pressRequest{}, "", fmt.Errorf("%w %s", ErrUnknownPost, postID)
	}
	var act *action
	for _, a := range attachments(p.Props) {
		for i := range a.Actions {
			if a.Actions[i].ID == actionID && act == nil {
				act = &a.Actions[i]
			}
		}
	}
	if act == nil || act.Integration.URL == "" {
		return pressRequest{}, "", fmt.Errorf("%w %s on post %s", ErrUnknownAction, actionID, postID)
	}
	u, ok := userByID(userID)
	if !ok {
		return pressRequest{}, "", fmt.Errorf("%w %s", ErrUnknownUser, userID)
	}
	c := f.st.channels[p.ChannelID]
	req := pressRequest{
		UserID:      u.ID,
		UserName:    u.Username,
		ChannelID:   c.ID,
		ChannelName: c.Name,
		PostID:      p.ID,
		TriggerID:   newID(),
		Context:     act.Integration.Context,
	}
	if c.TeamID == TeamID {
		req.TeamID, req.TeamDomain = TeamID, TeamName
	}
	return req, act.Integration.URL, nil
}

func (f *Fake) failPress(res *PressResult, reason string) {
	res.PersonStatus, res.Error = http.StatusBadRequest, ActionIntegrationError
	f.mu.Lock()
	f.logLocked("error", reason)
	f.mu.Unlock()
}

// forbiddenAddress returns why the server refuses to call target, or "" when it may (F-022): a host that resolves to
// a reserved address must be listed in AllowedUntrustedInternalConnections by name, by address or by a range.
func (f *Fake) forbiddenAddress(ctx context.Context, target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return "action integration request failed: invalid integration URL"
	}
	host := u.Hostname()
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
		addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		cancel()
		if err != nil {
			return "action integration request failed: " + err.Error()
		}
	}
	allowed := strings.FieldsFunc(f.Config().AllowedUntrustedInternalConnections, func(r rune) bool {
		return r == ' ' || r == ','
	})
	for _, a := range addrs {
		a = a.Unmap()
		if reserved(a) && !allowedAddress(host, a, allowed) {
			return fmt.Sprintf("address forbidden, you may need to set AllowedUntrustedInternalConnections to allow an "+
				"integration access to your internal network: %s resolves to %s, in a reserved range and not in "+
				"AllowedUntrustedInternalConnections", host, a)
		}
	}
	return ""
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func reserved(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || cgnat.Contains(a)
}

func allowedAddress(host string, a netip.Addr, allowed []string) bool {
	for _, entry := range allowed {
		if strings.EqualFold(entry, host) {
			return true
		}
		if e, err := netip.ParseAddr(entry); err == nil && e.Unmap() == a {
			return true
		}
		if p, err := netip.ParsePrefix(entry); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

// callIntegration posts the press and applies a successful answer. A transport error, a timeout, a status other than
// 2xx or an answer that is not JSON fails the press for the person.
func (f *Fake) callIntegration(ctx context.Context, res *PressResult, target string, body []byte, req pressRequest) {
	ctx, cancel := context.WithTimeout(ctx, f.PressTimeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		f.failPress(res, "action integration request failed: "+err.Error())
		return
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(hreq)
	if err != nil {
		f.failPress(res, "action integration request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	res.Status = resp.StatusCode
	answer = bytes.TrimSpace(answer)
	if len(answer) > 0 && json.Valid(answer) {
		res.Answer = answer
	}
	switch {
	case err != nil:
		f.failPress(res, "action integration request failed: reading the answer: "+err.Error())
		return
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		f.failPress(res, fmt.Sprintf("action integration request failed: %s answered status %d", target,
			resp.StatusCode))
		return
	}
	var a pressAnswer
	if len(answer) > 0 {
		if err := json.Unmarshal(answer, &a); err != nil {
			f.failPress(res, "action integration request failed: the answer is not valid JSON: "+err.Error())
			return
		}
	}
	if reason := f.applyAnswer(a, req); reason != "" {
		f.failPress(res, reason)
	}
}

// applyAnswer edits the pressed post with the answer's update, which replaces its message and props and marks it
// edited without notifying anyone (F-023, F-029), and shows the answer's ephemeral_text to the person in the post's
// Thread, from System (F-025).
func (f *Fake) applyAnswer(a pressAnswer, req pressRequest) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.st.posts[req.PostID]
	if !ok || p.DeleteAt != 0 {
		return "action integration answer not applied: the post is gone"
	}
	now := f.nowMs()
	if a.Update != nil {
		props, ok := objectProps(a.Update.Props)
		if !ok {
			return "action integration answer not applied: update.props is not an object"
		}
		p.Message, p.Props = a.Update.Message, props
		p.EditAt, p.UpdateAt = now, now
	}
	if a.EphemeralText != "" {
		root := p.RootID
		if root == "" {
			root = p.ID
		}
		f.st.ephemeral = append(f.st.ephemeral, Ephemeral{
			AtMs:      now,
			PostID:    newID(),
			UserID:    req.UserID,
			From:      "System",
			ChannelID: p.ChannelID,
			RootID:    root,
			Message:   a.EphemeralText,
			ShownIn:   ShownInThread,
		})
	}
	return ""
}

func (f *Fake) handlePress(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PostID   string `json:"post_id"`
		ActionID string `json:"action_id"`
		UserID   string `json:"user_id"`
	}
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := f.Press(r.Context(), in.PostID, in.ActionID, in.UserID)
	switch {
	case errors.Is(err, ErrUnknownUser):
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		fakeserver.WriteError(w, http.StatusNotFound, err.Error())
	default:
		fakeserver.WriteJSON(w, http.StatusOK, res)
	}
}

// handleAction is a press by the bot itself through the API (F-054).
func (f *Fake) handleAction(w http.ResponseWriter, r *http.Request) {
	res, err := f.Press(r.Context(), r.PathValue("post_id"), r.PathValue("action_id"), BotUserID)
	switch {
	case errors.Is(err, ErrUnknownPost):
		postNotFound(w)
	case err != nil:
		writeAppError(w, http.StatusNotFound, "api.post.do_action.action_id.app_error", "Invalid action id.")
	case res.PersonStatus != http.StatusOK:
		writeAppError(w, http.StatusBadRequest, "api.post.do_action.action_integration.app_error",
			ActionIntegrationError+".")
	default:
		fakeserver.WriteJSON(w, http.StatusOK, map[string]string{"status": "OK", "trigger_id": res.triggerID})
	}
}
