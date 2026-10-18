// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/logging"
)

// Recorder keeps the record of each attempt of a client that has one and an owner (C-23.FR-1): the Request log. It
// gets the record after the attempt has its outcome and fails nothing: a record it cannot keep is its own concern.
type Recorder interface {
	Record(ctx context.Context, r Record)
}

// Owner is whose Request log a client's requests go to, by internal id: a Connection or an outgoing webhook
// Destination. PublicID names it in the log line of a record that could not be written.
type Owner struct {
	Connection  int64
	Destination int64
	PublicID    string
}

// set reports whether the owner names a Connection or a Destination.
func (o Owner) set() bool { return o.Connection != 0 || o.Destination != 0 }

// Links are what a request concerns besides its owner: the Destination and the Alert Group of a delivery, 0 for none.
type Links struct {
	Destination int64
	AlertGroup  int64
}

type linksKey struct{}

// WithLinks is ctx with the links of the requests made under it.
func WithLinks(ctx context.Context, l Links) context.Context {
	return context.WithValue(ctx, linksKey{}, l)
}

// LinksOf are the links WithLinks put in ctx, none without.
func LinksOf(ctx context.Context) Links {
	l, _ := ctx.Value(linksKey{}).(Links)
	return l
}

type secretsKey struct{}

// WithSecrets is ctx with secrets added to those of the requests made under it, which their records mask besides the
// client's own: an Account link code in a direct message, the secret token of setWebhook.
func WithSecrets(ctx context.Context, secrets ...logging.Secret) context.Context {
	return context.WithValue(ctx, secretsKey{}, append(slices.Clone(secretsOf(ctx)), secrets...))
}

// secretsOf are the secrets WithSecrets put in ctx.
func secretsOf(ctx context.Context) []logging.Secret {
	s, _ := ctx.Value(secretsKey{}).([]logging.Secret)
	return s
}

// The paths of a request (ConnectionPath): direct, or through the client's proxy.
const (
	ViaDirect = "direct"
	ViaProxy  = "proxy"
)

// MaskedHeaders are the headers whose values every record shows as [redacted], whatever they hold (C-23.FR-3): the
// credentials of the request and of the proxy, cookies, Telegram's webhook secret token and the signature of an
// outgoing webhook or a Bridge push.
var MaskedHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
	"X-Telegram-Bot-Api-Secret-Token", "Webhook-Signature"}

// MaskHeaders is a copy of h with the values of MaskedHeaders replaced by [redacted] and every other name and value
// passed through redact; nil stays nil.
func MaskHeaders(h http.Header, redact func(string) string) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for name, values := range h {
		masked := slices.Contains(MaskedHeaders, http.CanonicalHeaderKey(name))
		if !masked {
			name = redact(name)
		}
		for _, v := range values {
			if masked {
				v = redacted
			} else {
				v = redact(v)
			}
			out[name] = append(out[name], v)
		}
	}
	return out
}

// Record is one attempt of a request, as the Recorder gets it: its URL as errors show it, its headers as sent and its
// bodies, all masked of the client's and the request's Secrets and of MaskedHeaders. Status is 0, and the answer's
// header and body nil, when the attempt got no answer; Outcome is ok for an attempt that succeeded, and Network and
// Error say what failed otherwise.
type Record struct {
	Owner          Owner
	Links          Links
	Operation      string
	Method         string
	URL            string
	RequestHeader  http.Header
	RequestBody    []byte
	Status         int
	ResponseHeader http.Header
	ResponseBody   []byte
	// Latency is measured on the client's real clock.
	Latency time.Duration
	Via     string
	Attempt int
	Outcome Outcome
	Network string
	Error   string
}

// exchange is what one attempt sent — nothing when its request could not be built — and the answer it got, if any.
type exchange struct {
	sent       bool
	header     http.Header
	answered   bool
	status     int
	respHeader http.Header
	body       []byte
}

// answer notes the answer resp, whose body read was data.
func (ex *exchange) answer(resp *http.Response, data []byte) {
	ex.answered, ex.status, ex.respHeader, ex.body = true, resp.StatusCode, resp.Header, data
}

// record hands the Recorder the record of attempt n of req to u, which ended with res and err after elapsed, masked
// of the client's and the request's Secrets and of MaskedHeaders. Nothing is recorded without a Recorder or an Owner,
// for an attempt that sent nothing, for one whose caller went away (context.Canceled) and for a successful one that
// req.SkipRecord leaves out. Everything handed over is a copy, so that no Recorder can change the result.
func (c *Client) record(ctx context.Context, req Request, u *url.URL, n int, res Result, ex exchange,
	elapsed time.Duration, err error) {
	switch {
	case c.cfg.Recorder == nil, !c.cfg.Owner.set(), !ex.sent, errors.Is(ctx.Err(), context.Canceled):
		return
	case err == nil && req.SkipRecord != nil && req.SkipRecord(res.Status, res.Body):
		return
	}
	red := c.redactor
	if extra := secretsOf(ctx); len(extra) > 0 {
		red = newRedactor(append(slices.Clone(c.secrets), extra...))
	}
	r := Record{Owner: c.cfg.Owner, Links: LinksOf(ctx), Operation: req.Operation,
		Method: cmp.Or(req.Method, http.MethodGet), URL: c.displayWith(red, u),
		RequestHeader: MaskHeaders(ex.header, red.redact), RequestBody: redactBytes(red, req.Body),
		Latency: elapsed, Via: ViaDirect, Attempt: n, Outcome: res.Outcome}
	if c.cfg.Proxy != nil {
		r.Via = ViaProxy
	}
	if ex.answered {
		r.Status, r.ResponseHeader, r.ResponseBody = ex.status, MaskHeaders(ex.respHeader, red.redact),
			redactBytes(red, ex.body)
	}
	if e, ok := errors.AsType[*Error](err); ok {
		r.Network, r.Error = e.Network, red.redact(e.msg)
	}
	c.cfg.Recorder.Record(ctx, r)
}

// redactBytes is a copy of b with the secrets of red replaced, nil for nil.
func redactBytes(red redactor, b []byte) []byte {
	if b == nil {
		return nil
	}
	return []byte(red.redact(string(b)))
}
