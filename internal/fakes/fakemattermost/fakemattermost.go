// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakemattermost is the fake Mattermost server: REST API v4 answers for its bot user, every other call is
// recorded and answered 501.
package fakemattermost

import (
	"net/http"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

const (
	BotUserID   = "musterdevbotuserfake000000"
	BotUsername = "muster-dev-bot"
)

type Fake struct {
	*fakeserver.Server
}

func New() *Fake {
	return &Fake{Server: fakeserver.New("Mattermost", http.HandlerFunc(serve))}
}

type user struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Roles     string `json:"roles"`
}

// appError is the shape of Mattermost's error answers.
type appError struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	StatusCode int    `json:"status_code"`
}

func serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/v4/users/me" {
		fakeserver.WriteJSON(w, http.StatusOK, user{
			ID:       BotUserID,
			Username: BotUsername,
			IsBot:    true,
			Roles:    "system_user",
		})
		return
	}
	fakeserver.WriteJSON(w, http.StatusNotImplemented, appError{
		ID:         "fake.not_implemented",
		Message:    "not implemented by the fake Mattermost server",
		StatusCode: http.StatusNotImplemented,
	})
}
