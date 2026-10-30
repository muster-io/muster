// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"slices"
	"time"
)

// The reasons a person may not be Paged (C-27.FR-5), which are also the kinds of their warnings.
const (
	ReasonViewer    = "viewer"
	ReasonDisabled  = "disabled"
	ReasonNotInTeam = "not_in_team"
)

// The kinds of the source of a segment of the Result.
const (
	SourceOverride      = "override"
	SourceRotationLayer = "rotation_layer"
)

// skipReason is why a person may not be Paged, empty when they may: a disabled User, a Viewer, or a User who is not
// in the Team — a deleted one among them, since deletion takes a User out of every Team.
func skipReason(p Person, inTeam bool) string {
	switch {
	case p.Status == "disabled":
		return ReasonDisabled
	case p.Role == "viewer":
		return ReasonViewer
	case !inTeam || p.Status != "active":
		return ReasonNotInTeam
	}
	return ""
}

// LayerRef names a Rotation layer.
type LayerRef struct {
	PublicID string
	Name     string
}

// Source is what decides a segment of the Result: an Override, or a Rotation layer.
type Source struct {
	Kind     string
	Override string
	Layer    LayerRef
}

// Segment is a segment of the Result with the person On-call in it, or nobody — a gap — when User is nil.
type Segment struct {
	Start, End time.Time
	User       *Person
	Source     *Source
}

// LayerShift is a Shift of a Rotation layer in the Result, with why it is skipped, empty when it is not.
type LayerShift struct {
	Shift
	Skipped string
}

// LayerShifts are the Shifts of one Rotation layer in the period.
type LayerShifts struct {
	Layer  LayerRef
	Shifts []LayerShift
}

// Warning names a person of a Rotation layer or of an Override in the period who may not be Paged.
type Warning struct {
	Kind     string
	User     Person
	Layer    *LayerRef
	Override string
}

// Result is the Result of a Schedule over a period: its segments, the Shifts of each Rotation layer, from the lowest,
// the Overrides in the period, in the order they were created, and the warnings.
type Result struct {
	From, To  time.Time
	Segments  []Segment
	Layers    []LayerShifts
	Overrides []Override
	Warnings  []Warning
}

// compute is the Result of the layers, from the lowest, and the overrides, in the order they were created, over the
// period from from to to in the time zone loc. skipped says why a person may not be Paged, by User id.
func compute(loc *time.Location, layers []Layer, overrides []Override, skipped map[int64]string, from,
	to time.Time) Result {
	res := Result{From: from, To: to, Segments: []Segment{}, Layers: make([]LayerShifts, len(layers)),
		Overrides: []Override{}, Warnings: []Warning{}}
	bounds := []time.Time{from, to}
	for i, l := range layers {
		ref := LayerRef{PublicID: l.PublicID, Name: l.Name}
		res.Layers[i] = LayerShifts{Layer: ref, Shifts: []LayerShift{}}
		warned := map[int64]bool{}
		for _, sh := range l.Shifts(loc, from, to) {
			reason := skipped[sh.User.ID]
			res.Layers[i].Shifts = append(res.Layers[i].Shifts, LayerShift{Shift: sh, Skipped: reason})
			bounds = append(bounds, sh.Start, sh.End)
			if reason != "" && !warned[sh.User.ID] {
				warned[sh.User.ID] = true
				res.Warnings = append(res.Warnings, Warning{Kind: reason, User: sh.User, Layer: &ref})
			}
		}
	}
	for _, o := range overrides {
		if !o.End.After(from) || !o.Start.Before(to) {
			continue
		}
		res.Overrides = append(res.Overrides, o)
		bounds = append(bounds, later(o.Start, from), earlier(o.End, to))
		if reason := skipped[o.User.ID]; reason != "" {
			res.Warnings = append(res.Warnings, Warning{Kind: reason, User: o.User, Override: o.PublicID})
		}
	}
	slices.SortFunc(bounds, time.Time.Compare)
	bounds = slices.CompactFunc(bounds, time.Time.Equal)
	next := make([]int, len(layers)) // the first Shift of each layer that may still cover a segment
	for i := 0; i+1 < len(bounds); i++ {
		a, b := bounds[i], bounds[i+1]
		if !a.Before(to) {
			break
		}
		if a.Before(from) {
			continue
		}
		seg := Segment{Start: a, End: b}
		decide(&seg, res, next, skipped, a)
		res.Segments = appendSegment(res.Segments, seg)
	}
	return res
}

// decide sets the person and the source of the segment starting at a: the Override created last that covers it and
// whose person may be Paged, else the highest Rotation layer with a Shift then whose person may be Paged, else nobody.
func decide(seg *Segment, res Result, next []int, skipped map[int64]string, a time.Time) {
	for i := len(res.Overrides) - 1; i >= 0; i-- {
		o := res.Overrides[i]
		if !o.Start.After(a) && o.End.After(a) && skipped[o.User.ID] == "" {
			user := o.User
			seg.User, seg.Source = &user, &Source{Kind: SourceOverride, Override: o.PublicID}
			return
		}
	}
	for i := len(res.Layers) - 1; i >= 0; i-- {
		shifts := res.Layers[i].Shifts
		for next[i] < len(shifts) && !shifts[next[i]].End.After(a) {
			next[i]++
		}
		if next[i] == len(shifts) {
			continue
		}
		sh := shifts[next[i]]
		if !sh.Start.After(a) && sh.Skipped == "" {
			user := sh.User
			seg.User, seg.Source = &user, &Source{Kind: SourceRotationLayer, Layer: res.Layers[i].Layer}
			return
		}
	}
}

// appendSegment appends seg, joining it to the last segment when they touch and have the same person and source.
func appendSegment(list []Segment, seg Segment) []Segment {
	if n := len(list); n > 0 && list[n-1].End.Equal(seg.Start) && samePerson(list[n-1].User, seg.User) &&
		sameSource(list[n-1].Source, seg.Source) {
		list[n-1].End = seg.End
		return list
	}
	return append(list, seg)
}

func samePerson(a, b *Person) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID
}

func sameSource(a, b *Source) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
