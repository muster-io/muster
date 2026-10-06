// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/totp"
	"github.com/muster-io/muster/internal/users"
)

// problemBase is where the problem type URIs live (x-problem-types).
const problemBase = "https://muster-io.github.io/muster/problems/"

// The problem types of x-problem-types that this package answers with.
const (
	typeValidationFailed     = "validation-failed"
	typeUnauthenticated      = "unauthenticated"
	typeForbidden            = "forbidden"
	typeNotFound             = "not-found"
	typeConflict             = "conflict"
	typeGone                 = "gone"
	typePreconditionFailed   = "precondition-failed"
	typePreconditionRequired = "precondition-required"
	typePayloadTooLarge      = "payload-too-large"
	typeRateLimited          = "rate-limited"
	typeInternal             = "internal"
	typeNotImplemented       = "not-implemented"
)

// The codes of x-problem-codes that this package answers with: problem codes, then the codes of errors[].
const (
	codeInvalidCredentials    = "invalid_credentials" //nolint:gosec // G101: a problem code, not a credential
	codeSessionExpired        = "session_expired"
	codeCSRFInvalid           = "csrf_invalid"
	codeSessionRequired       = "session_required"
	codeTOTPRequired          = "totp_required"
	codeTOTPEnrolmentRequired = "totp_enrolment_required"
	codeLocalUserOnly         = "local_user_only"
	codeNameTaken             = "name_taken"
	codeLastAdmin             = "last_admin"
	codeLinkExpired           = "link_expired"
	codeLinkUsed              = "link_used"
	codeTOTPAlreadyEnrolled   = "totp_already_enrolled"
	codeTOTPNotEnrolled       = "totp_not_enrolled"
	codeTOTPNotStarted        = "totp_enrolment_not_started"
	codeTOTPNotPending        = "totp_not_pending"
	codeOIDCNotEnabled        = "oidc_not_enabled"

	fieldRequired      = "required"
	fieldInvalidFormat = "invalid_format"
	fieldTooShort      = "too_short"
	fieldTooLong       = "too_long"
	fieldUnsupported   = "unsupported"
)

const (
	contentTypeProblem       = "application/problem+json"
	retryAfterHeader         = "Retry-After"
	detailValidationFailed   = "The request is not valid."
	detailSemanticValidation = "A field is semantically invalid."
)

var titles = map[string]string{
	typeValidationFailed:     "Validation failed",
	typeUnauthenticated:      "Unauthenticated",
	typeForbidden:            "Forbidden",
	typeNotFound:             "Not found",
	typeConflict:             "Conflict",
	typeGone:                 "Gone",
	typePreconditionFailed:   "Precondition failed",
	typePreconditionRequired: "Precondition required",
	typePayloadTooLarge:      "Payload too large",
	typeRateLimited:          "Rate limited",
	typeInternal:             "Internal error",
	typeNotImplemented:       "Not implemented",
}

// Problem is an RFC 9457 problem as an error: handlers and middleware return it, and writeProblem answers it.
type Problem struct {
	Status     int
	Type       string
	Code       string
	Detail     string
	Errors     []gen.ProblemError
	RetryAfter int
}

func (p *Problem) Error() string {
	if p.Code != "" {
		return p.Type + " (" + p.Code + ")"
	}
	return p.Type
}

func problem(status int, typ, code, detail string) *Problem {
	return &Problem{Status: status, Type: typ, Code: code, Detail: detail}
}

// fieldProblem is a validation-failed problem with one item: 400 for a malformed request, 422 for a field that is
// semantically invalid.
func fieldProblem(status int, pointer, code, detail string) *Problem {
	p := problem(status, typeValidationFailed, "", detailValidationFailed)
	if status == http.StatusUnprocessableEntity {
		p.Detail = detailSemanticValidation
	}
	p.Errors = []gen.ProblemError{{Pointer: pointer, Code: code, Detail: &detail}}
	return p
}

