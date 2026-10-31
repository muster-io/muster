// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// oracleZones are zones with every kind of change of the clocks: none, an hour forward and back in either hemisphere,
// half an hour, odd offsets, and a day skipped.
var oracleZones = []string{"UTC", "Europe/Berlin", "America/New_York", "Australia/Sydney", "America/Santiago",
	"Australia/Lord_Howe", "Asia/Tehran", "Asia/Kathmandu", "Pacific/Apia", "America/Havana", "Asia/Beirut",
	"Europe/Dublin"}

var oraclePeople = []Person{{ID: 1, Login: "a"}, {ID: 2, Login: "b"}, {ID: 3, Login: "c"}, {ID: 4, Login: "d"}}

// oracleWall is the wall-clock time of t in loc as seconds since the epoch read as UTC.
func oracleWall(loc *time.Location, t time.Time) int64 {
	w := t.In(loc)
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC).Unix()
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// oracle is who is on Shift in the layer at t, by plain wall-clock arithmetic on the definition of C-27.FR-2,
// independent of handover, at, cut and window: the Shift index from the wall-clock time elapsed since the start, and
// the windows of the day of t and of the day before.
func oracle(l RotationLayer, loc *time.Location, t time.Time) (Person, bool) {
	w := oracleWall(loc, t)
	y, m, d := l.StartDate.Date()
	hh, mm := clockOf(l.HandoverTime)
	base := time.Date(y, m, d, hh, mm, 0, 0, time.UTC).Unix()
	if w < base {
		return Person{}, false
	}
	var k int64
	switch l.ShiftLength {
	case ShiftDay:
		k = floorDiv(w-base, daySeconds)
	case ShiftHours:
		k = floorDiv(w-base, int64(l.ShiftSeconds))
	case ShiftWeek:
		first := int64((int(weekdayOf[l.HandoverDay]) - int(l.StartDate.Weekday()) + 7) % 7)
		if first == 0 {
			first = 7
		}
		if w >= base+first*daySeconds {
			k = 1 + floorDiv(w-base-first*daySeconds, weekSeconds)
		}
	}
	if l.Limits != nil && !inOracleWindow(l.Limits, w) {
		return Person{}, false
	}
	return l.Users[int(k)%len(l.Users)], true
}

// inOracleWindow reports whether the wall-clock time w is in a window of the limits.
func inOracleWindow(lm *Limits, w int64) bool {
	wd := time.Unix(w, 0).UTC()
	for back := range 2 {
		day := time.Date(wd.Year(), wd.Month(), wd.Day()-back, 0, 0, 0, 0, time.UTC)
		if !slices.Contains(lm.Days, Weekdays[(int(day.Weekday())+6)%7]) {
			continue
		}
		s, e := day.Unix(), day.Unix()+daySeconds
		if lm.Start != "" {
			sh, sm := clockOf(lm.Start)
			eh, em := clockOf(lm.End)
			s, e = day.Unix()+int64(sh*3600+sm*60), day.Unix()+int64(eh*3600+em*60)
			if lm.End <= lm.Start {
				e += daySeconds
			}
		}
		if w >= s && w < e {
			return true
		}
	}
	return false
}

// nearChange reports whether t is within 3 hours of a change of the clocks in loc, where wall-clock arithmetic and
// instants part.
func nearChange(loc *time.Location, t time.Time) bool {
	for _, x := range []time.Time{t.Add(-3 * time.Hour), t, t.Add(3 * time.Hour)} {
		s, e := x.In(loc).ZoneBounds()
		for _, b := range []time.Time{s, e} {
			if !b.IsZero() && b.Sub(t) < 3*time.Hour && t.Sub(b) < 3*time.Hour {
				return true
			}
		}
	}
	return false
}

func randomClock(r *rand.Rand) string {
	if r.IntN(3) == 0 {
		return fmt.Sprintf("%02d:%02d", r.IntN(24), []int{0, 30}[r.IntN(2)])
	}
	return fmt.Sprintf("%02d:%02d", r.IntN(24), r.IntN(60))
}

