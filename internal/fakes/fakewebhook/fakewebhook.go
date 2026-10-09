// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakewebhook is the fake receiver of outgoing webhooks. It answers POST /hook/{name}, and any method on any
// path below /hook/{name}/, with 200 and an empty JSON object, and the harness records each request. The control
// endpoints under /_fake/ register the Signing secrets of a name and list what a name received, with the number of
// signatures that verify per the Standard Webhooks specification against the secrets registered at that moment.
package fakewebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

const (
	// HookPrefix starts the paths the fake receives on: /hook/{name} and below it.
	HookPrefix = "/hook/"

	secretPrefix    = "whsec_"
	signatureV1     = "v1"
	headerID        = "Webhook-Id"
	headerTimestamp = "Webhook-Timestamp"
	headerSignature = "Webhook-Signature"
)

// Received is a request to /hook/{name} or below it, as GET /_fake/received/{name} lists it.
type Received struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	// Headers are keyed by the lower-case header name, with the values of a repeated header joined by ", ".
	Headers map[string]string `json:"headers"`
	// Body is the body parsed as JSON when it is valid JSON, and the body as a string otherwise.
	Body      any    `json:"body"`
	AtMs      int64  `json:"at_ms"`
	Status    int    `json:"status"`
	WebhookID string `json:"webhook_id"`
	// SignaturesValid is how many signatures of webhook-signature verify with one of the name's Signing secrets.
	SignaturesValid int `json:"signatures_valid"`
}

// Fake is the fake receiver of outgoing webhooks.
type Fake struct {
	*fakeserver.Server

	mu      sync.Mutex
	secrets map[string][]string
}

// New returns the fake with no Signing secrets.
func New() *Fake {
	f := &Fake{secrets: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+HookPrefix+"{name}", receive)
	mux.HandleFunc(HookPrefix+"{name}/", receive)
	f.Server = fakeserver.New("webhook", mux)
	f.HandleControl("PUT /_fake/secrets/{name}", f.putSecrets)
	f.HandleControl("GET /_fake/received/{name}", func(w http.ResponseWriter, r *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Received(r.PathValue("name")))
	})
	return f
}

func receive(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "{}")
}

// SetSecrets replaces the Signing secrets of name; each is whsec_ and a key in standard base64.
func (f *Fake) SetSecrets(name string, secrets []string) error {
	for _, s := range secrets {
		if _, err := decodeSecret(s); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[name] = slices.Clone(secrets)
	return nil
}

func (f *Fake) putSecrets(w http.ResponseWriter, r *http.Request) {
	var secrets []string
	if err := fakeserver.DecodeJSON(w, r, &secrets); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if secrets == nil {
		fakeserver.WriteError(w, http.StatusBadRequest, "the body is a JSON array of Signing secrets")
		return
	}
	if err := f.SetSecrets(r.PathValue("name"), secrets); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeSecret(s string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(s, secretPrefix)
	if !ok {
		return nil, errors.New("a Signing secret starts with " + secretPrefix)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) == 0 {
		return nil, errors.New("a Signing secret is " + secretPrefix + " and a key in standard base64")
	}
	return key, nil
}

// keys are the HMAC keys of the Signing secrets registered for name.
func (f *Fake) keys(name string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([][]byte, 0, len(f.secrets[name]))
	for _, s := range f.secrets[name] {
		if key, err := decodeSecret(s); err == nil {
			keys = append(keys, key)
		}
	}
	return keys
}

// Received lists the requests to /hook/{name} and below it in arrival order, verifying their signatures with the
// Signing secrets registered for name now.
func (f *Fake) Received(name string) []Received {
	keys := f.keys(name)
	hook := HookPrefix + name
	out := []Received{}
	for _, req := range f.Requests() {
		if req.Path != hook && !strings.HasPrefix(req.Path, hook+"/") {
			continue
		}
		out = append(out, received(req, keys))
	}
	return out
}

func received(req fakeserver.Request, keys [][]byte) Received {
	body := []byte(req.Body)
	if req.BodyEncoding == fakeserver.EncodingBase64 {
		if b, err := base64.StdEncoding.DecodeString(req.Body); err == nil {
			body = b
		}
	}
	header := http.Header(req.Headers)
	headers := make(map[string]string, len(header))
	for name, values := range header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	var parsed any = string(body)
	if json.Valid(body) {
		parsed = json.RawMessage(body)
	}
	id := header.Get(headerID)
	signed := id + "." + header.Get(headerTimestamp) + "." + string(body)
	return Received{
		Method: req.Method, Path: req.Path, Query: req.Query, Headers: headers, Body: parsed, AtMs: req.AtMs,
		Status: req.Status, WebhookID: id,
		SignaturesValid: validSignatures(strings.Join(header.Values(headerSignature), " "), []byte(signed), keys),
	}
}

// validSignatures counts the space-separated signatures of header that are a v1 signature of signed with one of keys.
func validSignatures(header string, signed []byte, keys [][]byte) int {
	n := 0
	for _, sig := range strings.Fields(header) {
		version, encoded, ok := strings.Cut(sig, ",")
		if !ok || version != signatureV1 {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		for _, key := range keys {
			mac := hmac.New(sha256.New, key)
			mac.Write(signed)
			if hmac.Equal(mac.Sum(nil), got) {
				n++
				break
			}
		}
	}
	return n
}
