// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package buttons

import (
	"encoding/json"
	"fmt"
	"slices"
)

// The buttons an item of a button set names (ButtonSetItem): Acknowledge, which brings Unacknowledge with it,
// Resolve, one Snooze duration by its value in seconds, and one Snooze preset.
const (
	ItemAcknowledge  = "acknowledge"
	ItemResolve      = "resolve"
	ItemSnooze       = "snooze"
	ItemSnoozePreset = "snooze_preset"
)

// The Snooze presets an item names (SnoozePreset), in the order of the arguments ArgumentTomorrow and
// ArgumentWeekStart.
const (
	PresetTomorrow  = "tomorrow"
	PresetWeekStart = "week_start"
)

// presetOf is the preset of the argument of a Snooze preset button.
var presetOf = map[int]string{ArgumentTomorrow: PresetTomorrow, ArgumentWeekStart: PresetWeekStart}

// Item is one entry of a button set: a button, the value of a Snooze duration in seconds, or a Snooze preset.
type Item struct {
	Button        string `json:"button"`
	SnoozeSeconds int64  `json:"snooze_seconds,omitempty"`
	Preset        string `json:"preset,omitempty"`
}

// Set is a button set (C-28.FR-8): every button, durations a Route gains later included, or the buttons its items
// list. The zero Set lists none; All is the default of the Organization (organization.buttons).
type Set struct {
	All   bool   `json:"all"`
	Items []Item `json:"items,omitempty"`
}

// All is the set of every button.
func All() Set { return Set{All: true} }

// Shows reports whether a Root message with the set shows the button b of a Route with the Snooze durations given, in
// seconds: Acknowledge and Unacknowledge by the item acknowledge, Resolve by resolve, a Snooze duration when its value
// is an item, a Snooze preset when its preset is; Unsnooze and the buttons of Reminders always, whatever the set. A
// Snooze button of a duration the Route does not have is not shown.
func (s Set) Shows(b Button, durations []int64) bool {
	if s.All {
		return true
	}
	switch b.Command {
	case CommandAcknowledge, CommandUnacknowledge:
		return s.has(Item{Button: ItemAcknowledge})
	case CommandResolve:
		return s.has(Item{Button: ItemResolve})
	case CommandSnooze:
		if preset, ok := presetOf[b.Argument]; ok {
			return s.has(Item{Button: ItemSnoozePreset, Preset: preset})
		}
		return b.Argument >= 0 && b.Argument < len(durations) &&
			s.has(Item{Button: ItemSnooze, SnoozeSeconds: durations[b.Argument]})
	}
	return true
}

func (s Set) has(it Item) bool {
	return slices.Contains(s.Items, it)
}

// Filter are the buttons of bs that the set shows on a Route with the Snooze durations given, in their order, so that
// the order of ForStatus — reference.md's — stays.
func (s Set) Filter(bs []Button, durations []int64) []Button {
	return slices.DeleteFunc(slices.Clone(bs), func(b Button) bool { return !s.Shows(b, durations) })
}

// ParseSet reads the Organization's set as organizations.buttons stores it; an empty value is every button, the
// default of a row that a replica of 0.1 read before the column existed.
func ParseSet(raw []byte) (Set, error) {
	if len(raw) == 0 {
		return All(), nil
	}
	var s Set
	if err := json.Unmarshal(raw, &s); err != nil {
		return Set{}, fmt.Errorf("read the button set: %w", err)
	}
	if s.All {
		s.Items = nil
	}
	return s, nil
}

// Own is the button set of a Destination (destination.buttons): its own items when Set, otherwise "As in the
// Organization", which follows the Organization's set.
type Own struct {
	Set   bool
	Items []Item
}

// ParseOwn reads a Destination's set as destinations.buttons stores it: an array of items, or null for "As in the
// Organization".
func ParseOwn(raw []byte) (Own, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return Own{}, nil
	}
	items := []Item{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return Own{}, fmt.Errorf("read the button set of the destination: %w", err)
	}
	return Own{Set: true, Items: items}, nil
}

// JSON is the value destinations.buttons stores: the array of items, nil for "As in the Organization".
func (o Own) JSON() []byte {
	if !o.Set {
		return nil
	}
	items := o.Items
	if items == nil {
		items = []Item{}
	}
	b, _ := json.Marshal(items) // strings and numbers only
	return b
}

// Effective is the set a Destination's Root messages show: its own, else the Organization's org.
func (o Own) Effective(org Set) Set {
	if !o.Set {
		return org
	}
	return Set{Items: o.Items}
}

// The codes of an ItemError, from the catalogue of the API (x-problem-codes).
const (
	CodeDuplicate     = "duplicate"
	CodeRequired      = "required"
	CodeOutOfRange    = "out_of_range"
	CodeInvalidFormat = "invalid_format"
)

// ItemError is an item of a set that is not valid: its index, -1 for the list itself, its code and what is wrong.
type ItemError struct {
	Index  int
	Code   string
	Detail string
}

func (e *ItemError) Error() string { return fmt.Sprintf("item %d: %s", e.Index, e.Detail) }

// Pointer is the JSON pointer of the item under the pointer of its set, such as /buttons/items/1, or of the list.
func (e *ItemError) Pointer(set string) string {
	if e.Index < 0 {
		return set + "/items"
	}
	return fmt.Sprintf("%s/items/%d", set, e.Index)
}

// ItemInput is an item of a set as a request writes it: snooze_seconds and preset may be missing.
type ItemInput struct {
	Button        string
	SnoozeSeconds *int64
	Preset        *string
}

// Validate reads the items of a request in their order and returns them as a set stores them, each with only the
// fields its button takes, or the first item that is not valid: a snooze without snooze_seconds (required) or with one
// below 1 (out_of_range), a snooze_preset without its preset (required), an unknown button or preset
// (invalid_format), or the same button, duration or preset a second time (duplicate). An item of a duration that no
// Route has is valid: it is simply never shown.
func Validate(in []ItemInput) ([]Item, *ItemError) {
	out := make([]Item, 0, len(in))
	for i, it := range in {
		item := Item{Button: it.Button}
		switch it.Button {
		case ItemAcknowledge, ItemResolve:
		case ItemSnooze:
			if it.SnoozeSeconds == nil {
				return nil, &ItemError{Index: i, Code: CodeRequired, Detail: "A snooze item needs its snooze_seconds."}
			}
			if *it.SnoozeSeconds < 1 {
				return nil, &ItemError{Index: i, Code: CodeOutOfRange, Detail: "snooze_seconds is below 1."}
			}
			item.SnoozeSeconds = *it.SnoozeSeconds
		case ItemSnoozePreset:
			if it.Preset == nil || *it.Preset == "" {
				return nil, &ItemError{Index: i, Code: CodeRequired, Detail: "A snooze_preset item needs its preset."}
			}
			if *it.Preset != PresetTomorrow && *it.Preset != PresetWeekStart {
				return nil, &ItemError{Index: i, Code: CodeInvalidFormat,
					Detail: "The preset is not tomorrow or week_start."}
			}
			item.Preset = *it.Preset
		default:
			return nil, &ItemError{Index: i, Code: CodeInvalidFormat,
				Detail: "The button is not acknowledge, resolve, snooze or snooze_preset."}
		}
		if slices.Contains(out, item) {
			return nil, &ItemError{Index: i, Code: CodeDuplicate, Detail: "The item appears twice."}
		}
		out = append(out, item)
	}
	return out, nil
}
