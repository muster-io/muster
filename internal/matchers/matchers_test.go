// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package matchers

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tt := range []struct {
		in    string
		name  string
		op    Op
		value string
	}{
		{`namespace="payments"`, "namespace", Equal, "payments"},
		{` pod =~ "api-.*" `, "pod", Regexp, "api-.*"},
		{`env!="prod"`, "env", NotEqual, "prod"},
		{`env!~"dev|test"`, "env", NotRegexp, "dev|test"},
		{`team=db`, "team", Equal, "db"},
		{`team=`, "team", Equal, ""},
		{`team=""`, "team", Equal, ""},
		{`msg="say \"hi\""`, "msg", Equal, `say "hi"`},
		{`"service.name"="api"`, "service.name", Equal, "api"},
		{`__name__="up"`, "__name__", Equal, "up"},
		{`a:b="c"`, "a:b", Equal, "c"},
		{`x1="y"`, "x1", Equal, "y"},
	} {
		m, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q) = %v", tt.in, err)
			continue
		}
		if m.Name != tt.name || m.Op != tt.op || m.Value != tt.value {
			t.Errorf("Parse(%q) = %q %q %q", tt.in, m.Name, m.Op, m.Value)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{``, `="x"`, `1a="x"`, `a`, `a~"x"`, `a="x`, `a="x" b`, `a=x"y`, `"a="x"`, `""="x"`,
		`"a\q"="x"`, `a="\q"`} {
		if _, err := Parse(in); !errors.Is(err, ErrSyntax) {
			t.Errorf("Parse(%q) = %v, want ErrSyntax", in, err)
		}
	}
	_, err := Parse(`a=~"api-(.*"`)
	var re *RegexpError
	if !errors.As(err, &re) || re.Unwrap() == nil || re.Error() == "" {
		t.Errorf("an invalid regexp = %v", err)
	}
	if _, err := New("a", "<>", "x"); !errors.Is(err, ErrSyntax) {
		t.Errorf("New with a bad operator = %v", err)
	}
	if _, err := New("", Equal, "x"); !errors.Is(err, ErrSyntax) {
		t.Errorf("New without a name = %v", err)
	}
}

func TestMatches(t *testing.T) {
	labels := map[string]string{"pod": "api-7", "env": "prod", "empty": ""}
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{`pod="api-7"`, true},
		{`pod="api"`, false},
		{`pod!="api"`, true},
		{`pod=~"api-.*"`, true},
		{`pod=~"api"`, false}, // anchored at both ends
		{`pod=~"pi-7"`, false},
		{`pod!~"web-.*"`, true},
		{`pod!~"api-.*"`, false},
		{`missing=""`, true}, // a missing label is the empty value
		{`missing!=""`, false},
		{`missing=~".*"`, true},
		{`missing=~".+"`, false},
		{`empty=""`, true},
		{`env=~"prod|staging"`, true},
	} {
		m, err := Parse(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Matches(labels); got != tt.want {
			t.Errorf("%s matches = %v, want %v", tt.in, got, tt.want)
		}
	}
	if (Matcher{Name: "a", Op: "?"}).Matches(labels) {
		t.Error("an unknown operator matched")
	}
}

func TestAllAndString(t *testing.T) {
	a, _ := Parse(`env="prod"`)
	b, _ := Parse(`pod=~"api-.*"`)
	labels := map[string]string{"pod": "api-7", "env": "prod"}
	if !All([]Matcher{a, b}, labels) || All([]Matcher{a, b}, map[string]string{"env": "prod"}) || !All(nil, nil) {
		t.Error("All")
	}
	if s := b.String(); s != `pod=~"api-.*"` {
		t.Errorf("String = %s", s)
	}
}
