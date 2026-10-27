// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package buttons

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// names are the buttons of bs as their Commands, a Snooze button with its duration in seconds or its preset.
func names(bs []Button, durations []int64) string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Command
		if b.Command == CommandSnooze {
			if p, ok := presetOf[b.Argument]; ok {
				out[i] += ":" + p
			} else {
				out[i] += fmt.Sprintf(":%d", durations[b.Argument])
			}
		}
	}
	return strings.Join(out, " ")
}

func ptr[T any](v T) *T { return &v }

// TestSetShows filters the buttons of every status (C-28.FR-8, AC-7): the set of AC-7 on a Route with 1 h, 4 h and
// 24 h keeps Acknowledge or Unacknowledge, "Snooze 24 h" and "Until tomorrow" in the order of ForStatus, Unsnooze
// always and the Reminder's buttons always; every button with all; a set without durations keeps no duration; a Route
// without durations has no preset to show.
func TestSetShows(t *testing.T) {
	durations := []int64{3600, 14400, 86400}
	ac7 := Set{Items: []Item{{Button: ItemAcknowledge}, {Button: ItemSnooze, SnoozeSeconds: 86400},
		{Button: ItemSnoozePreset, Preset: PresetTomorrow}}}
	noDurations := Set{Items: []Item{{Button: ItemResolve}, {Button: ItemSnoozePreset, Preset: PresetWeekStart}}}
	for _, tt := range []struct {
		name      string
		set       Set
		status    string
		durations []int64
		want      string
	}{
		{"all, firing", All(), "firing", durations, "acknowledge resolve snooze:3600 snooze:14400 snooze:86400 " +
			"snooze:tomorrow snooze:week_start"},
		{"all, a Route without durations", All(), "firing", nil, "acknowledge resolve"},
		{"AC-7, firing", ac7, "firing", durations, "acknowledge snooze:86400 snooze:tomorrow"},
		{"AC-7, acknowledged", ac7, "acknowledged", durations, "unacknowledge snooze:86400 snooze:tomorrow"},
		{"AC-7, snoozed: Unsnooze always", ac7, "snoozed", durations, "acknowledge unsnooze"},
		{"AC-7, resolved", ac7, "resolved", durations, ""},
		{"AC-7, a Route without durations", ac7, "firing", nil, "acknowledge"},
		{"AC-7, a Route without 24 h", ac7, "firing", []int64{3600, 14400}, "acknowledge snooze:tomorrow"},
		{"no durations in the set", noDurations, "firing", durations, "resolve snooze:week_start"},
		{"no durations in the set, snoozed", noDurations, "snoozed", durations, "unsnooze resolve"},
		{"an empty set", Set{}, "acknowledged", durations, ""},
		{"an empty set, snoozed", Set{}, "snoozed", durations, "unsnooze"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.set.Filter(ForStatus(tt.status, len(tt.durations)), tt.durations)
			if s := names(got, tt.durations); s != tt.want {
				t.Errorf("%s", s)
			}
		})
	}
	if !(Set{}).Shows(Button{Command: CommandStillOnIt}, durations) {
		t.Error("an empty set hides the Reminder's Still on it")
	}
	if (Set{Items: []Item{{Button: ItemSnooze, SnoozeSeconds: 3600}}}).Shows(Button{Command: CommandSnooze,
		Argument: 7}, durations) {
		t.Error("a Snooze button of an index beyond the durations is shown")
	}
	before := ForStatus("firing", 3)
	ac7.Filter(before, durations)
	if !slices.Equal(before, ForStatus("firing", 3)) {
		t.Error("Filter changed its argument")
	}
}

