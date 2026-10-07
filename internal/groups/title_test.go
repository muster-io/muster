// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"bytes"
	"maps"
	"testing"
)

// TestKeyOf is C-08.FR-4: the Group key values in the key's order, a missing label as the empty value, and a hash
// that tells apart values that would read the same when joined.
func TestKeyOf(t *testing.T) {
	key := []string{"cluster", "namespace"}
	values, sha := keyOf(key, map[string]string{"cluster": "prod", "namespace": "payments", "pod": "p"})
	if !maps.Equal(values, map[string]string{"cluster": "prod", "namespace": "payments"}) || len(sha) != 32 {
		t.Errorf("values %v", values)
	}
	if keyText(key, values) != "cluster=prod, namespace=payments" {
		t.Errorf("text %q", keyText(key, values))
	}
	missing, a := keyOf(key, map[string]string{"cluster": "prod"})
	_, b := keyOf(key, map[string]string{"cluster": "prod", "namespace": ""})
	if missing["namespace"] != "" || !bytes.Equal(a, b) {
		t.Error("a missing label is not the empty value")
	}
	_, c := keyOf([]string{"a", "b"}, map[string]string{"a": "x,b=y"})
	_, d := keyOf([]string{"a", "b"}, map[string]string{"a": "x", "b": "y"})
	if bytes.Equal(c, d) {
		t.Error("ambiguous hash")
	}
	_, e := keyOf([]string{"namespace", "cluster"}, map[string]string{"cluster": "prod", "namespace": "payments"})
	if _, f := keyOf(key, map[string]string{"cluster": "prod", "namespace": "payments"}); bytes.Equal(e, f) {
		t.Error("the key's order is not part of the hash")
	}
}

// TestReplacedLabel is C-09.FR-7: only Instance labels may differ, in either direction.
func TestReplacedLabel(t *testing.T) {
	instance := []string{"pod", "instance"}
	for _, c := range []struct {
		next, firing map[string]string
		want         string
	}{
		{map[string]string{"a": "1", "pod": "x"}, map[string]string{"a": "1", "pod": "y"}, "pod"},
		{map[string]string{"a": "1", "pod": "x"}, map[string]string{"a": "1"}, "pod"},
		{map[string]string{"a": "1"}, map[string]string{"a": "1", "instance": "i"}, "instance"},
		{map[string]string{"a": "1", "pod": "x", "instance": "i"}, map[string]string{"a": "1", "pod": "y"},
			"instance, pod"},
		{map[string]string{"a": "1", "pod": "x"}, map[string]string{"a": "2", "pod": "y"}, ""},
		{map[string]string{"a": "1"}, map[string]string{"a": "1"}, ""},
	} {
		if got := replacedLabel(c.next, c.firing, instance); got != c.want {
			t.Errorf("replacedLabel(%v, %v) = %q, want %q", c.next, c.firing, got, c.want)
		}
	}
}

// TestCommon: the common labels keep the pairs all Alerts share.
func TestCommon(t *testing.T) {
	got := common(map[string]string{"a": "1", "b": "2", "c": "3"}, map[string]string{"a": "1", "b": "x"})
	if !maps.Equal(got, map[string]string{"a": "1"}) {
		t.Errorf("common = %v", got)
	}
}