var (
	errNotImplemented = problem(http.StatusNotImplemented, typeNotImplemented, "",
		"This build does not implement the operation yet.")
	errNotFound        = problem(http.StatusNotFound, typeNotFound, "", "No such operation.")
	errUnauthenticated = problem(http.StatusUnauthorized, typeUnauthenticated, "", "Sign in to continue.")
	errSessionExpired  = problem(http.StatusUnauthorized, typeUnauthenticated, codeSessionExpired,
		"The session expired; sign in again.")
	errInvalidCredentials = problem(http.StatusUnauthorized, typeUnauthenticated, codeInvalidCredentials,
		"The login, the password or the code is wrong.")
	errCSRF = problem(http.StatusForbidden, typeForbidden, codeCSRFInvalid,
		"The X-CSRF-Token header is missing or wrong.")
	errSessionRequired = problem(http.StatusForbidden, typeForbidden, codeSessionRequired,
		"This operation accepts the web session only.")
	errTooLarge = problem(http.StatusRequestEntityTooLarge, typePayloadTooLarge, "",
		"The request body is larger than 1 MiB.")
	errOIDCNotEnabled = problem(http.StatusConflict, typeConflict, codeOIDCNotEnabled, "OIDC sign-in is switched off.")
	errInternal       = problem(http.StatusInternalServerError, typeInternal, "", "The request failed; try again later.")
)

// writeProblem answers p as application/problem+json; instance is the request path.
func writeProblem(w http.ResponseWriter, r *http.Request, p *Problem) {
	body := gen.Problem{
		Type:   problemBase + p.Type,
		Title:  titles[p.Type],
		Status: p.Status,
	}
	if p.Detail != "" {
		body.Detail = &p.Detail
	}
	if p.Code != "" {
		body.Code = &p.Code
	}
	if len(p.Errors) > 0 {
		body.Errors = &p.Errors
	}
	instance := r.URL.Path
	body.Instance = &instance
	if p.RetryAfter > 0 {
		body.RetryAfterSeconds = &p.RetryAfter
		w.Header().Set(retryAfterHeader, strconv.Itoa(p.RetryAfter))
	}
	w.Header().Set("Content-Type", contentTypeProblem)
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(body)
}

