// Package probes holds the rule 5 fixtures: code paths that leak the secrets they are given, and code paths that
// redact them. Each function has the signature of archlint.Probe.Run.
package probes

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
)

// LogsSecret is bad: it writes the first secret to the log.
func LogsSecret(_ context.Context, secrets []string, log io.Writer) error {
	_, err := fmt.Fprintf(log, "level=info msg=connecting token=%s\n", secrets[0])
	return err
}

// WrapsSecret is bad: it puts the second secret into a URL in the returned error.
func WrapsSecret(_ context.Context, secrets []string, _ io.Writer) error {
	return fmt.Errorf("post https://hooks.example.org/%s: %w", secrets[1], io.ErrUnexpectedEOF)
}

// HidesSecretInCause is bad: the error text is clean, but the error it wraps carries the third secret.
func HidesSecretInCause(_ context.Context, secrets []string, _ io.Writer) error {
	return &opaqueError{cause: errors.New("bad token " + secrets[2])}
}

// FormatsSecret is bad: only the %+v form of the error shows the first secret.
func FormatsSecret(_ context.Context, secrets []string, _ io.Writer) error {
	return verboseError{detail: secrets[0]}
}

// JoinsSecret is bad: one of the joined errors hides the second secret in its cause.
func JoinsSecret(_ context.Context, secrets []string, _ io.Writer) error {
	return errors.Join(errors.New("first attempt failed"), &opaqueError{cause: errors.New(secrets[1])})
}

// LogsBasicAuth is bad: the first secret is the password inside a base64-encoded Basic authorization header.
func LogsBasicAuth(_ context.Context, secrets []string, log io.Writer) error {
	auth := base64.StdEncoding.EncodeToString([]byte("muster:" + secrets[0]))
	_, err := fmt.Fprintf(log, "level=debug msg=request authorization=\"Basic %s\"\n", auth)
	return err
}

// EscapesSecretInURL is bad: the second secret is URL-escaped into a query parameter of the URL in the error.
func EscapesSecretInURL(_ context.Context, secrets []string, _ io.Writer) error {
	return fmt.Errorf("get https://hooks.example.org/?token=%s: %w", url.QueryEscape(secrets[1]), io.ErrUnexpectedEOF)
}

// LogsSecretInHex is bad: the third secret is logged as hex bytes.
func LogsSecretInHex(_ context.Context, secrets []string, log io.Writer) error {
	_, err := fmt.Fprintf(log, "level=debug msg=key key=%x\n", secrets[2])
	return err
}

// Redacts is good: it writes and returns the placeholder instead of the secret.
func Redacts(_ context.Context, _ []string, log io.Writer) error {
	if _, err := fmt.Fprintln(log, "level=info msg=connecting token=[redacted]"); err != nil {
		return err
	}
	return fmt.Errorf("post https://hooks.example.org/[redacted]: %w", io.ErrUnexpectedEOF)
}

// Silent is good: it neither logs nor fails.
func Silent(context.Context, []string, io.Writer) error {
	return nil
}

type opaqueError struct {
	cause error
}

func (e *opaqueError) Error() string { return "request failed" }

func (e *opaqueError) Unwrap() error { return e.cause }

type verboseError struct {
	detail string
}

func (e verboseError) Error() string { return "request failed" }

func (e verboseError) Format(s fmt.State, verb rune) {
	if verb == 'v' && s.Flag('+') {
		_, _ = fmt.Fprintf(s, "request failed: %s", e.detail)
		return
	}
	_, _ = io.WriteString(s, e.Error())
}
