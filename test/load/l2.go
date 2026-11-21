// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
	"github.com/muster-io/muster/pkg/apiclient"
)

const (
	scenarioL1 = "l1"
	scenarioL2 = "l2"
	// teamLabel is the default of organization.team_label.
	teamLabel    = "team"
	maxTeamUsers = 20
	// provisionWorkers bounds the Teams provisioned at once: each signs its people in, which hashes a password.
	provisionWorkers = 8
	// chainWaitSeconds is the wait that ends each chain of the run, longer than any run, so that its runs stay running.
	chainWaitSeconds = 3600
	// combineWindow is the default of gateway.combine_window.
	combineWindow = time.Minute
	// statisticsEvery is how often the statistics are read while the storm is sent and settles.
	statisticsEvery = 2 * time.Second
	// statisticsAfter is how many times each statistics query is read after the run settled.
	statisticsAfter = 5
)

// codePattern finds the code of gateway.contact_code in the text of a notification.
var codePattern = regexp.MustCompile(`\b\d{6}\b`)

// team is a Team of the run with the people its chain Pages.
type team struct {
	id, slug string
	users    []string
}

// runL2 is the l2 scenario: the storm of run, sent to Teams whose Escalation chains Page their people by Mention and
// by call through the Notification gateway "Example Gateway" of `muster dev`. It keeps the checks of the storm and
// reports, from the database and the sends, how long Root messages, Pages and calls took, how long statistics took to
// read, and the database's load; it fails as well when no Page was made or no call was accepted.
func runL2(ctx context.Context, o options, fake *fakealertmanager.Fake, endpoint string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "load test: %v\n", err)
		return 1
	}
	runName := fmt.Sprintf("load-test-%d", time.Now().Unix())
	s, err := provision(ctx, o, runName)
	if err != nil {
		return fail(err)
	}
	pool, err := pgxpool.New(ctx, o.databaseURL)
	if err != nil {
		return fail(fmt.Errorf("connect to the database: %w", err))
	}
	defer pool.Close()
	sc, err := lookupScope(ctx, pool, s.route)
	if err != nil {
		return fail(err)
	}
	begin := time.Now()
	teams, err := s.provisionTeams(ctx, o, runName)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "load test: %d Teams with %d Users each, confirmed contacts at the gateway, chains and rules "+
		"in %.1fs\n", len(teams), o.teamUsers, time.Since(begin).Seconds())
	if err := fake.Register(fakealertmanager.Receiver{Name: receiverName, URL: endpoint, Token: s.token}); err != nil {
		return fail(err)
	}
	before, err := scrape(ctx, o.metricsURL)
	if err != nil {
		return fail(err)
	}

	sent := make([]time.Time, o.groups)
	p := plan{groups: o.groups, perGroup: o.alerts / o.groups, passes: o.webhooks() / o.groups, run: runName,
		began: func(g int, at time.Time) { sent[g] = at }}
	for _, t := range teams {
		p.teams = append(p.teams, t.slug)
	}
	fmt.Fprintf(stdout, "load test: %d/s to %s for %v: %d Alerts in %d Alert Groups, %d Snapshots each; destination %s; "+
		"%d Teams\n", o.rate, endpoint, o.duration, o.alerts, o.groups, p.passes, s.destination, len(teams))
	stopProgress := progress(ctx, stdout, o.metricsURL, before, 5*time.Second)
	stopSampling := sampleDatabase(ctx, pool)
	stopStatistics := s.timeStatistics(ctx)
	report, err := p.send(ctx, fake, o.rate)
	if err != nil {
		stopProgress()
		stopSampling()
		stopStatistics()
		return fail(err)
	}
	counts, latencies, _ := strings.Cut(report.String(), "\n")
	fmt.Fprintf(stdout, "load test: %s\nload test: webhook %s\n", counts, latencies)

	settleBegin := time.Now()
	state := s.settle(ctx, o, before)
	paging := sc.settle(ctx, o.groups, settleBegin.Add(o.settle))
	stopProgress()
	load := stopSampling()
	during := stopStatistics()
	fmt.Fprintf(stdout, "load test: settled in %.1fs: %s\n", time.Since(settleBegin).Seconds(), paging)
	after, err := scrape(ctx, o.metricsURL)
	if err != nil {
		return fail(err)
	}
	breakdown(stdout, before, after)
	afterStorm := timings{}
	for range statisticsAfter {
		s.readStatistics(ctx, afterStorm)
	}
	failures := sc.report(ctx, stdout, sent, load, during, afterStorm)
	return verdict(stdout, o, report, state, before, after, s.destination, failures...)
}

