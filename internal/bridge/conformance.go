// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/webhooks"
)

// The conformance check `muster bridge check` (C-22.FR-21): the steps of the Bridge protocol against one bridge, run
// by the people who write a bridge and by the CI of the bridge libraries, outside any installation. It posts, edits,
// replies and deletes through the protocol client directly, outside delivery, which ADR-0017 permits; it needs no
// database, master key or configuration file and writes nothing to the file system. Every step prints one line as soon
// as it ends — `PASS <step>`, `FAIL <step>: <reason>` or `SKIP <step>: <why>` — with the token and the push secret
// masked in every reason; an interactive run also prints `PUSH <url> [<secret>]` once its push listener listens and
// `WAIT <step>: <what to do>, message_id <id>` before it waits for a person.

// ConformanceWait is how long the press step waits for a person.
const ConformanceWait = 5 * time.Minute

// DefaultPushListen is where the push listener of the conformance check listens unless it is told otherwise.
const DefaultPushListen = "127.0.0.1:0"

const (
	// pushGrace is how long the push step waits, after a press that came by polling, for its push, and how long the
	// press step waits at the push listener after it saw the first push the listener refused.
	pushGrace = 30 * time.Second
	// checkTitle starts the title of every test message, followed by its step.
	checkTitle = "muster bridge check: "
	checkLink  = "https://muster-io.github.io/muster/"
	checkBody  = "Posted by muster bridge check, the conformance check of the Bridge protocol."
	// maxAttempts bounds the pushes the listener keeps, the oldest dropped first.
	maxAttempts = 1000
)

// buttonStyles are the style hints of the buttons step, one button each, as many as the bridge takes.
var buttonStyles = []gen.ButtonStyle{gen.Primary, gen.Success, gen.Neutral, gen.Danger}

// ConformanceConfig is a run of the conformance check: the bridge's base URL and token; the push secret, generated
// when it is empty; the channel the bot posts to, the channel it cannot use and the handle of a person, the last two
// skipping their steps when empty; where the push listener listens (DefaultPushListen when empty) and the URL the
// bridge reaches it at (the listener's own when empty); whether a person takes part; how long press waits for them
// (ConformanceWait when zero); where the lines go; the logger of the outbound client (nothing logged when nil); and
// the real clock (the system clock when nil).
type ConformanceConfig struct {
	BaseURL          string
	Token            logging.Secret
	PushSecret       logging.Secret
	Channel          string
	ForbiddenChannel string
	Handle           string
	PushListen       string
	PushURL          string
	Interactive      bool
	Wait             time.Duration
	Out              io.Writer
	Log              *logging.Logger
	Real             clock.Clock

	// longPoll is the wait of the long polls of press (zero: PollWait), sleep the pause after an empty poll, and timer
	// a wait at the push listener; tests replace them.
	longPoll time.Duration
	sleep    func(ctx context.Context, d time.Duration)
	timer    func(d time.Duration) (<-chan time.Time, func())
}

