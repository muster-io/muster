package alertmanager

import (
	"net/http"
	"time"
)

// Good: internal/fakes/alertmanager may build HTTP clients and transports.
func NewClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: transport, Timeout: time.Second}
}
