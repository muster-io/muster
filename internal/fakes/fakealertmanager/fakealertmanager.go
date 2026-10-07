// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakealertmanager is the fake Alertmanager: it sends Alertmanager webhooks (version 4), or any body, to a URL
// or to a registered receiver, once or at a steady rate, on request through its control endpoints or from Go. Its
// group model keeps Alertmanager groups and their Alerts and sends the Snapshots Alertmanager would send for them, its
// Heartbeat senders signal Muster's Heartbeat endpoint at an interval, on request or not at all, and its scenarios
// replay the verified Alertmanager facts that processing relies on.
package fakealertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

const (
	// UserAgent is what Alertmanager v0.34.1 sends.
	UserAgent = "Alertmanager/0.34.1"
	// sendTimeout is Alertmanager's peer timeout in the verified setup.
	sendTimeout = 15 * time.Second

	maxLoadRate     = 10_000
	maxLoadDuration = time.Hour
)

// Endpoint is where a webhook goes; a Token is sent as Authorization: Bearer, or appended to the URL as a path segment
// with TokenInPath.
type Endpoint struct {
	URL         string
	Token       string
	TokenInPath bool
}

// The places of a receiver's token: the Authorization header or the path.
const (
	TokenInHeader = "header"
	TokenInPath   = "path"
)

// Receiver is a webhook receiver of the fake's configuration, such as Muster's ingestion endpoint with an Integration
// token.
type Receiver struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
	// TokenIn is TokenInHeader, the default, or TokenInPath.
	TokenIn string `json:"token_in"`
}

// Endpoint is where the receiver's webhooks go.
func (r Receiver) Endpoint() Endpoint {
	return Endpoint{URL: r.URL, Token: r.Token, TokenInPath: r.TokenIn == TokenInPath}
}

type Fake struct {
	*fakeserver.Server
	client *http.Client

	mu        sync.Mutex
	seq       int
	receivers map[string]Receiver
	groups    map[string]*fakeGroup
	// heartbeats are the running Heartbeat senders by name; ticker makes the ticks of their intervals.
	heartbeats map[string]*heartbeatSender
	ticker     func(time.Duration) (<-chan time.Time, func())
	// clock is the time of the group model; nil is the system time.
	clock func() time.Time
}

