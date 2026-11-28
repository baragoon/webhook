package yaml

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	goyaml "go.yaml.in/yaml/v2"
)

func TestConversionDoesNotAllocateDestinationOnError(t *testing.T) {
	type item struct {
		Label string `json:"label"`
	}
	var out struct {
		Item *item `json:"item"`
	}
	if err := UnmarshalStrict([]byte("item:\n  label: first\n  Label: second\n"), &out); err == nil {
		t.Fatal("expected duplicate field error")
	}
	if out.Item != nil {
		t.Fatalf("destination was allocated: %#v", out.Item)
	}
	if err := UnmarshalStrict([]byte("item:\n  label: valid\n"), &out); err != nil || out.Item == nil || out.Item.Label != "valid" {
		t.Fatalf("valid destination = %#v, %v", out.Item, err)
	}
}

type embeddedFields struct {
	Other struct {
		Label string `json:"label"`
	} `json:"other"`
	Promoted string `json:"promoted"`
}

func TestPromotedFieldsAndDuplicates(t *testing.T) {
	var out struct {
		embeddedFields
		Direct string `json:"Promoted"`
	}
	if err := UnmarshalStrict([]byte("promoted: inner\nPromoted: direct\n"), &out); err != nil || out.Promoted != "inner" || out.Direct != "direct" {
		t.Fatalf("distinct promoted fields = %#v, %v", out, err)
	}
	if err := UnmarshalStrict([]byte("other:\n  label: one\n  Label: two\n"), &out); err == nil {
		t.Fatal("expected duplicate in promoted nested field")
	}
}

func TestStrictFixedArrayElements(t *testing.T) {
	var out struct {
		Items [1]struct {
			Label string `json:"label"`
		} `json:"items"`
	}
	if err := UnmarshalStrict([]byte("items:\n- label: one\n  Label: two\n"), &out); err == nil {
		t.Fatal("expected duplicate field in array element")
	}
	if err := UnmarshalStrict([]byte("items:\n- label: one\n- label: extra\n  Label: ignored\n"), &out); err != nil || out.Items[0].Label != "one" {
		t.Fatalf("excess array entry = %#v, %v", out, err)
	}
}

type numericString string

func (s *numericString) UnmarshalJSON(data []byte) error {
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = numericString(string(data))
	return nil
}

func TestMapElementCustomDecoder(t *testing.T) {
	var values map[string]numericString
	if err := Unmarshal([]byte("answer: 42"), &values); err != nil || values["answer"] != "42" {
		t.Fatalf("custom values = %#v, %v", values, err)
	}
	var stringsMap map[string]string
	if err := Unmarshal([]byte("answer: 42"), &stringsMap); err != nil || stringsMap["answer"] != "42" {
		t.Fatalf("string values = %#v, %v", stringsMap, err)
	}
}