// provisionTeams creates the Teams of the run, provisionWorkers at a time, each with o.teamUsers people Paged by its
// Escalation chain.
func (s setup) provisionTeams(ctx context.Context, o options, run string) ([]team, error) {
	gateway, err := s.gateway(ctx)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	password := hex.EncodeToString(secret)
	teams := make([]team, o.teams)
	errs := make([]error, o.teams)
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(provisionWorkers, o.teams) {
		wg.Go(func() {
			for i := range next {
				teams[i], errs[i] = s.provisionTeam(ctx, o, fmt.Sprintf("%s-t%04d", run, i), password, gateway)
			}
		})
	}
	for i := range o.teams {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return teams, ctx.Err()
}

// gateway is the public_id of the Notification gateway "Example Gateway" that `muster dev` ensures.
func (s setup) gateway(ctx context.Context) (string, error) {
	role := apiclient.BridgeInfoRole("gateway")
	resp, err := s.api.ListConnectionsWithResponse(ctx, &apiclient.ListConnectionsParams{Role: &role}, s.session)
	var list struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err = check("list the Notification gateways", resp, err, resp != nil && resp.StatusCode() == http.StatusOK &&
		json.Unmarshal(resp.Body, &list) == nil); err != nil {
		return "", err
	}
	for _, c := range list.Items {
		if c.Name == devmode.GatewayConnectionName {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("no Notification gateway %q: the l2 scenario runs against `muster dev`",
		devmode.GatewayConnectionName)
}

// provisionTeam creates the people of a Team, each with a confirmed contact at the gateway and Notification rules
// whose important sequence is a Mention and a call; then the Team's Escalation chain, which Pages them importantly,
// calls them through the gateway and waits; and the Team, named and slugged name, which names the chain and has them.
func (s setup) provisionTeam(ctx context.Context, o options, name, password, gateway string) (team, error) {
	t := team{slug: name}
	for u := range o.teamUsers {
		id, err := s.provisionUser(ctx, fmt.Sprintf("%s-u%02d", name, u), password, gateway)
		if err != nil {
			return team{}, err
		}
		t.users = append(t.users, id)
	}
	importance, target := apiclient.PageImportance("important"), apiclient.EscalationStepInputCallTarget("people")
	wait := chainWaitSeconds
	chain, err := s.api.CreateEscalationChainWithResponse(ctx, apiclient.EscalationChainInput{Name: name,
		Steps: []apiclient.EscalationStepInput{
			{Kind: "page_people", UserIds: &t.users, Importance: &importance},
			{Kind: "call", CallTarget: &target, UserIds: &t.users, GatewayId: &gateway},
			{Kind: "wait", WaitSeconds: &wait},
		}}, s.session)
	if err = check("create the Escalation chain "+name, chain, err, chain != nil && chain.JSON201 != nil); err != nil {
		return team{}, err
	}
	created, err := s.api.CreateTeamWithResponse(ctx, apiclient.TeamInput{Name: name, Slug: name,
		ChainEntries: &[]apiclient.ChainEntry{{ChainId: chain.JSON201.Id}}}, s.session)
	if err = check("create the Team "+name, created, err, created != nil && created.JSON201 != nil); err != nil {
		return team{}, err
	}
	t.id = created.JSON201.Id
	for _, u := range t.users {
		added, err := s.api.AddTeamUserWithResponse(ctx, t.id, apiclient.TeamUserAdd{UserId: u}, s.session)
		if err = check("add a User to the Team "+name, added, err, added != nil && added.StatusCode()/100 == 2); err != nil {
			return team{}, err
		}
	}
	return t, nil
}

// provisionUser creates a Responder with the password, signs in as them and, in their session, adds a contact at the
// gateway, confirms it with the code the fake bridge received and sets their Notification rules: a Mention and a call
// importantly, a Mention normally. It returns the User's public_id.
func (s setup) provisionUser(ctx context.Context, login, password, gateway string) (string, error) {
	user, err := s.api.CreateUserWithResponse(ctx, apiclient.UserCreate{Login: login, Name: login, Role: "responder"},
		s.session)
	if err = check("create the User "+login, user, err, user != nil && user.JSON201 != nil); err != nil {
		return "", err
	}
	_, token, ok := strings.Cut(user.JSON201.PasswordSetupLink.Url, "#token=")
	if !ok {
		return "", fmt.Errorf("create the User %s: the password setup link carries no token", login)
	}
	setupDone, err := s.api.CompletePasswordSetupWithResponse(ctx, apiclient.PasswordSetup{Token: &token,
		Password: &password})
	if err = check("set the password of "+login, setupDone, err, setupDone != nil &&
		setupDone.StatusCode() == http.StatusNoContent); err != nil {
		return "", err
	}
	session, err := signIn(ctx, s.api, login, password)
	if err != nil {
		return "", fmt.Errorf("%s: %w", login, err)
	}

	contact := login
	var created *apiclient.CreateMyGatewayContactResponse
	err = limited(ctx, func() (response, error) {
		created, err = s.api.CreateMyGatewayContactWithResponse(ctx, apiclient.GatewayContactInput{
			ConnectionId: gateway, Contact: contact}, session)
		return created, err
	})
	if err = check("add the contact of "+login, created, err, created != nil && created.JSON202 != nil); err != nil {
		return "", err
	}
	code, err := contactCode(ctx, contact)
	if err != nil {
		return "", fmt.Errorf("the code of %s: %w", login, err)
	}
	confirmed, err := s.api.ConfirmMyGatewayContactWithResponse(ctx, created.JSON202.Id,
		apiclient.GatewayContactConfirm{Code: &code}, session)
	if err = check("confirm the contact of "+login, confirmed, err, confirmed != nil && confirmed.JSON200 != nil &&
		confirmed.JSON200.Confirmed); err != nil {
		return "", err
	}

	rules, err := s.api.GetMyNotificationRulesWithResponse(ctx, session)
	if err = check("read the rules of "+login, rules, err, rules != nil && rules.JSON200 != nil); err != nil {
		return "", err
	}
	etag := rules.HTTPResponse.Header.Get("ETag")
	important := []apiclient.NotificationRuleAction{{Kind: "mention"}, {Kind: "call", GatewayId: &gateway}}
	normal := []apiclient.NotificationRuleAction{{Kind: "mention"}}
	updated, err := s.api.UpdateMyNotificationRulesWithResponse(ctx, &apiclient.UpdateMyNotificationRulesParams{
		IfMatch: etag}, apiclient.NotificationRulesUpdate{State: "custom", Important: &important, Normal: &normal},
		session)
	if err = check("set the rules of "+login, updated, err, updated != nil && updated.JSON200 != nil); err != nil {
		return "", err
	}
	return user.JSON201.User.Id, nil
}

// limited calls call again while it answers 503 or 429, as the interactive path does when the gateway's limiter has
// no token free, up to a minute.
func limited(ctx context.Context, call func() (response, error)) error {
	for range 60 {
		resp, err := call()
		if err != nil || resp == nil ||
			(resp.StatusCode() != http.StatusServiceUnavailable && resp.StatusCode() != http.StatusTooManyRequests) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil
}

// contactCode reads the latest code that the fake bridge received for contact, polling while it has none.
func contactCode(ctx context.Context, contact string) (string, error) {
	for range 50 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+devmode.BridgeAddr+"/_fake/notifications",
			nil)
		if err != nil {
			return "", err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("read the fake bridge's notifications: %w", err)
		}
		var sent []struct {
			Contact string `json:"contact"`
			Text    string `json:"text"`
		}
		err = errors.Join(json.NewDecoder(resp.Body).Decode(&sent), resp.Body.Close())
		if err != nil {
			return "", fmt.Errorf("read the fake bridge's notifications: %w", err)
		}
		for _, n := range slices.Backward(sent) {
			if n.Contact == contact {
				if code := codePattern.FindString(n.Text); code != "" {
					return code, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return "", errors.New("the fake bridge received no code")
}

// timings are how long each read of a statistics query took, by the query's name.
type timings map[string][]time.Duration

// readStatistics reads each statistics query once in the Admin's session and adds how long it took to t; a read that
// fails counts under the query's name with "failed", and nothing counts once ctx ended.
func (s setup) readStatistics(ctx context.Context, t timings) {
	byTeam := &apiclient.GetAlertGroupStatisticsParams{GroupBy: apiclient.GetAlertGroupStatisticsParamsGroupByTeam}
	byRoute := &apiclient.GetAlertGroupStatisticsParams{GroupBy: apiclient.GetAlertGroupStatisticsParamsGroupByRoute}
	queries := []struct {
		name string
		read func() (response, error)
	}{
		{"statistics by Team", func() (response, error) {
			return s.api.GetAlertGroupStatisticsWithResponse(ctx, byTeam, s.session)
		}},
		{"statistics by Route", func() (response, error) {
			return s.api.GetAlertGroupStatisticsWithResponse(ctx, byRoute, s.session)
		}},
		{"statistics by people", func() (response, error) {
			return s.api.GetAlertGroupActorStatisticsWithResponse(ctx, &apiclient.GetAlertGroupActorStatisticsParams{},
				s.session)
		}},
		{"counts", func() (response, error) {
			return s.api.GetAlertGroupCountsWithResponse(ctx, &apiclient.GetAlertGroupCountsParams{}, s.session)
		}},
	}
	for _, q := range queries {
		begin := time.Now()
		resp, err := q.read()
		took := time.Since(begin)
		if ctx.Err() != nil {
			return
		}
		if err != nil || resp.StatusCode() != http.StatusOK {
			t[q.name+" failed"] = append(t[q.name+" failed"], took)
			continue
		}
		t[q.name] = append(t[q.name], took)
	}
}

// timeStatistics reads the statistics every statisticsEvery until stop is called, which returns how long they took.
func (s setup) timeStatistics(ctx context.Context) (stop func() timings) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan timings)
	go func() {
		t := timings{}
		tk := time.NewTicker(statisticsEvery)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- t
				return
			case <-tk.C:
			}
			s.readStatistics(ctx, t)
		}
	}()
	return func() timings {
		cancel()
		return <-done
	}
}

// activity is one sample of the backends of the database: active, idle in a transaction, waiting for a heavyweight
// lock, for a lightweight lock and for I/O, and the locks not granted to them.
type activity struct {
	active, idleInTransaction, lock, lwlock, io, ungranted int64
}

// databaseStats are the counters of pg_stat_database that the run reports as deltas.
type databaseStats struct {
	commits, rollbacks, deadlocks, blocksRead, tuplesUpdated int64
}

// databaseLoad is what sampleDatabase saw: the samples' maxima and sums, and the counters before and after.
type databaseLoad struct {
	samples       int
	max, sum      activity
	before, after databaseStats
	err           error
}

// sampleDatabase samples the database's backends every second until stop is called, which returns what it saw.
func sampleDatabase(ctx context.Context, db *pgxpool.Pool) (stop func() databaseLoad) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan databaseLoad)
	go func() {
		var l databaseLoad
		l.before, l.err = readDatabaseStats(ctx, db)
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- l
				return
			case <-tk.C:
			}
			var a activity
			err := db.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE a.state = 'active'),
       count(*) FILTER (WHERE a.state = 'idle in transaction'),
       count(*) FILTER (WHERE a.wait_event_type = 'Lock'),
       count(*) FILTER (WHERE a.wait_event_type = 'LWLock'),
       count(*) FILTER (WHERE a.wait_event_type = 'IO'),
       (SELECT count(*) FROM pg_locks l JOIN pg_stat_activity w ON w.pid = l.pid
         WHERE NOT l.granted AND w.datname = current_database())