func New() *Fake {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.MaxIdleConnsPerHost = 100
	f := &Fake{
		receivers:  map[string]Receiver{},
		groups:     map[string]*fakeGroup{},
		heartbeats: map[string]*heartbeatSender{},
		client: &http.Client{
			Transport: t,
			Timeout:   sendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	f.Server = fakeserver.New("Alertmanager", http.HandlerFunc(notImplemented))
	f.HandleControl("POST /_fake/receivers", f.handleReceivers)
	f.HandleControl("POST /_fake/send", f.handleSend)
	f.HandleControl("POST /_fake/load", f.handleLoad)
	f.HandleControl("PUT /_fake/groups/{group}", f.handlePutGroup)
	f.HandleControl("PUT /_fake/groups/{group}/alerts/{alert}", f.handlePutAlert)
	f.HandleControl("DELETE /_fake/groups/{group}/alerts/{alert}", f.handleDeleteAlert)
	f.HandleControl("POST /_fake/groups/{group}/notify", f.handleNotify)
	f.HandleControl("POST /_fake/heartbeats", f.handleStartHeartbeat)
	f.HandleControl("POST /_fake/heartbeats/{name}/send", f.handleSendHeartbeat)
	f.HandleControl("DELETE /_fake/heartbeats/{name}", f.handleStopHeartbeat)
	return f
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	fakeserver.WriteError(w, http.StatusNotImplemented, "not implemented by the fake Alertmanager server")
}

// Register adds the receiver r, or replaces the one of the same name. Unlike the control endpoint, it takes any URL.
func (f *Fake) Register(r Receiver) error {
	if r.Name == "" {
		return errors.New("a receiver needs a name")
	}
	if r.TokenIn == "" {
		r.TokenIn = TokenInHeader
	}
	if r.TokenIn != TokenInHeader && r.TokenIn != TokenInPath {
		return errors.New(`token_in must be "header" or "path"`)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.receivers[r.Name] = r
	return nil
}

// Receiver returns the receiver named name.
func (f *Fake) Receiver(name string) (Receiver, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.receivers[name]
	return r, ok
}

// Send posts body to the endpoint as Alertmanager does, as JSON, and returns the answer's status. Unlike the control
// endpoints, it sends to any URL.
func (f *Fake) Send(ctx context.Context, e Endpoint, body []byte) (int, error) {
	return f.SendAs(ctx, e, body, "application/json")
}

// SendAs posts body with the Content-Type contentType, none when it is empty, and returns the answer's status.
func (f *Fake) SendAs(ctx context.Context, e Endpoint, body []byte, contentType string) (int, error) {
	target := e.URL
	if e.TokenInPath && e.Token != "" {
		target = strings.TrimSuffix(target, "/") + "/" + url.PathEscape(e.Token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", UserAgent)
	if e.Token != "" && !e.TokenInPath {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

func (f *Fake) nextSeq(n int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	first := f.seq
	f.seq += n
	return first
}

type webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []alert           `json:"alerts"`
}

type alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Webhook builds a firing version 4 webhook with one Alert; seq varies the labels, so consecutive bodies differ and
// spread over 100 Alertmanager groups.
func (f *Fake) Webhook(seq int, now time.Time) []byte {
	externalURL := f.URL()
	if externalURL == "" {
		externalURL = "http://localhost:9093"
	}
	service := fmt.Sprintf("svc-%02d", seq%100)
	groupLabels := map[string]string{"alertname": "FakeAlert", "service": service}
	labels := map[string]string{
		"alertname": "FakeAlert",
		"service":   service,
		"instance":  fmt.Sprintf("host-%d", seq),
		"job":       "fake",
		"severity":  "warning",
	}
	annotations := map[string]string{"summary": fmt.Sprintf("Fake alert %d of %s", seq, service)}
	w := webhook{
		Version:           "4",
		GroupKey:          fmt.Sprintf(`{}:{alertname="FakeAlert", service=%q}`, service),
		Status:            "firing",
		Receiver:          "muster",
		GroupLabels:       groupLabels,
		CommonLabels:      labels,
		CommonAnnotations: annotations,
		ExternalURL:       externalURL,
		Alerts: []alert{{
			Status:       "firing",
			Labels:       labels,
			Annotations:  annotations,
			StartsAt:     now.UTC().Format(time.RFC3339),
			EndsAt:       "0001-01-01T00:00:00Z",
			GeneratorURL: "http://localhost:9090/graph?g0.expr=up+%3D%3D+0",
			Fingerprint:  fingerprint(labels),
		}},
	}
	// Strings and maps of strings only: marshalling cannot fail.
	b, _ := json.Marshal(w)
	return b
}

// fingerprint is a 64-bit FNV-1a hash of the sorted labels in hex, the form Alertmanager's fingerprints take.
func fingerprint(labels map[string]string) string {
	var buf []byte
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		buf = append(buf, name...)
		buf = append(buf, 0xff)
		buf = append(buf, labels[name]...)
		buf = append(buf, 0xff)
	}
	h := fnv.New64a()
	_, _ = h.Write(buf)
	return fmt.Sprintf("%016x", h.Sum64())
}

type LoadOptions struct {
	Endpoint Endpoint
	// Rate is the number of webhooks per second.
	Rate     int
	Duration time.Duration
}

// LoadReport counts the sends of a load run: Accepted got a 2xx answer, Rejected another status, Failed no answer.
// The latencies are of the answered sends.
type LoadReport struct {
	Sent, Accepted, Rejected, Failed int
	P50, P95, P99, Max               time.Duration
}

func (r LoadReport) String() string {
	return fmt.Sprintf("accepted %d, rejected %d, failed %d of %d sent\nlatency p50 %v, p95 %v, p99 %v, max %v",
		r.Accepted, r.Rejected, r.Failed, r.Sent, round(r.P50), round(r.P95), round(r.P99), round(r.Max))
}

func round(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }

// Load sends Rate webhooks per second for Duration, each in its own goroutine at a steady tick, and waits for the
// sends in flight. A cancelled ctx stops it early.
func (f *Fake) Load(ctx context.Context, o LoadOptions) LoadReport {
	total := 0
	if o.Rate > 0 {
		total = int(math.Round(o.Duration.Seconds() * float64(o.Rate)))
	}
	if total <= 0 {
		return LoadReport{}
	}
	first := f.nextSeq(total)
	var (
		mu        sync.Mutex
		report    LoadReport
		latencies []time.Duration
		wg        sync.WaitGroup
	)
	ticker := time.NewTicker(max(time.Second/time.Duration(o.Rate), time.Microsecond))
	defer ticker.Stop()
	for i := range total {
		if i > 0 && !tick(ctx, ticker) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		body := f.Webhook(first+i, time.Now())
		wg.Go(func() {
			begin := time.Now()
			status, err := f.Send(ctx, o.Endpoint, body)
			took := time.Since(begin)
			mu.Lock()
			defer mu.Unlock()
			report.Sent++
			switch {
			case err != nil:
				report.Failed++
				return
			case status >= 200 && status < 300:
				report.Accepted++
			default:
				report.Rejected++
			}
			latencies = append(latencies, took)
		})
	}
	wg.Wait()
	slices.Sort(latencies)
	report.P50 = percentile(latencies, 0.50)
	report.P95 = percentile(latencies, 0.95)
	report.P99 = percentile(latencies, 0.99)
	report.Max = percentile(latencies, 1)
	return report
}

func tick(ctx context.Context, t *time.Ticker) bool {
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// percentile is the nearest-rank percentile of sorted durations.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

type sendInput struct {
	URL      string          `json:"url"`
	Receiver string          `json:"receiver"`
	Token    *string         `json:"token"`
	NoToken  bool            `json:"no_token"`
	Payload  json.RawMessage `json:"payload"`
	Raw      *string         `json:"raw"`
	// ContentType is the Content-Type of a raw body; none when it is empty.
	ContentType string `json:"content_type"`
	SizeBytes   *int   `json:"size_bytes"`
}

// maxFillerBytes bounds a filler body: well above ingest.body_limit, so that its refusal can be checked.
const maxFillerBytes = 64 << 20

func (f *Fake) handleReceivers(w http.ResponseWriter, r *http.Request) {
	var in Receiver
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := checkURL(in.URL); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := f.Register(in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// endpoint is where a send or a load run goes: the registered receiver, or the URL with the token.
func (f *Fake) endpoint(receiver, rawURL, token string) (Endpoint, error) {
	if receiver == "" {
		if err := checkURL(rawURL); err != nil {
			return Endpoint{}, err
		}
		return Endpoint{URL: rawURL, Token: token}, nil
	}
	if rawURL != "" {
		return Endpoint{}, errors.New("give either receiver or url")
	}
	rc, ok := f.Receiver(receiver)
	if !ok {
		return Endpoint{}, fmt.Errorf("no receiver %q is registered", receiver)
	}
	return rc.Endpoint(), nil
}

func (f *Fake) handleSend(w http.ResponseWriter, r *http.Request) {
	var in sendInput
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := ""
	if in.Token != nil {
		token = *in.Token
	}
	e, err := f.endpoint(in.Receiver, in.URL, token)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case in.NoToken:
		e.Token = ""
	case in.Token != nil:
		e.Token = *in.Token
	}
	body, contentType, err := f.body(in)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	status, err := f.SendAs(r.Context(), e, body, contentType)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]int{"status": status})
}

// body is the body of a send: the payload as JSON, the raw text with its Content-Type, a filler of the given size, or
// a generated webhook.
func (f *Fake) body(in sendInput) ([]byte, string, error) {
	given := 0
	hasPayload := len(in.Payload) > 0 && string(in.Payload) != "null"
	for _, set := range []bool{hasPayload, in.Raw != nil, in.SizeBytes != nil} {
		if set {
			given++
		}
	}
	switch {
	case given > 1:
		return nil, "", errors.New("give one of payload, raw and size_bytes")
	case hasPayload:
		return []byte(in.Payload), "application/json", nil
	case in.Raw != nil:
		return []byte(*in.Raw), in.ContentType, nil
	case in.SizeBytes != nil:
		if *in.SizeBytes < 0 || *in.SizeBytes > maxFillerBytes {
			return nil, "", fmt.Errorf("size_bytes must be between 0 and %d", maxFillerBytes)
		}
		return bytes.Repeat([]byte("x"), *in.SizeBytes), "application/json", nil
	}
	return f.Webhook(f.nextSeq(1), time.Now()), "application/json", nil
}

type loadInput struct {
	URL             string  `json:"url"`
	Receiver        string  `json:"receiver"`
	Token           string  `json:"token"`
	RatePerSecond   int     `json:"rate_per_second"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type loadOutput struct {
	Sent     int     `json:"sent"`
	Accepted int     `json:"accepted"`
	Rejected int     `json:"rejected"`
	Failed   int     `json:"failed"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
}

func (f *Fake) handleLoad(w http.ResponseWriter, r *http.Request) {
	var in loadInput
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	duration := time.Duration(in.DurationSeconds * float64(time.Second))
	e, err := f.endpoint(in.Receiver, in.URL, in.Token)
	switch {
	case err != nil:
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	case in.RatePerSecond <= 0 || in.RatePerSecond > maxLoadRate:
		fakeserver.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("rate_per_second must be between 1 and %d", maxLoadRate))
		return
	case duration <= 0 || duration > maxLoadDuration:
		fakeserver.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("duration_seconds must be above 0 and at most %d", int(maxLoadDuration.Seconds())))
		return
	}
	rep := f.Load(r.Context(), LoadOptions{
		Endpoint: e,
		Rate:     in.RatePerSecond,
		Duration: duration,
	})
	fakeserver.WriteJSON(w, http.StatusOK, loadOutput{
		Sent: rep.Sent, Accepted: rep.Accepted, Rejected: rep.Rejected, Failed: rep.Failed,
		P50Ms: ms(rep.P50), P95Ms: ms(rep.P95), P99Ms: ms(rep.P99), MaxMs: ms(rep.Max),
	})
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// checkURL is the rule of the control endpoints: development mode reaches nothing but loopback.
func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an absolute http or https URL")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("url must name localhost or a loopback address: development mode reaches nothing but loopback")
	}
	return nil
}

// HeartbeatSender is a Heartbeat sender of the fake: what Alertmanager's Heartbeat route does with an always-firing
// alert. It signals URL, Muster's Heartbeat endpoint, with Token as a bearer token or, with TokenIn TokenInPath, in the
// path, by Method (POST, the default, or GET), every IntervalSeconds, or only on request when that is 0.
type HeartbeatSender struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	Token           string `json:"token"`
	TokenIn         string `json:"token_in"`
	Method          string `json:"method"`
	IntervalSeconds int    `json:"interval_seconds"`
}

type heartbeatSender struct {
	HeartbeatSender
	stop context.CancelFunc
	// done is closed once the sender's ticks are no longer read; nil for a sender without an interval.
	done chan struct{}
}

// halt stops the sender and waits until its ticks are no longer read, so that a stopped sender never takes another
// tick: its loop could otherwise still pick a tick over the end of its context, which select chooses at random.
func (s *heartbeatSender) halt() {
	s.stop()
	if s.done != nil {
		<-s.done
	}
}

// maxHeartbeatInterval bounds the interval of a Heartbeat sender.
const maxHeartbeatInterval = 24 * 60 * 60

// heartbeatBody is what a signal by POST carries: Alertmanager's webhook of the always-firing alert. Muster ignores it.
const heartbeatBody = `{"version":"4","status":"firing","receiver":"muster-heartbeat",` +
	`"groupLabels":{"alertname":"MusterHeartbeat"},"alerts":[{"status":"firing",` +
	`"labels":{"alertname":"MusterHeartbeat"}}]}`

// StartHeartbeat starts the Heartbeat sender h, or replaces the one of the same name; with an interval it signals
// every interval from then on until it is stopped, whether ctx ends or not. Unlike the control endpoint, it takes any
// URL.
func (f *Fake) StartHeartbeat(ctx context.Context, h HeartbeatSender) error {
	if h.Name == "" {
		return errors.New("a heartbeat sender needs a name")
	}
	if h.TokenIn == "" {
		h.TokenIn = TokenInHeader
	}
	if h.TokenIn != TokenInHeader && h.TokenIn != TokenInPath {
		return errors.New(`token_in must be "header" or "path"`)
	}
	h.Method = strings.ToUpper(h.Method)
	if h.Method == "" {
		h.Method = http.MethodPost
	}
	if h.Method != http.MethodPost && h.Method != http.MethodGet {
		return errors.New(`method must be "GET" or "POST"`)
	}
	if h.IntervalSeconds < 0 || h.IntervalSeconds > maxHeartbeatInterval {
		return fmt.Errorf("interval_seconds must be between 0 and %d", maxHeartbeatInterval)
	}
	ctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s := &heartbeatSender{HeartbeatSender: h, stop: stop}
	if h.IntervalSeconds > 0 {
		s.done = make(chan struct{})
	}
	f.mu.Lock()
	old := f.heartbeats[h.Name]
	f.heartbeats[h.Name] = s
	f.mu.Unlock()
	if old != nil {
		old.halt()
	}
	if h.IntervalSeconds > 0 {
		ticks, stopTicks := f.heartbeatTicker()(time.Duration(h.IntervalSeconds) * time.Second)
		go f.beat(ctx, s.HeartbeatSender, ticks, stopTicks, s.done)
	}
	return nil
}

// SetHeartbeatTicker replaces the ticks of the Heartbeat senders started afterwards, for tests.
func (f *Fake) SetHeartbeatTicker(ticker func(time.Duration) (<-chan time.Time, func())) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ticker = ticker
}