// ValidPushSecret reports whether s is a push secret in the Standard Webhooks form, whsec_ followed by base64 of at
// least one byte, as a Connection's push secret is checked.
func ValidPushSecret(s logging.Secret) bool {
	rest, ok := strings.CutPrefix(string(s), webhooks.SigningSecretPrefix)
	if !ok {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(rest)
	return err == nil && len(key) > 0
}

// conformance is one run: what the steps learned, for the steps after them.
type conformance struct {
	cfg       ConformanceConfig
	c         *Client
	run       string
	generated bool
	failed    bool
	forms     []string

	info       Info
	postIn     gen.MessageInput
	postID     string
	threadRef  string
	buttonsID  string
	buttonData []string
	person     *gen.Person
	cursor     *string
	listener   *pushListener
	server     *http.Server
	listenErr  string
	pressed    *Event
}

// RunConformance runs the conformance check of cfg and prints each line to cfg.Out as soon as its step ends. failed
// is whether a step failed. err is the end of ctx, which interrupted the run, or a client that cannot be built, after
// which nothing ran.
func RunConformance(ctx context.Context, cfg ConformanceConfig) (failed bool, err error) {
	base, err := ParseBaseURL(cfg.BaseURL)
	if err != nil {
		return false, err
	}
	cfg = cfg.withDefaults()
	cfg.BaseURL = base
	cn := &conformance{cfg: cfg, run: randomText(9)}
	if cn.cfg.PushSecret == "" {
		cn.cfg.PushSecret, cn.generated = webhooks.NewSigningSecret(), true
	}
	cn.forms = secretForms(cn.cfg.Token, cn.cfg.PushSecret)
	policy, err := outbound.ParsePolicy(string(outbound.ModeStandard), []string{"127.0.0.0/8", "::1/128"}, nil)
	if err != nil {
		return false, err
	}
	cn.c, err = NewClient(Network{Policy: outbound.StaticPolicy(policy), Log: cfg.Log, Real: cfg.Real},
		Settings{BaseURL: cfg.BaseURL, Token: cfg.Token, PushSecret: cn.cfg.PushSecret})
	if err != nil {
		return false, err
	}
	defer cn.closeListener()
	steps := []func(context.Context){cn.infoStep, cn.channelCheck, cn.post, cn.replayedKey, cn.buttons, cn.edit,
		cn.threadReply, cn.threadRefStep, cn.forbiddenChannel, cn.userLookup, cn.directMessage, cn.deleteStep, cn.poll,
		cn.openListener, cn.press, cn.push, cn.answer}
	for n, step := range steps {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		step(ctx)
		if n == 0 && cn.failed {
			// Every later step depends on what info declares.
			return true, ctx.Err()
		}
	}
	err = ctx.Err()
	return cn.failed || err != nil, err
}

func (cfg ConformanceConfig) withDefaults() ConformanceConfig {
	cfg.PushListen = cmp.Or(cfg.PushListen, DefaultPushListen)
	cfg.Wait = cmp.Or(cfg.Wait, ConformanceWait)
	cfg.longPoll = cmp.Or(cfg.longPoll, PollWait)
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.Log == nil {
		cfg.Log = logging.New(io.Discard, logging.LevelInfo)
	}
	if cfg.Real == nil {
		cfg.Real = clock.Real{}
	}
	if cfg.sleep == nil {
		cfg.sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if cfg.timer == nil {
		cfg.timer = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		}
	}
	return cfg
}

// randomText is n random bytes in base64url.
func randomText(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// secretForms are the secrets as a text may carry them, verbatim and in the escaped forms a URL gives them, as outbound
// redacts them: the longer first, so that a secret contained in another is not replaced inside it first.
func secretForms(secrets ...logging.Secret) []string {
	var forms []string
	for _, s := range secrets {
		if s == "" {
			continue
		}
		raw := string(s)
		for _, f := range []string{raw, url.PathEscape(raw), url.QueryEscape(raw), (&url.URL{Path: raw}).EscapedPath()} {
			if !slices.Contains(forms, f) {
				forms = append(forms, f)
			}
		}
	}
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	return forms
}

// text is a text of the bridge, or one given on the command line, as a line shows it: with the token and the push
// secret masked in every form and on one line.
func (cn *conformance) text(s string) string {
	for _, f := range cn.forms {
		s = strings.ReplaceAll(s, f, "[redacted]")
	}
	return oneLine(s)
}

func (cn *conformance) pass(step string) { _, _ = fmt.Fprintf(cn.cfg.Out, "PASS %s\n", step) }

func (cn *conformance) skip(step, why string) {
	_, _ = fmt.Fprintf(cn.cfg.Out, "SKIP %s: %s\n", step, why)
}

func (cn *conformance) fail(step, reason string) {
	cn.failed = true
	_, _ = fmt.Fprintf(cn.cfg.Out, "FAIL %s: %s\n", step, cn.text(reason))
}

// failure is why a call failed: no answer, an answer that does not match the protocol, or an error answer.
func (cn *conformance) failure(r Result) string {
	switch {
	case r.Status == 0:
		return "no answer: " + string(r.Error)
	case r.Status >= http.StatusOK && r.Status < http.StatusMultipleChoices:
		return string(r.Error)
	}
	return fmt.Sprintf("answered %d %s: %s", r.Status, r.Classification.Class, cmp.Or(string(r.Error), "no details"))
}

// status fails step unless the call r succeeded with the status want, and reports whether it did.
func (cn *conformance) status(step string, r Result, want int) bool {
	switch {
	case !r.OK():
		cn.fail(step, cn.failure(r))
		return false
	case r.Status != want:
		cn.fail(step, fmt.Sprintf("answered %d instead of %d", r.Status, want))
		return false
	}
	return true
}

// key is the Idempotency-Key of step, new for each run, so that a run never replays an earlier one.
func (cn *conformance) key(step string) string { return "muster-check-" + cn.run + "-" + step }

// offers reports whether the bridge offers the event delivery mode.
func (cn *conformance) offers(mode gen.InfoEventDelivery) bool {
	return slices.Contains(cn.info.EventDelivery, mode)
}

// content is the envelope of the test message of step in the colour, with buttons.
func (cn *conformance) content(step, colour string, buttons []gen.Button) gen.MessageContent {
	return gen.MessageContent{Status: gen.Status(colour), Color: messages.StatusColour(colour),
		Emoji: messages.StatusEmoji(colour), Title: checkTitle + step, Link: gen.Link{Url: checkLink, Label: "Muster"},
		Body: messages.EscapeCommonMark(messages.TestMark("en")) + blockBreak + checkBody, Buttons: buttons}
}

// reply is the Thread reply of step under threadRef, null when it is empty.
func (cn *conformance) reply(step, threadRef string) gen.ThreadReplyInput {
	ref := nullable.NewNullNullable[string]()
	if threadRef != "" {
		ref = nullable.NewNullableWithValue(threadRef)
	}
	return gen.ThreadReplyInput{ThreadRef: ref, Body: checkTitle + step, Buttons: []gen.Button{},
		Mentions: []gen.Mention{}}
}

func (cn *conformance) infoStep(ctx context.Context) {
	i, r := cn.c.GetInfo(ctx, outbound.ClassInteractive)
	if cn.status("info", r, http.StatusOK) {
		cn.info = i
		cn.pass("info")
	}
}

func (cn *conformance) channelCheck(ctx context.Context) {
	if _, r := cn.c.CheckChannel(ctx, outbound.ClassInteractive, cn.cfg.Channel); cn.status("channel_check", r,
		http.StatusOK) {
		cn.pass("channel_check")
	}
}

func (cn *conformance) post(ctx context.Context) {
	const step = "post"
	cn.postIn = postInput(gen.Test, cn.cfg.Channel, cn.content(step, messages.ColourFiring, []gen.Button{}), false)
	out, r := cn.c.PostMessage(ctx, outbound.ClassInteractive, cn.key(step), cn.postIn)
	if r.OK() {
		cn.postID = out.MessageId
	}
	switch {
	case !cn.status(step, r, http.StatusCreated):
	case out.Replayed:
		cn.fail(step, "a new Idempotency-Key answered replayed true")
	default:
		cn.pass(step)
	}
}

func (cn *conformance) replayedKey(ctx context.Context) {
	const step = "replayed_key"
	if cn.postID == "" {
		cn.fail(step, "the post step failed")
		return
	}
	out, r := cn.c.PostMessage(ctx, outbound.ClassInteractive, cn.key("post"), cn.postIn)
	switch {
	case !cn.status(step, r, http.StatusCreated):
	case out.MessageId != cn.postID:
		cn.fail(step, fmt.Sprintf("the second request with the same Idempotency-Key created message %s instead of "+
			"returning %s", out.MessageId, cn.postID))
	case !out.Replayed:
		cn.fail(step, fmt.Sprintf("the second request with the same Idempotency-Key returned message %s without "+
			"replayed true", out.MessageId))
	default:
		cn.pass(step)
	}
}

func (cn *conformance) buttons(ctx context.Context) {
	const step = "buttons"
	caps := cn.info.Capabilities.Buttons
	if caps == nil {
		cn.skip(step, "the bridge does not offer buttons")
		return
	}
	styles := buttonStyles[:min(len(buttonStyles), caps.MaxPerMessage)]
	buttons := make([]gen.Button, 0, len(styles))
	data := make([]string, 0, len(styles))
	for n, style := range styles {
		d := (fmt.Sprintf("check-%d-", n+1) + randomText(maxData))[:maxData]
		buttons = append(buttons, gen.Button{Label: string(style), Style: style, Data: d})
		data = append(data, d)
	}
	in := postInput(gen.Test, cn.cfg.Channel, cn.content(step, messages.ColourFiring, buttons), false)
	out, r := cn.c.PostMessage(ctx, outbound.ClassInteractive, cn.key(step), in)
	if r.OK() {
		cn.buttonsID, cn.buttonData = out.MessageId, data
	}
	if cn.status(step, r, http.StatusCreated) {
		cn.pass(step)
	}
}

func (cn *conformance) edit(ctx context.Context) {
	const step = "edit"
	if cn.postID == "" {
		cn.fail(step, "the post step failed")
		return
	}
	_, r := cn.c.EditMessage(ctx, outbound.ClassInteractive, cn.postID,
		cn.content(step, messages.ColourAcknowledged, []gen.Button{}))
	if cn.status(step, r, http.StatusOK) {
		cn.pass(step)
	}
}

func (cn *conformance) threadReply(ctx context.Context) {
	const step = "thread_reply"
	if cn.postID == "" {
		cn.fail(step, "the post step failed")
		return
	}
	out, r := cn.c.ReplyInThread(ctx, outbound.ClassInteractive, cn.postID, cn.key(step), cn.reply(step, ""))
	if r.OK() {
		cn.threadRef = out.ThreadRef
	}
	if cn.status(step, r, http.StatusCreated) {
		cn.pass(step)
	}
}

func (cn *conformance) threadRefStep(ctx context.Context) {
	const step = "thread_ref"
	if cn.threadRef == "" {
		cn.fail(step, "the thread_reply step failed")
		return
	}
	_, r := cn.c.ReplyInThread(ctx, outbound.ClassInteractive, cn.postID, cn.key(step), cn.reply(step, cn.threadRef))
	if cn.status(step, r, http.StatusCreated) {
		cn.pass(step)
	}
}

func (cn *conformance) forbiddenChannel(ctx context.Context) {
	const step = "forbidden_channel"
	if cn.cfg.ForbiddenChannel == "" {
		cn.skip(step, "no --forbidden-channel")
		return
	}
	_, r := cn.c.CheckChannel(ctx, outbound.ClassInteractive, cn.cfg.ForbiddenChannel)
	switch {
	case r.OK():
		cn.fail(step, fmt.Sprintf("answered %d instead of forbidden", r.Status))
	case r.Status != 0 && r.Classification.Class == ClassForbidden:
		cn.pass(step)
	default:
		cn.fail(step, "expected forbidden; "+cn.failure(r))
	}
}

func (cn *conformance) userLookup(ctx context.Context) {
	const step = "user_lookup"
	switch {
	case cn.info.UserLookup == gen.None:
		cn.skip(step, "the bridge looks up nobody")
		return
	case cn.cfg.Handle == "":
		cn.skip(step, "no --handle")
		return
	}
	p, found, r := cn.c.FindUsers(ctx, outbound.ClassInteractive, cn.cfg.Handle)
	switch {
	case !cn.status(step, r, http.StatusOK):
	case !found:
		cn.fail(step, "the bridge found nobody with the handle "+cn.cfg.Handle)
	default:
		cn.person = &p
		cn.pass(step)
	}
}

func (cn *conformance) directMessage(ctx context.Context) {
	const step = "direct_message"
	switch dm := cn.info.Capabilities.DirectMessages; {
	case dm == nil:
		cn.skip(step, "the bridge does not offer direct_messages")
		return
	case !dm.BotCanStart:
		cn.skip(step, "the bot cannot start a direct chat (bot_can_start is false)")
		return
	case cn.info.UserLookup == gen.None:
		cn.skip(step, "the bridge looks up nobody")
		return
	case cn.cfg.Handle == "":
		cn.skip(step, "no --handle")
		return
	case cn.person == nil:
		// user_lookup ran and its own line says why it found nobody.
		cn.skip(step, "no person found")
		return
	}
	_, r := cn.c.SendDirectMessage(ctx, outbound.ClassInteractive, cn.key(step), cn.person.ExternalId,
		checkTitle+step)
	if cn.status(step, r, http.StatusCreated) {
		cn.pass(step)
	}
}

func (cn *conformance) deleteStep(ctx context.Context) {
	const step = "delete"
	switch {
	case cn.info.Capabilities.Delete == nil:
		cn.skip(step, "the bridge does not offer delete")
		return
	case cn.postID == "":
		cn.fail(step, "the post step failed")
		return
	}
	if r := cn.c.DeleteMessage(ctx, outbound.ClassInteractive, cn.postID); cn.status(step, r, http.StatusNoContent) {
		cn.pass(step)
	}
}

// pollEvents makes one pollEvents call, bounded by its wait plus PollMargin, since its client class retries until its
// context ends.
func (cn *conformance) pollEvents(ctx context.Context, cursor *string, wait time.Duration) (Batch, Result) {
	ctx, cancel := context.WithTimeout(ctx, wait+PollMargin)
	defer cancel()
	return cn.c.PollEvents(ctx, cursor, wait)
}

func (cn *conformance) poll(ctx context.Context) {
	const step = "poll"
	if !cn.offers(gen.Poll) {
		cn.skip(step, "the bridge does not offer poll")
		return
	}
	var cursor *string
	for _, answer := range []string{"first", "second"} {
		b, r := cn.pollEvents(ctx, cursor, 0)
		if !cn.status(step, r, http.StatusOK) {
			return
		}
		for n, raw := range b.Events {
			if _, err := DecodeEvent(raw); err != nil {
				cn.fail(step, fmt.Sprintf("event %d of the %s answer: %v", n+1, answer, err))
				return
			}
		}
		cursor = &b.NextCursor
	}
	cn.cursor = cursor
	cn.pass(step)
}

// openListener opens the push listener of an interactive run against a bridge that offers push and showed the buttons
// to press, and prints the PUSH line: where to push to and the push secret, unless it was given.
func (cn *conformance) openListener(ctx context.Context) {
	if !cn.cfg.Interactive || !cn.offers(gen.Push) || cn.buttonsID == "" {
		return
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cn.cfg.PushListen)
	if err != nil {
		cn.listenErr = "the push listener cannot listen on " + cn.cfg.PushListen + ": " + err.Error()
		return
	}
	cn.listener = newPushListener(cn.cfg.PushSecret, cn.cfg.Real)
	cn.server = &http.Server{Handler: cn.listener, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = cn.server.Serve(ln) }()
	line := "PUSH " + cn.text(cmp.Or(cn.cfg.PushURL, "http://"+ln.Addr().String()+"/"))
	if cn.generated {
		// The one secret a line shows, on purpose: the bridge's push target needs it.
		line += " " + string(cn.cfg.PushSecret)
	}
	_, _ = fmt.Fprintln(cn.cfg.Out, line)
}

func (cn *conformance) closeListener() {
	if cn.server != nil {
		_ = cn.server.Close()
	}
}

func (cn *conformance) press(ctx context.Context) {
	const step = "press"
	switch {
	case cn.info.Capabilities.Buttons == nil:
		cn.skip(step, "the bridge does not offer buttons")
		return
	case !cn.cfg.Interactive:
		cn.skip(step, "--no-interactive")
		return
	case cn.buttonsID == "":
		cn.fail(step, "the buttons step failed")
		return
	case !cn.offers(gen.Poll) && cn.listener == nil:
		cn.fail(step, cmp.Or(cn.listenErr, "the push listener is not open"))
		return
	}
	id := cn.text(cn.buttonsID)
	_, _ = fmt.Fprintf(cn.cfg.Out, "WAIT %s: press a button of the message %q within %s, message_id %s\n", step,
		checkTitle+"buttons", cn.cfg.Wait, id)
	var ev *Event
	var why string
	if cn.offers(gen.Poll) {
		ev, why = cn.pressByPolling(ctx)
	} else {
		ev, why = cn.pressByPush(ctx)
	}
	switch {
	case ctx.Err() != nil:
		cn.fail(step, "interrupted")
		return
	case ev == nil:
		cn.fail(step, why)
		return
	}
	cn.pressed = ev
	if !slices.Contains(cn.buttonData, ev.Data) {
		cn.fail(step, "the press of message "+id+" carries data that none of its buttons has")
		return
	}
	cn.pass(step)
}

// pressByPolling polls from the cursor of poll until a press of the buttons message comes, or Wait passed: the press,
// or nil and why.
func (cn *conformance) pressByPolling(ctx context.Context) (*Event, string) {
	deadline := cn.cfg.Real.Now().Add(cn.cfg.Wait)
	for ctx.Err() == nil {
		remaining := deadline.Sub(cn.cfg.Real.Now())
		if remaining <= 0 {
			return nil, cn.noPress()
		}
		b, r := cn.pollEvents(ctx, cn.cursor, min(remaining, cn.cfg.longPoll).Truncate(time.Second))
		if ctx.Err() != nil {
			break
		}
		if !r.OK() {
			return nil, "polling failed: " + cn.failure(r)
		}
		for _, raw := range b.Events {
			ev, err := DecodeEvent(raw)
			if err != nil {
				return nil, fmt.Sprintf("polling returned an event that is not one of the protocol: %v", err)
			}
			if ev.Type == TypePress && ev.MessageID == cn.buttonsID {
				return &ev, ""
			}
		}
		next := b.NextCursor
		cn.cursor = &next
		if len(b.Events) == 0 {
			cn.cfg.sleep(ctx, emptyPause)
		}
	}
	return nil, "interrupted"
}

// pressByPush waits at the push listener until a press of the buttons message comes, Wait passed, or pushGrace passed
// since the wait saw the first push the listener refused, so that a bridge whose pushes are all refused does not hold
// the run for the whole of Wait: the press, or nil and why.
func (cn *conformance) pressByPush(ctx context.Context) (*Event, string) {
	timeout, stop := cn.cfg.timer(cn.cfg.Wait)
	defer stop()
	var grace <-chan time.Time
	stopGrace := func() {}
	defer func() { stopGrace() }()
	for {
		changed := cn.listener.changes()
		if ev := cn.listener.press(cn.buttonsID); ev != nil {
			return ev, ""
		}
		if grace == nil && cn.listener.refusedAny() {
			grace, stopGrace = cn.cfg.timer(pushGrace)
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, "interrupted"
		case <-timeout:
			return nil, cn.noPress() + cn.listener.refusals()
		case <-grace:
			return nil, fmt.Sprintf("no valid press of message %s within %s of the first refused push",
				cn.buttonsID, pushGrace) + cn.listener.refusals()
		}
	}
}

func (cn *conformance) noPress() string {
	return fmt.Sprintf("no press of message %s within %s", cn.buttonsID, cn.cfg.Wait)
}

func (cn *conformance) push(ctx context.Context) {
	const step = "push"
	switch {
	case !cn.offers(gen.Push):
		cn.skip(step, "the bridge does not offer push")
		return
	case cn.info.Capabilities.Buttons == nil:
		cn.skip(step, "the bridge does not offer buttons")
		return
	case !cn.cfg.Interactive:
		cn.skip(step, "--no-interactive")
		return
	case cn.buttonsID == "":
		cn.fail(step, "the buttons step failed, so nothing was pressed")
		return
	case cn.listener == nil:
		cn.fail(step, cmp.Or(cn.listenErr, "the push listener is not open"))
		return
	case cn.pressed == nil:
		cn.fail(step, "the press step saw no press"+cn.listener.refusals())
		return
	}
	id := cn.pressed.ID
	a, ok := cn.awaitPush(ctx, id)
	switch {
	case ctx.Err() != nil:
		cn.fail(step, "interrupted")
	case !ok:
		cn.fail(step, fmt.Sprintf("no push of event %s reached the listener within %s", id, pushGrace))
	case a.event == nil:
		cn.fail(step, "the push of event "+id+" "+a.problem)
	case a.event.Type != TypePress || a.event.MessageID != cn.pressed.MessageID || a.event.Data != cn.pressed.Data:
		cn.fail(step, "the push of event "+id+" is not the press that polling returned")
	default:
		cn.pass(step)
	}
}

// awaitPush waits up to pushGrace for a push of the event id at the listener: the first valid one, else the first
// refused one; false when none came.
func (cn *conformance) awaitPush(ctx context.Context, id string) (pushAttempt, bool) {
	timeout, stop := cn.cfg.timer(pushGrace)
	defer stop()
	for {
		changed := cn.listener.changes()
		if a, ok := cn.listener.attempt(id); ok {
			return a, true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return pushAttempt{}, false
		case <-timeout:
			return pushAttempt{}, false
		}
	}
}

func (cn *conformance) answer(ctx context.Context) {
	const step = "answer"
	if cn.pressed != nil {
		if _, r := cn.c.AnswerEvent(ctx, outbound.ClassInteractive, cn.pressed.ID, checkTitle+step); cn.status(step,
			r, http.StatusOK) {
			cn.pass(step)
		}
		return
	}
	id := "muster-check-unknown-" + cn.run
	_, r := cn.c.AnswerEvent(ctx, outbound.ClassInteractive, id, checkTitle+step)
	switch {
	case r.OK():
		cn.fail(step, fmt.Sprintf("answered %d for the unknown event id %s instead of gone", r.Status, id))
	case r.Status != 0 && r.Classification.Class == ClassGone:
		cn.pass(step)
	default:
		cn.fail(step, "expected gone for an unknown event id; "+cn.failure(r))
	}
}

// pushAttempt is a push the listener received: its webhook-id, and the event when it was valid, otherwise what was
// wrong with it.
type pushAttempt struct {
	id      string
	event   *Event
	problem string
}

// pushListener receives the pushes of the bridge in an interactive run and answers them as bridgeEvent does, on any
// path: a missing header, a wrong signature and a webhook-timestamp out of PushTolerance 401, a body that is too large,
// is no event or whose id is not webhook-id 400, and a valid event 204, whatever it is about, so that the events of
// other messages and of other bots are acknowledged and ignored. It keeps every push and never prints or logs.
type pushListener struct {
	secret logging.Secret
	fresh  func(timestamp string) bool

	mu       sync.Mutex
	attempts []pushAttempt
	refused  int
	last     string
	changed  chan struct{}
}

func newPushListener(secret logging.Secret, realClock clock.Clock) *pushListener {
	p := &push{cfg: PushConfig{Real: realClock, Tolerance: PushTolerance}}
	return &pushListener{secret: secret, fresh: p.fresh, changed: make(chan struct{})}
}

func (l *pushListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeProblem(w, http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed",
			"The push listener takes POST only.")
		return
	}
	id := r.Header.Get(webhooks.HeaderID)
	timestamp := r.Header.Get(webhooks.HeaderTimestamp)
	signature := r.Header.Get(webhooks.HeaderSignature)
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxEventBytes+1))
	switch {
	case err != nil:
		l.badRequest(w, id, "could not be read")
	case len(body) > MaxEventBytes:
		l.badRequest(w, id, "is larger than 64 KiB")
	case id == "" || timestamp == "" || signature == "":
		l.unauthorized(w, id, "has no webhook-id, webhook-timestamp or webhook-signature header")
	case !webhooks.Verify(l.secret, id, timestamp, body, signature):
		l.unauthorized(w, id, "has a signature that does not match the push secret")
	case !l.fresh(timestamp):
		l.unauthorized(w, id, fmt.Sprintf("has a webhook-timestamp more than %s from this machine's clock",
			PushTolerance))
	default:
		ev, err := DecodeEvent(body)
		switch {
		case err != nil:
			l.badRequest(w, id, fmt.Sprintf("is not an event of the protocol: %v", err))
		case ev.ID != id:
			l.badRequest(w, id, "carries an event whose id is not its webhook-id")
		default:
			l.record(pushAttempt{id: id, event: &ev})
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func (l *pushListener) unauthorized(w http.ResponseWriter, id, problem string) {
	l.record(pushAttempt{id: id, problem: problem})
	writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Unauthenticated",
		"The signature or its timestamp is wrong.")
}

func (l *pushListener) badRequest(w http.ResponseWriter, id, problem string) {
	l.record(pushAttempt{id: id, problem: problem})
	writeProblem(w, http.StatusBadRequest, "validation-failed", "Bad request", "The body "+problem+".")
}

func (l *pushListener) record(a pushAttempt) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.attempts) >= maxAttempts {
		l.attempts = slices.Delete(l.attempts, 0, 1)
	}
	l.attempts = append(l.attempts, a)
	if a.event == nil {
		l.refused++
		l.last = a.problem
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

// changes is closed by the next push.
func (l *pushListener) changes() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.changed
}

// press is the first valid press of the message messageID, nil before one came.
func (l *pushListener) press(messageID string) *Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range l.attempts {
		if a.event != nil && a.event.Type == TypePress && a.event.MessageID == messageID {
			return a.event
		}
	}
	return nil
}

// attempt is the first valid push of the event id, else the first refused one; false before one came.
func (l *pushListener) attempt(id string) (pushAttempt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	refused := slices.IndexFunc(l.attempts, func(a pushAttempt) bool { return a.id == id && a.event == nil })
	if valid := slices.IndexFunc(l.attempts, func(a pushAttempt) bool {
		return a.id == id && a.event != nil
	}); valid >= 0 {
		return l.attempts[valid], true
	}
	if refused >= 0 {
		return l.attempts[refused], true
	}
	return pushAttempt{}, false
}

// refusedAny reports whether the listener refused a push.
func (l *pushListener) refusedAny() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused > 0
}

// refusals tells how many pushes the listener refused and why it refused the last, nothing when it refused none.
func (l *pushListener) refusals() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.refused {
	case 0:
		return ""
	case 1:
		return "; the push listener refused 1 push, which " + l.last
	}
	return fmt.Sprintf("; the push listener refused %d pushes, the last of which %s", l.refused, l.last)
}
