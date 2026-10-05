package apiclient

import (
	"net/http"
	"time"
)

// Good: pkg/apiclient may build HTTP clients and transports.
func NewClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: transport, Timeout: time.Second}
}
