// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"fmt"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/bridge/gen"
)

func problemOfClass(class string) *gen.Problem {
	c := gen.ErrorClass(class)
	return &gen.Problem{Class: &c}
}

// TestClassifyByStatus covers C-22.FR-8 for answers without a problem class: one case per status.
func TestClassifyByStatus(t *testing.T) {
	const ra = 7 * time.Second
	for _, tc := range []struct {
		status int
		ra     bool
		want   Class
	}{
		{0, false, ClassTransient},
		{200, false, ""},
		{204, false, ""},
		{301, false, ClassInvalid},
		{302, false, ClassInvalid},
		{307, false, ClassInvalid},
		{400, false, ClassInvalid},
		{401, false, ClassForbidden},
		{403, false, ClassForbidden},
		{404, false, ClassInvalid},
		{408, false, ClassTransient},
		{409, false, ClassInvalid},
		{410, false, ClassInvalid},
		{422, false, ClassInvalid},
		{429, true, ClassRateLimited},
		{429, false, ClassRateLimited},
		{500, false, ClassTransient},
		{502, false, ClassTransient},
		{503, true, ClassRateLimited},
		{503, false, ClassTransient},
		{504, false, ClassTransient},
	} {
		t.Run(fmt.Sprintf("%d retry-after %v", tc.status, tc.ra), func(t *testing.T) {
			var delay time.Duration
			if tc.ra {
				delay = ra
			}
			c := Classify(tc.status, delay, tc.ra, nil)
			if c.Class != tc.want {
				t.Fatalf("class %q, want %q", c.Class, tc.want)
			}
			if c.Class == ClassRateLimited && (c.HasRetryAfter != tc.ra || c.RetryAfter != delay || c.Scope != ScopeChannel) {
				t.Fatalf("rate limit %+v", c)
			}
			if c.Class != ClassRateLimited && (c.HasRetryAfter || c.RetryAfter != 0 || c.Scope != "") {
				t.Fatalf("a wait outside rate_limited: %+v", c)
			}
		})
	}
}

// TestNoStatusIsGone: only the bridge decides that a message is gone.
func TestNoStatusIsGone(t *testing.T) {
	for status := range 600 {
		for _, ra := range []bool{false, true} {
			if c := Classify(status, time.Second, ra, nil); c.Class == ClassGone {
				t.Fatalf("status %d is gone", status)
			}
		}
	}
}

// TestClassifyByProblem covers C-22.FR-8: a known class wins over a status that says otherwise.
func TestClassifyByProblem(t *testing.T) {
	bot, channel := gen.Bot, gen.Channel
	edit := gen.EditWindowClosed
	withScope := func(p *gen.Problem, s *gen.ProblemScope) *gen.Problem { p.Scope = s; return p }
	withReason := func(p *gen.Problem) *gen.Problem { p.Reason = &edit; return p }
	for _, tc := range []struct {
		name   string
		status int
		ra     bool
		p      *gen.Problem
		want   Classification
	}{
		{"transient over 400", 400, false, problemOfClass("transient"), Classification{Class: ClassTransient}},
		{"forbidden over 500", 500, false, problemOfClass("forbidden"), Classification{Class: ClassForbidden}},
		{"invalid over 503 with Retry-After", 503, true, problemOfClass("invalid"),
			Classification{Class: ClassInvalid}},
		{"gone over 404", 404, false, problemOfClass("gone"), Classification{Class: ClassGone}},
		{"gone over 200", 200, false, problemOfClass("gone"), Classification{Class: ClassGone}},
		{"gone past the edit window", 409, false, withReason(problemOfClass("gone")),
			Classification{Class: ClassGone, Reason: ReasonEditWindowClosed}},
		{"rate limited for the bot", 400, true, withScope(problemOfClass("rate_limited"), &bot),
			Classification{Class: ClassRateLimited, RetryAfter: 3 * time.Second, HasRetryAfter: true, Scope: ScopeBot}},
		{"rate limited for the channel", 429, true, withScope(problemOfClass("rate_limited"), &channel),
			Classification{Class: ClassRateLimited, RetryAfter: 3 * time.Second, HasRetryAfter: true,
				Scope: ScopeChannel}},
		{"rate limited by default for the channel", 429, false, problemOfClass("rate_limited"),
			Classification{Class: ClassRateLimited, Scope: ScopeChannel}},
		{"a 429 without a class keeps the problem's scope", 429, true, withScope(&gen.Problem{}, &bot),
			Classification{Class: ClassRateLimited, RetryAfter: 3 * time.Second, HasRetryAfter: true, Scope: ScopeBot}},
		{"unknown class: by status", 401, false, problemOfClass("teapot"), Classification{Class: ClassForbidden}},
		{"no class: by status", 404, false, &gen.Problem{}, Classification{Class: ClassInvalid}},
		{"a reason outside gone is ignored", 404, false, withReason(problemOfClass("invalid")),
			Classification{Class: ClassInvalid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var delay time.Duration
			if tc.ra {
				delay = 3 * time.Second
			}
			if got := Classify(tc.status, delay, tc.ra, tc.p); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
