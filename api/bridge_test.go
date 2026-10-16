// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// markKey marks a schema of openapi.yaml as a copy of the schema of bridge.yaml it names (C-22.FR-20).
const markKey = "x-bridge-schema"

const schemaRef = "#/components/schemas/"

// dataKeys hold values, not schemas: they are compared as they are.
var dataKeys = []string{"enum", "const", "default", "example", "examples"}

func parseYAML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func schemasOf(doc map[string]any) map[string]any {
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	return schemas
}

// compareCopies returns one line per copy in api that differs from its original in bridge, apart from descriptions;
// a reference and a discriminator mapping are compared by the x-bridge-schema of the schema they name.
func compareCopies(api, bridge map[string]any) []string {
	apiSchemas, bridgeSchemas := schemasOf(api), schemasOf(bridge)
	var diffs []string
	for _, name := range slices.Sorted(maps.Keys(apiSchemas)) {
		s, _ := apiSchemas[name].(map[string]any)
		mark, marked := s[markKey]
		if !marked {
			continue
		}
		original, _ := mark.(string)
		if _, ok := bridgeSchemas[original]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s: %s %q names no schema of bridge.yaml", name, markKey, mark))
			continue
		}
		if _, ok := apiSchemas[original]; ok {
			diffs = append(diffs, fmt.Sprintf("%s: openapi.yaml has a schema named %s, a name of the Bridge protocol",
				name, original))
		}
		var problems []string
		copied := normalize(s, func(target string) string {
			t, _ := apiSchemas[target].(map[string]any)
			m, ok := t[markKey].(string)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: it refers to %s, which is not a copy of a Bridge "+
					"protocol schema", name, target))
				return "not a copy: " + target
			}
			return m
		}, false)
		want := normalize(bridgeSchemas[original], func(target string) string { return target }, false)
		diffs = append(diffs, problems...)
		if path, ok := firstDiff(copied, want, ""); ok {
			diffs = append(diffs, fmt.Sprintf("%s differs from %s of bridge.yaml at %q", name, original, path))
		}
	}
	return diffs
}

// normalize drops descriptions and marks and replaces every schema reference by the name ref gives its target. In a
// map of names (properties), every key is kept.
func normalize(v any, ref func(target string) string, names bool) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			switch {
			case names:
				out[k] = normalize(val, ref, false)
			case k == "description" || k == markKey:
			case slices.Contains(dataKeys, k):
				out[k] = val
			case k == "$ref":
				out[k] = refValue(val, ref)
			case k == "discriminator":
				d, _ := val.(map[string]any)
				nd := maps.Clone(d)
				if m, ok := d["mapping"].(map[string]any); ok {
					nm := make(map[string]any, len(m))
					for mk, mv := range m {
						nm[mk] = refValue(mv, ref)
					}
					nd["mapping"] = nm
				}
				out[k] = nd
			case k == "properties" || k == "patternProperties" || k == "$defs":
				out[k] = normalize(val, ref, true)
			default:
				out[k] = normalize(val, ref, false)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalize(item, ref, false)
		}
		return out
	}
	return v
}

func refValue(v any, ref func(target string) string) any {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, schemaRef) {
		return v
	}
	return "schema:" + ref(strings.TrimPrefix(s, schemaRef))
}

// firstDiff is the JSON pointer of the first place where a and b differ.
func firstDiff(a, b any, path string) (string, bool) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := slices.Sorted(maps.Keys(am))
		for k := range bm {
			if _, ok := am[k]; !ok {
				keys = append(keys, k)
			}
		}
		for _, k := range keys {
			p := path + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
			av, ain := am[k]
			bv, bin := bm[k]
			if ain != bin {
				return p, true
			}
			if d, ok := firstDiff(av, bv, p); ok {
				return d, true
			}
		}
		return "", false
	}
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok && bok {
		if len(as) != len(bs) {
			return path, true
		}
		for i := range as {
			if d, ok := firstDiff(as[i], bs[i], path+"/"+strconv.Itoa(i)); ok {
				return d, true
			}
		}
		return "", false
	}
	if !reflect.DeepEqual(a, b) {
		return path, true
	}
	return "", false
}

