// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
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

// Adapter sends the events-mode requests of outgoing webhooks for delivery (C-15.FR-2, FR-5, FR-6, FR-8): it renders
// the URL and the headers with the Destination's Secrets, signs the body with every Signing secret that still signs,
// sends it through internal/outbound with the Destination's proxy in the client class of the call, and maps the answer
// to a delivery outcome. Every secret of the Destination is registered for redaction, so no error it returns carries
// one.
type Adapter struct {
	Service *Service
	Network Network
	Sandbox *templates.Sandbox
	mu      sync.Mutex
	clients map[clientKey]cachedClient
}

var _ delivery.EventSender = (*Adapter)(nil)

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
		return templateFailure(red, err)
	}
	header := http.Header{}
	for i, h := range t.events.Headers {
		name := "headers/" + strconv.Itoa(i)
		v, err := a.render(t, name, h.Value)
		if err == nil && strings.ContainsAny(v, "\r\n\x00") {
			err = errors.New(name + ": the header value contains a line break or a NUL")
		}
		if err != nil {
			return templateFailure(red, err)
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
	return outcomeOf(res, err, red)
}

// render runs the request template src of the target with its Secrets; a Secret it reads that the Destination does
// not have is an error.
func (a *Adapter) render(t target, name, src string) (string, error) {
	for _, ref := range SecretRefs(src) {
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

// templateFailure is the outcome of a request whose template failed, its error redacted of the secrets.
func templateFailure(red redactor, err error) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeTemplateError,
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

// validURL reports whether a rendered URL is an absolute http or https URL.
func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
