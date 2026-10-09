// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/templates"
	"github.com/muster-io/muster/internal/webhooks/dbgen"
)

// The timeouts of a request and the largest answer read; only its status and the start of its text are used.
const (
	connectTimeout = 5 * time.Second
	callTimeout    = 10 * time.Second
	maxAnswerBytes = 64 << 10
)

// Network is what the outbound clients of the adapter need: the outbound address policy, the logger, the real clock
// of the signatures and the outbound metrics, and a resolver (nil: the system resolver).
type Network struct {
	Policy   outbound.PolicySource
	Log      *logging.Logger
	Real     clock.Clock
	Resolver outbound.Resolver
}

// Adapter sends the requests of outgoing webhooks for delivery (C-15.FR-2, FR-3, FR-5, FR-6, FR-8): the events-mode
// requests, and in the template mode "create", "update", "open thread" and "reply in thread" as the adapter of the
// Destination type webhook, which delivery reconciles like a messenger's (ADR-0005). It renders the request templates
// with the Destination's Secrets in the sandbox, signs the body with every Signing secret that still signs, sends it
// through internal/outbound with the Destination's proxy in the client class of the call, maps the answer to a delivery
// outcome and extracts the values of the response. Every secret of the Destination is registered for redaction, so no
// error it returns carries one. It also renders the Desired state of the template mode and previews request
// templates.
type Adapter struct {
	Service *Service
	Network Network
	Sandbox *templates.Sandbox
	mu      sync.Mutex
	clients map[clientKey]cachedClient
}

var (
	_ delivery.EventSender = (*Adapter)(nil)
	_ delivery.Adapter     = (*Adapter)(nil)
	_ delivery.Requests    = (*Adapter)(nil)
)

type clientKey struct {
	destination int64
	class       outbound.Class
}

// cachedClient is the outbound client of a Destination and a class, built for the settings whose digest it keeps.
type cachedClient struct {
	digest [32]byte
	client *outbound.Client
}

// target is an outgoing webhook as a request needs it, with its secrets opened.
type target struct {
	events   *EventsConfig
	template *TemplateConfig
	proxy    proxyconf.Config
	password logging.Secret
	signing  []logging.Secret
	secrets  map[string]string
}

// all are every secret of the target, for redaction.
func (t target) all() []logging.Secret {
	out := []logging.Secret{t.password}
	out = append(out, t.signing...)
	for _, v := range t.secrets {
		out = append(out, logging.Secret(v))
	}
	return out
}

// errNoTarget is a Destination that is not an outgoing webhook.
var errNoTarget = errors.New("the destination is not an outgoing webhook")

