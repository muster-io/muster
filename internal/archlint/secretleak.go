// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

package archlint

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
)

const (
	probeSecrets  = 3
	maxErrorDepth = 64

	// secretSuffix makes every secret change under URL escaping, so an escaped copy is not the plain one.
	secretSuffix = "+/="
)

// Probe pushes known secrets through one code path. Run hands the secrets to the code under test, sends its log
// output to log and returns the error the code path returned.
type Probe struct {
	Name string
	Run  func(secrets []string, log io.Writer) error
}

// registry holds the probes of rule 5. A story whose code carries secrets (the logger, outbound HTTP, the keyring, the
// messenger adapters, ...) adds a probe for that path here.
var registry = []Probe{}

func Probes() []Probe {
	return slices.Clone(registry)
}

// CheckProbe runs p with fresh random secrets and reports every secret found, verbatim or encoded (see leakForms), in
// its log output or in the returned error, its %+v form and every error it wraps.
func CheckProbe(p Probe) []Diagnostic {
	secrets := make([]string, probeSecrets)
	for i := range secrets {
		secrets[i] = rand.Text() + secretSuffix
	}
	var log bytes.Buffer
	err := p.Run(slices.Clone(secrets), &log)
	sinks := []struct{ name, text string }{
		{"the log output", log.String()},
		{"the returned error", strings.Join(errorTexts(err, 0), "\n")},
	}
	var diags []Diagnostic
	for i, secret := range secrets {
		forms := leakForms(secret)
		for _, sink := range sinks {
			j := slices.IndexFunc(forms, func(f leakForm) bool { return strings.Contains(sink.text, f.text) })
			if j < 0 {
				continue
			}
			msg := fmt.Sprintf("secret %d of %d reached %s", i+1, len(secrets), sink.name)
			if enc := forms[j].encoding; enc != "" {
				msg += " (" + enc + ")"
			}
			diags = append(diags, Diagnostic{Rule: 5, Path: p.Name, Message: msg})
		}
	}
	return diags
}

type leakForm struct {
	encoding string // empty for the secret itself
	text     string
}

// leakForms returns the secret as it may reach a log line or an error: verbatim, in hex, URL-escaped and in base64
// with the standard and the URL alphabet. The base64 forms are the characters that do not depend on the bytes around
// the secret, for each of its three possible alignments inside a longer encoded value, such as "user:secret" in a
// Basic authorization header.
func leakForms(secret string) []leakForm {
	h := hex.EncodeToString([]byte(secret))
	forms := []leakForm{
		{"", secret},
		{"hex", h},
		{"hex", strings.ToUpper(h)},
		{"URL-escaped", url.QueryEscape(secret)},
		{"URL-escaped", url.PathEscape(secret)},
	}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for shift := range 3 {
			encoded := enc.EncodeToString(append(make([]byte, shift), secret...))
			end := len(encoded)
			if (shift+len(secret))%3 != 0 {
				end-- // the last character also takes bits of the byte after the secret
			}
			forms = append(forms, leakForm{"base64", encoded[(8*shift+5)/6 : end]})
		}
	}
	return forms
}

func errorTexts(err error, depth int) []string {
	if err == nil || depth > maxErrorDepth {
		return nil
	}
	texts := []string{err.Error(), fmt.Sprintf("%+v", err)}
	if inner := errors.Unwrap(err); inner != nil {
		texts = append(texts, errorTexts(inner, depth+1)...)
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range multi.Unwrap() {
			texts = append(texts, errorTexts(inner, depth+1)...)
		}
	}
	return texts
}
