// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakemattermost

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// Post is a post as the API shows it.
type Post struct {
	ID        string          `json:"id"`
	CreateAt  int64           `json:"create_at"`
	UpdateAt  int64           `json:"update_at"`
	EditAt    int64           `json:"edit_at"`
	DeleteAt  int64           `json:"delete_at"`
	UserID    string          `json:"user_id"`
	ChannelID string          `json:"channel_id"`
	RootID    string          `json:"root_id"`
	Message   string          `json:"message"`
	Type      string          `json:"type"`
	Props     json.RawMessage `json:"props"`
	// ReplyCount is the number of replies in the Thread of a root post (F-027).
	ReplyCount int64 `json:"reply_count"`
}

// RecordedPost is a post as GET /_fake/posts lists it, with the users who follow its Thread.
type RecordedPost struct {
	Post
	Followers []string `json:"followers"`
}

type post struct {
	Post
	followers []string
}

func (p *post) follow(userID string) {
	if !slices.Contains(p.followers, userID) {
		p.followers = append(p.followers, userID)
	}
}

// The kinds of notifications.
const (
	KindMention = "mention"
	KindChannel = "channel"
	KindAll     = "all"
	KindHere    = "here"
	KindDirect  = "direct"
)

// Notification is what a person is notified of, as GET /_fake/notifications lists it. Preview is the text the
// notification shows: the post's message, empty when only an attachment mentions the person (F-056).
type Notification struct {
	AtMs      int64  `json:"at_ms"`
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	PostID    string `json:"post_id"`
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	Kind      string `json:"kind"`
	Preview   string `json:"preview"`
}

// Where an ephemeral post shows: in the channel view, or only in the open Thread (F-025, F-026).
const (
	ShownInChannel = "channel"
	ShownInThread  = "thread"
)

// Ephemeral is an ephemeral post shown to one person, as GET /_fake/ephemeral lists it. From is the bot's username
// for a post through the API and "System" for the ephemeral_text of a press's answer.
type Ephemeral struct {
	AtMs      int64  `json:"at_ms"`
	PostID    string `json:"post_id"`
	UserID    string `json:"user_id"`
	From      string `json:"from"`
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	Message   string `json:"message"`
	ShownIn   string `json:"shown_in"`
}

// Posts returns every post in the order it was created, deleted ones and direct messages included.
func (f *Fake) Posts() []RecordedPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RecordedPost, 0, len(f.st.order))
	for _, id := range f.st.order {
		p := f.st.posts[id]
		out = append(out, RecordedPost{Post: p.Post, Followers: append([]string{}, p.followers...)})
	}
	return out
}

// Notifications returns the notifications in the order they were sent.
func (f *Fake) Notifications() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notification{}, f.st.notifications...)
}

// Ephemeral returns the ephemeral posts in the order they were made.
func (f *Fake) Ephemeral() []Ephemeral {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Ephemeral{}, f.st.ephemeral...)
}