func TestIndirectInaccessiblePointer(t *testing.T) {
	value := numericString("42")
	target := struct{ hidden *numericString }{hidden: &value}
	_, _, got, err := indirect(reflect.ValueOf(target).Field(0), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind() != reflect.String {
		t.Fatalf("inspected value kind = %v", got.Kind())
	}
}

type recursivePointer *recursivePointer

func TestIndirectPointerCycles(t *testing.T) {
	var first, second interface{}
	first, second = &second, &first
	if err := Unmarshal([]byte("value"), &first); err == nil || !strings.Contains(err.Error(), "cyclic pointer") {
		t.Fatalf("pointer cycle error = %v", err)
	}
	var recursive recursivePointer
	if err := Unmarshal([]byte("value"), &recursive); err == nil || !strings.Contains(err.Error(), "cyclic pointer type") {
		t.Fatalf("recursive pointer type error = %v", err)
	}
	var self interface{}
	self = &self
	if err := Unmarshal([]byte("value"), &self); err != nil || self != "value" {
		t.Fatalf("self-reference = %#v, %v", self, err)
	}
	var finite **string
	if err := Unmarshal([]byte("42"), &finite); err != nil || finite == nil || *finite == nil || **finite != "42" {
		t.Fatalf("finite pointers = %#v, %v", finite, err)
	}
}

func TestStrictReusedSequenceElements(t *testing.T) {
	type item struct {
		Label string `json:"label"`
	}
	reused := []interface{}{&item{}}
	reused = reused[:0]
	for _, out := range []interface{}{
		&[]interface{}{&item{}},
		&[1]interface{}{&item{}},
		&reused,
	} {
		if err := UnmarshalStrict([]byte("- label: one\n  Label: two\n"), out); err == nil {
			t.Errorf("%T accepted duplicate field", out)
		}
	}
	out := []interface{}{&item{}}
	if err := UnmarshalStrict([]byte("- label: one\n"), &out); err != nil || out[0].(*item).Label != "one" {
		t.Fatalf("single field = %#v, %v", out, err)
	}
}

func TestTimestampJSONKeySpellings(t *testing.T) {
	for _, key := range []string{"2024-01-01", "2024-01-01T00:00:00Z"} {
		doc := key + ": value\n"
		data, err := YAMLToJSONStrict([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]string
		if err := json.Unmarshal(data, &out); err != nil || len(out) != 1 || out[key] != "value" {
			t.Fatalf("%q = %s, %#v, %v", doc, data, out, err)
		}
		if _, err := YAMLToJSONStrict([]byte(doc + "\"" + key + "\": other\n")); err == nil {
			t.Fatalf("timestamp/string JSON collision accepted: %q", doc)
		}
	}
}

func TestExplicitTimestampJSONKeySpellings(t *testing.T) {
	input := []byte("dates: {!!timestamp 2024-01-01: first, 2024-01-01T00:00:00Z: second}")
	want := map[string]map[string]string{"dates": {
		"2024-01-01": "first", "2024-01-01T00:00:00Z": "second",
	}}
	for _, convert := range []func([]byte) ([]byte, error){YAMLToJSON, YAMLToJSONStrict} {
		data, err := convert(input)
		var got map[string]map[string]string
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("conversion = %s, %#v, %v", data, got, err)
		}
	}
}

func TestMergePrecedenceForStructFields(t *testing.T) {
	type config struct {
		ID   string            `json:"id"`
		Tags map[string]string `json:"tags"`
	}
	for _, strict := range []bool{false, true} {
		decode := Unmarshal
		if strict {
			decode = UnmarshalStrict
		}
		for _, doc := range []string{
			"ID: chosen\n<<: {id: fallback}\n",
			"id: chosen\n<<: {ID: fallback}\n",
			"<<: {id: fallback}\nID: chosen\n",
			"<<: [{ID: chosen}, {id: fallback}]\n",
			"<<: [{<<: {ID: chosen}}, {id: fallback}]\n",
			"<<: [{ID: chosen, <<: {id: fallback}}, {Id: later}]\n",
			"<<: {<<: {id: fallback}, ID: chosen}\n",
			"ID: chosen\n<<: {id: {nested: value}}\n",
		} {
			var got config
			if err := decode([]byte(doc), &got); err != nil || got.ID != "chosen" {
				t.Errorf("strict=%t %q: %#v, %v", strict, doc, got, err)
			}
			var inherited config
			if err := decode([]byte("<<: {ID: inherited}\n"), &inherited); err != nil || inherited.ID != "inherited" {
				t.Errorf("strict=%t inherited field = %#v, %v", strict, inherited, err)
			}
		}
		var got config
		if err := decode([]byte("tags: {ID: first, id: second}\n"), &got); err != nil || len(got.Tags) != 2 {
			t.Errorf("strict=%t map keys: %#v, %v", strict, got, err)
		}
		var nested struct {
			Defaults map[string]string `json:"defaults"`
			Result   config            `json:"result"`
		}
		doc := "defaults: &defaults {id: fallback}\nresult: {<<: *defaults, ID: chosen}\n"
		if err := decode([]byte(doc), &nested); err != nil || nested.Result.ID != "chosen" || nested.Defaults["id"] != "fallback" {
			t.Errorf("strict=%t nested alias: %#v, %v", strict, nested, err)
		}
		if err := decode([]byte("ID: chosen\n<<: {id: {<<: invalid}}\n"), &got); err == nil {
			t.Errorf("strict=%t accepted malformed overridden merge", strict)
		}
		var nullable struct {
			ID *string `json:"id"`
		}
		if err := decode([]byte("ID: null\n<<: {id: fallback}\n"), &nullable); err != nil || nullable.ID != nil {
			t.Errorf("strict=%t explicit null = %#v, %v", strict, nullable, err)
		}
	}
	var got config
	if err := UnmarshalStrict([]byte("ID: first\nid: second\n"), &got); err == nil {
		t.Fatal("strict decoding accepted duplicate explicit struct fields")
	}
	if err := Unmarshal([]byte("ID: first\nid: second\n"), &got); err != nil || got.ID != "second" {
		t.Fatalf("ordinary case-insensitive fields = %#v, %v", got, err)
	}
	var distinct struct {
		Upper string `json:"ID"`
		Lower string `json:"id"`
	}
	if err := UnmarshalStrict([]byte("ID: chosen\n<<: {id: fallback}\n"), &distinct); err != nil || distinct.Upper != "chosen" || distinct.Lower != "fallback" {
		t.Fatalf("distinct exact fields = %#v, %v", distinct, err)
	}
	var raw map[string]string
	if err := Unmarshal([]byte("ID: explicit\n<<: {id: fallback}\n"), &raw); err != nil || len(raw) != 2 || raw["ID"] != "explicit" || raw["id"] != "fallback" {
		t.Fatalf("map merge = %#v, %v", raw, err)
	}
	data, err := YAMLToJSON([]byte("ID: explicit\n<<: {id: fallback}\n"))
	var jsonMap map[string]string
	if err != nil || json.Unmarshal(data, &jsonMap) != nil || len(jsonMap) != 2 || jsonMap["ID"] != "explicit" || jsonMap["id"] != "fallback" {
		t.Fatalf("JSON merge = %s, %#v, %v", data, jsonMap, err)
	}
}

func TestMergePrecedenceForRedirect(t *testing.T) {
	for _, decode := range []func([]byte, interface{}, ...JSONOpt) error{Unmarshal, UnmarshalStrict} {
		for _, tt := range []struct {
			doc, want string
		}{
			{"<<: {redirect: old}\n", "old"},
			{"<<: {redirect: old}\nredirect: new\n", "new"},
			{"<<: {REDIRECT: old}\nredirect: new\n", "new"},
		} {
			var got struct {
				Redirect string `json:"redirect"`
			}
			if err := decode([]byte(tt.doc), &got); err != nil || got.Redirect != tt.want {
				t.Errorf("%q = %#v, %v; want %q", tt.doc, got, err, tt.want)
			}
		}
	}
}

func TestIntegerMapKeyMergePrecedence(t *testing.T) {
	for _, decode := range []func([]byte, interface{}, ...JSONOpt) error{Unmarshal, UnmarshalStrict} {
		var got map[int]string
		doc := "'1': explicit\n<<: {'01': {invalid: value}, '2': kept}\n"
		if err := decode([]byte(doc), &got); err != nil || !reflect.DeepEqual(got, map[int]string{1: "explicit", 2: "kept"}) {
			t.Errorf("integer merge = %#v, %v", got, err)
		}
		got = nil
		doc = "<<: [{'01': first}, {'1': {invalid: value}}]\n"
		if err := decode([]byte(doc), &got); err != nil || !reflect.DeepEqual(got, map[int]string{1: "first"}) {
			t.Errorf("integer merge sequence = %#v, %v", got, err)
		}
		var stringsMap map[string]string
		if err := decode([]byte("'1': explicit\n<<: {'01': inherited}\n"), &stringsMap); err != nil || len(stringsMap) != 2 {
			t.Errorf("string keys = %#v, %v", stringsMap, err)
		}
	}
	var got map[int]string
	if err := UnmarshalStrict([]byte("'1': first\n'01': second\n"), &got); err == nil {
		t.Fatal("strict decoding accepted equal integer keys")
	}
}

func TestStrictNormalizedJSONKeyCollisions(t *testing.T) {
	for _, doc := range []string{
		"true: first\n\"true\": second\n",
		"!!timestamp 2024-01-02: first\n\"2024-01-02\": second\n",
	} {
		if _, err := YAMLToJSONStrict([]byte(doc)); err == nil {
			t.Errorf("accepted normalized duplicate: %q", doc)
		}
	}
	for _, convert := range []func([]byte) ([]byte, error){YAMLToJSON, YAMLToJSONStrict} {
		data, err := convert([]byte("2024-01-02: value\n"))
		var got map[string]string
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &got); err != nil || got["2024-01-02"] != "value" {
			t.Fatalf("timestamp key = %s, %#v, %v", data, got, err)
		}
	}
}

