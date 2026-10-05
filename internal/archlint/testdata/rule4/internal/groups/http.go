package groups

import (
	"net/http"
	nethttp "net/http"
	"net/url"
	"time"
)

type client = http.Client

type holder struct {
	c              http.Client // want: 4
	t              *http.Transport
	http.Transport // want: 4
}

var shared http.Client // want: 4

// Bad: every marked line creates or reaches an HTTP client or transport outside internal/outbound.
func Clients(t *http.Transport, target string) []any {
	a := &http.Client{Timeout: time.Second}     // want: 4
	b := &http.Transport{}                      // want: 4
	c := new(http.Client)                       // want: 4
	d := client{}                               // want: 4
	e := t.Clone()                              // want: 4
	f := http.DefaultClient                     // want: 4
	g := nethttp.DefaultTransport               // want: 4
	get := http.Get                             // want: 4
	clone := (*http.Transport).Clone            // want: 4
	_, _ = http.Get(target)                     // want: 4
	_, _ = http.Post(target, "text/plain", nil) // want: 4
	_, _ = nethttp.Head(target)                 // want: 4
	_, _ = http.PostForm(target, url.Values{})  // want: 4
	return []any{a, b, c, d, e, f, g, get, clone, holder{}}
}