FROM pg_stat_activity a
WHERE a.datname = current_database() AND a.pid <> pg_backend_pid()`).Scan(&a.active, &a.idleInTransaction, &a.lock,
				&a.lwlock, &a.io, &a.ungranted)
			if err != nil {
				if ctx.Err() == nil {
					l.err = err
				}
				continue
			}
			l.samples++
			l.max = activity{max(l.max.active, a.active), max(l.max.idleInTransaction, a.idleInTransaction),
				max(l.max.lock, a.lock), max(l.max.lwlock, a.lwlock), max(l.max.io, a.io),
				max(l.max.ungranted, a.ungranted)}
			l.sum = activity{l.sum.active + a.active, l.sum.idleInTransaction + a.idleInTransaction,
				l.sum.lock + a.lock, l.sum.lwlock + a.lwlock, l.sum.io + a.io, l.sum.ungranted + a.ungranted}
		}
	}()
	return func() databaseLoad {
		cancel()
		l := <-done
		var err error
		if l.after, err = readDatabaseStats(parent, db); err != nil && l.err == nil {
			l.err = err
		}
		return l
	}
}

func readDatabaseStats(ctx context.Context, db *pgxpool.Pool) (databaseStats, error) {
	var st databaseStats
	err := db.QueryRow(ctx, `