func bridgeDoc(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("bridge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return parseYAML(t, data)
}

// TestBridgeSchemaCopies covers C-22.FR-20: every schema of openapi.yaml marked x-bridge-schema equals its original in
// bridge.yaml, and the copies of info are there.
func TestBridgeSchemaCopies(t *testing.T) {
	api, bridge := parseYAML(t, Spec()), bridgeDoc(t)
	if diffs := compareCopies(api, bridge); len(diffs) > 0 {
		t.Fatalf("copies differ:\n%s", strings.Join(diffs, "\n"))
	}
	for name, original := range map[string]string{"BridgeInfo": "Info", "BridgeCapabilities": "Capabilities",
		"BridgeLimits": "Limits", "BridgeButtonStyle": "ButtonStyle"} {
		s, _ := schemasOf(api)[name].(map[string]any)
		if s[markKey] != original {
			t.Errorf("%s is not marked as a copy of %s", name, original)
		}
	}
}

// TestBridgeSchemaCopiesFindDifferences: a changed bound, a reference to a schema that is not a copy and a mapping that
// names another schema each differ; a changed description does not.
func TestBridgeSchemaCopiesFindDifferences(t *testing.T) {
	schema := func(doc map[string]any, path ...string) map[string]any {
		m := schemasOf(doc)
		for _, p := range path {
			m = m[p].(map[string]any)
		}
		return m
	}
	for _, tc := range []struct {
		name   string
		mutate func(api map[string]any)
		want   string
	}{
		{"a bound", func(api map[string]any) {
			schema(api, "BridgeLimits", "properties", "max_body_length")["minimum"] = 400
		}, `BridgeLimits differs from Limits of bridge.yaml at "/properties/max_body_length/minimum"`},
		{"a reference to a schema that is not a copy", func(api map[string]any) {
			schema(api, "BridgeInfo", "properties", "limits")["$ref"] = schemaRef + "Limiter"
		}, "BridgeInfo: it refers to Limiter, which is not a copy of a Bridge protocol schema"},
		{"a reference to another copy", func(api map[string]any) {
			schema(api, "BridgeInfo", "properties", "limits")["$ref"] = schemaRef + "BridgeCapabilities"
		}, `BridgeInfo differs from Info of bridge.yaml at "/properties/limits/$ref"`},
		{"a missing property", func(api map[string]any) {
			delete(schema(api, "BridgeInfo", "properties"), "link_url_template")
		}, `BridgeInfo differs from Info of bridge.yaml at "/properties/link_url_template"`},
		{"an unknown original", func(api map[string]any) {
			schema(api, "BridgeButtonStyle")[markKey] = "Colour"
		}, `BridgeButtonStyle: x-bridge-schema "Colour" names no schema of bridge.yaml`},
		{"a generic name", func(api map[string]any) {
			schemasOf(api)["Limits"] = map[string]any{"type": "object"}
		}, "BridgeLimits: openapi.yaml has a schema named Limits, a name of the Bridge protocol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := parseYAML(t, Spec())
			tc.mutate(api)
			diffs := compareCopies(api, bridgeDoc(t))
			if !slices.Contains(diffs, tc.want) {
				t.Fatalf("diffs %q, want %q", diffs, tc.want)
			}
		})
	}
	t.Run("a description", func(t *testing.T) {
		api := parseYAML(t, Spec())
		schema(api, "BridgeLimits")["description"] = "Something else entirely."
		schema(api, "BridgeLimits", "properties", "max_body_length")["description"] = "Shorter."
		if diffs := compareCopies(api, bridgeDoc(t)); len(diffs) > 0 {
			t.Fatalf("diffs %q", diffs)
		}
	})
}

// TestBridgeSchemaCopiesCompareMappings: a discriminator mapping is compared by the x-bridge-schema of the schema it
// names, and a property named description is a property.
func TestBridgeSchemaCopiesCompareMappings(t *testing.T) {
	doc := func(schemas map[string]any) map[string]any {
		return map[string]any{"components": map[string]any{"schemas": schemas}}
	}
	bridge := doc(map[string]any{
		"Event": map[string]any{"oneOf": []any{map[string]any{"$ref": schemaRef + "Press"}},
			"discriminator": map[string]any{"propertyName": "type", "mapping": map[string]any{
				"press": schemaRef + "Press"}}},
		"Press": map[string]any{"type": "object", "properties": map[string]any{
			"description": map[string]any{"type": "string"}}},
		"Other": map[string]any{"type": "object"},
	})
	api := func(target, propertyType string) map[string]any {
		return doc(map[string]any{
			"BridgeEvent": map[string]any{markKey: "Event", "description": "Copied.",
				"oneOf": []any{map[string]any{"$ref": schemaRef + "BridgePress"}},
				"discriminator": map[string]any{"propertyName": "type", "mapping": map[string]any{
					"press": schemaRef + target}}},
			"BridgePress": map[string]any{markKey: "Press", "type": "object", "properties": map[string]any{
				"description": map[string]any{"type": propertyType}}},
			"BridgeOther": map[string]any{markKey: "Other", "type": "object"},
		})
	}
	if diffs := compareCopies(api("BridgePress", "string"), bridge); len(diffs) > 0 {
		t.Fatalf("equal copies differ: %q", diffs)
	}
	if diffs := compareCopies(api("BridgeOther", "string"), bridge); !slices.Contains(diffs,
		`BridgeEvent differs from Event of bridge.yaml at "/discriminator/mapping/press"`) {
		t.Fatalf("a mapping to another schema: %q", diffs)
	}
	if diffs := compareCopies(api("BridgePress", "integer"), bridge); !slices.Contains(diffs,
		`BridgePress differs from Press of bridge.yaml at "/properties/description/type"`) {
		t.Fatalf("a property named description: %q", diffs)
	}
}
