// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package faketelegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"
)

// SecretTokenHeader carries the webhook's secret token on every update the fake posts to it.
const SecretTokenHeader = "X-Telegram-Bot-Api-Secret-Token" //nolint:gosec // G101: a header name, not a credential

// maxPollTimeout is the longest long poll the fake waits, in seconds, as the Bot API's own bound.
const maxPollTimeout = 50

// secretTokenPattern is what the Bot API accepts as a webhook's secret token.
var secretTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// bot is the state of one token: its queue of updates, its webhook and its running long poll.
type bot struct {
	queue []queued
	// next is the update_id of the next update queued without one.
	next    int64
	webhook *Webhook
	// allowed are the update types of the last getUpdates or setWebhook that named them; empty is every type.
	allowed []string
	// poll is the running long poll, and conflict a 409 owed to the next getUpdates.
	poll       *poll
	conflict   bool
	changed    chan struct{}
	delivering bool
}

// queued is an update waiting to be confirmed or posted.
type queued struct {
	id   int64
	kind string
	raw  json.RawMessage
}

// poll is a running long poll; end ends it with a 409 and its description.
type poll struct {
	end chan string
}

// Webhook is a bot's webhook as setWebhook set it.
type Webhook struct {
	Token          string   `json:"-"`
	URL            string   `json:"url"`
	SecretToken    string   `json:"secret_token"`
	AllowedUpdates []string `json:"allowed_updates"`
	MaxConnections int64    `json:"max_connections"`
}

// WebhookInfo is the answer of getWebhookInfo.
type WebhookInfo struct {
	URL                  string   `json:"url"`
	HasCustomCertificate bool     `json:"has_custom_certificate"`
	PendingUpdateCount   int      `json:"pending_update_count"`
	MaxConnections       int64    `json:"max_connections,omitempty"`
	AllowedUpdates       []string `json:"allowed_updates,omitempty"`
}

// bot is the state of token, created on first use; f.mu is held.
func (f *Fake) bot(token string) *bot {
	b, ok := f.bots[token]
	if !ok {
		b = &bot{next: 1, changed: make(chan struct{})}
		f.bots[token] = b
	}
	return b
}

// signal wakes the long polls of b; f.mu is held.
func (b *bot) signal() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// kindOf is the type of an update: the name of its field other than update_id.
func kindOf(raw json.RawMessage) (int64, bool, string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, false, "", errors.New("update is not a JSON object")
	}
	var id int64
	_, hasID := m["update_id"]
	if hasID {
		if err := json.Unmarshal(m["update_id"], &id); err != nil {
			return 0, false, "", errors.New("update_id is not an integer")
		}
	}
	kind := ""
	for k := range m {
		if k != "update_id" {
			kind = k
			break
		}
	}
	return id, hasID, kind, nil
}

// Enqueue queues update for the bot of token and returns its update_id: the one it carries, or the next one. With a
// webhook set, the queue is posted to it.
func (f *Fake) Enqueue(ctx context.Context, token string, update json.RawMessage) (int64, error) {
	if token == "" {
		return 0, errors.New("token is required")
	}
	id, hasID, kind, err := kindOf(update)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.bot(token)
	if !hasID {
		id = b.next
		var m map[string]json.RawMessage
		_ = json.Unmarshal(update, &m)
		m["update_id"] = json.RawMessage(jsonInt(id))
		update, _ = json.Marshal(m)
	}
	b.next = max(b.next, id+1)
	b.queue = append(b.queue, queued{id: id, kind: kind, raw: slices.Clone(update)})
	b.signal()
	f.startDelivery(ctx, b)
	return id, nil
}

func jsonInt(n int64) []byte {
	b, _ := json.Marshal(n)
	return b
}

// Conflict ends the running long poll of the bot of token with 409 and reports whether one was running; without one,
// the next getUpdates of the bot gets the 409.
func (f *Fake) Conflict(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.bot(token)
	if b.poll != nil {
		b.poll.end <- descriptionTerminated
		b.poll = nil
		return true
	}
	b.conflict = true
	return false
}

