// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"container/heap"
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

// skipReason is why a person may not be Paged, empty when they may: a disabled User; a deleted one, who is not in the
// Team since deletion takes a User out of every Team, whatever their Role was; a Viewer; or a User who is not in the
// Team.
func skipReason(p Person, inTeam bool) string {
	switch {
	case p.Status == "disabled":
		return ReasonDisabled
	case p.Status == "deleted":
		return ReasonNotInTeam
	case p.Role == "viewer":
		return ReasonViewer
	case !inTeam || p.Status != "active":
		return ReasonNotInTeam
	}
	return ""
}

// RotationLayerRef names a Rotation layer.
type RotationLayerRef struct {
	PublicID string
	Name     string
}

// Source is what decides a segment of the Result: an Override, or a Rotation layer.
type Source struct {
	Kind          string
	Override      string
	RotationLayer RotationLayerRef
}

// Segment is a segment of the Result with the person On-call in it, or nobody — a gap — when User is nil.
type Segment struct {
	Start, End time.Time
	User       *Person
	Source     *Source
}

// RotationLayerShift is a Shift of a Rotation layer in the Result, with why it is skipped, empty when it is not.
type RotationLayerShift struct {
	Shift
	Skipped string
}

// RotationLayerShifts are the Shifts of one Rotation layer in the period.
type RotationLayerShifts struct {
	RotationLayer RotationLayerRef
	Shifts        []RotationLayerShift
}

// Warning names a person of a Rotation layer or of an Override in the period who may not be Paged.
type Warning struct {
	Kind          string
	User          Person
	RotationLayer *RotationLayerRef
	Override      string
}

// Result is the Result of a Schedule over a period: its segments, the Shifts of each Rotation layer, from the lowest,
// the Overrides in the period, in the order they were created, and the warnings.
type Result struct {
	From, To       time.Time
	Segments       []Segment
	RotationLayers []RotationLayerShifts
	Overrides      []Override
	Warnings       []Warning
}

// compute is the Result of the layers, from the lowest, and the overrides, in the order they were created, over the
// period from from to to in the time zone loc. skipped says why a person may not be Paged, by User id.
func compute(loc *time.Location, layers []RotationLayer, overrides []Override, skipped map[int64]string, from,
	to time.Time) Result {
	res := Result{From: from, To: to, Segments: []Segment{}, RotationLayers: make([]RotationLayerShifts, len(layers)),
		Overrides: []Override{}, Warnings: []Warning{}}
	bounds := []time.Time{from, to}
	for i, l := range layers {
		ref := RotationLayerRef{PublicID: l.PublicID, Name: l.Name}
		res.RotationLayers[i] = RotationLayerShifts{RotationLayer: ref, Shifts: []RotationLayerShift{}}
		warned := map[int64]bool{}
		for _, sh := range l.Shifts(loc, from, to) {
			reason := skipped[sh.User.ID]
			res.RotationLayers[i].Shifts = append(res.RotationLayers[i].Shifts, RotationLayerShift{Shift: sh, Skipped: reason})
			bounds = append(bounds, sh.Start, sh.End)
			if reason != "" && !warned[sh.User.ID] {
				warned[sh.User.ID] = true
				res.Warnings = append(res.Warnings, Warning{Kind: reason, User: sh.User, RotationLayer: &ref})
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
	sweep := newOverrideSweep(res.Overrides, skipped)
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
		if o, ok := sweep.at(a); ok {
			user := o.User
			seg.User, seg.Source = &user, &Source{Kind: SourceOverride, Override: o.PublicID}
		} else {
			decide(&seg, res, next, a)
		}
		res.Segments = appendSegment(res.Segments, seg)
	}
	return res
}

// overrideSweep finds the Override of each segment, in the order of the segments: of those that cover its start and
// whose person may be Paged, the one created last (P-65). It adds each Override when the segments reach its start, to a
// heap that keeps the one created last on top, and drops those that have ended from the top, so that S segments and
// N Overrides take O((S+N) log N).
type overrideSweep struct {
	overrides []Override
	byStart   []int // the Overrides whose person may be Paged, by start
	next      int   // the first of byStart not added yet
	active    createdLast
}

func newOverrideSweep(overrides []Override, skipped map[int64]string) *overrideSweep {
	s := &overrideSweep{overrides: overrides}
	for i, o := range overrides {
		if skipped[o.User.ID] == "" {
			s.byStart = append(s.byStart, i)
		}
	}
	slices.SortStableFunc(s.byStart, func(x, y int) int { return overrides[x].Start.Compare(overrides[y].Start) })
	return s
}

// at is the Override of the segment that starts at a; the segments are asked in order.
func (s *overrideSweep) at(a time.Time) (Override, bool) {
	for s.next < len(s.byStart) && !s.overrides[s.byStart[s.next]].Start.After(a) {
		heap.Push(&s.active, s.byStart[s.next])
		s.next++
	}
	for s.active.Len() > 0 && !s.overrides[s.active[0]].End.After(a) {
		heap.Pop(&s.active)
	}
	if s.active.Len() == 0 {
		return Override{}, false
	}
	return s.overrides[s.active[0]], true
}

// createdLast is a heap of positions in the order of creation with the highest, the Override created last, on top.
type createdLast []int

func (h createdLast) Len() int           { return len(h) }
func (h createdLast) Less(i, j int) bool { return h[i] > h[j] }
func (h createdLast) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *createdLast) Push(x any)        { *h = append(*h, x.(int)) } //nolint:forcetypeassert // only ints are pushed

func (h *createdLast) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// decide sets the person and the source of a segment that no Override decides, starting at a: the highest Rotation
// layer with a Shift then whose person may be Paged, else nobody.
func decide(seg *Segment, res Result, next []int, a time.Time) {
	for i := len(res.RotationLayers) - 1; i >= 0; i-- {
		shifts := res.RotationLayers[i].Shifts
		for next[i] < len(shifts) && !shifts[next[i]].End.After(a) {
			next[i]++
		}
		if next[i] == len(shifts) {
			continue
		}
		sh := shifts[next[i]]
		if !sh.Start.After(a) && sh.Skipped == "" {
			user := sh.User
			seg.User, seg.Source = &user, &Source{Kind: SourceRotationLayer, RotationLayer: res.RotationLayers[i].RotationLayer}
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
