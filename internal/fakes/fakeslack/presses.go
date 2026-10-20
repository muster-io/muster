// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack

import (
	"bytes"
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The presses of the fake (F-093, F-104). POST /_fake/press presses a button, or chooses an option of the Snooze menu,
// of a message the fake keeps, as one of its people, and sends the block_actions payload Slack would send to the app's
// request_url, signed with its Slack signing secret, as Slack's Interactivity does in http mode. The payload's
// response_url is the fake's own /actions/<team>/<n>/<hex>, outside /_fake/, so that what Muster posts to it is recorded
// in /_fake/requests and takes faults; GET /_fake/responses lists it. GET /_fake/ephemeral lists what
// chat.postEphemeral showed.

// The people of the fake's workspaces, members of every channel of both apps where the bot is a member, and their
// names.
const (
	UserAlice = "U0FAKEALICE"
	UserBob   = "U0FAKEBOB"
	UserCarol = "U0FAKECAROL"
	UserDora  = "U0FAKEDORA"
)

// people are the names of the people of the fake, the person of a person's token included.
var people = map[string]string{UserAlice: "alice", UserBob: "bob", UserCarol: "carol", UserDora: "dora",
	UserID: UserName}

// member reports whether user is one of the fake's people, who are in every channel the bot is in.
func member(user string) bool {
	_, ok := people[user]
	return ok
}

// The published legacy verification tokens of the two apps, which every payload carries (F-093).
const (
	VerificationToken       = "muster-dev-slack-verification-token"   //nolint:gosec // G101: the published development token of the fake
	SecondVerificationToken = "muster-dev-slack-verification-token-b" //nolint:gosec // G101: the published development token of the fake
)

// TransportHTTP is how /_fake/press sends a press: to the app's request_url. It is the only one until Socket Mode.
const TransportHTTP = "http"

const (
	// actionsPrefix starts the path of every response_url of the fake.
	actionsPrefix = "/actions/"
	// responseUses is how many times a response_url takes an answer (F-093).
	responseUses = 5
	// pressTimeout bounds a press's request to the app's request_url.
	pressTimeout = 10 * time.Second
)

// PressRequest is the body of POST /_fake/press: the app, b by default; the message by its channel and ts; the action
// — ack, unack, resolve, unsnooze, still_on_it, or snooze with the index of the option; the person who pressed; for the
// negative cases, a Signature sent as it is instead of the right one, a timestamp that many seconds from now, the value
// of the same action of the message ValueOfTS of the channel, or a Value sent as it is; and the Transport, http.
type PressRequest struct {
	App                    string  `json:"app"`
	Channel                string  `json:"channel"`
	TS                     string  `json:"ts"`
	Action                 string  `json:"action"`
	Option                 int     `json:"option"`
	User                   string  `json:"user"`
	Signature              string  `json:"signature"`
	TimestampOffsetSeconds int64   `json:"timestamp_offset_seconds"`
	ValueOfTS              string  `json:"value_of_ts"`
	Value                  *string `json:"value"`
	Transport              string  `json:"transport"`
}

// PressResult is Muster's answer to a press: its status and body.
type PressResult struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// Ephemeral is a message chat.postEphemeral showed, as GET /_fake/ephemeral lists it: the app, channel and user, the
// Thread when one was named, its text and its message_ts.
type Ephemeral struct {
	App      string  `json:"app"`
	Channel  string  `json:"channel"`
	User     string  `json:"user"`
	ThreadTS *string `json:"thread_ts"`
	Text     string  `json:"text"`
	TS       string  `json:"ts"`
}

// Response is an answer to a press's response_url, as GET /_fake/responses lists it: the app, channel and user of the
// press, and what the answer sent, null for a field it did not send.
type Response struct {
	App             string  `json:"app"`
	Channel         string  `json:"channel"`
	User            string  `json:"user"`
	ResponseType    *string `json:"response_type"`
	ReplaceOriginal *bool   `json:"replace_original"`
	DeleteOriginal  *bool   `json:"delete_original"`
	Text            string  `json:"text"`
	ThreadTS        *string `json:"thread_ts"`
}

// responseSlot is a press's response_url: the press it answers and the answers it took.
type responseSlot struct {
	app, channel, user string
	uses               int
}

// Ephemerals are the messages chat.postEphemeral showed, in order.
func (f *Fake) Ephemerals() []Ephemeral {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ephemeral)
}

// Responses are the answers to the response_url of presses, in order.
func (f *Fake) Responses() []Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.responses)
}

func (f *Fake) listEphemeral(w http.ResponseWriter, _ *http.Request) {
	fakeserver.WriteJSON(w, http.StatusOK, f.Ephemerals())
}