// randomLayer is a Rotation layer of random length, handover, start and limits, starting from base minus up to
// back days, never before 2000.
func randomLayer(r *rand.Rand, base time.Time, back, maxHours int) RotationLayer {
	l := RotationLayer{Users: oraclePeople[:1+r.IntN(len(oraclePeople))], HandoverTime: randomClock(r)}
	switch r.IntN(3) {
	case 0:
		l.ShiftLength = ShiftDay
	case 1:
		l.ShiftLength, l.HandoverDay = ShiftWeek, Weekdays[r.IntN(7)]
	default:
		l.ShiftLength, l.ShiftSeconds = ShiftHours, 3600*(1+r.IntN(maxHours))
	}
	l.StartDate = later(base.AddDate(0, 0, -r.IntN(back)), FirstStartDate)
	if r.IntN(2) == 0 {
		lm := &Limits{}
		for _, d := range Weekdays {
			if r.IntN(2) == 0 {
				lm.Days = append(lm.Days, d)
			}
		}
		if len(lm.Days) == 0 {
			lm.Days = []string{"friday"}
		}
		if r.IntN(3) > 0 {
			lm.Start, lm.End = randomClock(r), randomClock(r)
			for lm.End == lm.Start {
				lm.End = randomClock(r)
			}
		}
		l.Limits = lm
	}
	return l
}

// TestShiftsOracle checks Shifts against the oracle at random moments of random layers in many zones, away from the
// changes of the clocks, with a fixed seed.
func TestShiftsOracle(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 9)) //nolint:gosec // G404: a fixed seed makes the test deterministic
	for iter := range 3000 {
		loc := zone(t, oracleZones[r.IntN(len(oracleZones))])
		base := time.Date(2000+r.IntN(40), time.Month(1+r.IntN(12)), 1+r.IntN(28), 0, 0, 0, 0, time.UTC)
		l := randomLayer(r, base, 3000, 24*28)
		from := base.Add(time.Duration(r.IntN(20*24*60)) * time.Minute)
		to := from.Add(time.Duration(60+r.IntN(67*24*60)) * time.Minute)
		shifts := l.Shifts(loc, from, to)
		for m := from; m.Before(to); m = m.Add(time.Duration(1+r.IntN(240)) * time.Minute) {
			if nearChange(loc, m) {
				continue
			}
			want, wok := oracle(l, loc, m)
			got, gok := "", false
			for _, s := range shifts {
				if !s.Start.After(m) && s.End.After(m) {
					got, gok = s.User.Login, true
				}
			}
			if wok != gok || (wok && want.Login != got) {
				t.Fatalf("iteration %d in %s at %s (%s): got %v %s, want %v %s; layer %+v, limits %+v", iter, loc,
					m.Format(time.RFC3339), m.In(loc).Format("Mon 01-02 15:04"), gok, got, wok, want.Login, l, l.Limits)
			}
		}
	}
}

// referenceAt is who is On-call at t by the definition of C-27.FR-4, the slow way: the Override created last that
// covers t and whose person may be Paged, else the highest layer with a Shift then whose person may be Paged.
func referenceAt(loc *time.Location, layers []RotationLayer, overrides []Override, skipped map[int64]string,
	t time.Time) string {
	for i := len(overrides) - 1; i >= 0; i-- {
		o := overrides[i]
		if !o.Start.After(t) && o.End.After(t) && skipped[o.User.ID] == "" {
			return o.User.Login + "/" + o.PublicID
		}
	}
	for i := len(layers) - 1; i >= 0; i-- {
		for _, s := range layers[i].Shifts(loc, t, t.Add(time.Second)) {
			if skipped[s.User.ID] == "" {
				return s.User.Login + "/" + layers[i].Name
			}
		}
	}
	return "gap"
}

// resultAt is who the Result has On-call at t.
func resultAt(res Result, t time.Time) string {
	for _, s := range res.Segments {
		if !s.Start.After(t) && s.End.After(t) {
			switch {
			case s.User == nil:
				return "gap"
			case s.Source.Kind == SourceOverride:
				return s.User.Login + "/" + s.Source.Override
			default:
				return s.User.Login + "/" + s.Source.RotationLayer.Name
			}
		}
	}
	return "none"
}

