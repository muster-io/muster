// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command load is the load test: its own fake Alertmanager sends webhooks at a steady rate to Muster's ingestion
// endpoint through a receiver registered with an Integration token, and it reports accepted, rejected and failed
// requests with latency percentiles. It signs in as the bootstrap Admin and creates the Personal access token,
// Integration and Integration token it needs, and fails unless every webhook was accepted. When nothing listens at the
// ingestion endpoint it reports that it skipped.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
	"github.com/muster-io/muster/pkg/apiclient"
)

const (
	sessionCookie = "muster_session"
	// duplicateWindowSeconds is the default of integration.duplicate_window.
	duplicateWindowSeconds = 45
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("load", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rate := flags.Int("rate", 50, "webhooks per `second`")
	duration := flags.Duration("duration", time.Minute, "how long to send")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *rate <= 0 || *duration <= 0 || flags.NArg() > 0 {
		fmt.Fprintln(stderr, "load test: -rate and -duration must be positive, and there are no arguments")
		return 2
	}
	publicURL := strings.TrimSuffix(env("MUSTER_PUBLIC_URL", devmode.PublicURL), "/")
	ingestURL := strings.TrimSuffix(env("MUSTER_INGEST_URL", "http://localhost:8081"), "/")
	endpoint := ingestURL + "/api/v1/ingest"

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fake := fakealertmanager.New()
	if err := fake.Start(ctx, "127.0.0.1:0"); err != nil {
		fmt.Fprintf(stderr, "load test: start the fake Alertmanager: %v\n", err)
		return 1
	}
	defer fake.Close(context.WithoutCancel(ctx))

	switch skip, err := probe(ctx, fake, endpoint); {
	case err != nil:
		fmt.Fprintf(stderr, "load test: %v\n", err)
		return 1
	case skip != "":
		fmt.Fprintf(stdout, "load test: skipped: ingestion is not available (%s)\n", skip)
		return 0
	}
	token, err := provision(ctx, publicURL, env("MUSTER_LOAD_ADMIN_EMAIL", devmode.AdminEmail),
		env("MUSTER_LOAD_ADMIN_PASSWORD", devmode.AdminPassword))
	if err != nil {
		fmt.Fprintf(stderr, "load test: %v\n", err)
		return 1
	}

	receiver := fakealertmanager.Receiver{Name: "muster", URL: endpoint, Token: token}
	if err := fake.Register(receiver); err != nil {
		fmt.Fprintf(stderr, "load test: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "load test: %d/s to %s for %v\n", *rate, endpoint, *duration)
	report := fake.Load(ctx, fakealertmanager.LoadOptions{
		Endpoint: receiver.Endpoint(),
		Rate:     *rate,
		Duration: *duration,
	})
	counts, latencies, _ := strings.Cut(report.String(), "\n")
	fmt.Fprintf(stdout, "load test: %s\nload test: ingest %s\n", counts, latencies)
	if report.Accepted != report.Sent {
		fmt.Fprintln(stderr, "load test: failed: not every webhook was accepted")
		return 1
	}
	return 0
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

// provision signs in, creates a Personal access token with the session, then an Integration and its token with the
// Personal access token, and returns the Integration token's value.
func provision(ctx context.Context, publicURL, email, password string) (string, error) {
	httpClient := &http.Client{
		Timeout:       30 * time.Second,
		Transport:     &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	c, err := apiclient.NewClientWithResponses(publicURL+"/api/v1", apiclient.WithHTTPClient(httpClient))
	if err != nil {
		return "", fmt.Errorf("API client: %w", err)
	}
	name := fmt.Sprintf("load-test-%d", time.Now().Unix())

	session, err := c.CreateSessionWithResponse(ctx, apiclient.SessionCreate{Login: email, Password: &password})
	if err = check("sign in", session, err, session != nil && session.JSON201 != nil); err != nil {
		return "", err
	}
	var cookie *http.Cookie
	for _, ck := range session.HTTPResponse.Cookies() {
		if ck.Name == sessionCookie {
			cookie = ck
		}
	}
	if cookie == nil {
		return "", fmt.Errorf("sign in: the answer sets no %s cookie", sessionCookie)
	}
	// The session cookie is Secure, so a cookie jar would not send it over plain HTTP.
	withSession := func(_ context.Context, req *http.Request) error {
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", session.JSON201.CsrfToken)
		return nil
	}

	pat, err := c.CreatePersonalAccessTokenWithResponse(ctx, apiclient.PersonalAccessTokenCreate{
		Name:        name,
		Permissions: []apiclient.Permission{apiclient.IntegrationsRead, apiclient.IntegrationsWrite},
		ExpiresAt:   nullable.NewNullableWithValue(time.Now().Add(24 * time.Hour)),
	}, withSession)
	if err = check("create the Personal access token", pat, err, pat != nil && pat.JSON201 != nil); err != nil {
		return "", err
	}
	withToken := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+pat.JSON201.Value)
		return nil
	}

	integration, err := c.CreateIntegrationWithResponse(ctx, apiclient.IntegrationInput{
		Name:                   name,
		ConnectionMode:         apiclient.IntegrationInputConnectionModeWebhookOnly,
		StaticLabels:           apiclient.Labels{},
		DuplicateWindowSeconds: duplicateWindowSeconds,
		Heartbeat:              apiclient.HeartbeatSettingsInput{Enabled: false},
	}, withToken)
	created := integration != nil && integration.JSON201 != nil
	if err = check("create the Integration", integration, err, created); err != nil {
		return "", err
	}

	token, err := c.CreateIntegrationTokenWithResponse(ctx, integration.JSON201.Id,
		apiclient.IntegrationTokenCreate{Name: &name}, withToken)
	if err = check("create the Integration token", token, err, token != nil && token.JSON201 != nil); err != nil {
		return "", err
	}
	return token.JSON201.Value, nil
}

type response interface{ Status() string }

// check names the step and the answer's status when a call failed or did not answer 201.
func check(step string, resp response, err error, created bool) error {
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", step, err)
	case !created:
		return fmt.Errorf("%s: answered %s", step, resp.Status())
	}
	return nil
}
