package groups

import (
	"context"
	"net/http"
)

// Good: clients arrive from internal/outbound as *http.Client; requests, headers and round trippers are fine.
type Fetcher struct {
	client    *http.Client
	transport http.RoundTripper
}

func NewFetcher(client *http.Client, transport *http.Transport) *Fetcher {
	return &Fetcher{client: client, transport: transport}
}

func (f *Fetcher) Fetch(ctx context.Context, target string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header = http.Header{"Accept": {"application/json"}}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, nil
}
