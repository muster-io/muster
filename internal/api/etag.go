// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/muster-io/muster/internal/auth"
)

// ifMatchHeader carries the ETag of the representation a client replaces (NFR-13).
const ifMatchHeader = "If-Match"

var (
	errPreconditionFailed = problem(http.StatusPreconditionFailed, typePreconditionFailed, "",
		"The resource changed since you read it; read it again and retry.")
	errPreconditionRequired = problem(http.StatusPreconditionRequired, typePreconditionRequired, "",
		"Send If-Match with the ETag of the last read.")
)

// etag is the entity tag of a row's version, as the ETag header and the etag field carry it.
func etag(version int64) string {
	return `"` + strconv.FormatInt(version, 10) + `"`
}

// ifMatch reads an If-Match value as the version it names. "*" matches any version (RFC 9110), and so does an
// absent value of an optional If-Match: both give nil. A value that names no version of ours is a stale one: 412.
func ifMatch(value string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "*" {
		return nil, nil
	}
	m := entityTag.FindStringSubmatch(value)
	if m == nil {
		return nil, errPreconditionFailed
	}
	v, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return nil, errPreconditionFailed
	}
	return &v, nil
}

// entityTag is one entity tag of ours, strong or weak: a version in quotes.
var entityTag = regexp.MustCompile(`^(?:W/)?"([0-9]+)"$`)

// ifMatchRequired lists the operations of the app listener whose If-Match header is required: a configuration update
// without it answers 428 before the request validation, which would answer 400.
func ifMatchRequired(doc *openapi3.T) map[string]bool {
	out := map[string]bool{}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			for _, p := range op.Parameters {
				if p.Value != nil && p.Value.In == openapi3.ParameterInHeader && p.Value.Name == ifMatchHeader &&
					p.Value.Required {
					out[op.OperationID] = true
				}
			}
		}
	}
	return out
}

// preconditions answers 428 to a request without the If-Match header that its operation requires, once the caller
// holds the operation's Permission; a caller without it is left to the permission check, which answers 403.
func (s *Server) preconditions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := infoFrom(r.Context()).operation
		if op != nil && s.ifMatchRequired[op.id] && strings.TrimSpace(r.Header.Get(ifMatchHeader)) == "" {
			if id, ok := auth.IdentityFrom(r.Context()); ok && !allowed(id, op.permissions) {
				s.permit(next).ServeHTTP(w, r)
				return
			}
			writeProblem(w, r, errPreconditionRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}