func (f *Fake) heartbeatTicker() func(time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ticker != nil {
		return f.ticker
	}
	return func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTicker(d)
		return t.C, t.Stop
	}
}

// beat signals at every tick until the sender stops, then closes done; a failed signal is lost, as with Alertmanager.
func (f *Fake) beat(ctx context.Context, h HeartbeatSender, ticks <-chan time.Time, stop func(), done chan struct{}) {
	defer close(done)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			_, _ = f.signal(ctx, h)
		}
	}
}

// SendHeartbeat signals once now with the sender named name and returns the answer's status.
func (f *Fake) SendHeartbeat(ctx context.Context, name string) (int, error) {
	f.mu.Lock()
	s, ok := f.heartbeats[name]
	f.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("no heartbeat sender %q is started", name)
	}
	return f.signal(ctx, s.HeartbeatSender)
}

// StopHeartbeat stops the sender named name, as a cut network would, and reports whether it was started; once it
// returns, the sender takes no more ticks.
func (f *Fake) StopHeartbeat(name string) bool {
	f.mu.Lock()
	s, ok := f.heartbeats[name]
	delete(f.heartbeats, name)
	f.mu.Unlock()
	if ok {
		s.halt()
	}
	return ok
}

// StopHeartbeats stops every sender.
func (f *Fake) StopHeartbeats() {
	f.mu.Lock()
	stopped := make([]*heartbeatSender, 0, len(f.heartbeats))
	for name, s := range f.heartbeats {
		stopped = append(stopped, s)
		delete(f.heartbeats, name)
	}
	f.mu.Unlock()
	for _, s := range stopped {
		s.halt()
	}
}

