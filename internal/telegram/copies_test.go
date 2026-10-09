// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// learner records the copies learned; err answers every one when set.
type learner struct {
	mu     sync.Mutex
	copies []delivery.Copy
	err    error
}

func (l *learner) LearnCopy(_ context.Context, c delivery.Copy) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.copies = append(l.copies, c)
	return nil
}

func (l *learner) learned() []delivery.Copy {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]delivery.Copy(nil), l.copies...)
}

const (
	copyJSON = `{"message_id":31,"from":{"id":777000,"is_bot":false,"first_name":"Telegram"},
		"sender_chat":{"id":-1001000000001,"type":"channel","title":"Muster alerts"},
		"chat":{"id":-1001000000002,"type":"supergroup","title":"Muster alerts Chat"},"date":1791549475,
		"edit_date":1791549475,"is_automatic_forward":true,"text":"post",
		"forward_origin":{"type":"channel","chat":{"id":-1001000000001,"type":"channel"},"message_id":7,"date":1791549474}}`
	commentJSON = `{"message_id":32,"from":{"id":7001,"is_bot":false,"first_name":"Ann"},
		"chat":{"id":-1001000000002,"type":"supergroup"},"date":1791549480,"message_thread_id":31,"text":"looking",
		"reply_to_message":` + copyJSON + `}`
)

// TestCopyOf is C-14.FR-3 and C-14.AC-15: the automatic copy, even with its edit_date, and a person's comment under it
// teach the copy; its edited_message, a channel post, a plain group message, a reply to another message, a forward from
// a person, a reply into another chat and a private message teach nothing.
func TestCopyOf(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want *delivery.Copy
	}{
		{"automatic copy", `{"update_id":1,"message":` + copyJSON + `}`, &delivery.Copy{ChannelChatID: -1001000000001,
			PostID: 7, DiscussionChatID: -1001000000002, CopyID: 31, LearnedFrom: delivery.LearnedFromAutomaticForward}},
		{"comment", `{"update_id":2,"message":` + commentJSON + `}`, &delivery.Copy{ChannelChatID: -1001000000001,
			PostID: 7, DiscussionChatID: -1001000000002, CopyID: 31, LearnedFrom: delivery.LearnedFromComment}},
		{"edited copy", `{"update_id":3,"edited_message":` + copyJSON + `}`, nil},
		{"channel post", `{"update_id":4,"channel_post":{"message_id":7,"chat":{"id":-1001000000001,"type":"channel"},
			"text":"by a person"}}`, nil},
		{"plain group message", `{"update_id":5,"message":{"message_id":40,"chat":{"id":-1001000000002,
			"type":"supergroup"},"text":"hi"}}`, nil},
		{"reply to another message", `{"update_id":6,"message":{"message_id":41,"chat":{"id":-1001000000002,
			"type":"supergroup"},"reply_to_message":{"message_id":40,"chat":{"id":-1001000000002,"type":"supergroup"}}}}`,
			nil},
		{"forward from a person", `{"update_id":7,"message":{"message_id":42,"chat":{"id":-1001000000002,
			"type":"supergroup"},"is_automatic_forward":true,"forward_origin":{"type":"user","message_id":3}}}`, nil},
		{"copy without a post", `{"update_id":8,"message":{"message_id":43,"chat":{"id":-1001000000002,
			"type":"supergroup"},"is_automatic_forward":true,"forward_origin":{"type":"channel",
			"chat":{"id":-1001000000001,"type":"channel"}}}}`, nil},
		{"reply into another chat", `{"update_id":9,"message":{"message_id":44,"chat":{"id":-1001000000009,
			"type":"supergroup"},"reply_to_message":` + copyJSON + `}}`, nil},
		{"group without an id", `{"update_id":11,"message":{"message_id":46,"chat":{"type":"supergroup"},
			"is_automatic_forward":true,"forward_origin":{"type":"channel","chat":{"id":-1001000000001},"message_id":7}}}`,
			nil},
		{"private message", `{"update_id":10,"message":{"message_id":45,"chat":{"id":7001,"type":"private"},
			"is_automatic_forward":true,"forward_origin":{"type":"channel","chat":{"id":-1001000000001},"message_id":7}}}`,
			nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := ParseUpdate([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			l := &learner{}
			if err := (Copies{Learner: l}).Handle(t.Context(), Conn{ID: 5, PublicID: "CN0000000000T1"}, u); err != nil {
				t.Fatal(err)
			}
			got := l.learned()
			switch {
			case tc.want == nil && len(got) != 0:
				t.Fatalf("learned %+v", got)
			case tc.want != nil && (len(got) != 1 || got[0] != withConnection(*tc.want)):
				t.Fatalf("learned %+v, want %+v", got, *tc.want)
			}
		})
	}
	// An update whose raw form does not parse teaches nothing.
	if _, ok := copyOf(Update{Message: &Message{}, Raw: []byte("{")}); ok {
		t.Error("a broken update taught a copy")
	}
}