// target reads the outgoing webhook destinationID, deleted or not, and opens its secrets.
func (s *Service) target(ctx context.Context, destinationID int64) (target, error) {
	row, err := s.cfg.Store.GetTarget(ctx, dbgen.GetTargetParams{OrgID: s.orgID, ID: destinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return target{}, errNoTarget
	}
	if err != nil {
		return target{}, fmt.Errorf("read the destination %d: %w", destinationID, err)
	}
	t := target{secrets: map[string]string{}}
	if row.WebhookMode.String == ModeEvents || row.WebhookMode.String == ModeBoth {
		c, err := ParseEventsConfig(row.WebhookEventsConfig)
		if err != nil {
			return target{}, err
		}
		t.events = &c
	}
	if (row.WebhookMode.String == ModeTemplate || row.WebhookMode.String == ModeBoth) &&
		len(row.WebhookTemplateConfig) > 0 {
		c, err := ParseTemplateConfig(row.WebhookTemplateConfig)
		if err != nil {
			return target{}, err
		}
		t.template = &c
	}
	if t.proxy, err = proxyconf.Parse(row.Proxy); err != nil {
		return target{}, err
	}
	k := s.cfg.Keyring
	if t.password, err = k.OpenSecret(FieldProxyPassword, keyring.StoredSecret{Ciphertext: row.ProxyPasswordCiphertext,
		KeyID: row.ProxyPasswordKeyID.String}); err != nil {
		return target{}, err
	}
	for _, f := range []struct {
		field string
		s     keyring.StoredSecret
	}{
		{FieldSigningSecret, keyring.StoredSecret{Ciphertext: row.SigningSecretCiphertext,
			KeyID: row.SigningSecretKeyID.String}},
		{FieldPreviousSigningSecret, keyring.StoredSecret{Ciphertext: row.PreviousSigningSecretCiphertext,
			KeyID: row.PreviousSigningSecretKeyID.String}},
	} {
		plain, err := k.OpenSecret(f.field, f.s)
		if err != nil {
			return target{}, err
		}
		if plain != "" {
			t.signing = append(t.signing, plain)
		}
	}
	rows, err := s.cfg.Store.ListSecretValues(ctx, dbgen.ListSecretValuesParams{OrgID: s.orgID,
		DestinationID: destinationID})
	if err != nil {
		return target{}, fmt.Errorf("read the secrets of the destination %d: %w", destinationID, err)
	}
	for _, r := range rows {
		plain, err := k.OpenSecret(FieldSecret, keyring.StoredSecret{Ciphertext: r.ValueCiphertext, KeyID: r.ValueKeyID})
		if err != nil {
			return target{}, err
		}
		t.secrets[r.Name] = string(plain)
	}
	return t, nil
}

// SendEvent sends one events-mode request (C-15.FR-2): POST with the body of the event as application/json, the
// headers of the Destination and webhook-id, webhook-timestamp and webhook-signature. A URL or header template that
// fails, or reads a Secret the Destination does not have, sends nothing and is a template error.
func (a *Adapter) SendEvent(ctx context.Context, c delivery.EventCall) delivery.Outcome {
	t, err := a.Service.target(ctx, c.Destination.ID)
	switch {
	case errors.Is(err, errNoTarget):
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(errNoTarget.Error())}
	case err != nil:
		return delivery.Outcome{Kind: delivery.OutcomeTransient,
			Error: "the settings of the destination could not be read"}
	case t.events == nil:
		return delivery.Outcome{Kind: delivery.OutcomeUnknown,
			Error: "the destination has no request of the events mode"}
	}
	red := newRedactor(t.all())
	target, err := a.render(t, "url", t.events.URL)
	if err == nil && (strings.ContainsAny(target, "\r\n\x00") || !validURL(target)) {
		err = errors.New("url: the URL is not an absolute http or https URL")
	}
	if err != nil {
		return templateFailure(red, RequestEvents, err)
	}
	header := http.Header{}
	for i, h := range t.events.Headers {
		name := "headers/" + strconv.Itoa(i)
		v, err := a.render(t, name, h.Value)
		if err == nil && strings.ContainsAny(v, "\r\n\x00") {
			err = errors.New(name + ": the header value contains a line break or a NUL")
		}
		if err != nil {
			return templateFailure(red, RequestEvents, err)
		}
		header[h.Name] = []string{v}
	}
	at := a.Network.Real.Now()
	sig, err := Sign(t.signing, c.WebhookID, at, c.Body)
	if err != nil {
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(err.Error())}
	}
	header["Content-Type"] = []string{"application/json"}
	header[HeaderID] = []string{c.WebhookID}
	header[HeaderTimestamp] = []string{strconv.FormatInt(at.Unix(), 10)}
	header[HeaderSignature] = []string{sig}
	client, err := a.client(c.Destination.ID, c.Class, t)
	if err != nil {
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(red.redact(err.Error()))}
	}
	res, err := client.Do(ctx, outbound.Request{Method: http.MethodPost, URL: target, Header: header, Body: c.Body,
		Mapping: Mapping})
	out := outcomeOf(res, err, red)
	out.Rendered, out.Request = true, RequestEvents
	return out
}

// render runs the request template src of the events mode of the target with its Secrets; a Secret it reads that the
// Destination does not have, or a read of .Secrets other than by a literal name, is an error.
func (a *Adapter) render(t target, name, src string) (string, error) {
	refs, _, err := checkRefs(name, src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, errBareReference)
	}
	for _, ref := range refs {
		if _, ok := t.secrets[ref]; !ok {
			return "", fmt.Errorf("%s: the Secret %s is not set", name, ref)
		}
	}
	tmpl, err := a.Sandbox.Parse(name, src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	out, err := tmpl.Execute(Data{Secrets: t.secrets})
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// templateFailure is the outcome of the request name whose template failed, its error redacted of the secrets.
func templateFailure(red redactor, name string, err error) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeTemplateError, Request: name,
		Error: outbound.Untrusted(red.redact("the request template failed: " + err.Error()))}
}

