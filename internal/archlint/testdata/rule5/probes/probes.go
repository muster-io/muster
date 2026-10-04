// Package probes holds the rule 5 fixtures: code paths that leak the secrets they are given, and code paths that
// redact them. Each function has the signature of archlint.Probe.Run.
package probes

import (
	"errors"
	"fmt"
	"io"
)

// LogsSecret is bad: it writes the first secret to the log.
func LogsSecret(secrets []string, log io.Writer) error {
	_, err := fmt.Fprintf(log, "level=info msg=connecting token=%s\n", secrets[0])
	return err
}

// WrapsSecret is bad: it puts the second secret into a URL in the returned error.
func WrapsSecret(secrets []string, _ io.Writer) error {
	return fmt.Errorf("post https://hooks.example.org/%s: %w", secrets[1], io.ErrUnexpectedEOF)
}

// HidesSecretInCause is bad: the error text is clean, but the error it wraps carries the third secret.
func HidesSecretInCause(secrets []string, _ io.Writer) error {
	return &opaqueError{cause: errors.New("bad token " + secrets[2])}
}

// FormatsSecret is bad: only the %+v form of the error shows the first secret.
func FormatsSecret(secrets []string, _ io.Writer) error {
	return verboseError{detail: secrets[0]}
}

// JoinsSecret is bad: one of the joined errors hides the second secret in its cause.
func JoinsSecret(secrets []string, _ io.Writer) error {
	return errors.Join(errors.New("first attempt failed"), &opaqueError{cause: errors.New(secrets[1])})
}

// Redacts is good: it writes and returns the placeholder instead of the secret.
func Redacts(_ []string, log io.Writer) error {
	if _, err := fmt.Fprintln(log, "level=info msg=connecting token=[redacted]"); err != nil {
		return err
	}
	return fmt.Errorf("post https://hooks.example.org/[redacted]: %w", io.ErrUnexpectedEOF)
}

// Silent is good: it neither logs nor fails.
func Silent([]string, io.Writer) error {
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
