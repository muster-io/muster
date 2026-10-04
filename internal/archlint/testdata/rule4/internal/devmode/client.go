package devmode

import (
	"net/http"
	"time"
)

// Good: internal/devmode may build HTTP clients and transports.
func NewClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: transport, Timeout: time.Second}
}