// client is the outbound client of the Destination in class, built again when its proxy or its secrets changed.
func (a *Adapter) client(destination int64, class outbound.Class, t target) (*outbound.Client, error) {
	h := sha256.New()
	h.Write(t.proxy.JSON())
	for _, s := range t.all() {
		h.Write([]byte(strconv.Itoa(len(s)) + ":" + string(s)))
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	key := clientKey{destination: destination, class: class}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[key]; ok && c.digest == digest {
		return c.client, nil
	}
	n := a.Network
	c, err := outbound.New(outbound.Config{Class: class, ConnectTimeout: connectTimeout, Timeout: callTimeout,
		Proxy: t.proxy.Outbound(t.password), Secrets: t.all(), Policy: n.Policy, Logger: n.Log, Clock: n.Real,
		Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes})
	if err != nil {
		return nil, err
	}
	if a.clients == nil {
		a.clients = map[clientKey]cachedClient{}
	}
	a.clients[key] = cachedClient{digest: digest, client: c}
	return c, nil
}

// Mapping is the response mapping of outgoing webhooks (C-15.FR-6): 2xx is delivered; 429 or 503 with Retry-After is
// a RetryAfter; 408 and every other 5xx are Transient; 401, 403, 404 and 410 are Fatal; 400, 413, 422 and every other
// answer are unknown.
func Mapping(status int, _ time.Duration, hasRetryAfter bool) outbound.Outcome {
	switch {
	case status >= 200 && status < 300:
		return outbound.OutcomeOK
	case (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && hasRetryAfter:
		return outbound.OutcomeRetryAfter
	case status == http.StatusRequestTimeout || (status >= 500 && status < 600):
		return outbound.OutcomeTransient
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return outbound.OutcomeFatal
	}
	return outbound.OutcomeUnknown
}

// outcomeOf is the delivery outcome of a request: a redirect is never followed and is unknown, its error naming the
// target; a request the outbound address policy blocked is Fatal, its error naming the rule, because every later
// request would be blocked too; timeouts and network errors are Transient. The error of an answer is its status and
// the start of its text, untrusted.
func outcomeOf(res outbound.Result, err error, red redactor) delivery.Outcome {
	if err == nil {
		return delivery.Outcome{Kind: delivery.OutcomeOK, Status: res.Status}
	}
	var e *outbound.Error
	if !errors.As(err, &e) {
		return delivery.Outcome{Kind: delivery.OutcomeTransient, Error: outbound.Untrusted(red.redact(err.Error()))}
	}
	if e.Outcome == outbound.OutcomeUnknown && e.Status >= 200 && e.Status < 300 {
		// A 2xx whose answer is too large to read was accepted all the same.
		return delivery.Outcome{Kind: delivery.OutcomeOK, Status: e.Status}
	}
	out := delivery.Outcome{Status: e.Status, Error: outbound.Untrusted(red.redact(e.Error()))}
	if e.Status != 0 && e.Outcome != outbound.OutcomeRedirect {
		text := "answered " + strconv.Itoa(e.Status)
		if e.ProviderError != "" {
			text += ": " + string(e.ProviderError)
		}
		out.Error = outbound.Untrusted(red.redact(text))
	}
	switch e.Outcome {
	case outbound.OutcomeRetryAfter:
		out.Kind, out.RetryAfter, out.Scope = delivery.OutcomeRetryAfter, e.RetryAfter, delivery.ScopeDestination
	case outbound.OutcomeTransient:
		out.Kind = delivery.OutcomeTransient
	case outbound.OutcomeFatal, outbound.OutcomeBlocked:
		out.Kind = delivery.OutcomeFatal
	default:
		out.Kind = delivery.OutcomeUnknown
	}
	return out
}

// Publish sends "create" of the template mode for a new Root message or Storm summary (C-15.FR-3): Loud with its
// Mention targets as delivery decided; the values its extraction rules find are returned, the first rule's as the
// message id. The Message is not used: the request templates render the Desired state of the call.
func (a *Adapter) Publish(ctx context.Context, c delivery.Call, _ delivery.Message) delivery.Outcome {
	t, out, ok := a.templateTarget(ctx, c)
	if !ok {
		return out
	}
	return a.send(ctx, c, t, RequestCreate, a.data(c, t))
}

// Update sends "update" of the template mode with the latest Desired state, so that changes made while a request was
// pending collapse into it; the final edit carries `.Final`.
func (a *Adapter) Update(ctx context.Context, c delivery.Call, _ string, _ delivery.Message) delivery.Outcome {
	t, out, ok := a.templateTarget(ctx, c)
	if !ok {
		return out
	}
	return a.send(ctx, c, t, RequestUpdate, a.data(c, t))
}

// Reply sends "reply in thread" of the template mode for a lifecycle event whose messenger form is a Thread reply, with
// the latest Desired state; "open thread", when the Destination has one, runs first once per Root message and adds the
// values it extracts, which the reply reads. A failure of "open thread" is the outcome and sends no reply; once it ran,
// the outcome says so whatever the reply came to. Without "reply in thread" nothing is sent.
func (a *Adapter) Reply(ctx context.Context, c delivery.Call, _ delivery.Root, _ delivery.Message) delivery.Outcome {
	t, out, ok := a.templateTarget(ctx, c)
	if !ok {
		return out
	}
	if t.template.ReplyInThread == nil {
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	}
	d := a.data(c, t)
	var opened *delivery.Outcome
	if t.template.OpenThread != nil && !c.Webhook.ThreadOpened {
		o := a.send(ctx, c, t, RequestOpenThread, d)
		if o.Kind != delivery.OutcomeOK {
			return o
		}
		for k, v := range o.Values {
			d.Response[k] = v
		}
		opened = &o
	}
	out = a.send(ctx, c, t, RequestReplyInThread, d)
	if opened != nil {
		out.ThreadOpened, out.Values, out.Missing = true, opened.Values, opened.Missing
	}
	return out
}

// LengthLimit is the longest output of a template: the receiver's own limits are unknown.
func (a *Adapter) LengthLimit() int { return templates.OutputCap }

// templateTarget reads the outgoing webhook of a template-mode call; ok is false with the outcome of a call that cannot
// be made: no such Destination, settings that cannot be read, or no request templates.
func (a *Adapter) templateTarget(ctx context.Context, c delivery.Call) (target, delivery.Outcome, bool) {
	t, err := a.Service.target(ctx, c.Destination.ID)
	switch {
	case errors.Is(err, errNoTarget):
		return t, delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(errNoTarget.Error())}, false
	case err != nil:
		return t, delivery.Outcome{Kind: delivery.OutcomeTransient,
			Error: "the settings of the destination could not be read"}, false
	case t.template == nil || c.Webhook == nil:
		return t, delivery.Outcome{Kind: delivery.OutcomeUnknown,
			Error: "the destination has no request templates of the template mode"}, false
	}
	return t, delivery.Outcome{}, true
}

// data is what the request templates of a call read: its Desired state, the values extracted so far, the lifecycle
// event of a Thread reply, whether it is Loud, its Mention targets, the Destination's Secrets and the final edit's
// text.
func (a *Adapter) data(c delivery.Call, t target) RequestData {
	var st delivery.RequestState
	_ = json.Unmarshal(c.Webhook.State, &st) // a delivery of an outgoing webhook stores a RequestState
	d := dataOf(st)
	for k, v := range c.Webhook.Response {
		d.Response[k] = v
	}
	d.Event, d.Final, d.Secrets = c.Webhook.Event, c.Webhook.Final, t.secrets
	d.Notify = c.Loudness == groups.Loud
	if d.Notify {
		d.Mentions = mentionsOf(c.Targets)
	}
	return d
}

// send renders the request name of the target with the data d and sends it, signed, through internal/outbound; a
// template that fails sends nothing and is a template error. A delivered "create" or "open thread" extracts the values
// of its rules from the response.
func (a *Adapter) send(ctx context.Context, c delivery.Call, t target, name string, d RequestData) delivery.Outcome {
	rt := t.template.request(name)
	red := newRedactor(t.all())
	start := a.Network.Real.Now()
	b, err := renderer{sandbox: a.Sandbox}.request(name, *rt, d)
	metrics.TemplateRenderDuration.With(messages.TemplateWebhookRequest).Update(
		a.Network.Real.Now().Sub(start).Seconds())
	if err != nil {
		return templateFailure(red, name, err)
	}
	header := http.Header{}
	for _, h := range b.header {
		header.Set(h[0], h[1])
	}
	if len(b.body) > 0 && header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	id, at := newRequestID(), a.Network.Real.Now()
	sig, err := Sign(t.signing, id, at, b.body)
	if err != nil {
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(err.Error()), Request: name}
	}
	header[HeaderID] = []string{id}
	header[HeaderTimestamp] = []string{strconv.FormatInt(at.Unix(), 10)}
	header[HeaderSignature] = []string{sig}
	client, err := a.client(c.Destination.ID, c.Class, t)
	if err != nil {
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(red.redact(err.Error())),
			Request: name}
	}
	res, err := client.Do(ctx, outbound.Request{Method: b.method, URL: b.url, Header: header, Body: b.body,
		Mapping: Mapping})
	out := outcomeOf(res, err, red)
	out.Rendered, out.Request = true, name
	if out.Kind == delivery.OutcomeOK && len(rt.Extract) > 0 {
		out.Values, out.Missing = extract(res.Body, rt.Extract)
		if name == RequestCreate {
			out.MessageID = out.Values[rt.Extract[0].Name]
		}
	}
	return out
}