SELECT xact_commit, xact_rollback, deadlocks, blks_read, tup_updated
FROM pg_stat_database WHERE datname = current_database()`).Scan(&st.commits, &st.rollbacks, &st.deadlocks,
		&st.blocksRead, &st.tuplesUpdated)
	return st, err
}

// scope is the run in the database: its Organization and its Route, whose Alert Groups are the run's.
type scope struct {
	db         *pgxpool.Pool
	org, route int64
}

// runGroups is the common table of the run's Alert Groups, for the queries that take the Organization as $1 and the
// Route as $2.
const runGroups = `WITH run_groups AS (SELECT id FROM alert_groups WHERE org_id = $1 AND route_id = $2) `

func lookupScope(ctx context.Context, db *pgxpool.Pool, route string) (scope, error) {
	sc := scope{db: db}
	err := db.QueryRow(ctx, `
SELECT o.id, r.id FROM organizations o JOIN routes r ON r.org_id = o.id AND r.public_id = $1`, route).Scan(&sc.org,
		&sc.route)
	if err != nil {
		return sc, fmt.Errorf("find the Route %s in the database: %w", route, err)
	}
	return sc, nil
}

// paging is how far the run's Alert Groups are: how many there are, have a delivered Root message, have a Page and
// have every chain at its wait, and how many Pages are still pending and Pages are calls.
type paging struct {
	groups, published, paged, waiting, pending, calls int64
	err                                               error
}

func (p paging) String() string {
	if p.err != nil {
		return p.err.Error()
	}
	return fmt.Sprintf("%d Alert Groups, %d with a Root message delivered, %d Paged, %d with the chain at its wait; "+
		"%d Pages pending, %d Pages by call", p.groups, p.published, p.paged, p.waiting, p.pending, p.calls)
}

// settle waits until every one of the groups Alert Groups has its Root message delivered, a Page, and its chain at
// the wait that ends it, and no Page is pending, or until deadline.
func (sc scope) settle(ctx context.Context, groups int, deadline time.Time) paging {
	for {
		var p paging
		p.err = sc.db.QueryRow(ctx, runGroups+`
