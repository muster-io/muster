// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command load is the load test (C-01.FR-7, NFR-1, NFR-2, P-44). It signs in as the bootstrap Admin and creates a
// Personal access token, an Integration, a Route with the Group key [alertname, cluster] and, unless
// -destination=none, a Mattermost Connection and Destination on the fake Mattermost server of `muster dev`. Its own
// fake Alertmanager then sends -rate webhooks per second for -duration that bring -alerts fingerprints into -groups
// Alert Groups, each group's Snapshot growing until it lists all its Alerts. It fails when a webhook is rejected or
// fails, when fewer Alerts fire or fewer Alert Groups are open than it sent, when the 99th percentile of
// muster_ingest_request_duration_seconds exceeds 1 s (P-44) or the 95th percentile of muster_delivery_latency_seconds
// exceeds 5 s (NFR-2); both are read from the histogram buckets on /metrics before and after the run. When nothing
// listens at the ingestion endpoint it reports that it skipped.
package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/pkg/apiclient"
)

const (
	sessionCookie = "muster_session"
	// duplicateWindowSeconds is the default of integration.duplicate_window.
	duplicateWindowSeconds = 45
	receiverName           = "muster"
	alertname              = "LoadTest"

	// The thresholds: P-44 for ingestion, NFR-2 for delivery.
	ingestP99Max   = time.Second
	deliveryP95Max = 5 * time.Second

	ingestHistogram   = "muster_ingest_request_duration_seconds"
	deliveryHistogram = "muster_delivery_latency_seconds"
	deliveryQueue     = "muster_delivery_queue"

	destinationMattermost = "mattermost"
	destinationNone       = "none"
	// limiterPerSecond is the limiter of the run's Connection and Destination: well above the rate of the run, so that
	// only Muster's own speed shows in the delivery latency.
	limiterPerSecond = 1000
)

// fakeBotToken is the bot token of the run's Connection; the fake Mattermost server takes any token as its bot's.
//
//nolint:gosec // G101: not a credential, the fake server accepts every token
const fakeBotToken = "load-test-bot-token"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the flags and the environment of a run.
type options struct {
	rate                      int
	duration, settle          time.Duration
	groups, alerts            int
	destination               string
	publicURL, ingestURL      string
	metricsURL, mattermost    string
	adminEmail, adminPassword string
}

func (o options) webhooks() int { return int(math.Round(o.duration.Seconds() * float64(o.rate))) }

func parse(args []string, stderr io.Writer) (options, int) {
	flags := flag.NewFlagSet("load", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o options
	flags.IntVar(&o.rate, "rate", 50, "webhooks per `second`")
	flags.DurationVar(&o.duration, "duration", time.Minute, "how long to send")
	flags.IntVar(&o.groups, "groups", 1000, "Alert Groups to bring")
	flags.IntVar(&o.alerts, "alerts", 10_000, "Alerts to bring, the same number in every Alert Group")
	flags.StringVar(&o.destination, "destination", destinationMattermost,
		"`mattermost` delivers to the fake Mattermost server and checks the delivery latency; none creates no Destination")
	flags.DurationVar(&o.settle, "settle", 5*time.Minute, "how long at most to wait for processing and delivery after sending")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0
		}
		return o, 2
	}
	switch {
	case o.rate <= 0 || o.duration <= 0 || o.settle < 0 || o.groups <= 0 || o.alerts <= 0 || flags.NArg() > 0:
		fmt.Fprintln(stderr, "load test: -rate, -duration, -groups and -alerts must be positive, and there are no arguments")
		return o, 2
	case o.destination != destinationMattermost && o.destination != destinationNone:
		fmt.Fprintln(stderr, "load test: -destination is mattermost or none")
		return o, 2
	case o.webhooks() < o.groups || o.webhooks()%o.groups != 0 || o.alerts%o.groups != 0:
		fmt.Fprintf(stderr, "load test: the %d webhooks of -rate × -duration must be a multiple of -groups (%d), and "+
			"-alerts (%d) too\n", o.webhooks(), o.groups, o.alerts)
		return o, 2
	}
	o.publicURL = strings.TrimSuffix(env("MUSTER_PUBLIC_URL", devmode.PublicURL), "/")
	o.ingestURL = strings.TrimSuffix(env("MUSTER_INGEST_URL", devmode.IngestURL), "/")
	o.metricsURL = env("MUSTER_LOAD_METRICS_URL", "http://localhost:8082/metrics")
	o.mattermost = strings.TrimSuffix(env("MUSTER_LOAD_MATTERMOST_URL", "http://"+devmode.MattermostAddr), "/")
	o.adminEmail = env("MUSTER_LOAD_ADMIN_EMAIL", devmode.AdminEmail)
	o.adminPassword = env("MUSTER_LOAD_ADMIN_PASSWORD", devmode.AdminPassword)
	return o, -1
}