// Pending is how many updates of the bot of token are not confirmed or posted yet.
func (f *Fake) Pending(token string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bot(token).queue)
}

// Webhooks are the webhooks set, by token.
func (f *Fake) Webhooks() map[string]Webhook {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]Webhook{}
	for token, b := range f.bots {
		if b.webhook != nil {
			out[token] = *b.webhook
		}
	}
	return out
}

// confirm drops the updates before offset, as getUpdates with an offset does; f.mu is held.
func (b *bot) confirm(offset int64) {
	b.queue = slices.DeleteFunc(b.queue, func(q queued) bool { return q.id < offset })
}

// take returns up to limit updates of the allowed types in order and drops those of other types; f.mu is held.
func (b *bot) take(limit int64) []json.RawMessage {
	if len(b.allowed) > 0 {
		b.queue = slices.DeleteFunc(b.queue, func(q queued) bool { return !slices.Contains(b.allowed, q.kind) })
	}
	out := []json.RawMessage{}
	for _, q := range b.queue {
		if int64(len(out)) == limit {
			break
		}
		out = append(out, q.raw)
	}
	return out
}

// getUpdates answers a long poll: with a webhook set, or after POST /_fake/conflict, 409; otherwise the queued
// updates from offset, waiting up to timeout seconds for one. A second getUpdates on the token ends a running one with
// 409 (F-018).
func (f *Fake) getUpdates(w http.ResponseWriter, r *http.Request, token string, p params) {
	offset, hasOffset, err := p.int("offset")
	if err == nil {
		var limit, timeout int64
		var hasLimit bool
		if limit, hasLimit, err = p.int("limit"); err == nil {
			if !hasLimit || limit < 1 || limit > 100 {
				limit = 100
			}
			if timeout, _, err = p.int("timeout"); err == nil {
				var allowed []string
				var hasAllowed bool
				if allowed, hasAllowed, err = p.list("allowed_updates"); err == nil {
					f.longPoll(w, r, token, longPollArgs{offset: offset, hasOffset: hasOffset, limit: limit,
						timeout: min(max(timeout, 0), maxPollTimeout), allowed: allowed, hasAllowed: hasAllowed})
					return
				}
			}
		}
	}
	writeFailure(w, http.StatusBadRequest, "Bad Request: "+err.Error())
}

type longPollArgs struct {
	offset, limit, timeout int64
	hasOffset, hasAllowed  bool
	allowed                []string
}

func (f *Fake) longPoll(w http.ResponseWriter, r *http.Request, token string, a longPollArgs) {
	f.mu.Lock()
	b := f.bot(token)
	switch {
	case b.webhook != nil:
		f.mu.Unlock()
		writeFailure(w, http.StatusConflict, descriptionWebhookActive)
		return
	case b.conflict:
		b.conflict = false
		f.mu.Unlock()
		writeFailure(w, http.StatusConflict, descriptionTerminated)
		return
	}
	if b.poll != nil {
		b.poll.end <- descriptionTerminated
	}
	me := &poll{end: make(chan string, 1)}
	b.poll = me
	if a.hasOffset && a.offset > 0 {
		b.confirm(a.offset)
	}
	if a.hasAllowed {
		b.allowed = slices.Clone(a.allowed)
	}
	timer := time.NewTimer(time.Duration(a.timeout) * time.Second)
	defer timer.Stop()
	done := func() {
		if b.poll == me {
			b.poll = nil
		}
	}
	for {
		items := b.take(a.limit)
		if len(items) > 0 || a.timeout == 0 {
			done()
			f.mu.Unlock()
			writeOK(w, items)
			return
		}
		changed := b.changed
		f.mu.Unlock()
		select {
		case description := <-me.end:
			writeFailure(w, http.StatusConflict, description)
			return
		case <-changed:
			f.mu.Lock()
		case <-timer.C:
			f.mu.Lock()
			a.timeout = 0
		case <-r.Context().Done():
			f.mu.Lock()
			done()
			f.mu.Unlock()
			return
		}
	}
}

