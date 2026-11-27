package yaml

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestUnmarshalStrictPreservesOptions(t *testing.T) {
	original := func(d *json.Decoder) *json.Decoder { return d }
	other := func(d *json.Decoder) *json.Decoder { d.UseNumber(); return d }
	opts := make([]JSONOpt, 2)
	opts[0], opts[1] = original, other
	if err := UnmarshalStrict([]byte("value: 1"), &struct{ Value int }{}, opts[:1]...); err != nil {
		t.Fatal(err)
	}
	decoder := opts[1](json.NewDecoder(strings.NewReader("1")))
	var got interface{}
	if err := decoder.Decode(&got); err != nil || got != json.Number("1") {
		t.Fatalf("option beyond the supplied slice = %#v (%T), %v", got, got, err)
	}
}

func TestJSONFieldTagOptions(t *testing.T) {
	for _, decode := range []func([]byte, interface{}, ...JSONOpt) error{Unmarshal, UnmarshalStrict} {
		var got struct {
			Value int    `json:"value,string,omitempty"`
			Name  string `json:",omitempty"`
		}
		if err := decode([]byte("value: \"12\"\nName: 42\n"), &got); err != nil || got.Value != 12 || got.Name != "42" {
			t.Errorf("tagged fields = %#v, %v", got, err)
		}
	}
}

func TestJSONObjectToYAMLNumberBounds(t *testing.T) {
	tests := []struct {
		input float64
		want  interface{}
	}{
		{42.0, int(42)},
		{-0x1p63, int64(-1 << 63)},
		{0x1p63, uint64(1 << 63)},
		{0x1p64, float64(0x1p64)},
		{math.Inf(1), math.Inf(1)},
	}
	for _, tt := range tests {
		got := jsonToYAMLValue(tt.input)
		// On 64-bit platforms -2^63 is narrowed to int.
		if tt.input == -0x1p63 && reflect.TypeOf(int(0)).Bits() == 64 {
			minInt64 := int64(-1 << 63)
			tt.want = int(minInt64)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%g: got %#v (%T), want %#v (%T)", tt.input, got, got, tt.want, tt.want)
		}
	}
}

func TestStrictNormalizedKeyCollisions(t *testing.T) {
	for _, input := range []string{"1: a\n\"1\": b\n", "true: a\n\"true\": b\n"} {
		if _, err := YAMLToJSONStrict([]byte(input)); err == nil {
			t.Errorf("expected collision error for %q", input)
		}
	}
	for _, input := range []string{"1: a\n", "\"1\": a\n"} {
		if _, err := YAMLToJSONStrict([]byte(input)); err != nil {
			t.Errorf("%q: %v", input, err)
		}
	}
}

func TestUnmarshalStrictDuplicateStructFields(t *testing.T) {
	type item struct {
		Label string `json:"label"`
	}

	for _, input := range []string{"label: a\nLabel: b\n", "Label: b\nlabel: a\n"} {
		var out item
		if err := UnmarshalStrict([]byte(input), &out); err == nil {
			t.Errorf("expected duplicate field error for %q", input)
		}
	}
	for _, input := range []string{"label: a\n", "Label: a\n"} {
		var out item
		if err := UnmarshalStrict([]byte(input), &out); err != nil || out.Label != "a" {
			t.Errorf("%q: %#v, %v", input, out, err)
		}
	}
	var values map[string]string
	if err := UnmarshalStrict([]byte("label: a\nLabel: b\n"), &values); err != nil || len(values) != 2 {
		t.Fatalf("map: %#v, %v", values, err)
	}
	var nested struct {
		Items []item `json:"items"`
	}
	if err := UnmarshalStrict([]byte("items:\n- label: a\n  Label: b\n"), &nested); err == nil || !strings.Contains(err.Error(), "label") {
		t.Fatalf("nested duplicate error = %v", err)
	}
	if err := UnmarshalStrict([]byte("items:\n- label: a\n- Label: b\n"), &nested); err != nil || len(nested.Items) != 2 {
		t.Fatalf("separate objects = %#v, %v", nested, err)
	}
	var distinct struct {
		Lower string `json:"label"`
		Upper string `json:"Label"`
	}
	if err := UnmarshalStrict([]byte("label: a\nLabel: b\n"), &distinct); err != nil || distinct.Lower != "a" || distinct.Upper != "b" {
		t.Fatalf("distinct fields = %#v, %v", distinct, err)
	}
}