func (f *Fake) signal(ctx context.Context, h HeartbeatSender) (int, error) {
	target := h.URL
	if h.TokenIn == TokenInPath && h.Token != "" {
		target = strings.TrimSuffix(target, "/") + "/" + url.PathEscape(h.Token)
	}
	var body io.Reader
	if h.Method == http.MethodPost {
		body = strings.NewReader(heartbeatBody)
	}
	req, err := http.NewRequestWithContext(ctx, h.Method, target, body)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", UserAgent)
	if h.Token != "" && h.TokenIn != TokenInPath {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

func (f *Fake) handleStartHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in HeartbeatSender
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := checkURL(in.URL); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := f.StartHeartbeat(r.Context(), in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) handleSendHeartbeat(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	f.mu.Lock()
	_, ok := f.heartbeats[name]
	f.mu.Unlock()
	if !ok {
		fakeserver.WriteError(w, http.StatusNotFound, fmt.Sprintf("no heartbeat sender %q is started", name))
		return
	}
	status, err := f.SendHeartbeat(r.Context(), name)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]int{"status": status})
}

func (f *Fake) handleStopHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !f.StopHeartbeat(r.PathValue("name")) {
		fakeserver.WriteError(w, http.StatusNotFound, fmt.Sprintf("no heartbeat sender %q is started",
			r.PathValue("name")))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
