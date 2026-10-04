package outboundx

import "net/http"

// Bad: a directory whose name only starts with "outbound" is outside internal/outbound.
func NewClient() *http.Client {
	return &http.Client{} // want: 4
}