// problemFor maps an error of a handler or a domain package to its problem. An error it does not know is 500
// internal and is logged; the answer never carries the error text.
func (s *Server) problemFor(ctx context.Context, operation string, err error) *Problem {
	if p, ok := errors.AsType[*Problem](err); ok {
		return p
	}
	if t, ok := errors.AsType[*auth.ThrottledError](err); ok {
		p := problem(http.StatusTooManyRequests, typeRateLimited, "",
			"Too many failed sign-ins; wait before the next attempt.")
		p.RetryAfter = t.Seconds()
		return p
	}
	if f, ok := errors.AsType[*oidc.FieldError](err); ok {
		return fieldProblem(http.StatusUnprocessableEntity, f.Pointer, f.Code, f.Detail)
	}
	if f, ok := errors.AsType[*users.FieldError](err); ok {
		return fieldProblem(http.StatusUnprocessableEntity, f.Pointer, f.Code, f.Detail)
	}
	if u, ok := errors.AsType[*organization.UnsupportedError](err); ok {
		p := problem(http.StatusUnprocessableEntity, typeValidationFailed, "", detailSemanticValidation)
		detail := "This setting cannot be changed yet."
		for _, pointer := range u.Pointers {
			p.Errors = append(p.Errors, gen.ProblemError{Pointer: pointer, Code: fieldUnsupported, Detail: &detail})
		}
		return p
	}
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return errInvalidCredentials
	case errors.Is(err, auth.ErrSessionExpired):
		return errSessionExpired
	case errors.Is(err, auth.ErrUnauthenticated):
		return errUnauthenticated
	case errors.Is(err, auth.ErrPasswordTooShort):
		return fieldProblem(http.StatusUnprocessableEntity, "/new_password", fieldTooShort,
			"The password is shorter than "+strconv.Itoa(auth.PasswordMinLength)+" characters.")
	case errors.Is(err, auth.ErrNotLocal):
		return problem(http.StatusConflict, typeConflict, codeLocalUserOnly,
			"The account signs in through OIDC and has no password to change.")
	case errors.Is(err, users.ErrNotFound):
		return problem(http.StatusNotFound, typeNotFound, "", "No such user.")
	case errors.Is(err, users.ErrNameTaken):
		return problem(http.StatusConflict, typeConflict, codeNameTaken,
			"Another user has this login; logins are compared case-insensitively.")
	case errors.Is(err, users.ErrLastAdmin):
		return problem(http.StatusConflict, typeConflict, codeLastAdmin,
			"The Organization needs an active Admin: the last one cannot be disabled, deleted or given a lower Role.")
	case errors.Is(err, users.ErrVersionMismatch):
		return errPreconditionFailed
	case errors.Is(err, users.ErrLinkNotFound):
		return problem(http.StatusNotFound, typeNotFound, "", "No such password setup link.")
	case errors.Is(err, users.ErrLinkExpired):
		return problem(http.StatusGone, typeGone, codeLinkExpired,
			"The password setup link expired; ask an Admin for a new one.")
	case errors.Is(err, users.ErrLinkUsed):
		return problem(http.StatusGone, typeGone, codeLinkUsed,
			"The password setup link was used or replaced by a newer one.")
	case errors.Is(err, auth.ErrTOTPNotPending):
		return problem(http.StatusConflict, typeConflict, codeTOTPNotPending, "The session is not waiting for a code.")
	case errors.Is(err, totp.ErrAlreadyEnrolled):
		return problem(http.StatusConflict, typeConflict, codeTOTPAlreadyEnrolled, "TOTP is already on.")
	case errors.Is(err, totp.ErrNotStarted):
		return problem(http.StatusConflict, typeConflict, codeTOTPNotStarted,
			"No TOTP enrolment was begun, or another one replaced it; begin the enrolment again.")
	case errors.Is(err, totp.ErrNotEnrolled):
		return problem(http.StatusConflict, typeConflict, codeTOTPNotEnrolled, "There is no TOTP to use or change.")
	case errors.Is(err, totp.ErrOwnTOTP):
		return problem(http.StatusForbidden, typeForbidden, "",
			"Remove your own TOTP from your profile, with your password or a code.")
	case errors.Is(err, totp.ErrUserNotFound):
		return problem(http.StatusNotFound, typeNotFound, "", "No such user.")
	case errors.Is(err, oidc.ErrVersionMismatch):
		return errPreconditionFailed
	case errors.Is(err, oidc.ErrNotEnabled):
		return errOIDCNotEnabled
	case errors.Is(err, oidc.ErrTooManyRequests):
		p := problem(http.StatusTooManyRequests, typeRateLimited, "",
			"Too many OIDC sign-ins are in progress; try again in a minute.")
		p.RetryAfter = 60
		return p
	case errors.Is(err, organization.ErrVersionMismatch):
		return errPreconditionFailed
	case errors.Is(err, live.ErrTooManyStreams):
		p := problem(http.StatusTooManyRequests, typeRateLimited, "",
			"Too many live-updates streams are open; close a tab or try again later.")
		p.RetryAfter = int(live.CheckInterval.Seconds())
		return p
	}
	s.log.Log(ctx, logging.APIRequestFailed, logging.F("operation", operation), logging.F("error", err.Error()))
	return errInternal
}

// validationProblem maps the errors of the request validation (kin-openapi) to a 400 validation-failed problem with
// one item per field. A detail says which rule the field breaks and never repeats the value, which may be a password.
func validationProblem(err error) *Problem {
	p := problem(http.StatusBadRequest, typeValidationFailed, "", detailValidationFailed)
	var walk func(error)
	walk = func(err error) {
		re, ok := err.(*openapi3filter.RequestError) //nolint:errorlint // the direct type decides between the cases
		if !ok {
			if multi, isMulti := err.(openapi3.MultiError); isMulti { //nolint:errorlint // as above
				for _, e := range multi {
					walk(e)
				}
				return
			}
			p.Errors = append(p.Errors, item("", fieldInvalidFormat))
			return
		}
		if re.Parameter != nil {
			code := fieldInvalidFormat
			if errors.Is(re.Err, openapi3filter.ErrInvalidRequired) {
				code = fieldRequired
			}
			p.Errors = append(p.Errors, item("/"+re.Parameter.In+"/"+re.Parameter.Name, code))
			return
		}
		p.Errors = append(p.Errors, bodyItems(re.Err)...)
	}
	walk(err)
	if len(p.Errors) == 0 {
		p.Errors = append(p.Errors, item("", fieldInvalidFormat))
	}
	return p
}