SELECT (SELECT count(*) FROM run_groups),
       (SELECT count(DISTINCT d.alert_group_id) FROM deliveries d
         WHERE d.org_id = $1 AND d.alert_group_id IN (SELECT id FROM run_groups) AND d.published_at IS NOT NULL),
       (SELECT count(DISTINCT p.alert_group_id) FROM pages p
         WHERE p.org_id = $1 AND p.alert_group_id IN (SELECT id FROM run_groups)),
       (SELECT count(DISTINCT r.alert_group_id) FROM escalation_runs r
         WHERE r.org_id = $1 AND r.alert_group_id IN (SELECT id FROM run_groups) AND r.state = 'running'
           AND r.step_index >= 2),
       (SELECT count(*) FROM pages p
         WHERE p.org_id = $1 AND p.alert_group_id IN (SELECT id FROM run_groups) AND p.state = 'pending'),
       (SELECT count(*) FROM pages p
         WHERE p.org_id = $1 AND p.alert_group_id IN (SELECT id FROM run_groups) AND p.kind = 'call')`,
			sc.org, sc.route).Scan(&p.groups, &p.published, &p.paged, &p.waiting, &p.pending, &p.calls)
		g := int64(groups)
		done := p.err == nil && p.groups >= g && p.published >= g && p.paged >= g && p.waiting >= g && p.pending == 0
		if done || time.Now().After(deadline) || ctx.Err() != nil {
			return p
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// sinceSent is, for each Alert Group the query names by its cluster with a time, how long after the first webhook of
// its group that time came.
func (sc scope) sinceSent(ctx context.Context, sent []time.Time, query string) ([]time.Duration, error) {
	rows, err := sc.db.Query(ctx, query, sc.org, sc.route)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ds []time.Duration
	for rows.Next() {
		var cluster string
		var at time.Time
		if err := rows.Scan(&cluster, &at); err != nil {
			return nil, err
		}
		var g int
		if _, err := fmt.Sscanf(cluster, "c%d", &g); err != nil || g < 0 || g >= len(sent) || sent[g].IsZero() {
			continue
		}
		ds = append(ds, at.Sub(sent[g]))
	}
	return ds, rows.Err()
}

// tally counts the rows of a query that selects a key and a count.
func (sc scope) tally(ctx context.Context, query string) (string, int64, error) {
	rows, err := sc.db.Query(ctx, query, sc.org, sc.route)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	var total int64
	for rows.Next() {
		var key string
		var n int64
		if err := rows.Scan(&key, &n); err != nil {
			return "", 0, err
		}
		counts[key] += n
		total += n
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	parts := []string{}
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	if len(parts) == 0 {
		parts = append(parts, "none")
	}
	return strings.Join(parts, ", "), total, nil
}

// spread is the percentiles of durations and how many there are of want.
func spread(ds []time.Duration, want int) string {
	slices.Sort(ds)
	return fmt.Sprintf("p50 %s, p95 %s, p99 %s, max %s (%d of %d)", seconds(percentile(ds, 0.50).Seconds()),
		seconds(percentile(ds, 0.95).Seconds()), seconds(percentile(ds, 0.99).Seconds()),
		seconds(percentile(ds, 1).Seconds()), len(ds), want)
}

// call is a call through the gateway as its Pages saw it: the person, when its first attempt went and when it ended
// accepted, and its earliest Page.
type call struct {
	user                   int64
	firstPage              time.Time
	firstAttempt, accepted *time.Time
}

// report prints the figures of the l2 scenario and returns why it failed: no Page, or no accepted call.
func (sc scope) report(ctx context.Context, w io.Writer, sent []time.Time, load databaseLoad, during,
	after timings) []string {
	var failures []string
	failed := func(what string, err error) {
		failures = append(failures, fmt.Sprintf("%s: %v", what, err))
	}
	published, err := sc.sinceSent(ctx, sent, runGroups+`