func withConnection(c delivery.Copy) delivery.Copy {
	c.ConnectionID = 5
	return c
}

// TestCopyLearnerFails: a failure of delivery leaves the update unconfirmed, to come again; a copy delivery refuses for
// good is dropped, so that it does not hold back the later updates.
func TestCopyLearnerFails(t *testing.T) {
	u, err := ParseUpdate([]byte(`{"update_id":1,"message":` + copyJSON + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (Copies{Learner: &learner{err: fmt.Errorf("refused: %w", delivery.ErrBadCopy)}}).Handle(t.Context(),
		Conn{ID: 5}, u); err != nil {
		t.Fatalf("a copy that can never be learned = %v", err)
	}
	boom := errors.New("boom")
	err = Copies{Learner: &learner{err: boom}}.Handle(t.Context(), Conn{ID: 5}, u)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "post 7") {
		t.Fatalf("Handle = %v", err)
	}
}

// TestCopiesThroughTheRouter is C-14.FR-3 and C-14.AC-15 against the fake server: the copy of a post that the adapter
// published, and a person's comment under it, reach delivery through the router; the edited_message of the copy that
// an edit of the post makes does not, and no chat message is dropped as unhandled.
func TestCopiesThroughTheRouter(t *testing.T) {
	e := newAdapter(t)
	c := e.targets.byID[1].Client
	l := &learner{}
	var log strings.Builder
	r := &Router{Offsets: &memOffsets{}, Log: logging.New(&log, logging.LevelInfo)}
	r.Handle(KindChatMessage, Copies{Learner: l}.Handle)
	conn := Conn{ID: 5, PublicID: "CN0000000000T1", Client: c}
	var offset *int64
	poll := func() int {
		t.Helper()
		us, res := c.GetUpdates(t.Context(), offset, 0)
		if !res.OK() {
			t.Fatalf("getUpdates %+v", res)
		}
		for _, u := range us {
			if _, err := r.Route(t.Context(), conn, u); err != nil {
				t.Fatal(err)
			}
			next := u.UpdateID + 1
			offset = &next
		}
		return len(us)
	}
	o := e.adapter.Publish(t.Context(), call(1), root(messages.ColourFiring))
	if n := poll(); n != 1 || o.Kind != delivery.OutcomeOK {
		t.Fatalf("%d updates after the post %+v", n, o)
	}
	cp := e.fake.Messages(faketelegram.GroupID)[0]
	want := delivery.Copy{ConnectionID: 5, ChannelChatID: faketelegram.ChannelID, PostID: 1,
		DiscussionChatID: faketelegram.GroupID, CopyID: cp.ID, LearnedFrom: delivery.LearnedFromAutomaticForward}
	if got := l.learned(); len(got) != 1 || got[0] != want {
		t.Fatalf("learned %+v", got)
	}
	if u := e.adapter.Update(t.Context(), call(1), o.MessageID, root(messages.ColourAcknowledged)); u.Kind !=
		delivery.OutcomeOK {
		t.Fatal(u)
	}
	if n := poll(); n != 1 || len(l.learned()) != 1 {
		t.Fatalf("the edited copy: %d updates, learned %+v", n, l.learned())
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.fake.URL()+"/_fake/comment",
		strings.NewReader(fmt.Sprintf(`{"post_id":1,"from":{"id":7001},"text":"looking","token":%q}`, testToken)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("comment %v %v", resp, err)
	}
	_ = resp.Body.Close()
	if n := poll(); n != 1 {
		t.Fatalf("%d updates after the comment", n)
	}
	want.LearnedFrom = delivery.LearnedFromComment
	if got := l.learned(); len(got) != 2 || got[1] != want {
		t.Fatalf("learned %+v", got)
	}
	if strings.Contains(log.String(), "telegram_update_dropped") {
		t.Errorf("a chat message was dropped:\n%s", log.String())
	}
}
