// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"errors"
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