// bodyItems are the items of the errors in the request body. kin-openapi reports them in two forms: a SchemaError
// with the failing keyword and the path of the field, from its own validator, and, from the JSON Schema 2020-12
// validator it uses for OpenAPI 3.1, SchemaErrors whose reason reads "at '<pointer>': <message>", wrapped in the
// Origin of the error of the whole body.
func bodyItems(err error) []gen.ProblemError {
	if multi, ok := err.(openapi3.MultiError); ok { //nolint:errorlint // the list of the body's errors
		var out []gen.ProblemError
		for _, e := range multi {
			out = append(out, bodyItems(e)...)
		}
		return out
	}
	se, ok := err.(*openapi3.SchemaError) //nolint:errorlint // a wrapped SchemaError is reached through Origin below
	if !ok {
		if errors.Is(err, openapi3filter.ErrInvalidRequired) {
			return []gen.ProblemError{item("", fieldRequired)}
		}
		return []gen.ProblemError{item("", fieldInvalidFormat)}
	}
	if multi, ok := errors.AsType[openapi3.MultiError](se.Origin); ok {
		return bodyItems(multi)
	}
	if se.SchemaField != "" {
		return []gen.ProblemError{keywordItem(se)}
	}
	return messageItems(se.Reason)
}

// keywordItem is the item of an error of kin-openapi's own validator.
func keywordItem(se *openapi3.SchemaError) gen.ProblemError {
	pointer := pointerOf(se.JSONPointer())
	switch se.SchemaField {
	case "required":
		if name, ok := quoted(se.Reason, `property "`, `"`); ok && !strings.HasSuffix(pointer, "/"+escapePointer(name)) {
			pointer += "/" + escapePointer(name)
		}
		return item(pointer, fieldRequired)
	case "minLength", "minItems":
		return item(pointer, fieldTooShort)
	case "maxLength", "maxItems":
		return item(pointer, fieldTooLong)
	}
	return item(pointer, fieldInvalidFormat)
}

// messageItems are the items of an error of the JSON Schema validator, read from its message.
func messageItems(reason string) []gen.ProblemError {
	i := strings.LastIndex(reason, "at '")
	if i < 0 {
		return []gen.ProblemError{item("", fieldInvalidFormat)}
	}
	pointer, msg, ok := strings.Cut(reason[i+len("at '"):], "': ")
	if !ok {
		return []gen.ProblemError{item("", fieldInvalidFormat)}
	}
	switch {
	case strings.HasPrefix(msg, "missing propert"):
		var out []gen.ProblemError
		for name := range strings.SplitSeq(msg[strings.Index(msg, " ")+1:], ", ") {
			if n, ok := quoted(name, "'", "'"); ok {
				out = append(out, item(pointer+"/"+escapePointer(n), fieldRequired))
			}
		}
		if len(out) > 0 {
			return out
		}
		return []gen.ProblemError{item(pointer, fieldRequired)}
	case strings.HasPrefix(msg, "minLength"), strings.HasPrefix(msg, "minItems"):
		return []gen.ProblemError{item(pointer, fieldTooShort)}
	case strings.HasPrefix(msg, "maxLength"), strings.HasPrefix(msg, "maxItems"):
		return []gen.ProblemError{item(pointer, fieldTooLong)}
	}
	return []gen.ProblemError{item(pointer, fieldInvalidFormat)}
}

// quoted returns the text of s after the first opening and before the next closing.
func quoted(s, opening, closing string) (string, bool) {
	_, rest, ok := strings.Cut(s, opening)
	if !ok {
		return "", false
	}
	name, _, ok := strings.Cut(rest, closing)
	return name, ok && name != ""
}

func pointerOf(path []string) string {
	var b strings.Builder
	for _, p := range path {
		b.WriteString("/" + escapePointer(p))
	}
	return b.String()
}

func escapePointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}

// fieldDetails explain the codes of the validation items; they never repeat the value.
var fieldDetails = map[string]string{
	fieldRequired:      "The field is missing.",
	fieldInvalidFormat: "The value does not have the expected type, format or one of the allowed values.",
	fieldTooShort:      "The value is too short.",
	fieldTooLong:       "The value is too long.",
	fieldUnsupported:   "This setting cannot be changed yet.",
}

func item(pointer, code string) gen.ProblemError {
	detail := fieldDetails[code]
	return gen.ProblemError{Pointer: pointer, Code: code, Detail: &detail}
}