// newRequestID is the webhook-id of a request of the template mode: msg_ and 26 random characters, new for each
// request, since the template mode reconciles to the latest state instead of retrying one request.
func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return "msg_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// Desired renders, reading the Destination destinationID through db, the "update" request of its template mode for the
// Desired state st and returns its hash (delivery.Requests): the Secrets as [redacted] and the extracted values as
// example-<name>, so that neither a new Secret nor a new value changes it. A request that does not render hashes the
// state and the error, so that each new state makes the call that fails; one without request templates, the state.
func (a *Adapter) Desired(ctx context.Context, db deliverydb.DBTX, destinationID int64, st delivery.RequestState) (
	[]byte, error) {
	state, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("encode the request state: %w", err)
	}
	raw, err := dbgen.New(db).GetTemplateConfig(ctx, dbgen.GetTemplateConfigParams{OrgID: a.Service.orgID,
		ID: destinationID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read the request templates of destination %d: %w", destinationID, err)
	}
	h := sha256.New()
	if len(raw) == 0 {
		h.Write(state)
		return h.Sum(nil), nil
	}
	c, err := ParseTemplateConfig(raw)
	if err != nil {
		return nil, err
	}
	d := dataOf(st)
	d.Final = ""
	b, renderErr := renderer{sandbox: a.Sandbox, fill: masked}.request(RequestUpdate, c.Update, d)
	if renderErr != nil {
		// The call renders it again and fails as a template error; the hash only makes each state call.
		h.Write([]byte("error\x00" + renderErr.Error() + "\x00"))
		h.Write(state)
		return h.Sum(nil), nil //nolint:nilerr // a request that fails to render is a state to hash, not an error here
	}
	h.Write([]byte(b.method + "\x00" + b.url + "\x00"))
	for _, kv := range b.header {
		h.Write([]byte(kv[0] + "\x00" + kv[1] + "\x00"))
	}
	h.Write(b.body)
	return h.Sum(nil), nil
}