type attachment struct {
	Pretext string `json:"pretext"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	Fields  []struct {
		Value any `json:"value"`
	} `json:"fields"`
	Actions []action `json:"actions"`
}

type action struct {
	ID          string `json:"id"`
	Integration struct {
		URL     string          `json:"url"`
		Context json.RawMessage `json:"context"`
	} `json:"integration"`
}

// attachments returns the attachments of a post's props; props that do not parse have none.
func attachments(props json.RawMessage) []attachment {
	var p struct {
		Attachments []attachment `json:"attachments"`
	}
	if json.Unmarshal(props, &p) != nil {
		return nil
	}
	return p.Attachments
}

// objectProps returns props as a JSON object, {} when they are missing or null, and false when they are not an object.
func objectProps(props json.RawMessage) (json.RawMessage, bool) {
	props = bytes.TrimSpace(props)
	if len(props) == 0 || string(props) == "null" {
		return json.RawMessage(`{}`), true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(props, &m) != nil {
		return nil, false
	}
	return slices.Clone(props), true
}

func messageTooLong(w http.ResponseWriter, msg string) bool {
	if utf8.RuneCountInString(msg) <= MaxPostSize {
		return false
	}
	writeAppError(w, http.StatusBadRequest, "model.post.is_valid.message_length.app_error",
		"The post message is longer than the limit of 16383 characters.")
	return true
}

func postNotFound(w http.ResponseWriter) {
	writeAppError(w, http.StatusNotFound, "app.post.get.app_error", "post not found")
}

// handleCreatePost checks the channel, the bot's membership, the root post and the length, in this order, then
// creates the post and sends its notifications.
func (f *Fake) handleCreatePost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChannelID string          `json:"channel_id"`
		Message   string          `json:"message"`
		RootID    string          `json:"root_id"`
		Props     json.RawMessage `json:"props"`
	}
	if err := decodeBody(w, r, &in); err != nil {
		invalidBody(w, "post")
		return
	}
	props, ok := objectProps(in.Props)
	if !ok {
		invalidBody(w, "post")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.st.channels[in.ChannelID]
	if !ok || c.DeleteAt != 0 {
		unknownChannel(w, in.ChannelID)
		return
	}
	if !f.st.members[c.ID][BotUserID] {
		forbidden(w)
		return
	}
	var root *post
	if in.RootID != "" {
		root = f.st.posts[in.RootID]
		// A deleted root post is refused like an unknown one (F-058).
		if root == nil || root.DeleteAt != 0 || root.ChannelID != c.ID || root.RootID != "" {
			writeAppError(w, http.StatusBadRequest, "api.post.create_post.root_id.app_error", "Invalid RootId parameter.")
			return
		}
	}
	if messageTooLong(w, in.Message) {
		return
	}
	now := f.nowMs()
	p := &post{Post: Post{
		ID:        newID(),
		CreateAt:  now,
		UpdateAt:  now,
		UserID:    BotUserID,
		ChannelID: c.ID,
		RootID:    in.RootID,
		Message:   in.Message,
		Props:     props,
	}}
	f.st.posts[p.ID] = p
	f.st.order = append(f.st.order, p.ID)
	thread := p
	if root != nil {
		root.ReplyCount++
		root.follow(root.UserID)
		root.follow(p.UserID)
		thread = root
	}
	f.notifyLocked(p, c, thread)
	fakeserver.WriteJSON(w, http.StatusCreated, p.Post)
}

// mentionPattern finds @-words; the character before the @ must not belong to a username, so addresses do not count.
var mentionPattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9._-])@([a-z0-9._-]+)`)

var kindRank = map[string]int{KindMention: 3, KindDirect: 2, KindChannel: 1, KindAll: 1, KindHere: 1}

// notifyLocked records the notifications of a new post: the post's message and the texts of its attachments are
// searched for Mentions (F-056), a Mention makes the user a follower of the Thread (F-027), and everyone else in a
// direct channel is notified of every post (F-028). Each person gets one notification, of the strongest kind.
// f.mu is held.
func (f *Fake) notifyLocked(p *post, c *Channel, thread *post) {
	members := f.st.members[c.ID]
	kinds := map[string]string{}
	notify := func(userID, kind string) {
		if userID != p.UserID && members[userID] && kindRank[kind] > kindRank[kinds[userID]] {
			kinds[userID] = kind
		}
	}
	if c.Type == "D" {
		for id := range members {
			notify(id, KindDirect)
		}
	}
	for _, text := range postTexts(p) {
		for _, m := range mentionPattern.FindAllStringSubmatch(text, -1) {
			word := strings.ToLower(m[1])
			// A username can end in a dot, a dash or an underscore, which also end sentences.
			for _, w := range []string{word, strings.TrimRight(word, "._-")} {
				if w == KindChannel || w == KindAll || w == KindHere {
					for id := range members {
						notify(id, w)
					}
					break
				}
				if u, ok := userByName(w); ok {
					if u.ID != p.UserID && members[u.ID] {
						notify(u.ID, KindMention)
						thread.follow(u.ID)
					}
					break
				}
			}
		}
	}
	for _, u := range users {
		kind, ok := kinds[u.ID]
		if !ok {
			continue
		}
		f.st.notifications = append(f.st.notifications, Notification{
			AtMs:      p.CreateAt,
			UserID:    u.ID,
			Username:  u.Username,
			PostID:    p.ID,
			ChannelID: p.ChannelID,
			RootID:    p.RootID,
			Kind:      kind,
			Preview:   p.Message,
		})
	}
}

