// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

package archlint

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	probeSecrets  = 3
	maxErrorDepth = 64
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

// CheckProbe runs p with fresh random secrets and reports every secret found in its log output or in the returned
// error, its %+v form and every error it wraps.
func CheckProbe(p Probe) []Diagnostic {
	secrets := make([]string, probeSecrets)
	for i := range secrets {
		secrets[i] = rand.Text()
	}
	var log bytes.Buffer
	err := p.Run(slices.Clone(secrets), &log)
	errText := strings.Join(errorTexts(err, 0), "\n")
	var diags []Diagnostic
	for i, secret := range secrets {
		if strings.Contains(log.String(), secret) {
			diags = append(diags, Diagnostic{Rule: 5, Path: p.Name, Message: fmt.Sprintf("secret %d of %d reached the log output", i+1, len(secrets))})
		}
		if strings.Contains(errText, secret) {
			diags = append(diags, Diagnostic{Rule: 5, Path: p.Name, Message: fmt.Sprintf("secret %d of %d reached the returned error", i+1, len(secrets))})
		}
	}
	return diags
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