// PreviewRequest renders one request template src with the template data of a sample (previewTemplate of the kind
// webhook_request, C-12.FR-4): as a Loud message with a Mention of each kind, its Secrets as [redacted] and the
// extracted values it reads as example-<name>. A template that fails is the *templates.Error of its position.
func (a *Adapter) PreviewRequest(src string, sample templates.Data) (string, error) {
	start := a.Network.Real.Now()
	defer func() {
		metrics.TemplateRenderDuration.With(messages.TemplateWebhookRequest).Update(
			a.Network.Real.Now().Sub(start).Seconds())
	}()
	d := dataOf(delivery.RequestState{Group: &sample})
	ex := exampleData(sample.AlertGroup.StartedAt, RequestReplyInThread)
	d.Notify, d.Mentions, d.Event = true, ex.Mentions, ex.Event
	out, err := renderer{sandbox: a.Sandbox, fill: masked}.field("webhook_request", src, d)
	if err != nil {
		var fe *FieldError
		var te *templates.Error
		switch {
		case errors.As(err, &fe):
			return "", &templates.Error{Code: templates.CodeSyntax, Line: fe.Line, Column: fe.Column, Detail: fe.Detail}
		case errors.As(err, &te):
			return "", te
		}
		return "", &templates.Error{Code: templates.CodeSyntax, Detail: err.Error()}
	}
	return out, nil
}