SELECT g.group_key_values->>'cluster', min(d.published_at)
FROM alert_groups g JOIN deliveries d ON d.org_id = $1 AND d.alert_group_id = g.id
WHERE g.org_id = $1 AND g.id IN (SELECT id FROM run_groups) AND d.published_at IS NOT NULL
GROUP BY 1`)
	if err != nil {
		failed("read the Root messages", err)
	}
	fmt.Fprintf(w, "load test: l2 first webhook → Root message delivered %s\n", spread(published, len(sent)))
	paged, err := sc.sinceSent(ctx, sent, runGroups+`
SELECT g.group_key_values->>'cluster', min(p.created_at)
FROM alert_groups g JOIN pages p ON p.org_id = $1 AND p.alert_group_id = g.id
WHERE g.org_id = $1 AND g.id IN (SELECT id FROM run_groups)
GROUP BY 1`)
	if err != nil {
		failed("read the Pages", err)
	}
	fmt.Fprintf(w, "load test: l2 first webhook → first Page %s\n", spread(paged, len(sent)))

	toAttempt, toAccepted, accepted, err := sc.calls(ctx, w)
	if err != nil {
		failed("read the calls", err)
	}
	fmt.Fprintf(w, "load test: l2 Page by call → first attempt of its call %s; → accepted %s\n",
		spread(toAttempt, len(toAttempt)), spread(toAccepted, len(toAttempt)))

	for _, when := range []struct {
		name string
		t    timings
	}{{"during the storm", during}, {"after it", after}} {
		var parts []string
		for _, name := range slices.Sorted(maps.Keys(when.t)) {
			ds := when.t[name]
			slices.Sort(ds)
			parts = append(parts, fmt.Sprintf("%s p50 %s max %s (%d)", name, seconds(percentile(ds, 0.5).Seconds()),
				seconds(percentile(ds, 1).Seconds()), len(ds)))
		}
		fmt.Fprintf(w, "load test: l2 statistics %s: %s\n", when.name, strings.Join(parts, "; "))
	}

	if load.err != nil {
		failed("sample the database", load.err)
	}
	mean := func(sum int64) float64 { return float64(sum) / float64(max(load.samples, 1)) }
	fmt.Fprintf(w, "load test: l2 database, %d samples: active max %d mean %.1f; idle in transaction max %d mean %.1f; "+
		"Lock waits max %d mean %.1f; LWLock waits max %d mean %.1f; IO waits max %d mean %.1f; ungranted locks max %d\n",
		load.samples, load.max.active, mean(load.sum.active), load.max.idleInTransaction,
		mean(load.sum.idleInTransaction), load.max.lock, mean(load.sum.lock), load.max.lwlock, mean(load.sum.lwlock),
		load.max.io, mean(load.sum.io), load.max.ungranted)
	fmt.Fprintf(w, "load test: l2 database counters: xact_commit +%d, xact_rollback +%d, deadlocks +%d, blks_read +%d, "+
		"tup_updated +%d\n", load.after.commits-load.before.commits, load.after.rollbacks-load.before.rollbacks,
		load.after.deadlocks-load.before.deadlocks, load.after.blocksRead-load.before.blocksRead,
		load.after.tuplesUpdated-load.before.tuplesUpdated)

	var pages int64
	for _, c := range []struct {
		name  string
		query string
		total *int64
	}{
		{"escalation runs", `SELECT state, count(*) FROM escalation_runs