// TestValidate refuses each invalid item at its index with its code and keeps only the fields of each button.
func TestValidate(t *testing.T) {
	ok := []ItemInput{{Button: ItemAcknowledge, SnoozeSeconds: ptr(int64(5))}, {Button: ItemResolve},
		{Button: ItemSnooze, SnoozeSeconds: ptr(int64(86400)), Preset: ptr(PresetTomorrow)},
		{Button: ItemSnooze, SnoozeSeconds: ptr(int64(3600))},
		{Button: ItemSnoozePreset, Preset: ptr(PresetTomorrow)}, {Button: ItemSnoozePreset, Preset: ptr(PresetWeekStart)}}
	items, err := Validate(ok)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{Button: ItemAcknowledge}, {Button: ItemResolve}, {Button: ItemSnooze, SnoozeSeconds: 86400},
		{Button: ItemSnooze, SnoozeSeconds: 3600}, {Button: ItemSnoozePreset, Preset: PresetTomorrow},
		{Button: ItemSnoozePreset, Preset: PresetWeekStart}}
	if !slices.Equal(items, want) {
		t.Errorf("items %v", items)
	}
	if items, err := Validate(nil); err != nil || len(items) != 0 || items == nil {
		t.Errorf("no items: %v, %v", items, err)
	}
	for _, tt := range []struct {
		name  string
		in    []ItemInput
		index int
		code  string
	}{
		{"a second resolve", []ItemInput{{Button: ItemResolve}, {Button: ItemResolve}}, 1, CodeDuplicate},
		{"a second acknowledge", []ItemInput{{Button: ItemAcknowledge}, {Button: ItemResolve},
			{Button: ItemAcknowledge}}, 2, CodeDuplicate},
		{"a duration twice", []ItemInput{{Button: ItemSnooze, SnoozeSeconds: ptr(int64(60))},
			{Button: ItemSnooze, SnoozeSeconds: ptr(int64(60))}}, 1, CodeDuplicate},
		{"a preset twice", []ItemInput{{Button: ItemSnoozePreset, Preset: ptr(PresetWeekStart)},
			{Button: ItemSnoozePreset, Preset: ptr(PresetWeekStart)}}, 1, CodeDuplicate},
		{"a snooze without seconds", []ItemInput{{Button: ItemResolve}, {Button: ItemSnooze}}, 1, CodeRequired},
		{"a snooze of 0 seconds", []ItemInput{{Button: ItemSnooze, SnoozeSeconds: ptr(int64(0))}}, 0, CodeOutOfRange},
		{"a snooze of -5 seconds", []ItemInput{{Button: ItemSnooze, SnoozeSeconds: ptr(int64(-5))}}, 0,
			CodeOutOfRange},
		{"a preset without its preset", []ItemInput{{Button: ItemSnoozePreset}}, 0, CodeRequired},
		{"a preset that is empty", []ItemInput{{Button: ItemSnoozePreset, Preset: ptr("")}}, 0, CodeRequired},
		{"an unknown preset", []ItemInput{{Button: ItemSnoozePreset, Preset: ptr("never")}}, 0, CodeInvalidFormat},
		{"an unknown button", []ItemInput{{Button: "unsnooze"}}, 0, CodeInvalidFormat},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Validate(tt.in)
			if err == nil || err.Index != tt.index || err.Code != tt.code || err.Error() == "" {
				t.Fatalf("%v", err)
			}
			if p := err.Pointer("/buttons"); p != fmt.Sprintf("/buttons/items/%d", tt.index) {
				t.Errorf("pointer %s", p)
			}
		})
	}
	if p := (&ItemError{Index: -1}).Pointer("/buttons"); p != "/buttons/items" {
		t.Errorf("the pointer of the list %s", p)
	}
}

// TestStoredForms reads and writes the sets as organizations.buttons and destinations.buttons store them.
func TestStoredForms(t *testing.T) {
	for raw, want := range map[string]Set{
		"":              All(),
		`{"all": true}`: All(),
		`{"all": true, "items": [{"button": "resolve"}]}`:  All(),
		`{"all": false, "items": [{"button": "resolve"}]}`: {Items: []Item{{Button: ItemResolve}}},
		`{"all": false}`: {},
	} {
		got, err := ParseSet([]byte(raw))
		if err != nil || got.All != want.All || !slices.Equal(got.Items, want.Items) {
			t.Errorf("ParseSet(%s) = %+v, %v", raw, got, err)
		}
	}
	if _, err := ParseSet([]byte(`[]`)); err == nil {
		t.Error("ParseSet of an array")
	}
	for raw, want := range map[string]Own{
		"":                           {},
		"null":                       {},
		"[]":                         {Set: true, Items: []Item{}},
		`[{"button":"acknowledge"}]`: {Set: true, Items: []Item{{Button: ItemAcknowledge}}},
	} {
		got, err := ParseOwn([]byte(raw))
		if err != nil || got.Set != want.Set || !slices.Equal(got.Items, want.Items) {
			t.Errorf("ParseOwn(%s) = %+v, %v", raw, got, err)
		}
	}
	if _, err := ParseOwn([]byte(`{}`)); err == nil {
		t.Error("ParseOwn of an object")
	}
	if b := (Own{}).JSON(); b != nil {
		t.Errorf("As in the Organization stored as %s", b)
	}
	if b := (Own{Set: true}).JSON(); string(b) != "[]" {
		t.Errorf("an own set without items stored as %s", b)
	}
	own := Own{Set: true, Items: []Item{{Button: ItemSnooze, SnoozeSeconds: 60}, {Button: ItemSnoozePreset,
		Preset: PresetTomorrow}}}
	if b := own.JSON(); string(b) != `[{"button":"snooze","snooze_seconds":60},{"button":"snooze_preset",`+
		`"preset":"tomorrow"}]` {
		t.Errorf("an own set stored as %s", b)
	}
	org := Set{Items: []Item{{Button: ItemResolve}}}
	if got := (Own{}).Effective(org); !slices.Equal(got.Items, org.Items) || got.All {
		t.Errorf("As in the Organization is %+v", got)
	}
	if got := own.Effective(All()); got.All || !slices.Equal(got.Items, own.Items) {
		t.Errorf("an own set is %+v", got)
	}
}