func run(args []string, stdout, stderr io.Writer) int {
	o, code := parse(args, stderr)
	if code >= 0 {
		return code
	}
	endpoint := o.ingestURL + "/api/v1/ingest"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fail := func(err error) int {
		fmt.Fprintf(stderr, "load test: %v\n", err)
		return 1
	}
	fake := fakealertmanager.New()
	if err := fake.Start(ctx, "127.0.0.1:0"); err != nil {
		return fail(fmt.Errorf("start the fake Alertmanager: %w", err))
	}
	defer fake.Close(context.WithoutCancel(ctx))

	switch skip, err := probe(ctx, fake, endpoint); {
	case err != nil:
		return fail(err)
	case skip != "":
		fmt.Fprintf(stdout, "load test: skipped: ingestion is not available (%s)\n", skip)
		return 0
	}
	runName := fmt.Sprintf("load-test-%d", time.Now().Unix())
	s, err := provision(ctx, o, runName)
	if err != nil {
		return fail(err)
	}
	if err := fake.Register(fakealertmanager.Receiver{Name: receiverName, URL: endpoint, Token: s.token}); err != nil {
		return fail(err)
	}
	before, err := scrape(ctx, o.metricsURL)
	if err != nil {
		return fail(err)
	}

	p := plan{groups: o.groups, perGroup: o.alerts / o.groups, passes: o.webhooks() / o.groups, run: runName}
	target := "no destination"
	if s.destination != "" {
		target = "destination " + s.destination
	}
	fmt.Fprintf(stdout, "load test: %d/s to %s for %v: %d Alerts in %d Alert Groups, %d Snapshots each; %s\n",
		o.rate, endpoint, o.duration, o.alerts, o.groups, p.passes, target)
	report, err := p.send(ctx, fake, o.rate)
	if err != nil {
		return fail(err)
	}
	counts, latencies, _ := strings.Cut(report.String(), "\n")
	fmt.Fprintf(stdout, "load test: %s\nload test: webhook %s\n", counts, latencies)

	state := s.settle(ctx, o, before)
	after, err := scrape(ctx, o.metricsURL)
	if err != nil {
		return fail(err)
	}
	return verdict(stdout, o, report, state, before, after, s.destination)
}

func env(name, fallback string) string {
	return cmp.Or(os.Getenv(name), fallback)
}

// probe sends a webhook without a token, which ingestion answers 401. When nothing listens, probe returns why to skip;
// any other answer is an error, so a broken target fails the run.
func probe(ctx context.Context, fake *fakealertmanager.Fake, endpoint string) (skip string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, err := fake.Send(ctx, fakealertmanager.Endpoint{URL: endpoint}, []byte(`{}`))
	switch {
	case err != nil && connectionFailed(err):
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err.Error(), nil
	case err != nil:
		return "", fmt.Errorf("probe %s: %w", endpoint, err)
	case status == http.StatusUnauthorized:
		return "", nil
	}
	return "", fmt.Errorf("probe %s: answered %d %s without a token; want 401 from ingestion",
		endpoint, status, http.StatusText(status))
}

// connectionFailed is a failure before any answer: the connection was not made, or the peer closed it unanswered, as
// a published port does while its container restarts.
func connectionFailed(err error) bool {
	var op *net.OpError
	return (errors.As(err, &op) && op.Op == "dial") || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF)
}

