// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package audit

import (
	"reflect"
	"strings"
	"time"
)

// secretTag marks a field of a diffed struct as a Secret: `audit:"secret"`.
const secretTag = "secret"

var timeType = reflect.TypeFor[time.Time]()

// Diff returns the configured values that differ between before and after, two views of the same resource, as the
// before/after diff of an Audit log entry (C-03.FR-14). Each exported field with a json name is one value at the JSON
// pointer of that name; a nested struct is walked field by field under its own name, and slices and maps are compared
// whole. A pointer is its value, nil being null; a pointer to a nested struct that is nil on one side is walked
// against an empty struct. A field tagged `audit:"secret"` is a Secret (C-03.FR-21): when it
// changes, the change says only that, with no value before or after.
func Diff[T any](before, after T) []Change {
	var out []Change
	diffValue(&out, "", reflect.ValueOf(&before).Elem(), reflect.ValueOf(&after).Elem())
	return out
}

// Created returns the configured values of a resource that was just created, as the diff of its creation: each value
// that is set, with no value before.
func Created[T any](after T) []Change {
	var zero T
	out := Diff(zero, after)
	for i := range out {
		out[i].Before = nil
	}
	return out
}

func diffValue(out *[]Change, pointer string, before, after reflect.Value) {
	t := before.Type()
	if t.Kind() != reflect.Struct || t == timeType {
		if !reflect.DeepEqual(plain(before), plain(after)) {
			*out = append(*out, Change{Pointer: pointer, Before: plain(before), After: plain(after)})
		}
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		p := pointer + "/" + escapePointer(name)
		if f.Tag.Get("audit") == secretTag {
			if !reflect.DeepEqual(before.Field(i).Interface(), after.Field(i).Interface()) {
				*out = append(*out, Change{Pointer: p, SecretChanged: true})
			}
			continue
		}
		b, a := before.Field(i), after.Field(i)
		if b.Kind() == reflect.Pointer && b.Type().Elem().Kind() == reflect.Struct && b.Type().Elem() != timeType {
			// An object that appears or goes is walked against an empty one, so that its Secrets stay masked.
			if b.IsNil() && a.IsNil() {
				continue
			}
			b, a = elemOrZero(b), elemOrZero(a)
		}
		diffValue(out, p, b, a)
	}
}

func elemOrZero(v reflect.Value) reflect.Value {
	if v.IsNil() {
		return reflect.Zero(v.Type().Elem())
	}
	return v.Elem()
}

// plain is the value of v for the diff: a pointer is its value or nil.
func plain(v reflect.Value) any {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	return v.Interface()
}

func escapePointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}