func (f *Fake) listResponses(w http.ResponseWriter, _ *http.Request) {
	fakeserver.WriteJSON(w, http.StatusOK, f.Responses())
}

// pressError is a press the fake cannot send, with the status of its answer.
type pressError struct {
	status int
	msg    string
}

func (e *pressError) Error() string { return e.msg }

// pressAction is the element pressed and where it stands: its actions block and whether that is in an attachment.
type pressAction struct {
	element    map[string]any
	blockID    string
	attachment bool
}

// actionOf finds the element of action in the actions blocks of the message m — in its attachments, then its own
// blocks — with, for the Snooze menu, the option at index option.
func actionOf(m *stored, action string, option int) (pressAction, *pressError) {
	type block struct {
		Type     string           `json:"type"`
		BlockID  string           `json:"block_id"`
		Elements []map[string]any `json:"elements"`
	}
	var atts []struct {
		Blocks []block `json:"blocks"`
	}
	var own []block
	_ = json.Unmarshal(orEmpty(m.attachments), &atts)
	_ = json.Unmarshal(orEmpty(m.blocks), &own)
	type place struct {
		blocks     []block
		attachment bool
	}
	places := make([]place, 0, len(atts)+1)
	for _, a := range atts {
		places = append(places, place{a.Blocks, true})
	}
	places = append(places, place{own, false})
	for _, p := range places {
		for _, b := range p.blocks {
			if b.Type != "actions" {
				continue
			}
			for _, el := range b.Elements {
				if el["action_id"] != action {
					continue
				}
				if el["type"] == "static_select" {
					options, _ := el["options"].([]any)
					if option < 0 || option >= len(options) {
						return pressAction{}, &pressError{http.StatusUnprocessableEntity,
							fmt.Sprintf("the message has no option %d of %s", option, action)}
					}
					el = maps.Clone(el)
					el["selected_option"] = options[option]
				}
				return pressAction{element: el, blockID: b.BlockID, attachment: p.attachment}, nil
			}
		}
	}
	return pressAction{}, &pressError{http.StatusUnprocessableEntity, "the message has no " + action}
}

// valueOf is the value an element carries: a button's value, or the value of the chosen option of a menu.
func valueOf(a pressAction) string {
	if o, ok := a.element["selected_option"].(map[string]any); ok {
		s, _ := o["value"].(string)
		return s
	}
	s, _ := a.element["value"].(string)
	return s
}

// withValue is the action as a payload names it (F-093), carrying value.
func withValue(a pressAction, value string, actionTS string) map[string]any {
	out := map[string]any{"type": a.element["type"], "action_id": a.element["action_id"], "block_id": a.blockID,
		"action_ts": actionTS}
	if o, ok := a.element["selected_option"].(map[string]any); ok {
		o = maps.Clone(o)
		o["value"] = value
		out["selected_option"] = o
		if p, ok := a.element["placeholder"]; ok {
			out["placeholder"] = p
		}
		return out
	}
	out["text"], out["value"] = a.element["text"], value
	if s, ok := a.element["style"]; ok {
		out["style"] = s
	}
	return out
}

// randomHex is n random bytes in hex.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Sign is Slack's signature of body sent at timestamp under secret (F-104): `v0=` and the hex HMAC-SHA256 of
// `v0:<timestamp>:<body>`.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// payload is the block_actions payload of the press in (F-093), whose response_url the fake keeps, and the app's
// request_url, under f.mu.
func (f *Fake) payload(in PressRequest, app App, cfg Config, base string) ([]byte, string, *pressError) {
	target := cfg.RequestURL[app.Key]
	k := chatKey{app.Key, in.Channel}
	m, root := f.find(k, in.TS)
	switch {
	case m == nil:
		return nil, "", &pressError{http.StatusNotFound, "no such message"}
	case target == "":
		return nil, "", &pressError{http.StatusConflict, "the app has no request_url"}
	}
	a, perr := actionOf(m, in.Action, in.Option)
	if perr != nil {
		return nil, "", perr
	}
	value := valueOf(a)
	if in.ValueOfTS != "" {
		other, _ := f.find(k, in.ValueOfTS)
		if other == nil {
			return nil, "", &pressError{http.StatusNotFound, "no such message " + in.ValueOfTS}
		}
		o, perr := actionOf(other, in.Action, in.Option)
		if perr != nil {
			return nil, "", perr
		}
		value = valueOf(o)
	}
	if in.Value != nil {
		value = *in.Value
	}
	f.pressSeq++
	n := strconv.FormatInt(f.pressSeq, 10)
	path := actionsPrefix + app.TeamID + "/" + n + "/" + randomHex(12)
	f.slots[path] = &responseSlot{app: app.Key, channel: in.Channel, user: in.User}
	container := map[string]any{"type": "message", "message_ts": m.ts, "channel_id": in.Channel,
		"is_ephemeral": false}
	if a.attachment {
		container["type"], container["attachment_id"], container["is_app_unfurl"] = "message_attachment", 1, false
	}
	if root != nil {
		container["thread_ts"] = root.ts
	}
	name := people[in.User]
	domain := strings.TrimSuffix(strings.TrimPrefix(app.TeamURL, "https://"), ".slack.com/")
	b, err := json.Marshal(map[string]any{
		"type":                  "block_actions",
		"token":                 app.VerificationToken,
		"team":                  map[string]any{"id": app.TeamID, "domain": domain},
		"user":                  map[string]any{"id": in.User, "username": name, "name": name, "team_id": app.TeamID},
		"api_app_id":            app.AppID,
		"is_enterprise_install": false,
		"enterprise":            nil,
		"container":             container,
		"trigger_id":            n + "." + n + "." + randomHex(16),
		"channel":               map[string]any{"id": in.Channel, "name": cfg.Channels[app.Key][in.Channel].Name},
		"message":               answerOf(caller{app: app, kind: kindBot}, m, root),
		"state":                 map[string]any{"values": map[string]any{}},
		"response_url":          base + path,
		"actions":               []any{withValue(a, value, n+".000000")},
	})
	if err != nil {
		return nil, "", &pressError{http.StatusInternalServerError, err.Error()}
	}
	return b, target, nil
}