func TestNormalizedJSONKeyMergePrecedence(t *testing.T) {
	for _, doc := range []string{
		"true: explicit\n<<: {\"true\": fallback, other: kept}\n",
		"<<: {\"true\": fallback, other: kept}\ntrue: explicit\n",
	} {
		for _, convert := range []func([]byte) ([]byte, error){YAMLToJSON, YAMLToJSONStrict} {
			data, err := convert([]byte(doc))
			var got map[string]string
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &got); err != nil || len(got) != 2 || got["true"] != "explicit" || got["other"] != "kept" {
				t.Fatalf("%q = %s, %#v, %v", doc, data, got, err)
			}
		}
	}
}

func TestFloat64MapKeyPrecision(t *testing.T) {
	got, err := YAMLToJSONStrict([]byte("1.00000000001: first\n1.00000000002: second\n"))
	if err != nil {
		t.Fatal(err)
	}

	var out map[string]string
	if err := json.Unmarshal(got, &out); err != nil || !reflect.DeepEqual(out, map[string]string{
		"1.00000000001": "first",
		"1.00000000002": "second",
	}) {
		t.Fatalf("keys = %#v, %v", out, err)
	}
}

func TestLegacyFloatStringConversion(t *testing.T) {
	var out struct {
		Value string `json:"value"`
	}
	for _, tt := range []struct{ value, want string }{
		{"1.00000000001", "1"},
		{"1.5", "1.5"},
	} {
		if err := Unmarshal([]byte("value: "+tt.value), &out); err != nil || out.Value != tt.want {
			t.Fatalf("%s: %#v, %v", tt.value, out, err)
		}
	}
}