// TestResultReference checks the Result of random layers and Overrides, with a person who may not be Paged, against
// the slow definition at random moments, and that its segments cover the period without a hole, with a fixed seed.
func TestResultReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: a fixed seed makes the test deterministic
	for iter := range 1000 {
		loc := zone(t, oracleZones[r.IntN(len(oracleZones))])
		base := time.Date(2026, time.Month(1+r.IntN(12)), 1+r.IntN(28), 0, 0, 0, 0, time.UTC)
		var layers []RotationLayer
		for i := range 1 + r.IntN(3) {
			l := randomLayer(r, base.AddDate(0, 0, 10), 40, 72)
			l.Name, l.PublicID = fmt.Sprintf("L%d", i+1), fmt.Sprintf("L%d", i+1)
			layers = append(layers, l)
		}
		from := base.Add(time.Duration(r.IntN(20*24*60)) * time.Minute)
		to := from.Add(time.Duration(60+r.IntN(20*24*60)) * time.Minute)
		var overrides []Override
		for i := range r.IntN(6) {
			s := from.Add(time.Duration(r.IntN(30*24*60)-5*24*60) * time.Minute)
			overrides = append(overrides, Override{ID: int64(i), PublicID: fmt.Sprintf("O%d", i),
				User: oraclePeople[r.IntN(len(oraclePeople))], Start: s,
				End: s.Add(time.Duration(1+r.IntN(48*60)) * time.Minute)})
		}
		skipped := map[int64]string{}
		if r.IntN(2) == 0 {
			skipped[oraclePeople[r.IntN(len(oraclePeople))].ID] = ReasonDisabled
		}
		res := compute(loc, layers, overrides, skipped, from, to)
		if len(res.Segments) == 0 || !res.Segments[0].Start.Equal(from) ||
			!res.Segments[len(res.Segments)-1].End.Equal(to) {
			t.Fatalf("iteration %d: the segments do not cover the period", iter)
		}
		for i := 1; i < len(res.Segments); i++ {
			if !res.Segments[i].Start.Equal(res.Segments[i-1].End) {
				t.Fatalf("iteration %d: the segments have a hole at %s", iter, res.Segments[i].Start)
			}
		}
		for m := from; m.Before(to); m = m.Add(time.Duration(1+r.IntN(120)) * time.Minute) {
			if want, got := referenceAt(loc, layers, overrides, skipped, m), resultAt(res, m); want != got {
				t.Fatalf("iteration %d in %s at %s (%s): got %s, want %s; layers %+v", iter, loc,
					m.Format(time.RFC3339), m.In(loc).Format("Mon 2006-01-02 15:04 MST"), got, want, layers)
			}
		}
	}
}

// manyOverrides are n Overrides over the 67 days from from, each starting a step after the one before and lasting
// until the end, so that every one overlaps all those created before it and the one created last wins everywhere it
// has begun.
func manyOverrides(from time.Time, n int) []Override {
	step := 67 * 24 * time.Hour / time.Duration(n)
	out := make([]Override, n)
	for i := range n {
		out[i] = Override{ID: int64(i + 1), PublicID: fmt.Sprintf("VR%012d", i), User: oraclePeople[i%len(oraclePeople)],
			Start: from.Add(time.Duration(i) * step), End: from.Add(67 * 24 * time.Hour)}
	}
	return out
}

// TestManyOverrides is the Result of 5000 overlapping Overrides over 67 days: each segment is the Override that began
// last, and the sweep takes far less than a second.
func TestManyOverrides(t *testing.T) {
	loc := zone(t, "Europe/Berlin")
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	overrides := manyOverrides(from, 5000)
	layer := RotationLayer{Name: "L", PublicID: "L", Users: oraclePeople, ShiftLength: ShiftHours, ShiftSeconds: 3600,
		HandoverTime: "00:00", StartDate: FirstStartDate}
	start := time.Now()
	res := compute(loc, []RotationLayer{layer}, overrides, nil, from, from.Add(67*24*time.Hour))
	took := time.Since(start)
	if len(res.Segments) != 5000 {
		t.Fatalf("%d segments, want 5000", len(res.Segments))
	}
	for i, s := range res.Segments {
		if s.Source.Override != overrides[i].PublicID || !s.Start.Equal(overrides[i].Start) {
			t.Fatalf("segment %d is %s from %s, want %s", i, s.Source.Override, s.Start, overrides[i].PublicID)
		}
	}
	if took > 5*time.Second {
		t.Errorf("5000 Overrides took %s", took)
	}
}

func BenchmarkManyOverrides(b *testing.B) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	overrides := manyOverrides(from, 5000)
	layer := RotationLayer{Name: "L", PublicID: "L", Users: oraclePeople, ShiftLength: ShiftHours, ShiftSeconds: 3600,
		HandoverTime: "00:00", StartDate: FirstStartDate}
	for b.Loop() {
		compute(loc, []RotationLayer{layer}, overrides, nil, from, from.Add(67*24*time.Hour))
	}
}
