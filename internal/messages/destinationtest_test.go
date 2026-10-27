// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"errors"
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/buttons"
)

// TestDestinationTestSources is C-16.FR-1 and FR-4: the source of a test is an Alert Group by public_id with its Route,
// or the built-in example of three firing Alerts with the settings of the Default route and its links; an unknown
// Alert Group is ErrNotFound.
func TestDestinationTestSources(t *testing.T) {
	f := &fakeQueries{group: source("firing", 2)}
	r := withFake(t, f)
	src, err := r.TestSample(t.Context(), "AGK7M3QX9P2RTA")
	if err != nil || src.Number != 12 || src.Route.PublicID != "RTAAAAAAAAAAA1" || len(src.Alerts) != 2 {
		t.Fatalf("sample %+v %v", src, err)
	}
	if _, err := r.TestSample(t.Context(), "AG000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown %v", err)
	}
	r.links = &fakeLinks{}
	ex, err := r.TestExample(t.Context())
	if err != nil || len(ex.Alerts) != 3 || ex.Status != ColourFiring || ex.Route.Name != "Default" ||
		ex.Alerts[2].Labels["pod"] != "checkout-3" || len(ex.Links) != 3 {
		t.Fatalf("example %+v %v", ex, err)
	}
	r.links = &fakeLinks{fail: errBoom}
	if _, err := r.TestExample(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("links %v", err)
	}
	f.fail["GetRenderSettings"] = errBoom
	if _, err := r.TestExample(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("settings %v", err)
	}
}

// TestTestRoot is C-16.FR-1 and FR-3: the Root message of a test has the buttons of its status, whose action ids name
// the Destination under test as the subject test; without a Destination they are unsigned. The mark of a test is
// in both languages.
func TestTestRoot(t *testing.T) {
	r := newRenderer(t)
	src := source("firing", 2)
	rd := r.TestRoot(src, MarkupMarkdown, "DSAAAAAAAAAAA1")
	if len(rd.Message.Buttons) == 0 || rd.KeyID == "" {
		t.Fatalf("buttons %+v", rd)
	}
	for _, b := range rd.Message.Buttons {
		a, err := buttons.Verify(fakeKeys{}, b.ActionID, b.KeyID)
		if err != nil || a.Subject != buttons.SubjectTest || a.PublicID != "DSAAAAAAAAAAA1" || a.Command != b.Command {
			t.Errorf("button %+v: %+v %v", b, a, err)
		}
	}
	if rd := r.TestRoot(src, MarkupMarkdown, ""); rd.Message.Buttons[0].ActionID != "" {
		t.Errorf("unsigned %+v", rd.Message.Buttons[0])
	}
	if TestMark("en") != "🧪 Test message" || TestMark("ru") != "🧪 Тестовое сообщение" {
		t.Errorf("marks %q %q", TestMark("en"), TestMark("ru"))
	}
}

// TestTestRootButtonSet is C-16.FR-4 revised for L2 and C-28.FR-9: the test message of a Destination with its own set
// shows exactly the buttons of the set, signed, in the order of reference.md; Unsnooze always on a snoozed one; a set
// of every button or none at all shows them all; ShownButtons leaves a Thread reply and a Storm summary as they are,
// and a set without any shown button leaves an empty list, never null.
func TestTestRootButtonSet(t *testing.T) {
	r := newRenderer(t)
	ac7 := &buttons.Set{Items: []buttons.Item{{Button: buttons.ItemSnoozePreset, Preset: buttons.PresetTomorrow},
		{Button: buttons.ItemSnooze, SnoozeSeconds: 86400}, {Button: buttons.ItemAcknowledge}}}
	src := source("firing", 2)
	src.Buttons = ac7
	for _, markup := range []Markup{MarkupMarkdown, MarkupHTML, MarkupCommonMark, MarkupMrkdwn} {
		rd := r.TestRoot(src, markup, "DSAAAAAAAAAAA1")
		if got := labels(rd.Message.Buttons); !slices.Equal(got, []string{"Ack", "Snooze 24 h", "Until tomorrow 09:00"}) {
			t.Errorf("%s: %v", markup, got)
		}
		for _, b := range rd.Message.Buttons {
			if a, err := buttons.Verify(fakeKeys{}, b.ActionID, b.KeyID); err != nil || a.Subject != buttons.SubjectTest {
				t.Errorf("%s: button %+v: %v", markup, b, err)
			}
		}
		if markup == MarkupMrkdwn && !slices.Equal(rd.Message.SnoozeSeconds, []int64{3600, 14400, 86400}) {
			t.Errorf("the durations a button names by index: %v", rd.Message.SnoozeSeconds)
		}
	}
	src = source("acknowledged", 2)
	src.Buttons = ac7
	if got := labels(r.TestRoot(src, MarkupMarkdown, "").Message.Buttons); !slices.Equal(got, []string{"Unack", "Snooze 24 h",
		"Until tomorrow 09:00"}) {
		t.Errorf("acknowledged: %v", got)
	}
	src = source("snoozed", 2)
	src.Buttons = &buttons.Set{Items: []buttons.Item{{Button: buttons.ItemResolve}}}
	if got := labels(r.TestRoot(src, MarkupMarkdown, "").Message.Buttons); !slices.Equal(got, []string{"Unsnooze", "Resolve"}) {
		t.Errorf("snoozed: %v", got)
	}
	src = source("firing", 2)
	all := len(r.TestRoot(src, MarkupMarkdown, "").Message.Buttons)
	src.Buttons = &buttons.Set{All: true}
	if n := len(r.TestRoot(src, MarkupMarkdown, "").Message.Buttons); n != all || all != 7 {
		t.Errorf("every button: %d of %d", n, all)
	}
	src.Buttons = &buttons.Set{}
	if m := r.TestRoot(src, MarkupMarkdown, "").Message; m.Buttons == nil || len(m.Buttons) != 0 {
		t.Errorf("no button: %#v", m.Buttons)
	}
	// The commands line of a bridge without buttons names the Text commands of the shown buttons only (C-28.FR-9).
	src = source("firing", 2)
	src.Buttons = ac7
	if got := CommandsLine(r.TestRoot(src, MarkupCommonMark, "").Message, false); got !=
		"Reply in the Thread: ack · snooze 24h" {
		t.Errorf("the commands line of the set: %q", got)
	}
	src.Buttons = &buttons.Set{Items: []buttons.Item{{Button: buttons.ItemResolve}}}
	if got := CommandsLine(r.TestRoot(src, MarkupCommonMark, "").Message, false); got != "Reply in the Thread: resolve" {
		t.Errorf("the commands line of resolve only: %q", got)
	}
	reply := Message{Kind: KindReply, Buttons: []Button{{Command: buttons.CommandStillOnIt}}}
	if got := ShownButtons(reply, &buttons.Set{}, nil); len(got.Buttons) != 1 {
		t.Errorf("a reply lost its buttons: %+v", got)
	}
}