// postTexts are the texts of a post that can mention people: its message and the texts of its attachments.
func postTexts(p *post) []string {
	texts := []string{p.Message}
	for _, a := range attachments(p.Props) {
		texts = append(texts, a.Pretext, a.Title, a.Text)
		for _, fl := range a.Fields {
			if s, ok := fl.Value.(string); ok {
				texts = append(texts, s)
			}
		}
	}
	return texts
}

// handlePatchPost edits a post; an edit never notifies, even when it adds a Mention (F-029), and posts can be edited at
// any age (F-032).
func (f *Fake) handlePatchPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message *string          `json:"message"`
		Props   *json.RawMessage `json:"props"`
	}
	if err := decodeBody(w, r, &in); err != nil {
		invalidBody(w, "patch")
		return
	}
	var props json.RawMessage
	if in.Props != nil {
		var ok bool
		if props, ok = objectProps(*in.Props); !ok {
			invalidBody(w, "patch")
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.st.posts[r.PathValue("post_id")]
	switch {
	case !ok:
		postNotFound(w)
		return
	// An edit of a deleted post is refused as a missing permission, not as an unknown post (F-058).
	case p.DeleteAt != 0, !f.st.members[p.ChannelID][BotUserID]:
		forbidden(w)
		return
	}
	if in.Message != nil {
		if messageTooLong(w, *in.Message) {
			return
		}
		p.Message = *in.Message
	}
	if props != nil {
		p.Props = props
	}
	p.EditAt = f.nowMs()
	p.UpdateAt = p.EditAt
	fakeserver.WriteJSON(w, http.StatusOK, p.Post)
}

// handleGetPost answers 404 for a deleted post unless include_deleted is true (F-058).
func (f *Fake) handleGetPost(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.st.posts[r.PathValue("post_id")]
	if !ok || (p.DeleteAt != 0 && r.URL.Query().Get("include_deleted") != "true") {
		postNotFound(w)
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, p.Post)
}

// handleEphemeral shows a post to one person: in the channel view without a root_id (F-026), only in the Thread with
// one (F-025). It needs the create_post_ephemeral permission, which only the system admin role has by default: a bot
// with the role Member gets 403, after the body is read and before the channel is (F-063).
func (f *Fake) handleEphemeral(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID string `json:"user_id"`
		Post   struct {
			ChannelID string `json:"channel_id"`
			Message   string `json:"message"`
			RootID    string `json:"root_id"`
		} `json:"post"`
	}
	if err := decodeBody(w, r, &in); err != nil {
		invalidBody(w, "post")
		return
	}
	if _, ok := userByID(in.UserID); !ok {
		invalidBody(w, "user_id")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.st.config.BotSystemAdmin {
		forbidden(w)
		return
	}
	c, ok := f.st.channels[in.Post.ChannelID]
	if !ok || c.DeleteAt != 0 {
		unknownChannel(w, in.Post.ChannelID)
		return
	}
	p := Post{
		ID:        newID(),
		CreateAt:  f.nowMs(),
		UserID:    BotUserID,
		ChannelID: c.ID,
		RootID:    in.Post.RootID,
		Message:   in.Post.Message,
		Type:      "system_ephemeral",
		Props:     json.RawMessage(`{}`),
	}
	shownIn := ShownInChannel
	if p.RootID != "" {
		shownIn = ShownInThread
	}
	f.st.ephemeral = append(f.st.ephemeral, Ephemeral{
		AtMs:      p.CreateAt,
		PostID:    p.ID,
		UserID:    in.UserID,
		From:      BotUsername,
		ChannelID: p.ChannelID,
		RootID:    p.RootID,
		Message:   p.Message,
		ShownIn:   shownIn,
	})
	fakeserver.WriteJSON(w, http.StatusCreated, p)
}

// handleDeletePost deletes a post as a person does in the client.
func (f *Fake) handleDeletePost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.st.posts[id]
	if !ok {
		fakeserver.WriteError(w, http.StatusNotFound, "unknown post "+id)
		return
	}
	if p.DeleteAt == 0 {
		p.DeleteAt = f.nowMs()
		p.UpdateAt = p.DeleteAt
	}
	w.WriteHeader(http.StatusNoContent)
}