func TestSelfReferentialDestination(t *testing.T) {
	var dst interface{}
	dst = &dst
	if err := Unmarshal([]byte("value: 1"), &dst); err != nil {
		t.Fatal(err)
	}
	if _, ok := dst.(map[string]interface{}); !ok {
		t.Fatalf("destination = %#v", dst)
	}
}

func TestJSONToYAMLIntegerPrecision(t *testing.T) {
	got, err := JSONToYAML([]byte(`{"id":9007199254740993}`))
	if err != nil {
		t.Fatalf("JSONToYAML = %q, %v", got, err)
	}
	var decoded map[string]interface{}
	if err := goyaml.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decode %q: %v", got, err)
	}
	id := reflect.ValueOf(decoded["id"])
	if !id.IsValid() || (id.Kind() != reflect.Int && id.Kind() != reflect.Int64) || id.Int() != int64(9007199254740993) {
		t.Fatalf("id = %#v (%T), want integer 9007199254740993", decoded["id"], decoded["id"])
	}
}

func TestJSONToYAMLLiteralMergeKey(t *testing.T) {
	input := []byte(`{"<<":"literal","nested":{"<<":{"value":"kept"}}}`)
	yamlBytes, err := JSONToYAML(input)
	if err != nil {
		t.Fatal(err)
	}
	var want interface{}
	if err := json.Unmarshal(input, &want); err != nil {
		t.Fatal(err)
	}
	for _, convert := range []func([]byte) ([]byte, error){YAMLToJSON, YAMLToJSONStrict} {
		jsonBytes, err := convert(yamlBytes)
		if err != nil {
			t.Fatalf("round-trip YAML %q: %v", yamlBytes, err)
		}
		var got interface{}
		if err := json.Unmarshal(jsonBytes, &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("round-trip YAML %q = %#v, %v", yamlBytes, got, err)
		}
	}
}

func TestStrictIntegerMapKeyCollisions(t *testing.T) {
	for _, doc := range []string{"'1': a\n'01': b\n", "'0': a\n'-0': b\n"} {
		var ints map[int]string
		if err := UnmarshalStrict([]byte(doc), &ints); err == nil || !strings.Contains(err.Error(), "duplicate integer map key") {
			t.Fatalf("%q: %#v, %v", doc, ints, err)
		}
		var stringsMap map[string]string
		if err := UnmarshalStrict([]byte(doc), &stringsMap); err != nil || len(stringsMap) != 2 {
			t.Fatalf("%q: %#v, %v", doc, stringsMap, err)
		}
	}
	var narrow map[uint8]string
	if err := UnmarshalStrict([]byte("'256': a\n'0': b\n"), &narrow); err == nil || strings.Contains(err.Error(), "duplicate integer map key") {
		t.Fatalf("out-of-range key error = %v", err)
	}
}

type textIntegerKey int

func (k *textIntegerKey) UnmarshalText(text []byte) error {
	if string(text) == "01" {
		*k = 1
	} else {
		*k = 2
	}
	return nil
}

func TestStrictIntegerMapKeyTextUnmarshaler(t *testing.T) {
	var out map[textIntegerKey]string
	if err := UnmarshalStrict([]byte("'1': a\n'01': b\n"), &out); err != nil || len(out) != 2 {
		t.Fatalf("custom key map = %#v, %v", out, err)
	}
}

func TestNullClearsPointerDestination(t *testing.T) {
	value := "old"
	dst := &value
	if err := Unmarshal([]byte("null"), &dst); err != nil || dst != nil {
		t.Fatalf("null destination = %#v, %v", dst, err)
	}
	for _, input := range []string{"null", "value: 1"} {
		if err := Unmarshal([]byte(input), nil); err == nil || !strings.Contains(err.Error(), "Unmarshal(nil)") {
			t.Fatalf("nil destination for %q: %v", input, err)
		}
	}
}