// setWebhook sets the bot's webhook; an empty url removes it, as the Bot API does. A running long poll ends with 409.
func (f *Fake) setWebhook(ctx context.Context, w http.ResponseWriter, token string, p params) {
	raw := p.string("url")
	if raw == "" {
		f.removeWebhook(token, p.bool("drop_pending_updates"))
		writeOK(w, true)
		return
	}
	secret := p.string("secret_token")
	if _, present := p["secret_token"]; present && !secretTokenPattern.MatchString(secret) {
		writeFailure(w, http.StatusBadRequest, "Bad Request: secret token contains unallowed characters")
		return
	}
	allowed, hasAllowed, err := p.list("allowed_updates")
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	maxConnections, _, err := p.int("max_connections")
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if !validWebhookURL(raw) {
		writeFailure(w, http.StatusBadRequest, "Bad Request: bad webhook: invalid webhook URL specified")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.bot(token)
	if hasAllowed {
		b.allowed = slices.Clone(allowed)
	}
	if p.bool("drop_pending_updates") {
		b.queue = nil
	}
	b.webhook = &Webhook{Token: token, URL: raw, SecretToken: secret, AllowedUpdates: slices.Clone(b.allowed),
		MaxConnections: maxConnections}
	if b.poll != nil {
		b.poll.end <- descriptionSetWebhook
		b.poll = nil
	}
	f.startDelivery(ctx, b)
	writeOK(w, true)
}

func validWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (f *Fake) deleteWebhook(w http.ResponseWriter, token string, p params) {
	f.removeWebhook(token, p.bool("drop_pending_updates"))
	writeOK(w, true)
}

func (f *Fake) removeWebhook(token string, drop bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.bot(token)
	b.webhook = nil
	if drop {
		b.queue = nil
	}
}

func (f *Fake) webhookInfo(token string) WebhookInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.bot(token)
	info := WebhookInfo{PendingUpdateCount: len(b.queue)}
	if b.webhook != nil {
		info.URL, info.MaxConnections = b.webhook.URL, b.webhook.MaxConnections
		info.AllowedUpdates = slices.Clone(b.webhook.AllowedUpdates)
	}
	return info
}

// startDelivery posts the queue of b to its webhook in the background, in order, unless it is already being posted;
// the posts outlive the request that started them. f.mu is held.
func (f *Fake) startDelivery(ctx context.Context, b *bot) {
	if b.webhook == nil || b.delivering || len(b.queue) == 0 {
		return
	}
	b.delivering = true
	go f.deliver(context.WithoutCancel(ctx), b)
}

// deliver posts the queued updates of b one at a time with the secret token header; an update the webhook does not
// accept with 2xx stays queued until the next update or setWebhook.
func (f *Fake) deliver(ctx context.Context, b *bot) {
	for {
		f.mu.Lock()
		if len(b.allowed) > 0 {
			b.queue = slices.DeleteFunc(b.queue, func(q queued) bool { return !slices.Contains(b.allowed, q.kind) })
		}
		if b.webhook == nil || len(b.queue) == 0 {
			b.delivering = false
			f.mu.Unlock()
			return
		}
		hook, next := *b.webhook, b.queue[0]
		f.mu.Unlock()
		if !f.post(ctx, hook, next.raw) {
			f.mu.Lock()
			b.delivering = false
			f.mu.Unlock()
			return
		}
		f.mu.Lock()
		b.queue = slices.DeleteFunc(b.queue, func(q queued) bool { return q.id == next.id })
		f.mu.Unlock()
	}
}

// post sends one update to hook and reports whether it was accepted with 2xx.
func (f *Fake) post(ctx context.Context, hook Webhook, update json.RawMessage) bool {
	ctx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(update))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if hook.SecretToken != "" {
		req.Header.Set(SecretTokenHeader, hook.SecretToken)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode/100 == 2
}