WHERE org_id = $1 AND alert_group_id IN (SELECT id FROM run_groups) GROUP BY 1`, nil},
		{"notification rule runs", `SELECT state || ' ' || importance, count(*) FROM notification_rule_runs
WHERE org_id = $1 AND alert_group_id IN (SELECT id FROM run_groups) GROUP BY 1`, nil},
		{"Pages", `SELECT kind || ' ' || state || coalesce(' ' || reason, ''), count(*) FROM pages
WHERE org_id = $1 AND alert_group_id IN (SELECT id FROM run_groups) GROUP BY 1`, &pages},
		{"Thread replies of Pages", `SELECT state, count(*) FROM thread_replies
WHERE org_id = $1 AND alert_group_id IN (SELECT id FROM run_groups) AND page_id IS NOT NULL GROUP BY 1`, nil},
		{"gateway calls", `SELECT c.state, count(*) FROM gateway_calls c
WHERE c.org_id = $1 AND EXISTS (SELECT 1 FROM gateway_call_items i
  WHERE i.org_id = $1 AND i.call_id = c.id AND i.alert_group_id IN (SELECT id FROM run_groups)) GROUP BY 1`, nil},
		{"Assignees", `SELECT 'rows', count(*) FROM alert_group_assignees
WHERE org_id = $1 AND alert_group_id IN (SELECT id FROM run_groups)`, nil},
		{"escalation Timeline entries", `SELECT event, count(*) FROM timeline_entries
