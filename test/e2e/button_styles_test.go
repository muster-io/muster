// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// mmStyles are the actions of the attachment of a post, each its id and its style: "-" for an action without a style
// field, the JSON text for a style that is not a string or is empty.
func mmStyles(t *testing.T, p mmPost) string {
	t.Helper()
	var out []string
	for _, a := range p.attachment(t).Actions {
		style := "-"
		if a.Style != nil {
			var s string
			if json.Unmarshal(a.Style, &s) != nil || s == "" {
				s = string(a.Style)
			}
			style = s
		}
		out = append(out, a.ID+":"+style)
	}
	return strings.Join(out, " ")
}

// tgStyles are the buttons of a keyboard row by row, each its text and its style, "-" for a button without a style
// field.
func tgStyles(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var kb struct {
		InlineKeyboard [][]map[string]any `json:"inline_keyboard"`
	}
	if err := json.Unmarshal(raw, &kb); err != nil {
		t.Fatalf("keyboard %s: %v", raw, err)
	}
	var rows []string
	for _, row := range kb.InlineKeyboard {
		var bs []string
		for _, b := range row {
			style, ok := b["style"]
			if !ok {
				style = "-"
			}
			bs = append(bs, fmt.Sprintf("%v:%v", b["text"], style))
		}
		rows = append(rows, "["+strings.Join(bs, ", ")+"]")
	}
	return strings.Join(rows, " ")
}

// tgPostStyles are the styles of the keyboard of the channel post id.
func (e *timersEnv) tgPostStyles(id int64) string {
	e.t.Helper()
	for _, m := range e.messages(faketelegram.ChannelID) {
		if m.ID == id {
			return tgStyles(e.t, m.ReplyMarkup)
		}
	}
	return ""
}

// waitStyles waits until styles gives want, logging each value it gives on the way.
func waitStyles(t *testing.T, what, want string, styles func() string) {
	t.Helper()
	last := ""
	eventually(t, what+" styled "+want, func() bool {
		got := styles()
		if got != last {
			t.Logf("%s: %s", what, got)
			last = got
		}
		return got == want
	})
}

// TestButtonStyles is the Verification of S-082 against `muster dev`: in the fake Mattermost and Telegram servers a new
// Alert Group's Root message has Ack primary, Resolve success and plain Snooze buttons; acknowledged, Unack is plain
// and Resolve success; a snoozed Alert Group has Ack primary, Unsnooze plain and Resolve success; a Reminder has
// "Still on it" primary and Unack plain (D329, F-066, F-068); and the fake Telegram server refuses an unknown style
// (F-067).
func TestButtonStyles(t *testing.T) {
	h := Start(t, DevProcess)
	e := reminderEnv(t, h, h.Replicas[0])
	const snooze = " snooze0:- snooze1:- snooze2:-"
	const tgSnooze = " [Snooze 1 h:-, Snooze 4 h:-, Snooze 24 h:-]"

	g := e.fire("s1", "HostDown", "t", true)
	n, post := e.number(g), e.rootPost(g)
	mmRoot := func() string { return mmStyles(t, e.root(n)) }
	tgRoot := func() string { return e.tgPostStyles(post) }
	waitStyles(t, "the mattermost root message", "ack:primary resolve:success"+snooze, mmRoot)
	waitStyles(t, "the telegram root message", "[Ack:primary, Resolve:success]"+tgSnooze, tgRoot)

	e.ack(e.bob, g)
	waitStyles(t, "the acknowledged mattermost root message", "unack:- resolve:success"+snooze, mmRoot)
	waitStyles(t, "the acknowledged telegram root message", "[Unack:-, Resolve:success]"+tgSnooze, tgRoot)

	g2 := e.fire("s2", "DiskFull", "t", true)
	n2, post2 := e.number(g2), e.rootPost(g2)
	e.cmd(e.bob, g2, "snooze", `{"no_end":true}`)
	waitStyles(t, "the snoozed mattermost root message", "ack:primary unsnooze:- resolve:success",
		func() string { return mmStyles(t, e.root(n2)) })
	waitStyles(t, "the snoozed telegram root message", "[Ack:primary, Unsnooze:-, Resolve:success]",
		func() string { return e.tgPostStyles(post2) })

	e.adv(14400)
	rp, _ := e.reminderPost(g)
	if got := mmStyles(t, rp); got != "stillonit:primary unack:-" {
		t.Errorf("the mattermost reminder: %s", got)
	}
	if got := tgStyles(t, e.tgMessage(e.tgReminder(g)).ReplyMarkup); got != "[Still on it:primary, Unack:-]" {
		t.Errorf("the telegram reminder: %s", got)
	}

	// The fake Telegram server refuses a style the Bot API does not know, as Telegram does (F-067).
	a := call(t, http.MethodPost, h.Fakes.Telegram+"/bot"+devmode.TelegramConnectionBotToken+"/sendMessage",
		fmt.Sprintf(`{"chat_id":%d,"text":"x","reply_markup":{"inline_keyboard":[[{"text":"a","callback_data":"a",`+
			`"style":"neutral"}]]}}`, faketelegram.ChannelID), "Content-Type", "application/json")
	if a.status != http.StatusBadRequest || !strings.Contains(string(a.body), faketelegram.DescriptionButtonStyle) {
		t.Errorf("an unknown style = %d %s", a.status, a.body)
	}
}