// pressClient sends the presses to the apps' request_url.
var pressClient = &http.Client{Timeout: pressTimeout}

// press answers POST /_fake/press with PressResult: 400 for a request it cannot read or a transport other than http,
// 404 for a message the fake does not keep, 409 for an app without a request_url, 422 for an action the message does
// not carry and 502 when the request_url does not answer.
func (f *Fake) press(w http.ResponseWriter, r *http.Request) {
	var in PressRequest
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	app, ok := appByKey(cmp.Or(in.App, AppB))
	switch {
	case !ok:
		fakeserver.WriteError(w, http.StatusBadRequest, "app: "+in.App+" is not a or b")
		return
	case cmp.Or(in.Transport, TransportHTTP) != TransportHTTP:
		fakeserver.WriteError(w, http.StatusBadRequest, "transport: only http sends presses")
		return
	case in.Channel == "" || in.TS == "" || in.Action == "" || in.User == "":
		fakeserver.WriteError(w, http.StatusBadRequest, "channel, ts, action and user are required")
		return
	}
	base, cfg := f.URL(), f.Config()
	f.mu.Lock()
	payload, target, perr := f.payload(in, app, cfg, base)
	f.mu.Unlock()
	if perr != nil {
		fakeserver.WriteError(w, perr.status, perr.msg)
		return
	}
	body := []byte("payload=" + url.QueryEscape(string(payload)))
	timestamp := strconv.FormatInt(time.Now().Unix()+in.TimestampOffsetSeconds, 10)
	signature := cmp.Or(in.Signature, Sign(app.SigningSecret, timestamp, body))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, "request_url: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Slackbot 1.0 (+https://api.slack.com/robots)")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", signature)
	resp, err := pressClient.Do(req)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadGateway, "request_url: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	fakeserver.WriteJSON(w, http.StatusOK, PressResult{Status: resp.StatusCode, Body: string(answer)})
}

// respond answers a POST to a press's response_url (F-093): an unknown one, or one that took its five answers, is
// expired_url; a body that is not a JSON object is invalid_payload; anything else is kept in /_fake/responses.
func (f *Fake) respond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, `{"ok":false,"error":"method_not_allowed"}`)
		return
	}
	var in struct {
		ResponseType    *string `json:"response_type"`
		ReplaceOriginal *bool   `json:"replace_original"`
		DeleteOriginal  *bool   `json:"delete_original"`
		Text            string  `json:"text"`
		ThreadTS        *string `json:"thread_ts"`
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	decodeErr := json.Unmarshal(data, &in)
	f.mu.Lock()
	slot, ok := f.slots[r.URL.Path]
	switch {
	case !ok || slot.uses >= responseUses:
		f.mu.Unlock()
		answerStatus(w, http.StatusNotFound, "expired_url")
		return
	case err != nil || decodeErr != nil || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")):
		f.mu.Unlock()
		answerStatus(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	slot.uses++
	f.responses = append(f.responses, Response{App: slot.app, Channel: slot.channel, User: slot.user,
		ResponseType: in.ResponseType, ReplaceOriginal: in.ReplaceOriginal, DeleteOriginal: in.DeleteOriginal,
		Text: in.Text, ThreadTS: in.ThreadTS})
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

// answerStatus answers the status with Slack's error code.
func answerStatus(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": code})
}