WHERE org_id = $1 AND alert_group_id IN (SELECT id FROM run_groups) AND kind = 'escalation' GROUP BY 1`, nil},
	} {
		line, total, err := sc.tally(ctx, runGroups+c.query)
		if err != nil {
			failed("count the "+c.name, err)
			continue
		}
		if c.total != nil {
			*c.total = total
		}
		fmt.Fprintf(w, "load test: l2 %s: %s\n", c.name, line)
	}
	if pages == 0 {
		failures = append(failures, "no Page was made")
	}
	if accepted == 0 {
		failures = append(failures, "no call through the gateway was accepted")
	}
	return failures
}

// calls returns, for each Page by call of the run, how long after it the first attempt of its call went and, for an
// accepted call, how long until it was accepted, with the number of accepted calls; it prints the same for the first
// call of each person, which gateway.combine_window does not hold.
func (sc scope) calls(ctx context.Context, w io.Writer) (toAttempt, toAccepted []time.Duration, accepted int,
	err error) {
	rows, err := sc.db.Query(ctx, runGroups+`
SELECT p.created_at, c.id, c.user_id, c.first_attempt_at, CASE WHEN c.state = 'accepted' THEN c.ended_at END
FROM pages p JOIN gateway_calls c ON c.org_id = $1 AND c.id = p.gateway_call_id
WHERE p.org_id = $1 AND p.kind = 'call' AND p.alert_group_id IN (SELECT id FROM run_groups)`, sc.org, sc.route)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	calls := map[int64]*call{}
	for rows.Next() {
		var at time.Time
		var id int64
		var c call
		if err := rows.Scan(&at, &id, &c.user, &c.firstAttempt, &c.accepted); err != nil {
			return nil, nil, 0, err
		}
		if c.firstAttempt != nil {
			toAttempt = append(toAttempt, c.firstAttempt.Sub(at))
		}
		if c.accepted != nil {
			toAccepted = append(toAccepted, c.accepted.Sub(at))
		}
		if known, ok := calls[id]; ok {
			if at.Before(known.firstPage) {
				known.firstPage = at
			}
			continue
		}
		c.firstPage = at
		calls[id] = &c
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	first := map[int64]*call{}
	for _, c := range calls {
		if c.accepted != nil {
			accepted++
		}
		if c.firstAttempt == nil {
			continue
		}
		if known, ok := first[c.user]; !ok || c.firstAttempt.Before(*known.firstAttempt) {
			first[c.user] = c
		}
	}
	var firstAttempt, firstAccepted []time.Duration
	for _, c := range first {
		firstAttempt = append(firstAttempt, c.firstAttempt.Sub(c.firstPage))
		if c.accepted != nil {
			firstAccepted = append(firstAccepted, c.accepted.Sub(c.firstPage))
		}
	}
	fmt.Fprintf(w, "load test: l2 first call of each person: Page → first attempt %s; → accepted %s. %d calls, %d "+
		"accepted; gateway.combine_window (%v) holds a person's later Pages until that long after their previous call "+
		"ended, so the figures for every Page by call below include that hold\n", spread(firstAttempt, len(first)),
		spread(firstAccepted, len(first)), len(calls), accepted, combineWindow)
	return toAttempt, toAccepted, accepted, nil
}