var httpClient = &http.Client{
	Timeout:       30 * time.Second,
	Transport:     &http.Transport{},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// setup is what provision created: the Integration token, and the public_ids of the Integration, the Route and the
// Destination, empty with -destination=none.
type setup struct {
	api                             *apiclient.ClientWithResponses
	auth                            apiclient.RequestEditorFn
	token                           string
	integration, route, destination string
}

// provision signs in, creates a Personal access token with the session, then with it an Integration and its token,
// the Mattermost Connection and Destination unless o.destination is none, and the Route of the run.
func provision(ctx context.Context, o options, name string) (setup, error) {
	c, err := apiclient.NewClientWithResponses(o.publicURL+"/api/v1", apiclient.WithHTTPClient(httpClient))
	if err != nil {
		return setup{}, fmt.Errorf("API client: %w", err)
	}
	password := o.adminPassword
	session, err := c.CreateSessionWithResponse(ctx, apiclient.SessionCreate{Login: o.adminEmail, Password: &password})
	if err = check("sign in", session, err, session != nil && session.JSON201 != nil); err != nil {
		return setup{}, err
	}
	var cookie *http.Cookie
	for _, ck := range session.HTTPResponse.Cookies() {
		if ck.Name == sessionCookie {
			cookie = ck
		}
	}
	if cookie == nil {
		return setup{}, fmt.Errorf("sign in: the answer sets no %s cookie", sessionCookie)
	}
	// The session cookie is Secure, so a cookie jar would not send it over plain HTTP.
	withSession := func(_ context.Context, req *http.Request) error {
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", session.JSON201.CsrfToken)
		return nil
	}
	me, err := c.GetMeWithResponse(ctx, withSession)
	if err = check("read the Admin", me, err, me != nil && me.JSON200 != nil); err != nil {
		return setup{}, err
	}
	pat, err := c.CreatePersonalAccessTokenWithResponse(ctx, apiclient.PersonalAccessTokenCreate{
		Name:        name,
		Permissions: me.JSON200.Permissions,
		ExpiresAt:   nullable.NewNullableWithValue(time.Now().Add(24 * time.Hour)),
	}, withSession)
	if err = check("create the Personal access token", pat, err, pat != nil && pat.JSON201 != nil); err != nil {
		return setup{}, err
	}
	s := setup{api: c, auth: func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+pat.JSON201.Value)
		return nil
	}}

	integration, err := c.CreateIntegrationWithResponse(ctx, apiclient.IntegrationInput{
		Name:                   name,
		ConnectionMode:         apiclient.IntegrationInputConnectionModeWebhookOnly,
		StaticLabels:           apiclient.Labels{},
		DuplicateWindowSeconds: duplicateWindowSeconds,
		Heartbeat:              apiclient.HeartbeatSettingsInput{Enabled: false},
	}, s.auth)
	if err = check("create the Integration", integration, err, integration != nil && integration.JSON201 != nil); err != nil {
		return setup{}, err
	}
	s.integration = integration.JSON201.Id
	token, err := c.CreateIntegrationTokenWithResponse(ctx, s.integration, apiclient.IntegrationTokenCreate{Name: &name},
		s.auth)
	if err = check("create the Integration token", token, err, token != nil && token.JSON201 != nil); err != nil {
		return setup{}, err
	}
	s.token = token.JSON201.Value

	if o.destination == destinationMattermost {
		if s.destination, err = s.mattermostDestination(ctx, o, name); err != nil {
			return setup{}, err
		}
	}
	if s.route, err = s.createRoute(ctx, o, name); err != nil {
		return setup{}, err
	}
	return s, nil
}

// mattermostDestination creates a Connection to the fake Mattermost server and a Destination on its channel, both with
// a limiter that leaves room, after turning the fake's rate limit off.
func (s setup) mattermostDestination(ctx context.Context, o options, name string) (string, error) {
	if err := fakeRateLimitOff(ctx, o.mattermost); err != nil {
		return "", err
	}
	limiter := map[string]int{"limit": limiterPerSecond, "per_seconds": 1}
	conn, err := s.api.CreateConnectionWithBodyWithResponse(ctx, "application/json", jsonBody(map[string]any{
		"type": "mattermost", "name": name, "server_url": o.mattermost, "bot_token": fakeBotToken,
		"proxy": map[string]any{"enabled": false}, "limiter": limiter}), s.auth)
	var created struct {
		ID string `json:"id"`
	}
	if err = check("create the Connection", conn, err, conn != nil && conn.StatusCode() == http.StatusCreated &&
		json.Unmarshal(conn.Body, &created) == nil); err != nil {
		return "", err
	}
	none := map[string]any{"everyone": "none", "user_ids": []string{}, "groups": []string{}}
	mentions := map[string]any{}
	for _, event := range []string{"new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended",
		"rise_to_urgent"} {
		mentions[event] = none
	}
	dest, err := s.api.CreateDestinationWithBodyWithResponse(ctx, "application/json", jsonBody(map[string]any{
		"type": "mattermost", "name": name, "connection_id": created.ID, "team_id": fakemattermost.TeamID,
		"channel_id": fakemattermost.ChannelAlerts, "mentions": mentions, "limiter": limiter}), s.auth)
	var d struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	if err = check("create the Destination", dest, err, dest != nil && dest.StatusCode() == http.StatusCreated &&
		json.Unmarshal(dest.Body, &d) == nil); err != nil {
		return "", err
	}
	return d.Destination.ID, nil
}

