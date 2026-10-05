package groups

import "net/http"

// Good: test files are exempt.
var testClient = &http.Client{Transport: http.DefaultTransport}
