// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package faketelegram is the fake Telegram Bot API server: getMe answers its bot, every other method is recorded and
// answered 501.
package faketelegram

import (
	"net/http"
	"strings"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

const (
	BotID       = 123456
	BotUsername = "muster_dev_bot"
)

type Fake struct {
	*fakeserver.Server
}

func New() *Fake {
	return &Fake{Server: fakeserver.New("Telegram", http.HandlerFunc(serve))}
}

type bot struct {
	ID                      int64  `json:"id"`
	IsBot                   bool   `json:"is_bot"`
	FirstName               string `json:"first_name"`
	Username                string `json:"username"`
	CanJoinGroups           bool   `json:"can_join_groups"`
	CanReadAllGroupMessages bool   `json:"can_read_all_group_messages"`
	SupportsInlineQueries   bool   `json:"supports_inline_queries"`
}

type answer struct {
	OK          bool   `json:"ok"`
	Result      any    `json:"result,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
}

// serve answers /bot<token>/<method>, GET or POST, like the Bot API: method names are compared case-insensitively.
func serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	token, method, found := strings.Cut(rest, "/")
	if !ok || !found || token == "" || method == "" || strings.Contains(method, "/") ||
		(r.Method != http.MethodGet && r.Method != http.MethodPost) {
		fakeserver.WriteJSON(w, http.StatusNotFound, answer{ErrorCode: http.StatusNotFound, Description: "Not Found"})
		return
	}
	if strings.EqualFold(method, "getMe") {
		fakeserver.WriteJSON(w, http.StatusOK, answer{OK: true, Result: bot{
			ID:            BotID,
			IsBot:         true,
			FirstName:     "Muster Dev",
			Username:      BotUsername,
			CanJoinGroups: true,
		}})
		return
	}
	fakeserver.WriteJSON(w, http.StatusNotImplemented, answer{
		ErrorCode:   http.StatusNotImplemented,
		Description: "Not Implemented: method " + method + " is not implemented by the fake Telegram server",
	})
}