// fakeRateLimitOff turns the rate limit of the fake Mattermost server off, so that it leaves room for the run.
func fakeRateLimitOff(ctx context.Context, server string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, server+"/_fake/config",
		strings.NewReader(`{"rate_limit":{"enabled":false}}`))
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("turn the fake Mattermost's rate limit off: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("turn the fake Mattermost's rate limit off: answered %s", resp.Status)
	}
	return nil
}

// createRoute creates the Route of the run: the label load_run of the run, the Group key [alertname, cluster], the
// Destination if there is one, and the first profile's policy with a Storm threshold above the run, which would
// otherwise hold back the Root messages whose delivery it measures.
func (s setup) createRoute(ctx context.Context, o options, name string) (string, error) {
	profiles, err := s.api.ListRouteProfilesWithResponse(ctx, s.auth)
	var list struct {
		Items []struct {
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	if err = check("read the route profiles", profiles, err, profiles != nil &&
		profiles.StatusCode() == http.StatusOK && json.Unmarshal(profiles.Body, &list) == nil && len(list.Items) > 0); err != nil {
		return "", err
	}
	policy := list.Items[0].Policy
	policy["storm_threshold"] = 10 * o.groups
	destinations := []string{}
	if s.destination != "" {
		destinations = append(destinations, s.destination)
	}
	route, err := s.api.CreateRouteWithBodyWithResponse(ctx, "application/json", jsonBody(map[string]any{
		"name": name, "urgent": false, "group_key": []string{"alertname", "cluster"},
		"matchers":        []map[string]string{{"label": "load_run", "op": "=", "value": name}},
		"destination_ids": destinations, "policy": policy}), s.auth)
	if err = check("create the Route", route, err, route != nil && route.JSON201 != nil); err != nil {
		return "", err
	}
	return route.JSON201.Id, nil
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

type response interface {
	Status() string
	StatusCode() int
}

// check names the step and the answer's status when a call failed or did not answer as expected.
func check(step string, resp response, err error, ok bool) error {
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", step, err)
	case !ok:
		return fmt.Errorf("%s: answered %s", step, resp.Status())
	}
	return nil
}

// plan spreads the run over groups Alertmanager groups of perGroup Alerts, each notified passes times: webhook j
// notifies group j mod groups, whose Snapshot lists a further share of its Alerts each time and all of them the last
// time.
type plan struct {
	groups, perGroup, passes int
	run                      string
}

func (p plan) webhooks() int { return p.groups * p.passes }

// next prepares webhook j in the fake's group model and returns its body and where it goes.
func (p plan) next(fake *fakealertmanager.Fake, j int) ([]byte, fakealertmanager.Endpoint, error) {
	g, pass := j%p.groups, j/p.groups
	name := fmt.Sprintf("g%04d", g)
	reason := "new alerts added"
	if pass == 0 {
		reason = "first notification"
		if _, err := fake.PutGroup(name, fakealertmanager.GroupSpec{Receiver: receiverName, Route: "{}",
			Labels: map[string]string{"alertname": alertname, "cluster": fmt.Sprintf("c%04d", g)}}); err != nil {
			return nil, fakealertmanager.Endpoint{}, err
		}
	}
	for a := p.perGroup * pass / p.passes; a < p.perGroup*(pass+1)/p.passes; a++ {
		if err := fake.PutAlert(name, fmt.Sprintf("a%03d", a), fakealertmanager.AlertSpec{Labels: map[string]string{
			"load_run": p.run, "disk": fmt.Sprintf("d%03d", a), "severity": "warning"}}); err != nil {
			return nil, fakealertmanager.Endpoint{}, err
		}
	}
	_, rc, snaps, err := fake.Snapshots(name, fakealertmanager.NotifyOptions{Reason: reason})
	if err != nil {
		return nil, fakealertmanager.Endpoint{}, err
	}
	return snaps[0].Body, rc.Endpoint(), nil
}

// send sends the plan's webhooks at rate per second, each in its own goroutine at a steady tick, and waits for the
// sends in flight. A cancelled ctx stops it early.
func (p plan) send(ctx context.Context, fake *fakealertmanager.Fake, rate int) (fakealertmanager.LoadReport, error) {
	var (
		mu        sync.Mutex
		report    fakealertmanager.LoadReport
		latencies []time.Duration
		wg        sync.WaitGroup
	)
	ticker := time.NewTicker(max(time.Second/time.Duration(rate), time.Microsecond))
	defer ticker.Stop()
	for j := range p.webhooks() {
		if j > 0 && !tick(ctx, ticker) {
			break
		}
		body, endpoint, err := p.next(fake, j)
		if err != nil {
			wg.Wait()
			return report, fmt.Errorf("prepare webhook %d: %w", j, err)
		}
		wg.Go(func() {
			begin := time.Now()
			status, err := fake.Send(ctx, endpoint, body)
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
	return report, nil
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

// outcome is what Muster holds at the end of the run: the open Alert Groups with Alerts of the run's Integration and
// their firing Alerts, and the deliveries still waiting for the run's Destination.
type outcome struct {
	open, firing int
	queued       float64
	err          error
}

// settle waits until the Alert Groups and Alerts of the run are all there and, with a Destination, nothing waits for
// it any more and every Alert Group's Root message was delivered, or until o.settle passes.
func (s setup) settle(ctx context.Context, o options, before metrics) outcome {
	deadline := time.Now().Add(o.settle)
	for {
		st := s.state(ctx)
		done := st.err == nil && st.open >= o.groups && st.firing >= o.alerts
		if done && s.destination != "" {
			m, err := scrape(ctx, o.metricsURL)
			match := `destination="` + s.destination + `"`
			delivered := deltaOf(before.buckets(deliveryHistogram, match), m.buckets(deliveryHistogram, match))
			done = err == nil && m.value(deliveryQueue+`{destination="`+s.destination+`"}`) == 0 &&
				delivered.count() >= float64(o.groups)
			st.queued = m.value(deliveryQueue + `{destination="` + s.destination + `"}`)
		}
		if done || time.Now().After(deadline) || ctx.Err() != nil {
			return st
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// state counts the open Alert Groups of the run's Integration and their firing Alerts.
func (s setup) state(ctx context.Context) outcome {
	var st outcome
	limit := 500
	params := &apiclient.ListAlertGroupsParams{Integration: &apiclient.AgIntegration{s.integration}, Limit: &limit}
	for {
		page, err := s.api.ListAlertGroupsWithResponse(ctx, params, s.auth)
		if err = check("list the Alert Groups", page, err, page != nil && page.JSON200 != nil); err != nil {
			st.err = err
			return st
		}
		for _, g := range page.JSON200.Items {
			st.open++
			st.firing += g.FiringAlertCount
		}
		next, err := page.JSON200.NextCursor.Get()
		if err != nil || next == "" {
			return st
		}
		params.Cursor = &next
	}
}

// metrics are the series of a /metrics page by their name and labels, as the page writes them.
type metrics map[string]float64

func scrape(ctx context.Context, target string) (metrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read the metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read the metrics at %s: answered %s", target, resp.Status)
	}
	m := metrics{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		i := strings.LastIndexByte(line, ' ')
		if i < 0 || strings.HasPrefix(line, "#") {
			continue
		}
		if v, err := strconv.ParseFloat(line[i+1:], 64); err == nil {
			m[line[:i]] = v
		}
	}
	return m, sc.Err()
}

func (m metrics) value(series string) float64 { return m[series] }

// histogram is the cumulative count of each bucket by its upper bound.
type histogram map[float64]float64

// buckets is the histogram name whose series carry the label pair match, such as destination="DS…", or have no
// other label than le when match is empty.
func (m metrics) buckets(name, match string) histogram {
	h := histogram{}
	for series, v := range m {
		labels, ok := strings.CutPrefix(series, name+"_bucket{")
		if !ok {
			continue
		}
		le, rest := cutLabel(strings.TrimSuffix(labels, "}"), "le")
		if rest != match {
			continue
		}
		bound := math.Inf(1)
		if le != "+Inf" {
			var err error
			if bound, err = strconv.ParseFloat(le, 64); err != nil {
				continue
			}
		}
		h[bound] += v
	}
	return h
}

// cutLabel returns the value of the label name in a list such as a="1",le="0.5" and the list without it.
func cutLabel(labels, name string) (string, string) {
	var value string
	var rest []string
	for pair := range strings.SplitSeq(labels, ",") {
		if v, ok := strings.CutPrefix(pair, name+"="); ok {
			value = strings.Trim(v, `"`)
			continue
		}
		if pair != "" {
			rest = append(rest, pair)
		}
	}
	return value, strings.Join(rest, ",")
}

// deltaOf is what the histogram counted between before and after.
func deltaOf(before, after histogram) histogram {
	d := histogram{}
	for le, v := range after {
		d[le] = v - before[le]
	}
	return d
}

func (h histogram) count() float64 { return h[math.Inf(1)] }

// quantile estimates the q-quantile as Prometheus's histogram_quantile does: linearly within the bucket that holds
// it, the lower bound of the last bucket when it falls there.
func (h histogram) quantile(q float64) float64 {
	total := h.count()
	if total == 0 {
		return math.NaN()
	}
	bounds := make([]float64, 0, len(h))
	for le := range h {
		bounds = append(bounds, le)
	}
	slices.Sort(bounds)
	rank := q * total
	lower, below := 0.0, 0.0
	for _, le := range bounds {
		if h[le] >= rank {
			if math.IsInf(le, 1) {
				return lower
			}
			if h[le] == below {
				return le
			}
			return lower + (le-lower)*(rank-below)/(h[le]-below)
		}
		lower, below = le, h[le]
	}
	return lower
}

// verdict prints the figures of the run and PASS or FAIL with the reasons, and returns the exit code.
func verdict(w io.Writer, o options, report fakealertmanager.LoadReport, st outcome, before, after metrics,
	destination string) int {
	var failures []string
	if report.Accepted != o.webhooks() || report.Rejected > 0 || report.Failed > 0 {
		failures = append(failures, fmt.Sprintf("%d of %d webhooks accepted", report.Accepted, o.webhooks()))
	}
	if st.err != nil {
		failures = append(failures, st.err.Error())
	}
	fmt.Fprintf(w, "load test: alerts firing %d (≥ %d); alert groups open %d (≥ %d)\n", st.firing, o.alerts, st.open,
		o.groups)
	if st.firing < o.alerts {
		failures = append(failures, fmt.Sprintf("%d Alerts fire, fewer than %d", st.firing, o.alerts))
	}
	if st.open < o.groups {
		failures = append(failures, fmt.Sprintf("%d Alert Groups are open, fewer than %d", st.open, o.groups))
	}
	ingest := deltaOf(before.buckets(ingestHistogram, ""), after.buckets(ingestHistogram, ""))
	p99 := ingest.quantile(0.99)
	fmt.Fprintf(w, "load test: ingest p99 %s (≤ %v, %.0f requests)\n", seconds(p99), ingestP99Max, ingest.count())
	if math.IsNaN(p99) || p99 > ingestP99Max.Seconds() {
		failures = append(failures, "the ingestion's 99th percentile is above "+ingestP99Max.String()+" (P-44)")
	}
	if destination != "" {
		match := `destination="` + destination + `"`
		delivery := deltaOf(before.buckets(deliveryHistogram, match), after.buckets(deliveryHistogram, match))
		p95 := delivery.quantile(0.95)
		fmt.Fprintf(w, "load test: delivery p95 %s (≤ %v, %.0f root message changes, %.0f still waiting)\n",
			seconds(p95), deliveryP95Max, delivery.count(), st.queued)
		if math.IsNaN(p95) || p95 > deliveryP95Max.Seconds() || delivery.count() < float64(o.groups) {
			failures = append(failures, "the delivery latency's 95th percentile is above "+deliveryP95Max.String()+
				" or not every Alert Group was delivered (NFR-2)")
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(w, "load test: FAIL: %s\n", strings.Join(failures, "; "))
		return 1
	}
	fmt.Fprintln(w, "load test: PASS")
	return 0
}

func seconds(v float64) string {
	if math.IsNaN(v) {
		return "n/a"
	}
	return strconv.FormatFloat(v, 'f', 3, 64) + "s"
}
