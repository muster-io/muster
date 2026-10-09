// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/telegram"
)

// The kinds of a Finding.
const (
	FindingConnection  = "connection"
	FindingDestination = "destination"
)

// doctorPage is the size of the pages of Connections muster doctor reads.
const doctorPage = 1000

// errNoKeyring fails the checks of muster doctor when the master keys could not be loaded.
var errNoKeyring = errors.New("the bot token cannot be opened: the master keys could not be loaded")

// checkFailed is the message of a failed check whose answer said nothing.
const checkFailed = "the check failed"

// Finding is the outcome of muster doctor's check of a Connection or a Destination: its kind, its name, when the
// check failed, the message of the step that failed, and when it passed with a hint, the hint.
type Finding struct {
	Kind    string
	Name    string
	Message string
	Warning string
}

// OK reports whether the check passed.
func (f Finding) OK() bool { return f.Message == "" }

// DoctorQueries are the reads of muster doctor's checks; *dbgen.Queries implements them.
type DoctorQueries interface {
	ListConnections(ctx context.Context, arg dbgen.ListConnectionsParams) ([]dbgen.ListConnectionsRow, error)
	ListMattermostDestinations(ctx context.Context, orgID int64) ([]dbgen.ListMattermostDestinationsRow, error)
	ListTelegramDestinations(ctx context.Context, orgID int64) ([]dbgen.ListTelegramDestinationsRow, error)
}

// Doctor runs the checks of muster doctor (C-02.FR-14) over the Connections and the Mattermost and Telegram
// Destinations that are not deleted, read through q: the Connection check of a Mattermost Connection (GET
// /api/v4/users/me) and the steps of a Telegram one (the dry probe, getMe, getWebhookInfo), and the Destination checks
// of mattermost.CheckDestination and telegram.CheckDestination, each request made at once in the background client
// class, whose retries each check bounds by each. A Telegram Connection that fails names the failing step with its
// message. It only reads: it records nothing of what it finds. It needs the Keyring and the Network of the Service
// only; without a Keyring every check fails, for the bot tokens cannot be opened.
func (s *Service) Doctor(ctx context.Context, q DoctorQueries, each time.Duration) ([]Finding, error) {
	var out []Finding
	clients := map[int64]*mattermost.Client{}
	tgClients := map[int64]*telegram.Client{}
	failed := map[int64]string{}
	params := dbgen.ListConnectionsParams{OrgID: s.cfg.OrgID, PageSize: doctorPage}
	for {
		rows, err := q.ListConnections(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list the connections: %w", err)
		}
		for _, r := range rows {
			f := Finding{Kind: FindingConnection, Name: r.Name}
			if r.Type == TypeTelegram {
				var c *telegram.Client
				c, f.Message = s.doctorTelegram(ctx, dbgen.GetConnectionRow(r), each)
				if f.OK() {
					tgClients[r.ID] = c
				} else {
					failed[r.ID] = f.Message
				}
				out = append(out, f)
				continue
			}
			c, err := s.doctorClient(dbgen.GetConnectionRow(r))
			if err == nil {
				f.Message, f.Warning = connectionCheck(ctx, c, each)
			} else {
				f.Message = err.Error()
			}
			if f.OK() {
				clients[r.ID] = c
			} else {
				failed[r.ID] = f.Message
			}
			out = append(out, f)
		}
		if len(rows) < doctorPage {
			break
		}
		params.AfterID = nullInt(&rows[len(rows)-1].ID)
	}
	dests, err := q.ListMattermostDestinations(ctx, s.cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list the destinations: %w", err)
	}
	for _, d := range dests {
		f := Finding{Kind: FindingDestination, Name: d.Name}
		c, ok := clients[d.ConnectionID.Int64]
		switch {
		case ok:
			checkCtx, cancel := context.WithTimeout(ctx, each)
			res, _ := mattermost.CheckDestination(checkCtx, c, mattermost.Direct(delivery.Call{
				Class: outbound.ClassBackground}), d.MattermostTeamID.String, d.MattermostChannelID.String)
			cancel()
			if !res.OK() {
				f.Message = cmp.Or(res.Steps[len(res.Steps)-1].Message, checkFailed)
			}
		case failed[d.ConnectionID.Int64] != "":
			f.Message = "its Connection failed its check: " + failed[d.ConnectionID.Int64]
		default:
			f.Message = "its Connection is deleted"
		}
		out = append(out, f)
	}
	tgDests, err := q.ListTelegramDestinations(ctx, s.cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list the telegram destinations: %w", err)
	}
	for _, d := range tgDests {
		f := Finding{Kind: FindingDestination, Name: d.Name}
		c, ok := tgClients[d.ConnectionID.Int64]
		switch {
		case ok:
			checkCtx, cancel := context.WithTimeout(ctx, each)
			res, err := telegram.CheckDestination(checkCtx, c, telegram.Direct(delivery.Call{
				Class: outbound.ClassBackground}), d.TelegramChannelID.String)
			cancel()
			switch {
			case err != nil:
				f.Message = err.Error()
			case !res.OK():
				st := res.Steps[len(res.Steps)-1]
				f.Message = st.Name + ": " + cmp.Or(st.Message, checkFailed)
			}
		case failed[d.ConnectionID.Int64] != "":
			f.Message = "its Connection failed its check: " + failed[d.ConnectionID.Int64]
		default:
			f.Message = "its Connection is deleted"
		}
		out = append(out, f)
	}
	return out, nil
}

// doctorClient is the client of the Connection row for muster doctor.
func (s *Service) doctorClient(row dbgen.GetConnectionRow) (*mattermost.Client, error) {
	if s.cfg.Keyring == nil {
		return nil, errNoKeyring
	}
	return s.client(row)
}

// doctorTelegram runs the steps of the check of the Telegram Connection row in the background class, bounded by each:
// the client and an empty message when every step passed, otherwise the failing step with its message.
func (s *Service) doctorTelegram(ctx context.Context, row dbgen.GetConnectionRow, each time.Duration) (
	*telegram.Client, string) {
	if s.cfg.Keyring == nil {
		return nil, errNoKeyring.Error()
	}
	c, err := s.telegramClient(row)
	if err != nil {
		return nil, err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, each)
	defer cancel()
	res, err := telegram.RunCheck(ctx, c, nil, telegram.Direct(delivery.Call{Class: outbound.ClassBackground}),
		s.cfg.Clocks.Real)
	if err != nil {
		return nil, err.Error()
	}
	if st, failed := res.Failed(); failed {
		return nil, st.Name + ": " + cmp.Or(st.Message, checkFailed)
	}
	return c, ""
}

// connectionCheck is the Connection check in the background class, bounded by each: an empty message when the bot
// token works, otherwise the message of the failure; and the hint mattermost.HintPressAnswersInThread when the token
// works but the bot's roles do not let it make ephemeral posts (D284).
func connectionCheck(ctx context.Context, c *mattermost.Client, each time.Duration) (message, warning string) {
	ctx, cancel := context.WithTimeout(ctx, each)
	defer cancel()
	u, r := c.Me(ctx, outbound.ClassBackground)
	switch {
	case r.OK():
		if granted, r := c.Grants(ctx, outbound.ClassBackground, u, mattermost.PermissionEphemeralPosts); r.OK() &&
			!granted {
			return "", mattermost.HintPressAnswersInThread
		}
		return "", ""
	case r.Status == http.StatusUnauthorized:
		return mattermost.MessageTokenInvalid, ""
	default:
		return cmp.Or(string(r.Outcome.Error), checkFailed), ""
	}
}
