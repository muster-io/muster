package main

import "net/http"

// Good: the load test is its own HTTP client.
func main() {
	resp, err := http.Get("http://127.0.0.1:8081/healthz")
	if err == nil {
		resp.Body.Close()
	}
}
