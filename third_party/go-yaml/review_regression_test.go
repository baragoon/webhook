package yaml

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func TestStrictMergeAndDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
		err  bool
	}{
		{"merge before explicit", "base: &base {key: default}\nresult: {<<: *base, key: explicit}\n", "explicit", false},
		{"explicit before merge", "base: &base {key: default}\nresult: {key: explicit, <<: *base}\n", "explicit", false},
		{"merge sequence precedence", "a: &a {key: first}\nb: &b {key: second}\nresult: {<<: [*a, *b]}\n", "first", false},
		{"duplicate in merged mapping", "result: {<<: {key: first, key: second}, key: explicit}\n", "", true},
		{"duplicate explicit", "base: &base {key: default}\nresult: {<<: *base, key: first, key: second}\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out map[string]interface{}
			err := UnmarshalStrict([]byte(tt.doc), &out)
			if (err != nil) != tt.err {
				t.Fatalf("error = %v, want error %t", err, tt.err)
			}
			if err == nil && out["result"].(map[interface{}]interface{})["key"] != tt.want {
				t.Fatalf("result = %#v, want %q", out["result"], tt.want)
			}
		})
	}
	existing := map[string]string{"key": "old"}
	if err := UnmarshalStrict([]byte("key: new\n"), &existing); err != nil || existing["key"] != "new" {
		t.Fatalf("preexisting map = %#v, %v", existing, err)
	}
}

func TestStrictMergeIntoInlineMap(t *testing.T) {
	type config struct {
		Name  string                 `yaml:"name"`
		Extra map[string]interface{} `yaml:",inline"`
	}
	for _, doc := range []string{
		"<<: {name: default, option: default}\nname: explicit\noption: explicit\n",
		"name: explicit\noption: explicit\n<<: {name: default, option: default}\n",
	} {
		var out config
		if err := UnmarshalStrict([]byte(doc), &out); err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if out.Name != "explicit" || out.Extra["option"] != "explicit" {
			t.Fatalf("%q: %#v", doc, out)
		}
	}
	var merged config
	if err := UnmarshalStrict([]byte("<<: [{name: first, option: first}, {name: second, option: second, other: kept}]\n"), &merged); err != nil {
		t.Fatal(err)
	}
	if merged.Name != "first" || merged.Extra["option"] != "first" || merged.Extra["other"] != "kept" {
		t.Fatalf("merged defaults = %#v", merged)
	}
	var out config
	if err := UnmarshalStrict([]byte("option: first\noption: second\n"), &out); err == nil {
		t.Fatal("duplicate inline map key accepted")
	}
}

func TestNonspecificScalarTag(t *testing.T) {
	for _, value := range []string{"", "true", "012", "null", "<<", "2024-01-01"} {
		for _, yaml11 := range []bool{false, true} {
			tag, resolved := resolveForDocument("!", value, yaml11)
			if tag != yaml_STR_TAG || resolved != value {
				t.Errorf("resolve ! %q (yaml11=%t) = %q, %#v", value, yaml11, tag, resolved)
			}
		}
	}
	tag, resolved := resolveForDocument("!custom", "012", false)
	if tag != "!custom" || resolved != "012" {
		t.Errorf("custom tag = %q, %#v", tag, resolved)
	}
	var scalar interface{}
	if err := Unmarshal([]byte("! true"), &scalar); err != nil || scalar != "true" {
		t.Errorf("nonspecific scalar = %#v, %v", scalar, err)
	}
	var literal map[string]string
	if err := UnmarshalStrict([]byte("! <<: literal\n"), &literal); err != nil || literal["<<"] != "literal" {
		t.Errorf("nonspecific merge spelling = %#v, %v", literal, err)
	}
	var result MapSlice
	if err := UnmarshalStrict([]byte("! key: first\n!!str key: second\n"), &result); err == nil {
		t.Fatalf("duplicate string keys accepted: %#v", result)
	}
}

func TestEmitterQueueDrainAndLookahead(t *testing.T) {
	e := newEncoder()
	defer e.destroy()
	e.init()
	if len(e.emitter.events) != 0 || e.emitter.events_head != 0 {
		t.Fatalf("drained event queue retained: len=%d head=%d", len(e.emitter.events), e.emitter.events_head)
	}
	yaml_document_start_event_initialize(&e.event, nil, nil, true)
	e.emit()
	if len(e.emitter.events)-e.emitter.events_head != 1 {
		t.Fatalf("document start lookahead lost: %#v", e.emitter.events)
	}
	yaml_mapping_start_event_initialize(&e.event, nil, nil, true, yaml_BLOCK_MAPPING_STYLE)
	e.emit()
	if e.emitter.events_head != 1 || len(e.emitter.events)-e.emitter.events_head != 1 {
		t.Fatalf("pending mapping lookahead lost: len=%d head=%d", len(e.emitter.events), e.emitter.events_head)
	}
	e.marshal("", reflect.ValueOf("key"))
	e.marshal("", reflect.ValueOf("value"))
	yaml_mapping_end_event_initialize(&e.event)
	e.emit()
	yaml_document_end_event_initialize(&e.event, true)
	e.emit()
	e.finish()
	if string(e.out) != "key: value\n" {
		t.Fatalf("output = %q", e.out)
	}
}

func TestEmptyPlainDocumentMarker(t *testing.T) {
	for _, value := range []string{"", "nonempty"} {
		e := newEncoder()
		e.init()
		yaml_document_start_event_initialize(&e.event, nil, nil, true)
		e.emit()
		yaml_scalar_event_initialize(&e.event, nil, nil, []byte(value), true, false, yaml_PLAIN_SCALAR_STYLE)
		e.emit()
		yaml_document_end_event_initialize(&e.event, true)
		e.emit()
		e.finish()
		output := string(e.out)
		e.destroy()
		if strings.HasPrefix(output, "---") != (value == "") || value != "" && !strings.Contains(output, value) {
			t.Fatalf("value %q: output %q", value, output)
		}
	}
}

func TestDecoderRetainsParserError(t *testing.T) {
	d := NewDecoder(strings.NewReader("a: [\n"))
	var value interface{}
	first := d.Decode(&value)
	if first == nil {
		t.Fatal("expected parse error")
	}
	if err := d.Decode(&value); err == nil || errors.Is(err, io.EOF) || err.Error() != first.Error() {
		t.Fatalf("second decode error = %v", err)
	}
	d = NewDecoder(strings.NewReader("a: 1\n"))
	if err := d.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := d.Decode(&value); !errors.Is(err, io.EOF) {
		t.Fatalf("clean end error = %v", err)
	}
}

type dataAndErrorReader struct {
	data []byte
	err  error
}

func (r *dataAndErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

func TestDecoderProcessesDataBeforeReaderError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"unexpected EOF", io.ErrUnexpectedEOF},
		{"wrapped unexpected EOF", fmt.Errorf("reader: %w", io.ErrUnexpectedEOF)},
		{"other read error", errors.New("connection failed")},
		{"EOF", io.EOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &dataAndErrorReader{data: []byte("---\na: 1\n...\n---\nb: 2\n...\n"), err: tt.err}
			d := NewDecoder(r)
			var value map[string]int
			if err := d.Decode(&value); err != nil || value["a"] != 1 {
				t.Fatalf("first decode = %#v, %v", value, err)
			}
			value = nil
			if err := d.Decode(&value); err != nil || value["b"] != 2 {
				t.Fatalf("second decode = %#v, %v", value, err)
			}
			err := d.Decode(&value)
			if tt.err == io.EOF && !errors.Is(err, io.EOF) || tt.err != io.EOF && (err == nil || !strings.Contains(err.Error(), tt.err.Error())) {
				t.Fatalf("final error = %v", err)
			}
		})
	}
	errRead := errors.New("empty read failure")
	d := NewDecoder(&dataAndErrorReader{err: errRead})
	var value interface{}
	if err := d.Decode(&value); err == nil || !strings.Contains(err.Error(), errRead.Error()) {
		t.Fatalf("zero-byte read error = %v", err)
	}
}

func TestDecoderReportsReadErrorAfterCompleteDocument(t *testing.T) {
	readErr := errors.New("connection failed")
	for _, doc := range []string{
		"---\na: 1\n...\n",
		"---\na: 1\n... # end\n",
	} {
		d := NewDecoder(&dataAndErrorReader{data: []byte(doc), err: readErr})
		var value map[string]int
		if err := d.Decode(&value); err != nil || !reflect.DeepEqual(value, map[string]int{"a": 1}) {
			t.Fatalf("%q: first decode = %#v, %v", doc, value, err)
		}
		if err := d.Decode(&value); err == nil || errors.Is(err, io.EOF) || !strings.Contains(err.Error(), readErr.Error()) {
			t.Fatalf("%q: deferred error = %v", doc, err)
		}
	}
}

func TestEncodingDetectionDefersErrorWithBufferedBytes(t *testing.T) {
	readErr := errors.New("reader failed")
	p := newParserFromReader(io.MultiReader(strings.NewReader("a"), &dataAndErrorReader{err: readErr}))
	defer p.destroy()
	if !yaml_parser_determine_encoding(&p.parser) || p.parser.read_error != readErr {
		t.Fatalf("encoding detection lost buffered input: %v", p.parser.problem)
	}
	if !yaml_parser_update_buffer(&p.parser, 1) || p.parser.buffer[p.parser.buffer_pos] != 'a' {
		t.Fatalf("buffered character lost: %v", p.parser.problem)
	}
	if yaml_parser_update_buffer(&p.parser, 2) || !strings.Contains(p.parser.problem, readErr.Error()) {
		t.Fatalf("deferred reader error = %q", p.parser.problem)
	}
}

func TestReaderLookaheadBeyondWorkingBuffer(t *testing.T) {
	p := newParserFromReader(strings.NewReader("abcdefghijklmnop"))
	defer p.destroy()
	p.parser.buffer = make([]byte, 0, 16)
	if !yaml_parser_update_buffer(&p.parser, 12) {
		t.Fatalf("lookahead failed: %s", p.parser.problem)
	}
	if string(p.parser.buffer[:12]) != "abcdefghijkl" {
		t.Fatalf("lookahead = %q", p.parser.buffer)
	}
}

func TestStrictMergePrecedenceBeforeDecoding(t *testing.T) {
	type config struct {
		Value   int               `yaml:"value"`
		Options map[string]string `yaml:"options"`
	}
	for _, doc := range []string{
		"<<: {value: 10, options: {shared: old, merged-only: old}}\nvalue: 42\noptions: {shared: new}\n",
		"value: 42\noptions: {shared: new}\n<<: {value: 10, options: {shared: old, merged-only: old}}\n",
	} {
		var got config
		if err := UnmarshalStrict([]byte(doc), &got); err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got.Value != 42 || !reflect.DeepEqual(got.Options, map[string]string{"shared": "new"}) {
			t.Fatalf("got %#v", got)
		}
	}
	var got config
	if err := UnmarshalStrict([]byte("<<: {value: invalid}\n"), &got); err == nil {
		t.Fatal("expected invalid unoverridden merge value to fail")
	}
	if err := UnmarshalStrict([]byte("<<: {value: 42, options: {shared: old}}\n"), &got); err != nil || got.Options["shared"] != "old" {
		t.Fatalf("merged defaults = %#v, %v", got, err)
	}
	var typed map[string]int
	if err := UnmarshalStrict([]byte("<<: {value: invalid}\nvalue: 42\n"), &typed); err != nil || typed["value"] != 42 {
		t.Fatalf("typed map = %#v, %v", typed, err)
	}
	typed = nil
	if err := UnmarshalStrict([]byte("<<: [{value: 42}, {value: invalid}]\n"), &typed); err != nil || typed["value"] != 42 {
		t.Fatalf("merge sequence = %#v, %v", typed, err)
	}
	var fresh config
	if err := UnmarshalStrict([]byte("value: 42\n<<: {value: invalid}\n"), &fresh); err != nil || fresh.Value != 42 {
		t.Fatalf("shadowed struct value = %#v, %v", fresh, err)
	}
	for _, doc := range []string{
		"value: 42\n<<: {value: {a: 1, a: 2}}\n",
		"value: 42\n<<: {value: {<<: not-a-map}}\n",
		"value: 42\n<<: {value: {? [a, b]: 1, ? [a, b]: 2}}\n",
	} {
		if err := UnmarshalStrict([]byte(doc), &got); err == nil {
			t.Fatalf("invalid overridden source accepted: %s", doc)
		}
	}
	if err := UnmarshalStrict([]byte("value: 42\n<<: {value: {? [a, b]: ignored}}\n"), &got); err != nil || got.Value != 42 {
		t.Fatalf("shadowed complex key = %#v, %v", got, err)
	}
	var inline struct {
		Extra map[string]int `yaml:",inline"`
	}
	if err := UnmarshalStrict([]byte("value: 42\n<<: {value: invalid}\n"), &inline); err != nil || inline.Extra["value"] != 42 {
		t.Fatalf("shadowed inline value = %#v, %v", inline, err)
	}
	var nested struct {
		Value int `yaml:"value"`
		Child struct {
			Value int `yaml:"value"`
		} `yaml:"child"`
	}
	if err := UnmarshalStrict([]byte("value: 1\nchild: {<<: {value: 2}}\n"), &nested); err != nil || nested.Child.Value != 2 {
		t.Fatalf("nested merge = %#v, %v", nested, err)
	}
	var result map[string]int
	if err := UnmarshalStrict([]byte("<<: {a: 1}\n<<: {b: 2}\n"), &result); err == nil {
		t.Fatal("repeated merge directives accepted")
	}
	result = nil
	if err := UnmarshalStrict([]byte("<<: [{a: 1}, {b: 2}]\n"), &result); err != nil || !reflect.DeepEqual(result, map[string]int{"a": 1, "b": 2}) {
		t.Fatalf("merge sequence = %#v, %v", result, err)
	}
}

func TestCompactMappingsWithEmptyKeys(t *testing.T) {
	for _, tt := range []struct {
		doc  string
		want []interface{}
	}{
		{"[? : value, item]", []interface{}{map[interface{}]interface{}{nil: "value"}, "item"}},
		{"[? , item]", []interface{}{map[interface{}]interface{}{nil: nil}, "item"}},
		{"[? ]", []interface{}{map[interface{}]interface{}{nil: nil}}},
	} {
		var out interface{}
		if err := Unmarshal([]byte(tt.doc), &out); err != nil || !reflect.DeepEqual(out, tt.want) {
			t.Errorf("%q: got %#v, want %#v, error %v", tt.doc, out, tt.want, err)
		}
	}
}

func TestEmptyFlowCollectionAsMappingKey(t *testing.T) {
	for _, tt := range []struct {
		doc string
		key interface{}
	}{
		{"[]: value", []interface{}{}},
		{"{}: value", MapSlice(nil)},
	} {
		var out MapSlice
		if err := Unmarshal([]byte(tt.doc), &out); err != nil || len(out) != 1 || out[0].Value != "value" || !reflect.DeepEqual(out[0].Key, tt.key) {
			t.Errorf("%q: %#v, %v", tt.doc, out, err)
		}
	}
}

func TestExpiredSimpleKeyRemovedFromLookup(t *testing.T) {
	parser := yaml_parser_t{
		simple_keys_by_tok: map[int]int{4: 0, 5: 1},
		simple_keys: []yaml_simple_key_t{
			{possible: true, token_number: 4, mark: yaml_mark_t{line: 0}},
			{possible: true, token_number: 5, mark: yaml_mark_t{line: 1}},
		},
		mark: yaml_mark_t{line: 1},
	}
	if valid, ok := yaml_simple_key_is_valid(&parser, &parser.simple_keys[0]); valid || !ok {
		t.Fatalf("expired key: valid=%t ok=%t", valid, ok)
	}
	if _, ok := parser.simple_keys_by_tok[4]; ok || parser.simple_keys_by_tok[5] != 1 {
		t.Fatalf("lookup = %#v", parser.simple_keys_by_tok)
	}
}

func TestExplicitFloatTagForLargeUnsignedInteger(t *testing.T) {
	var tagged interface{}
	if err := Unmarshal([]byte("!!float 18446744073709551615"), &tagged); err != nil {
		t.Fatal(err)
	}
	if _, ok := tagged.(float64); !ok {
		t.Fatalf("tagged value = %#v (%T)", tagged, tagged)
	}
	var untagged interface{}
	if err := Unmarshal([]byte("18446744073709551615"), &untagged); err != nil {
		t.Fatal(err)
	}
	if _, ok := untagged.(uint64); !ok {
		t.Fatalf("untagged value = %#v (%T)", untagged, untagged)
	}
}

func TestFutureLineWrapConcurrentInitialization(t *testing.T) {
	previous := atomic.LoadUint32(&disableLineWrapping)
	defer atomic.StoreUint32(&disableLineWrapping, previous)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				FutureLineWrap()
				e := newEncoder()
				e.destroy()
			}
		}()
	}
	wg.Wait()
}

func TestNaturalSortLongDigitRuns(t *testing.T) {
	keys := []string{"x18446744073709551616", "x2", "x18446744073709551615", "x01", "x1", "x10", "x100"}
	values := make(keyList, len(keys))
	for i := range keys {
		values[i] = reflect.ValueOf(keys[i])
	}
	sort.Sort(values)
	got := make([]string, len(values))
	for i := range values {
		got[i] = values[i].String()
	}
	want := []string{"x1", "x01", "x2", "x10", "x100", "x18446744073709551615", "x18446744073709551616"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sort = %v, want %v", got, want)
	}
	keys = append(keys, "x0", "x00", "x000", "x0001", "x0002", "x9", "x09", "x1000", "x10a", "x100b", "x", "x8a", "x7286", "x8901")
	values = make(keyList, len(keys))
	for i := range keys {
		values[i] = reflect.ValueOf(keys[i])
	}
	for i := range values {
		for j := range values {
			for k := range values {
				if values.Less(i, j) && values.Less(j, k) && !values.Less(i, k) {
					t.Fatalf("comparison is not transitive: %q, %q, %q", keys[i], keys[j], keys[k])
				}
			}
		}
	}
}

type failingWriter struct {
	calls int
	err   error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.calls++
	return 0, w.err
}

func TestEncoderWriterErrorIsTerminal(t *testing.T) {
	sentinel := errors.New("write failed")
	w := &failingWriter{err: sentinel}
	e := NewEncoder(w)
	err := e.Encode(map[string]string{"key": "value"})
	if !errors.Is(err, sentinel) {
		t.Fatal("expected writer error")
	}
	first := err.Error()
	calls := w.calls
	if err := e.Encode("another"); !errors.Is(err, sentinel) || err.Error() != first {
		t.Fatalf("second encode error = %v, want %q", err, first)
	}
	if err := e.Close(); !errors.Is(err, sentinel) || err.Error() != first {
		t.Fatalf("close error = %v, want %q", err, first)
	}
	if w.calls != calls {
		t.Fatalf("writer called %d times after first error; want %d", w.calls, calls)
	}
}

func TestEmitterRejectsEventsAfterWriteError(t *testing.T) {
	sentinel := errors.New("write failed")
	for _, typ := range []yaml_event_type_t{yaml_SEQUENCE_END_EVENT, yaml_MAPPING_END_EVENT} {
		e := newEncoder()
		yaml_emitter_set_writer_error(&e.emitter, sentinel)
		event := yaml_event_t{typ: typ}
		if yaml_emitter_emit(&e.emitter, &event) || len(e.emitter.events) != 0 {
			t.Fatalf("event %v was accepted after write error", typ)
		}
		var got error
		func() {
			defer handleErr(&got)
			e.must(yaml_emitter_emit(&e.emitter, &event))
		}()
		if !errors.Is(got, sentinel) {
			t.Fatalf("event %v error = %v", typ, got)
		}
	}
}

type shortWriter struct {
	calls int
	data  []byte
	err   error
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.calls++
	n := len(p) / 2
	w.data = append(w.data, p[:n]...)
	return n, w.err
}

func TestEncoderShortWriteIsTerminal(t *testing.T) {
	sentinel := errors.New("partial write")
	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{"short write", nil, io.ErrShortWrite},
		{"partial write with error", sentinel, sentinel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &shortWriter{err: tt.err}
			e := NewEncoder(w)
			first := e.Encode("value")
			if !errors.Is(first, tt.want) || len(w.data) == 0 {
				t.Fatalf("encode error = %v, accepted = %q", first, w.data)
			}
			accepted := string(w.data)
			calls := w.calls
			if err := e.Encode("another"); err != first {
				t.Fatalf("second encode error = %v, want %v", err, first)
			}
			if err := e.Close(); err != first {
				t.Fatalf("close error = %v, want %v", err, first)
			}
			if w.calls != calls || string(w.data) != accepted {
				t.Fatalf("writes = %d, accepted = %q", w.calls, w.data)
			}
		})
	}
}

func TestNumericBoundsAndNullPointers(t *testing.T) {
	for _, tt := range []struct {
		doc  string
		dst  interface{}
		ok   bool
		want interface{}
	}{
		{"9223372036854775808.0", new(int64), false, nil},
		{"-9223372036854777856.0", new(int64), false, nil},
		{"-9223372036854775808.0", new(int64), true, int64(-9223372036854775808)},
		{"18446744073709551616.0", new(uint64), false, nil},
		{"-1.0", new(uint64), false, nil},
		{"42.0", new(uint8), true, uint8(42)},
		{"256.0", new(uint8), false, nil},
	} {
		err := Unmarshal([]byte(tt.doc), tt.dst)
		if (err == nil) != tt.ok {
			t.Errorf("%s: %v", tt.doc, err)
		} else if err == nil && !reflect.DeepEqual(reflect.ValueOf(tt.dst).Elem().Interface(), tt.want) {
			t.Errorf("%s: got %v, want %v", tt.doc, reflect.ValueOf(tt.dst).Elem(), tt.want)
		}
	}
	for _, doc := range []string{"null", "Null", "NULL", "~", "!!null null"} {
		value := "old"
		p := &value
		if err := Unmarshal([]byte(doc), &p); err != nil || p != nil {
			t.Errorf("%q: %#v, %v", doc, p, err)
		}
	}
	for _, doc := range []string{`"Null"`, `!!str null`} {
		var p *string
		if err := Unmarshal([]byte(doc), &p); err != nil || p == nil {
			t.Errorf("%q: %#v, %v", doc, p, err)
		}
	}
}

func TestExplicitIntegerWithYAML11Directive(t *testing.T) {
	for _, tt := range []struct {
		scalar string
		want   int64
	}{
		{"017", 15},
		{"18", 18},
		{"0", 0},
	} {
		var got int64
		doc := "%YAML 1.1\n---\n!!int " + tt.scalar + "\n"
		if err := Unmarshal([]byte(doc), &got); err != nil || got != tt.want {
			t.Errorf("%q = %d, %v; want %d", doc, got, err, tt.want)
		}
	}
	for _, scalar := range []string{"08", "019", "+09", "-08"} {
		var got interface{}
		doc := "%YAML 1.1\n---\n!!int " + scalar + "\n"
		if err := Unmarshal([]byte(doc), &got); err == nil {
			t.Errorf("%q accepted as %#v", doc, got)
		}
	}
	var noDirective int
	if err := Unmarshal([]byte("!!int 08\n"), &noDirective); err != nil || noDirective != 8 {
		t.Fatalf("no directive = %d, %v", noDirective, err)
	}
	var keys MapSlice
	if err := UnmarshalStrict([]byte("%YAML 1.1\n---\n? [!!int 08]\n: value\n"), &keys); err == nil {
		t.Fatal("invalid YAML 1.1 composite key accepted")
	}

	decoder := NewDecoder(strings.NewReader("%YAML 1.1\n---\n!!int 017\n...\n---\n!!int 08\n"))
	for _, want := range []int{15, 8} {
		var got int
		if err := decoder.Decode(&got); err != nil || got != want {
			t.Fatalf("stream value = %d, %v; want %d", got, err, want)
		}
	}
}

type stringInterface interface{ String() string }

func TestInterfaceTypeMismatchReturnsError(t *testing.T) {
	for _, doc := range []string{"plain", "2020-01-01", "[one]", "{key: value}"} {
		var value stringInterface
		if err := Unmarshal([]byte(doc), &value); err == nil {
			t.Errorf("%q: expected type error", doc)
		}
	}
	var value interface{}
	if err := Unmarshal([]byte("[one]"), &value); err != nil {
		t.Fatal(err)
	}
}

func TestMergeModesAndOrderedMaps(t *testing.T) {
	docs := []string{
		"<<: [{value: invalid, other: first}, {other: second, extra: kept}]\nvalue: 42\n",
		"value: 42\n<<: [{value: invalid, other: first}, {other: second, extra: kept}]\n",
	}
	for _, doc := range docs {
		var out struct {
			Value int               `yaml:"value"`
			Extra map[string]string `yaml:",inline"`
		}
		if err := Unmarshal([]byte(doc), &out); err != nil || out.Value != 42 || out.Extra["other"] != "first" || out.Extra["extra"] != "kept" {
			t.Fatalf("struct %q: %#v, %v", doc, out, err)
		}
		var values map[string]interface{}
		if err := Unmarshal([]byte(doc), &values); err != nil || values["value"] != 42 || values["other"] != "first" || values["extra"] != "kept" {
			t.Fatalf("map %q: %#v, %v", doc, values, err)
		}
		var ordered MapSlice
		if err := Unmarshal([]byte(doc), &ordered); err != nil {
			t.Fatal(err)
		}
		if len(ordered) != 3 || ordered[0].Key != "value" || ordered[0].Value != 42 ||
			ordered[1].Key != "other" || ordered[1].Value != "first" || ordered[2].Key != "extra" || ordered[2].Value != "kept" {
			t.Fatalf("ordered = %#v", ordered)
		}
	}
	for _, strict := range []bool{false, true} {
		var out MapSlice
		unmarshal := Unmarshal
		if strict {
			unmarshal = UnmarshalStrict
		}
		var duplicate map[string]int
		if err := Unmarshal([]byte("value: 1\nvalue: 2\n"), &duplicate); err != nil || duplicate["value"] != 2 {
			t.Fatalf("duplicate non-strict map = %#v, %v", duplicate, err)
		}
		var ordered MapSlice
		if err := UnmarshalStrict([]byte("? [a, b]\n: one\n? [a, b]\n: two\n"), &ordered); err == nil {
			t.Fatal("duplicate sequence key accepted")
		}
		err := unmarshal([]byte("a: 1\na: 2\n"), &out)
		if strict && err == nil || !strict && (err != nil || len(out) != 2) {
			t.Fatalf("strict=%t: %#v, %v", strict, out, err)
		}
	}
}

func TestScannerFlowAndBOM(t *testing.T) {
	var values []string
	if err := Unmarshal([]byte(`["a\/b", a?b]`), &values); err != nil || !reflect.DeepEqual(values, []string{"a/b", "a?b"}) {
		t.Fatalf("flow = %#v, %v", values, err)
	}
	var scalar string
	if err := Unmarshal([]byte(`"\q"`), &scalar); err == nil {
		t.Fatal("unknown escape accepted")
	}
	d := NewDecoder(strings.NewReader("---\na: 1\n...\n\ufeff---\nb: 2\n"))
	for _, key := range []string{"a", "b"} {
		var out map[string]int
		if err := d.Decode(&out); err != nil || out[key] == 0 {
			t.Fatalf("%s: %#v, %v", key, out, err)
		}
	}
}

func TestFoldedScalarRoundTrip(t *testing.T) {
	ordinary := strings.Repeat("longword ", 30) + "end\n"
	for _, value := range []string{"a\nb", "a\n\n", "a\n b", "a\n\nb", "  " + ordinary, ordinary} {
		e := newEncoder()
		e.init()
		yaml_document_start_event_initialize(&e.event, nil, nil, true)
		e.emit()
		e.must(yaml_scalar_event_initialize(&e.event, nil, nil, []byte(value), true, true, yaml_FOLDED_SCALAR_STYLE))
		e.emit()
		yaml_document_end_event_initialize(&e.event, true)
		e.emit()
		e.finish()
		if value == ordinary && strings.Count(string(e.out), "\n") < 3 {
			t.Errorf("ordinary folded scalar did not wrap: %q", e.out)
		}
		var got string
		err := Unmarshal(e.out, &got)
		e.destroy()
		if err != nil || got != value {
			t.Errorf("%q: got %q, %v", value, got, err)
		}
	}
}

func TestBlockScalarChompingOpenEnded(t *testing.T) {
	for _, tt := range []struct {
		name, value, hint string
		open              bool
	}{
		{"strip", "value", "-", false},
		{"clip", "value\n", "", false},
		{"keep", "value\n\n", "+", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEncoder()
			defer e.destroy()
			e.emitter.open_ended = true
			if !yaml_emitter_write_block_scalar_hints(&e.emitter, []byte(tt.value)) {
				t.Fatal("writing scalar hints failed")
			}
			if got := string(e.emitter.buffer[:e.emitter.buffer_pos]); got != tt.hint || e.emitter.open_ended != tt.open {
				t.Fatalf("hint = %q, open-ended = %t", got, e.emitter.open_ended)
			}

			stream := newEncoder()
			defer stream.destroy()
			stream.init()
			yaml_document_start_event_initialize(&stream.event, nil, nil, true)
			stream.emit()
			stream.must(yaml_scalar_event_initialize(&stream.event, nil, nil, []byte(tt.value), true, true, yaml_LITERAL_SCALAR_STYLE))
			stream.emit()
			yaml_document_end_event_initialize(&stream.event, true)
			stream.emit()
			yaml_document_start_event_initialize(&stream.event, &yaml_version_directive_t{major: 1, minor: 1}, nil, false)
			stream.emit()
			stream.must(yaml_scalar_event_initialize(&stream.event, nil, nil, []byte("next"), true, true, yaml_PLAIN_SCALAR_STYLE))
			stream.emit()
			yaml_document_end_event_initialize(&stream.event, true)
			stream.emit()
			stream.finish()
			if got := strings.Contains(string(stream.out), "...\n%YAML"); got != tt.open {
				t.Fatalf("document boundary = %q", stream.out)
			}
			decoder := NewDecoder(strings.NewReader(string(stream.out)))
			for _, want := range []string{tt.value, "next"} {
				var got string
				if err := decoder.Decode(&got); err != nil || got != want {
					t.Fatalf("round-trip %q: got %q, %v", stream.out, got, err)
				}
			}
		})
	}

	e := newEncoderWithWriter(&failingWriter{err: errors.New("write failed")})
	defer e.destroy()
	e.emitter.buffer_pos = len(e.emitter.buffer) - 5
	if yaml_emitter_write_block_scalar_hints(&e.emitter, []byte("value\n\n")) || e.emitter.open_ended {
		t.Fatal("failed keep hint left emitter open-ended")
	}
}

func TestEmitterTagsAndEncoding(t *testing.T) {
	for _, tt := range []struct {
		tag, want string
	}{
		{"tag:example.org,2024:", "!<tag:example.org,2024:>"},
		{"tag:example.org,2024:item", "!e!item"},
	} {
		e := newEncoder()
		e.init()
		directives := []yaml_tag_directive_t{{handle: []byte("!e!"), prefix: []byte("tag:example.org,2024:")}}
		yaml_document_start_event_initialize(&e.event, nil, directives, true)
		e.emit()
		e.must(yaml_scalar_event_initialize(&e.event, nil, []byte(tt.tag), []byte("value"), false, false, yaml_PLAIN_SCALAR_STYLE))
		e.emit()
		yaml_document_end_event_initialize(&e.event, true)
		e.emit()
		e.finish()
		if !strings.Contains(string(e.out), tt.want) {
			t.Errorf("tag %q output = %q", tt.tag, e.out)
		}
		e.destroy()
	}
	for _, encoding := range []yaml_encoding_t{yaml_UTF16LE_ENCODING, yaml_UTF16BE_ENCODING} {
		var emitter yaml_emitter_t
		yaml_emitter_initialize(&emitter)
		var output []byte
		yaml_emitter_set_output_string(&emitter, &output)
		emitter.encoding = encoding
		var event yaml_event_t
		yaml_stream_start_event_initialize(&event, encoding)
		if yaml_emitter_emit(&emitter, &event) || len(output) != 0 {
			t.Errorf("encoding %v accepted or wrote output %q", encoding, output)
		}
		yaml_emitter_delete(&emitter)
	}
}

type durationMarshaler struct{}

func (durationMarshaler) MarshalYAML() (interface{}, error) { return time.Second, nil }

type errorMarshaler struct{ err error }

func (m errorMarshaler) MarshalYAML() (interface{}, error) { return nil, m.err }

type zeroValueReceiver int

func (z zeroValueReceiver) IsZero() bool { return z == 0 }

type privateEmbedded struct{ Visible string }

func TestEncoderEdgeCases(t *testing.T) {
	for _, value := range []interface{}{time.Second, durationMarshaler{}} {
		data, err := Marshal(value)
		if err != nil || !strings.Contains(string(data), "1s") {
			t.Fatalf("duration = %q, %v", data, err)
		}
	}
	wantArray := [2]MapItem{{Key: "a", Value: 1}, {Key: "b", Value: 2}}
	data, err := Marshal(wantArray)
	var gotArray [2]MapItem
	if err != nil {
		t.Fatalf("array = %q, %v", data, err)
	}
	if err := Unmarshal(data, &gotArray); err != nil || !reflect.DeepEqual(gotArray, wantArray) {
		t.Fatalf("array = %q, %#v, %v", data, gotArray, err)
	}
	data, err = Marshal(json.Number("18446744073709551615"))
	if err != nil || strings.TrimSpace(string(data)) != "18446744073709551615" {
		t.Fatalf("uint number = %q, %v", data, err)
	}
	data, err = Marshal(map[float64]string{math.NaN(): "nan", 1: "one"})
	if err != nil || !strings.Contains(string(data), ".nan: nan") {
		t.Fatalf("NaN map = %q, %v", data, err)
	}
	data, err = Marshal(struct {
		privateEmbedded
		Zero interface{} `yaml:",omitempty"`
	}{Zero: (*zeroValueReceiver)(nil)})
	if err != nil || strings.Contains(string(data), "zero:") || strings.Contains(string(data), "privateembedded:") {
		t.Fatalf("private/zero = %q, %v", data, err)
	}
	zero := 0
	data, err = Marshal(struct {
		Zero *int `yaml:",omitempty"`
	}{Zero: &zero})
	if err != nil || !strings.Contains(string(data), "zero:") {
		t.Fatalf("explicit pointer = %q, %v", data, err)
	}
	sentinel := errors.New("marshal failed")
	var output strings.Builder
	e := NewEncoder(&output)
	if err := e.Encode(errorMarshaler{sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("encode error = %v", err)
	}
	if err := e.Encode("next"); !errors.Is(err, sentinel) {
		t.Fatalf("second error = %v", err)
	}
	if err := e.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("close error = %v", err)
	}
}

type partialWriter struct {
	calls             int
	err               error
	data              []byte
	offered, accepted int
}

func (w *partialWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 2 {
		w.offered, w.accepted = len(p), len(p)/2
		w.data = append(w.data, p[:w.accepted]...)
		return w.accepted, w.err
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func TestEncoderPartialWriterFailure(t *testing.T) {
	sentinel := errors.New("partial write")
	w := &partialWriter{err: sentinel}
	e := NewEncoder(w)
	if err := e.Encode("first"); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 || string(w.data) != "first\n" {
		t.Fatalf("first write: calls = %d, data = %q", w.calls, w.data)
	}
	first := e.Encode(strings.Repeat("x", 20000))
	if !errors.Is(first, sentinel) || w.calls != 2 || w.accepted <= 0 || w.accepted >= w.offered {
		t.Fatalf("write error = %v, calls = %d, accepted %d of %d", first, w.calls, w.accepted, w.offered)
	}
	accepted := string(w.data)
	if err := e.Encode("later"); err != first {
		t.Fatalf("later encode = %v, want %v", err, first)
	}
	if err := e.Close(); err != first || w.calls != 2 || string(w.data) != accepted {
		t.Fatalf("close = %v, calls = %d, data changed = %t", err, w.calls, string(w.data) != accepted)
	}
}

type InlineExtras struct {
	Extra map[string]int `yaml:",inline"`
}

type NestedInline struct {
	InlineExtras `yaml:",inline"`
}

func TestNestedInlineMap(t *testing.T) {
	type config struct {
		NestedInline `yaml:",inline"`
		Name         string `yaml:"name"`
	}
	var value config
	if err := Unmarshal([]byte("name: test\nextra: 42\n"), &value); err != nil || value.Extra["extra"] != 42 {
		t.Fatalf("unmarshal = %#v, %v", value, err)
	}
	data, err := Marshal(value)
	if err != nil || !strings.Contains(string(data), "extra: 42") {
		t.Fatalf("marshal = %q, %v", data, err)
	}
	value.Extra["name"] = 1
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(fmt.Sprint(recovered), `name`) {
			t.Errorf("expected inline map conflict for name, got %v", recovered)
		}
	}()
	_, _ = Marshal(value)
}

type sequenceMarshaler struct{}

func (sequenceMarshaler) MarshalYAML() (interface{}, error) { return []int{1, 2}, nil }

func TestFlowFieldDoesNotLeak(t *testing.T) {
	data, err := Marshal(struct {
		First string            `yaml:"first,flow"`
		Next  sequenceMarshaler `yaml:"next"`
		Flow  []int             `yaml:"flow,flow"`
	}{First: "text", Flow: []int{1, 2}})
	if err != nil || !strings.Contains(string(data), "next:\n- 1\n- 2\n") || !strings.Contains(string(data), "flow: [1, 2]") {
		t.Fatalf("flow output = %q, %v", data, err)
	}
}

type duplicateCustomKey struct {
	id    int
	calls *int
}

func (k duplicateCustomKey) MarshalYAML() (interface{}, error) {
	*k.calls++
	if k.id == 1 {
		return MapSlice{{Key: "a", Value: 1}, {Key: "b", Value: 2}}, nil
	}
	return MapSlice{{Key: "b", Value: 2}, {Key: "a", Value: 1}}, nil
}

func TestNaNKeyValuesAndOrdering(t *testing.T) {
	m := map[float64]string{1: "one"}
	m[math.NaN()] = "first"
	m[math.NaN()] = "second"
	data, err := Marshal(m)
	if err == nil {
		t.Fatalf("NaN map = %q, %v", data, err)
	}
	if data, err = Marshal(map[interface{}]string{int(1): "int", uint(1): "uint"}); err == nil {
		t.Fatalf("duplicate numeric key = %q", data)
	}
	if data, err = Marshal(map[interface{}]string{int(1): "int", "1": "string"}); err != nil {
		t.Fatalf("distinct keys = %q, %v", data, err)
	}
	calls := 0
	custom := map[duplicateCustomKey]string{{id: 1, calls: &calls}: "one", {id: 2, calls: &calls}: "two"}
	if data, err = Marshal(custom); err == nil || calls != 2 {
		t.Fatalf("custom keys = %q, %v, calls=%d", data, err, calls)
	}
	keys := keyList{reflect.ValueOf(math.NaN()), reflect.ValueOf(-1.0), reflect.ValueOf(1.0)}
	if !keys.Less(0, 1) || !keys.Less(0, 2) || keys.Less(1, 0) || !keys.Less(1, 2) {
		t.Fatal("NaN numeric ordering is inconsistent")
	}
}

func TestDecoderRestoresStateAfterFailedAlias(t *testing.T) {
	d := newDecoder(false)
	d.mapType = reflect.TypeOf(MapSlice{})
	alias := &node{kind: aliasNode, value: "x", alias: &node{
		kind: mappingNode,
		children: []*node{
			{kind: scalarNode, value: "key", implicit: true},
			{kind: scalarNode, tag: yaml_INT_TAG, value: "invalid"},
		},
	}}
	var out map[interface{}]interface{}
	var err error
	func() {
		defer handleErr(&err)
		d.unmarshal(alias, reflect.ValueOf(&out).Elem())
	}()
	if err == nil || d.aliasDepth != 0 || len(d.aliases) != 0 || d.mapType != reflect.TypeOf(MapSlice{}) {
		t.Fatalf("error=%v depth=%d aliases=%v mapType=%v", err, d.aliasDepth, d.aliases, d.mapType)
	}
	alias.alias.children[1].value = "42"
	func() {
		defer handleErr(&err)
		err = nil
		d.unmarshal(alias, reflect.ValueOf(&out).Elem())
	}()
	if err != nil || out["key"] != 42 {
		t.Fatalf("retry = %#v, %v", out, err)
	}
}

func TestNaNKeysAndClear(t *testing.T) {
	for _, out := range []interface{}{new(map[interface{}]interface{}), new(MapSlice)} {
		if err := UnmarshalStrict([]byte(".nan: one\n.NaN: two\n"), out); err == nil {
			t.Errorf("%T accepted duplicate NaN keys", out)
		}
		if err := UnmarshalStrict([]byte(".nan: one\n"), out); err != nil {
			t.Errorf("%T single NaN: %v", out, err)
		}
	}
	m := map[float64]string{math.NaN(): "nan", 1: "one"}
	alias := m
	if err := Unmarshal([]byte("null"), m); err != nil || len(alias) != 0 {
		t.Fatalf("clear map = %#v, %v", alias, err)
	}
}

func TestMappingKeyOrderInMerge(t *testing.T) {
	doc := "? {a: 1, b: 2}\n: explicit\n<<: {? {b: 2, a: 1}: default}\n"
	var out MapSlice
	if err := Unmarshal([]byte(doc), &out); err != nil || len(out) != 1 || out[0].Value != "explicit" {
		t.Fatalf("mapping key merge = %#v, %v", out, err)
	}
	var seq MapSlice
	if err := UnmarshalStrict([]byte("? [a, b]\n: one\n? [b, a]\n: two\n"), &seq); err != nil || len(seq) != 2 {
		t.Fatalf("sequence keys = %#v, %v", seq, err)
	}
	if err := UnmarshalStrict([]byte("a: chosen\n<<: {a: {nested: 1, nested: 2}}\n"), &out); err == nil {
		t.Fatal("invalid shadowed MapSlice value accepted")
	}
}

func TestScalarResolutionBoundaries(t *testing.T) {
	for _, doc := range []string{"0b-10", "0b-1"} {
		var got interface{}
		if err := Unmarshal([]byte(doc), &got); err != nil || got != doc {
			t.Fatalf("%q = %#v, %v", doc, got, err)
		}
	}
	var binary int
	if err := Unmarshal([]byte("-0b10"), &binary); err != nil || binary != -2 {
		t.Fatalf("binary = %d, %v", binary, err)
	}
	var unsigned uint64
	if err := Unmarshal([]byte("+18446744073709551615"), &unsigned); err != nil || unsigned != math.MaxUint64 {
		t.Fatalf("uint64 = %d, %v", unsigned, err)
	}
	var signed int64
	if err := Unmarshal([]byte("-9223372036854775809"), &signed); err == nil {
		t.Fatal("out-of-range integer accepted")
	}
	if err := Unmarshal([]byte("-9223372036854775808"), &signed); err != nil || signed != math.MinInt64 {
		t.Fatalf("int64 = %d, %v", signed, err)
	}
	var float float64
	if err := Unmarshal([]byte("1e3"), &float); err != nil || float != 1000 {
		t.Fatalf("float = %g, %v", float, err)
	}
	if err := Unmarshal([]byte("18446744073709551616"), &float); err != nil || float != 0x1p64 {
		t.Fatalf("large float destination = %g, %v", float, err)
	}
}

func TestFlowIndicatorsAndUnicode(t *testing.T) {
	var out interface{}
	for _, doc := range []string{"[: value]", "{: value}", "[? : value]", `{"name":1}`, "{key:}", "{key:[]}", "[?name, :name, https://example.com]"} {
		if err := Unmarshal([]byte(doc), &out); err != nil {
			t.Errorf("%q: %v", doc, err)
		}
	}
	var values []string
	if err := Unmarshal([]byte("[?name, :name, https://example.com]"), &values); err != nil ||
		!reflect.DeepEqual(values, []string{"?name", ":name", "https://example.com"}) {
		t.Fatalf("flow scalars = %#v, %v", values, err)
	}
	var mapping map[string]int
	if err := Unmarshal([]byte(`{"name":1}`), &mapping); err != nil || mapping["name"] != 1 {
		t.Fatalf("adjacent colon = %#v, %v", mapping, err)
	}
	for _, tt := range []struct{ doc, want string }{
		{`"\U00000041"`, "A"},
		{`"\U0001F600"`, "\U0001F600"},
	} {
		var got string
		if err := Unmarshal([]byte(tt.doc), &got); err != nil || got != tt.want {
			t.Errorf("%q = %q, %v", tt.doc, got, err)
		}
	}
	var got string
	if err := Unmarshal([]byte(`"\UFFFFFFFF"`), &got); err == nil {
		t.Error("invalid Unicode escape accepted")
	}
}

func TestBOMBeforeFollowingDocument(t *testing.T) {
	for _, input := range []string{"first\n\ufeff---\nsecond\n", "first\n\ufeff\n---\nsecond\n"} {
		d := NewDecoder(strings.NewReader(input))
		for _, want := range []string{"first", "second"} {
			var got string
			if err := d.Decode(&got); err != nil || got != want {
				t.Fatalf("%q: got %q, want %q: %v", input, got, want, err)
			}
		}
	}
}

type nilTimeMarshaler struct{}

func (nilTimeMarshaler) MarshalYAML() (interface{}, error) {
	var value *time.Time
	return value, nil
}

func TestMarshalerReturnsNilPointer(t *testing.T) {
	data, err := Marshal(nilTimeMarshaler{})
	if err != nil || string(data) != "null\n" {
		t.Fatalf("marshal = %q, %v", data, err)
	}
}

type emptyReader struct{ calls int }

var errTooManyEmptyReads = errors.New("read after 100 empty reads")

func (r *emptyReader) Read([]byte) (int, error) {
	r.calls++
	if r.calls > 100 {
		return 0, errTooManyEmptyReads
	}
	return 0, nil
}

func TestReaderNoProgress(t *testing.T) {
	r := &emptyReader{}
	var out interface{}
	err := NewDecoder(r).Decode(&out)
	if err == nil || !strings.Contains(err.Error(), io.ErrNoProgress.Error()) || r.calls != 100 {
		t.Fatalf("error = %v, reads = %d", err, r.calls)
	}
}

type transientEmptyReader struct {
	calls int
	r     io.Reader
}

func (r *transientEmptyReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls < 3 {
		return 0, nil
	}
	return r.r.Read(p)
}

func TestReaderTransientEmptyReads(t *testing.T) {
	r := &transientEmptyReader{r: strings.NewReader("value: 42\n")}
	var out map[string]int
	if err := NewDecoder(r).Decode(&out); err != nil || out["value"] != 42 {
		t.Fatalf("out = %#v, %v", out, err)
	}
}

type errorAfterDataReader struct {
	reader io.Reader
	err    error
}

func (r *errorAfterDataReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		return n, r.err
	}
	return n, err
}

func TestReaderErrorAfterFinalMarker(t *testing.T) {
	sentinel := errors.New("terminal failure")
	doc := "---\na: 1\n...\n---\nb: 2\n...\n"
	for _, reader := range []io.Reader{
		&dataAndErrorReader{data: []byte(doc), err: sentinel},
		&errorAfterDataReader{reader: strings.NewReader(doc), err: sentinel},
	} {
		d := NewDecoder(reader)
		for _, key := range []string{"a", "b"} {
			var out map[string]int
			if err := d.Decode(&out); err != nil || out[key] == 0 {
				t.Fatalf("%T %s = %#v, %v", reader, key, out, err)
			}
		}
		var out interface{}
		if err := d.Decode(&out); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
			t.Fatalf("%T final error = %v", reader, err)
		}
	}
}

func TestPendingReaderErrorAtDocumentMarker(t *testing.T) {
	sentinel := errors.New("incomplete marker")
	for _, tt := range []struct {
		doc      string
		err      error
		complete bool
	}{
		{"value\n...", sentinel, false},
		{"value\n...\n", sentinel, true},
		{"value\n...", io.EOF, true},
	} {
		d := NewDecoder(&dataAndErrorReader{data: []byte(tt.doc), err: tt.err})
		var value string
		err := d.Decode(&value)
		if tt.complete {
			if err != nil || value != "value" {
				t.Fatalf("%q = %q, %v", tt.doc, value, err)
			}
			err = d.Decode(&value)
			if tt.err == io.EOF && !errors.Is(err, io.EOF) || tt.err != io.EOF && (err == nil || !strings.Contains(err.Error(), sentinel.Error())) {
				t.Fatalf("%q next error = %v", tt.doc, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
			t.Fatalf("%q error = %v", tt.doc, err)
		}
	}
}

func TestMergeTagsAndDuplicatePointerKeys(t *testing.T) {
	for _, doc := range []string{"! <<: literal\n", "\"<<\": literal\n"} {
		var out map[string]string
		if err := Unmarshal([]byte(doc), &out); err != nil || out["<<"] != "literal" {
			t.Fatalf("%q = %#v, %v", doc, out, err)
		}
	}
	for _, doc := range []string{"<<: {key: value}\n", "!!merge <<: {key: value}\n"} {
		var out map[string]string
		if err := Unmarshal([]byte(doc), &out); err != nil || out["key"] != "value" {
			t.Fatalf("%q = %#v, %v", doc, out, err)
		}
	}
	var pointers map[*string]int
	if err := Unmarshal([]byte("key: 1\nkey: 2\n"), &pointers); err != nil || len(pointers) != 1 {
		t.Fatalf("pointer keys = %#v, %v", pointers, err)
	}
	for key, value := range pointers {
		if *key != "key" || value != 2 {
			t.Fatalf("pointer key = %q: %d", *key, value)
		}
	}
	if err := UnmarshalStrict([]byte("key: 1\nkey: 2\n"), &pointers); err == nil {
		t.Fatal("strict duplicate pointer key accepted")
	}
	var floats map[float64]int
	if err := Unmarshal([]byte(".nan: 1\n.nan: 2\n"), &floats); err != nil || len(floats) != 1 {
		t.Fatalf("NaN keys = %#v, %v", floats, err)
	}
	for _, value := range floats {
		if value != 2 {
			t.Fatalf("NaN value = %d", value)
		}
	}
}

func TestComparableArrayKeysIndexed(t *testing.T) {
	var keys mapSliceKeys
	for i := 0; i < 1000; i++ {
		keys.add([2]int{i, 1})
	}
	if len(keys.scalars) != 1000 || !keys.contains([2]int{999, 1}) || keys.contains([2]int{1000, 1}) {
		t.Fatal("comparable array keys not indexed correctly")
	}
	var out map[[2]int]string
	if err := Unmarshal([]byte("? [1, 2]\n: first\n? [1, 2]\n: last\n"), &out); err != nil || len(out) != 1 || out[[2]int{1, 2}] != "last" {
		t.Fatalf("array keys = %#v, %v", out, err)
	}
}

func TestDocumentReleasesParserReference(t *testing.T) {
	p := newParser([]byte("a: &value 1\nb: *value\n"))
	defer p.destroy()
	doc := p.parse()
	if doc == nil || len(doc.children) != 1 || p.doc != nil {
		t.Fatalf("document = %#v, parser document = %#v", doc, p.doc)
	}
}

func TestConsumedAndCompactedTokensReleasePayloads(t *testing.T) {
	parser := yaml_parser_t{
		tokens: []yaml_token_t{
			{typ: yaml_SCALAR_TOKEN, value: []byte("first")},
			{typ: yaml_SCALAR_TOKEN, value: []byte("second")},
			{typ: yaml_SCALAR_TOKEN, value: []byte("third")},
			{typ: yaml_SCALAR_TOKEN, value: []byte("fourth")},
		},
	}
	skip_token(&parser)
	if parser.tokens[0].value != nil {
		t.Fatal("consumed token retains payload")
	}
	skip_token(&parser)
	token := yaml_token_t{typ: yaml_VALUE_TOKEN}
	yaml_insert_token(&parser, -1, &token)
	if string(parser.tokens[0].value) != "third" || string(parser.tokens[1].value) != "fourth" || parser.tokens[2].typ != yaml_VALUE_TOKEN || parser.tokens[:cap(parser.tokens)][3].value != nil {
		t.Fatalf("compacted queue = %#v", parser.tokens)
	}
}

func TestBOMLessUTF16(t *testing.T) {
	for _, little := range []bool{false, true} {
		var input []byte
		for _, c := range []byte("key: value\n") {
			if little {
				input = append(input, c, 0)
			} else {
				input = append(input, 0, c)
			}
		}
		var out map[string]string
		if err := Unmarshal(input, &out); err != nil || out["key"] != "value" {
			t.Fatalf("little=%t: %#v, %v", little, out, err)
		}
	}
}

func TestBOMCommentAndFlowTags(t *testing.T) {
	d := NewDecoder(strings.NewReader("one\n\ufeff# comment\n---\ntwo\n"))
	for _, want := range []string{"one", "two"} {
		var value string
		if err := d.Decode(&value); err != nil || value != want {
			t.Fatalf("value = %q, want %q, %v", value, want, err)
		}
		var quoted string
		if err := Unmarshal([]byte("\"one\\n\ufeff# text\""), &quoted); err != nil || quoted != "one\n\ufeff# text" {
			t.Fatalf("quoted BOM = %q, %v", quoted, err)
		}
	}
	var values []interface{}
	if err := Unmarshal([]byte("[!foo, next]"), &values); err != nil || len(values) != 2 || values[1] != "next" {
		t.Fatalf("flow tags = %#v, %v", values, err)
	}
}

func TestFloatKeysAndFlowTimestampKeys(t *testing.T) {
	data, err := Marshal(MapSlice{{Key: 1, Value: "integer"}, {Key: 1.0, Value: "float"}})
	if err != nil {
		t.Fatal(err)
	}
	var out map[interface{}]string
	if err := Unmarshal(data, &out); err != nil || len(out) != 2 || out[1] != "integer" || out[1.0] != "float" {
		t.Fatalf("float keys = %q, %#v, %v", data, out, err)
	}
	timestamp := time.Date(2024, 1, 1, 1, 2, 3, 0, time.UTC)
	type flowMap struct {
		Values MapSlice `yaml:"values,flow"`
	}
	text := timestamp.Format(time.RFC3339Nano)
	data, err = Marshal(flowMap{MapSlice{{Key: timestamp, Value: 1}, {Key: text, Value: 2}}})
	if err != nil || !strings.Contains(string(data), "!!timestamp") {
		t.Fatalf("distinct flow timestamp and string = %q, %v", data, err)
	}
	var decoded struct {
		Values MapSlice `yaml:"values"`
	}
	if err := UnmarshalStrict(data, &decoded); err != nil || len(decoded.Values) != 2 ||
		decoded.Values[0].Key != text || decoded.Values[0].Value != 1 ||
		decoded.Values[1].Key != text || decoded.Values[1].Value != 2 {
		t.Fatalf("timestamp round trip = %#v, %v", decoded, err)
	}
	var typed map[time.Time]int
	if err := Unmarshal([]byte(text+": 1\n"), &typed); err != nil || typed[timestamp] != 1 {
		t.Fatalf("typed timestamp key = %#v, %v", typed, err)
	}
	var ordinary interface{}
	if err := Unmarshal([]byte(text), &ordinary); err != nil || ordinary != text {
		t.Fatalf("timestamp value = %#v, %v", ordinary, err)
	}
	other := timestamp.In(time.FixedZone("plus", 3600))
	if _, err := Marshal(flowMap{MapSlice{{Key: timestamp, Value: 1}, {Key: other, Value: 2}}}); err == nil {
		t.Fatal("equivalent flow timestamp keys accepted")
	}
	if _, err := Marshal(MapSlice{{Key: timestamp, Value: 1}, {Key: text, Value: 2}}); err != nil {
		t.Fatalf("block timestamp and string should differ: %v", err)
	}
}

type selfMarshalerKey struct {
	Name string
	Next *selfMarshalerKey `yaml:",omitempty"`
}

func (k *selfMarshalerKey) MarshalYAML() (interface{}, error) { return k, nil }

func TestSelfReturningMarshalerMappingKey(t *testing.T) {
	key := &selfMarshalerKey{Name: "ok"}
	if data, err := Marshal(MapSlice{{Key: key, Value: 1}}); err != nil || !strings.Contains(string(data), "name: ok") {
		t.Fatalf("self replacement = %q, %v", data, err)
	}
	key.Next = key
	if _, err := Marshal(MapSlice{{Key: key, Value: 1}}); err == nil || !strings.Contains(err.Error(), "cyclic mapping key") {
		t.Fatalf("ancestor cycle error = %v", err)
	}
}

type replacementKey struct{ replacement *replacementKey }

func (k replacementKey) MarshalYAML() (interface{}, error) {
	return k.replacement, nil
}

type integerReplacementKey struct{ value int }

func (k integerReplacementKey) MarshalYAML() (interface{}, error) {
	return &k.value, nil
}

func TestMappingKeyReplacementPointers(t *testing.T) {
	key := &replacementKey{}
	key.replacement = key
	if _, err := Marshal(MapSlice{{Key: *key, Value: 1}}); err == nil || !strings.Contains(err.Error(), "cyclic mapping key") {
		t.Fatalf("cyclic replacement error = %v", err)
	}
	if data, err := Marshal(MapSlice{{Key: integerReplacementKey{42}, Value: 1}}); err != nil || !strings.Contains(string(data), "42: 1") {
		t.Fatalf("acyclic replacement = %q, %v", data, err)
	}
}

func TestExplicitAndQuotedMergeKeys(t *testing.T) {
	for _, doc := range []string{
		"<<: {key: base}\nkey: explicit\n",
		"!!merge \"<<\": {key: base}\nkey: explicit\n",
	} {
		var out map[string]interface{}
		if err := UnmarshalStrict([]byte(doc), &out); err != nil || out["key"] != "explicit" {
			t.Fatalf("%q = %#v, %v", doc, out, err)
		}
	}
	var literal map[string]interface{}
	if err := UnmarshalStrict([]byte("\"<<\": literal\n"), &literal); err != nil || literal["<<"] != "literal" {
		t.Fatalf("quoted merge key = %#v, %v", literal, err)
	}
}

func TestMixedIntegerKeyOrder(t *testing.T) {
	values := []interface{}{int64(-1), uint64(0), int64(9007199254740992), uint64(9007199254740993), int64(9007199254740994), uint64(math.MaxUint64)}
	keys := make(keyList, len(values))
	for i, value := range values {
		keys[i] = reflect.ValueOf(value)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if !keys.Less(i, j) || keys.Less(j, i) {
				t.Fatalf("incorrect order: %v and %v", values[i], values[j])
			}
		}
	}
}

func TestTimestampKeyFingerprintBuckets(t *testing.T) {
	var keys mapSliceKeys
	for i := 0; i < 100; i++ {
		keys.add(time.Date(2024, 1, 1, 0, 0, i, 0, time.UTC))
	}
	if len(keys.complex) != 100 || !keys.contains(time.Date(2024, 1, 1, 0, 0, 50, 0, time.UTC)) {
		t.Fatal("timestamp keys were not indexed by value")
	}
}

func TestArrayNaNMappingKeys(t *testing.T) {
	var out map[[1]float64]string
	if err := UnmarshalStrict([]byte("? [.nan]\n: first\n? [.nan]\n: second\n"), &out); err == nil {
		t.Fatal("duplicate NaN array keys accepted")
	}
	out = nil
	if err := UnmarshalStrict([]byte("? [1.0]\n: first\n? [2.0]\n: second\n"), &out); err != nil || len(out) != 2 {
		t.Fatalf("finite array keys = %#v, %v", out, err)
	}
}

func TestStructNaNMappingKeys(t *testing.T) {
	type key struct {
		Value float64 `yaml:"value"`
	}
	var out map[key]string
	if err := UnmarshalStrict([]byte("? {value: .nan}\n: first\n? {value: .NaN}\n: second\n"), &out); err == nil {
		t.Fatal("duplicate NaN struct keys accepted")
	}
	out = nil
	if err := UnmarshalStrict([]byte("? {value: 1.0}\n: first\n? {value: 2.0}\n: second\n"), &out); err != nil || len(out) != 2 {
		t.Fatalf("finite struct keys = %#v, %v", out, err)
	}
}

func TestArrayKeyFingerprintValues(t *testing.T) {
	a := [2]float64{math.NaN(), 1}
	b := [2]float64{math.NaN(), 2}
	if yamlKeyFingerprint(a) == yamlKeyFingerprint(b) {
		t.Fatal("different arrays share a fingerprint")
	}
	a[1], b[1] = 0, math.Copysign(0, -1)
	if !equivalentYAMLKey(a, b) || yamlKeyFingerprint(a) != yamlKeyFingerprint(b) {
		t.Fatal("equivalent NaN and signed-zero arrays have different fingerprints")
	}
}

func TestStructKeyFingerprintValues(t *testing.T) {
	type key struct {
		Value float64
		ID    int
		zero  float64
	}
	a := key{Value: math.NaN(), ID: 1}
	b := key{Value: math.NaN(), ID: 2}
	var keys mapSliceKeys
	keys.add(a)
	keys.add(b)
	if len(keys.complex) != 2 || !keys.contains(b) {
		t.Fatal("distinct struct keys were not separately indexed")
	}
	b.ID, b.zero = 1, math.Copysign(0, -1)
	if !equivalentYAMLKey(a, b) || yamlKeyFingerprint(a) != yamlKeyFingerprint(b) || !keys.contains(b) {
		t.Fatal("equivalent struct keys have different fingerprints")
	}
	type privateKey struct{ field interface{} }
	first, second := privateKey{field: 1}, privateKey{field: 2}
	if yamlKeyFingerprint(first) == yamlKeyFingerprint(second) {
		t.Fatal("distinct unexported interface fields share a fingerprint")
	}
	first.field, second.field = 0.0, math.Copysign(0, -1)
	if !equivalentYAMLKey(first, second) || yamlKeyFingerprint(first) != yamlKeyFingerprint(second) {
		t.Fatal("equivalent unexported interface fields have different fingerprints")
	}
}

func TestComplexMapKeysHaveStableOrder(t *testing.T) {
	input := map[[1]int]string{{1}: "one", {2}: "two", {3}: "three"}
	first, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		next, err := Marshal(input)
		if err != nil || string(next) != string(first) {
			t.Fatalf("map order changed: %q, %q, %v", first, next, err)
		}
	}
}

type firstScalarSortKey int
type secondScalarSortKey int

func (firstScalarSortKey) MarshalYAML() (interface{}, error)  { return "first", nil }
func (secondScalarSortKey) MarshalYAML() (interface{}, error) { return "second", nil }

func TestEqualScalarKeysHaveStableOrder(t *testing.T) {
	input := map[interface{}]int{firstScalarSortKey(1): 1, secondScalarSortKey(1): 2}
	first, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		next, err := Marshal(input)
		if err != nil || string(next) != string(first) {
			t.Fatalf("map order changed: %q, %q, %v", first, next, err)
		}
	}
}

func TestTimestampOffsetSeconds(t *testing.T) {
	for _, offset := range []int{90, 120} {
		value := time.Date(2024, 1, 1, 12, 0, 0, 0, time.FixedZone("zone", offset))
		data, err := Marshal(value)
		var decoded time.Time
		if err != nil || Unmarshal(data, &decoded) != nil || !decoded.Equal(value) {
			t.Fatalf("offset %d: %q, decoded %v, %v", offset, data, decoded, err)
		}
		if offset == 120 && !strings.Contains(string(data), "+00:02") {
			t.Fatalf("whole-minute offset not preserved: %q", data)
		}
	}
}

func TestTimestampEncodingBounds(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value time.Time
		valid bool
	}{
		{"year zero", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"year 9999", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"maximum offset", time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("offset", 23*60*60+59*60)), true},
		{"negative year", time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"invalid offset", time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("24 hours", 24*60*60)), false},
		{"normalized before year zero", time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("second", 1)), false},
		{"normalized after year 9999", time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("second", -1)), false},
		{"normalized into range", time.Date(10000, 1, 1, 0, 0, 0, 0, time.FixedZone("second", 1)), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, input := range []interface{}{
				tt.value,
				struct {
					Values []time.Time `yaml:"values,flow"`
				}{[]time.Time{tt.value}},
			} {
				data, err := Marshal(input)
				if !tt.valid {
					if err == nil {
						t.Errorf("%T accepted invalid timestamp: %q", input, data)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%T: %v", input, err)
				}
				switch input.(type) {
				case time.Time:
					var got time.Time
					if err := Unmarshal(data, &got); err != nil || !got.Equal(tt.value) {
						t.Errorf("%q = %v, %v", data, got, err)
					}
				default:
					var got struct {
						Values []time.Time `yaml:"values"`
					}
					if err := Unmarshal(data, &got); err != nil || len(got.Values) != 1 || !got.Values[0].Equal(tt.value) {
						t.Errorf("%q = %#v, %v", data, got, err)
					}
				}
			}
			if !tt.valid {
				encoder := NewEncoder(io.Discard)
				err := encoder.Encode(tt.value)
				if err == nil || encoder.Close() != err {
					t.Fatalf("streaming encoder error = %v", err)
				}
			}
		})
	}
}

func TestDistinctYAMLKeysCannotOverwriteMapKey(t *testing.T) {
	key := time.Date(2024, 1, 1, 1, 2, 3, 0, time.UTC)
	value := struct {
		Values MapSlice `yaml:"values,flow"`
	}{MapSlice{{Key: key, Value: 1}, {Key: key.Format(time.RFC3339Nano), Value: 2}}}
	data, err := Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, decode := range []func([]byte, interface{}) error{Unmarshal, UnmarshalStrict} {
		var out struct {
			Values map[interface{}]int `yaml:"values"`
		}
		if err := decode(data, &out); err == nil {
			t.Fatalf("silently lost distinct YAML key: %q, %#v", data, out)
		}
	}
}

func TestMixedIntegerFloatKeyOrder(t *testing.T) {
	values := []interface{}{
		math.Inf(-1), int64(math.MinInt64), float64(-0x1p63),
		int64(-9007199254740993), float64(-9007199254740992),
		int64(-1), -0.5, uint64(0), float64(0.5), uint64(9007199254740992),
		uint64(9007199254740993), float64(9007199254740994),
		int64(math.MaxInt64), float64(0x1p63), uint64(math.MaxUint64), float64(0x1p64), math.Inf(1),
	}
	keys := make(keyList, len(values))
	for i := range values {
		keys[i] = reflect.ValueOf(values[i])
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if !keys.Less(i, j) || keys.Less(j, i) {
				t.Fatalf("incorrect order: %v and %v", values[i], values[j])
			}
		}
	}
	if !(keyList{reflect.ValueOf(math.NaN()), reflect.ValueOf(int64(0))}).Less(0, 1) {
		t.Fatal("NaN should sort before an integer")
	}
}

func TestInterfaceTimestampMappingKeys(t *testing.T) {
	var out map[interface{}]string
	if err := Unmarshal([]byte("2024-01-01: value\n"), &out); err != nil || out["2024-01-01"] != "value" {
		t.Fatalf("timestamp key = %#v, %v", out, err)
	}
	doc := "2024-01-01T00:00:00Z: explicit\n<<: {2024-01-01: default}\n"
	out = nil
	if err := UnmarshalStrict([]byte(doc), &out); err != nil || len(out) != 1 || out["2024-01-01T00:00:00Z"] != "explicit" {
		t.Fatalf("timestamp merge = %#v, %v", out, err)
	}
	if err := UnmarshalStrict([]byte("2024-01-01: first\n2024-01-01T00:00:00Z: second\n"), &out); err == nil {
		t.Fatal("equivalent timestamp keys accepted")
	}
}

func TestDestinationKeyCollisionsUseWinningKeys(t *testing.T) {
	for _, doc := range []string{
		"2024-01-01: first\n2024-01-01T00:00:00Z: winner\n\"2024-01-01\": distinct\n",
		"2024-01-01: first\n\"2024-01-01\": distinct\n2024-01-01T00:00:00Z: winner\n",
		"2024-01-01: first\n2024-01-01T00:00:00Z: winner\n<<: {\"2024-01-01\": distinct}\n",
	} {
		var got map[interface{}]string
		want := map[interface{}]string{"2024-01-01T00:00:00Z": "winner", "2024-01-01": "distinct"}
		if err := Unmarshal([]byte(doc), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %#v, %v; want %#v", doc, got, err, want)
		}
	}
	for _, decode := range []func([]byte, interface{}) error{Unmarshal, UnmarshalStrict} {
		var got map[string]string
		if err := decode([]byte("1: integer\n\"1\": string\n"), &got); err == nil {
			t.Error("distinct keys with the same destination accepted")
		}
	}
	var conflicted map[interface{}]string
	doc := "2024-01-01: first\n2024-01-01T00:00:00Z: winner\n\"2024-01-01T00:00:00Z\": conflict\n"
	if err := Unmarshal([]byte(doc), &conflicted); err == nil {
		t.Error("collision with winning destination accepted")
	}
	var merged map[interface{}]interface{}
	if err := UnmarshalForJSON([]byte("<<: {01: first, 1: winner}\n"), &merged, false); err != nil ||
		merged[JSONMergeKey{Key: 1, Priority: 1}] != "winner" {
		t.Fatalf("merged duplicate = %#v, %v", merged, err)
	}
}

func TestCompositeTimestampMappingKeys(t *testing.T) {
	var out MapSlice
	doc := "? [2024-01-01]\n: timestamp\n? [\"2024-01-01\"]\n: string\n"
	if err := UnmarshalStrict([]byte(doc), &out); err != nil || len(out) != 2 {
		t.Fatalf("distinct composite keys = %#v, %v", out, err)
	}
	if !reflect.DeepEqual(out[0].Key, []interface{}{"2024-01-01"}) || !reflect.DeepEqual(out[1].Key, []interface{}{"2024-01-01"}) {
		t.Fatalf("decoded key values changed: %#v", out)
	}
	if err := UnmarshalStrict([]byte("? [2024-01-01]\n: first\n? [2024-01-01T00:00:00Z]\n: second\n"), &out); err == nil {
		t.Fatal("equivalent composite timestamp keys accepted")
	}
	doc = "? [{date: 2024-01-01}]\n: timestamp\n? [{date: \"2024-01-01\"}]\n: string\n"
	if err := UnmarshalStrict([]byte(doc), &out); err != nil || len(out) != 2 {
		t.Fatalf("nested composite keys = %#v, %v", out, err)
	}
	doc = "<<:\n  ? [2024-01-01]\n  : merged\n? [2024-01-01T00:00:00Z]\n: explicit\n"
	if err := UnmarshalStrict([]byte(doc), &out); err != nil || len(out) != 1 || out[0].Value != "explicit" {
		t.Fatalf("composite timestamp merge = %#v, %v", out, err)
	}
}

func TestTypedCollectionKeyIdentity(t *testing.T) {
	for _, decode := range []struct {
		name string
		fn   func([]byte, interface{}) error
	}{
		{"ordinary", Unmarshal},
		{"strict", UnmarshalStrict},
	} {
		t.Run(decode.name, func(t *testing.T) {
			for _, doc := range []string{
				"? [1]\n: first\n? [\"1\"]\n: second\n",
				"? [1]\n: explicit\n<<: { ? [\"1\"] : merged }\n",
			} {
				var out map[[1]string]string
				err := decode.fn([]byte(doc), &out)
				if err == nil || !strings.Contains(err.Error(), "distinct YAML keys map to the same destination key") {
					t.Errorf("%q = %#v, %v", doc, out, err)
				}
			}
			var distinct map[[1]string]string
			if err := decode.fn([]byte("? [1]\n: first\n? [2]\n: second\n"), &distinct); err != nil ||
				len(distinct) != 2 || distinct[[1]string{"1"}] != "first" || distinct[[1]string{"2"}] != "second" {
				t.Errorf("distinct destination keys = %#v, %v", distinct, err)
			}
			var out map[struct {
				Value string `yaml:"value"`
			}]string
			doc := "? {value: 1}\n: first\n? {value: \"1\"}\n: second\n"
			if err := decode.fn([]byte(doc), &out); err == nil || !strings.Contains(err.Error(), "distinct YAML keys map to the same destination key") {
				t.Errorf("mapping keys = %#v, %v", out, err)
			}
		})
	}
	var nested map[struct {
		Value string `yaml:"value"`
	}]string
	doc := "? {value: one, ignored: {? [a]: b}}\n: first\n"
	if err := Unmarshal([]byte(doc), &nested); err != nil || len(nested) != 1 {
		t.Errorf("nested collection key = %#v, %v", nested, err)
	}
	doc = "? [01]\n: first\n? [1]\n: second\n"
	var ordinary map[[1]string]string
	if err := Unmarshal([]byte(doc), &ordinary); err != nil || len(ordinary) != 1 || ordinary[[1]string{"1"}] != "second" {
		t.Errorf("ordinary duplicates = %#v, %v", ordinary, err)
	}
	var strict map[[1]string]string
	if err := UnmarshalStrict([]byte(doc), &strict); err == nil || !strings.Contains(err.Error(), "already set") {
		t.Errorf("strict duplicates = %#v, %v", strict, err)
	}
	doc = "? &key [1]\n: first\n? *key\n: second\n"
	if err := UnmarshalStrict([]byte(doc), &strict); err == nil || !strings.Contains(err.Error(), "already set") {
		t.Errorf("aliased duplicates = %#v, %v", strict, err)
	}
	var maps map[struct {
		A int `yaml:"a"`
		B int `yaml:"b"`
	}]string
	doc = "? {a: 1, b: 2}\n: first\n? {b: 2, a: 1}\n: second\n"
	if err := UnmarshalStrict([]byte(doc), &maps); err == nil || !strings.Contains(err.Error(), "already set") {
		t.Errorf("reordered mapping duplicates = %#v, %v", maps, err)
	}
}

func TestSourceKeyTagsAndBinaryIdentity(t *testing.T) {
	for _, doc := range []string{
		"? !first value\n: one\n? !second value\n: two\n",
		"? !!binary YQ==\n: one\n? a\n: two\n",
		"? [!first value]\n: one\n? [!second value]\n: two\n",
	} {
		var got MapSlice
		if err := UnmarshalStrict([]byte(doc), &got); err != nil || len(got) != 2 {
			t.Errorf("%q = %#v, %v", doc, got, err)
		}
	}
	var got MapSlice
	if err := UnmarshalStrict([]byte("? !!binary YQ==\n: one\n? !!binary YQ\n: two\n"), &got); err == nil {
		t.Fatal("invalid binary key accepted")
	}
	if err := UnmarshalStrict([]byte("? !!binary YQ==\n: one\n? !!binary YR==\n: two\n"), &got); err == nil || !strings.Contains(err.Error(), "already set") {
		t.Fatal("equivalent binary keys accepted")
	}
	var target map[string]string
	if err := UnmarshalStrict([]byte("? !first value\n: one\n? !second value\n: two\n"), &target); err == nil {
		t.Fatal("distinct source keys silently collided in destination")
	}
}

func TestFlowTimestampAndFloat32Keys(t *testing.T) {
	stamp := time.Date(2024, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600))
	input := struct {
		Values []time.Time `yaml:"values,flow"`
	}{[]time.Time{stamp}}
	data, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Values []time.Time `yaml:"values"`
	}
	if err := Unmarshal(data, &decoded); err != nil || len(decoded.Values) != 1 || !decoded.Values[0].Equal(stamp) {
		t.Fatalf("flow timestamp = %q, %#v, %v", data, decoded, err)
	}
	block, err := Marshal(stamp)
	if err != nil || strings.Contains(string(block), "!!timestamp") {
		t.Fatalf("block timestamp = %q, %v", block, err)
	}
	data, err = Marshal(map[interface{}]string{float32(0.1): "float32", float64(0.1): "float64"})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[interface{}]string
	if err := Unmarshal(data, &keys); err != nil || len(keys) != 2 || keys[float64(float32(0.1))] != "float32" || keys[float64(0.1)] != "float64" {
		t.Fatalf("float keys = %q, %#v, %v", data, keys, err)
	}
	if _, err := Marshal(map[interface{}]string{float32(0.5): "float32", float64(0.5): "float64"}); err == nil {
		t.Fatal("equivalent float keys accepted")
	}
	if data, err := Marshal(float32(0.1)); err != nil || string(data) != "0.1\n" {
		t.Fatalf("ordinary float32 = %q, %v", data, err)
	}
}

func TestBOMAtExplicitDocumentBoundaries(t *testing.T) {
	for _, doc := range []string{
		"first\n...\n\ufeffsecond\n",
		"first\n...\n\ufeff---\nsecond\n",
	} {
		decoder := NewDecoder(strings.NewReader(doc))
		var first, second string
		if err := decoder.Decode(&first); err != nil {
			t.Fatalf("%q first: %v", doc, err)
		}
		if err := decoder.Decode(&second); err != nil || first != "first" || second != "second" {
			t.Errorf("%q = %q, %q, %v", doc, first, second, err)
		}
	}
	decoder := NewDecoder(strings.NewReader("first\n\ufeffsecond\n"))
	var value string
	if err := decoder.Decode(&value); err == nil {
		t.Fatal("BOM within a document accepted")
	}
}

func TestBOMBeforeDocumentEnd(t *testing.T) {
	for _, doc := range []string{
		"first\n\ufeff...\n",
		"first\n\ufeff# comment\n...\n",
		"first\n...\n\ufeff...\n",
	} {
		decoder := NewDecoder(strings.NewReader(doc))
		for {
			var value interface{}
			err := decoder.Decode(&value)
			if err != nil {
				if !strings.Contains(err.Error(), "byte order mark") {
					t.Errorf("%q: unexpected error: %v", doc, err)
				}
				break
			}
		}
	}
}

func TestBareDocumentAfterExplicitEnd(t *testing.T) {
	for _, doc := range []string{
		"first\n...\nsecond\n",
		"first\n...\n# comment\n...\nsecond\n",
		"first\n...\n\ufeffsecond\n",
	} {
		decoder := NewDecoder(strings.NewReader(doc))
		var first, second string
		if err := decoder.Decode(&first); err != nil {
			t.Fatalf("%q first: %v", doc, err)
		}
		if err := decoder.Decode(&second); err != nil || first != "first" || second != "second" {
			t.Errorf("%q = %q, %q, %v", doc, first, second, err)
		}
	}
	for _, doc := range []string{
		"{first: one}\n{second: two}\n",
		"{first: one}\n...\n{second: two}\n{third: three}\n",
	} {
		decoder := NewDecoder(strings.NewReader(doc))
		var value map[string]string
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("%q first: %v", doc, err)
		}
		if strings.Contains(doc, "...") {
			if err := decoder.Decode(&value); err != nil {
				t.Fatalf("%q second: %v", doc, err)
			}
		}
		if err := decoder.Decode(&value); err == nil || errors.Is(err, io.EOF) {
			t.Errorf("%q: expected an error rejecting the bare document, got %v", doc, err)
		}
	}
}

func TestCompactQuotedFlowMapping(t *testing.T) {
	for _, doc := range []string{`{"id":demo}`, `{"id": demo}`} {
		var got map[string]string
		if err := Unmarshal([]byte(doc), &got); err != nil || got["id"] != "demo" {
			t.Errorf("%q = %#v, %v", doc, got, err)
		}
	}
}

func TestAliasedMergeKey(t *testing.T) {
	doc := "base: &base {value: merged, other: kept}\nmarker: &merge !!merge <<\nresult: {*merge: *base, value: explicit}\n"
	var out map[string]interface{}
	if err := Unmarshal([]byte(doc), &out); err != nil {
		t.Fatal(err)
	}
	result := out["result"].(map[interface{}]interface{})
	if result["value"] != "explicit" || result["other"] != "kept" {
		t.Fatalf("result = %#v", result)
	}
	doc = "marker: &ordinary !!str '<<'\nresult: {*ordinary: value}\n"
	if err := Unmarshal([]byte(doc), &out); err != nil || out["result"].(map[interface{}]interface{})["<<"] != "value" {
		t.Fatalf("ordinary alias = %#v, %v", out, err)
	}
}

func TestAliasedMergeSequence(t *testing.T) {
	doc := "defaults: &defaults [{first: one}, {second: two}]\nresult: {<<: *defaults, first: explicit}\n"
	// Decode the result separately because defaults is a sequence.
	var root struct {
		Result map[string]string `yaml:"result"`
	}
	if err := Unmarshal([]byte(doc), &root); err != nil || root.Result["first"] != "explicit" || root.Result["second"] != "two" {
		t.Fatalf("merge sequence = %#v, %v", root.Result, err)
	}
	if err := Unmarshal([]byte("value: &v scalar\nresult: {<<: *v}\n"), &root); err == nil || !strings.Contains(err.Error(), "map merge requires") {
		t.Fatal("scalar alias accepted as merge source")
	}
}

func TestJSONMergeKeyPriorities(t *testing.T) {
	doc := "ID: explicit\n<<: [{id: first}, {Id: second}]\n"
	var got interface{}
	if err := UnmarshalForJSON([]byte(doc), &got, false); err != nil {
		t.Fatal(err)
	}
	mapping := got.(map[interface{}]interface{})
	if mapping["ID"] != "explicit" {
		t.Fatalf("explicit mapping = %#v", mapping)
	}
	priorities := make(map[interface{}]int)
	for key := range mapping {
		if merged, ok := key.(JSONMergeKey); ok {
			priorities[merged.Key] = merged.Priority
		}
	}
	if priorities["id"] <= 0 || priorities["Id"] <= priorities["id"] {
		t.Fatalf("merge priorities = %#v", priorities)
	}
	if err := UnmarshalWithStringTimestampKeys([]byte(doc), &got, false); err != nil {
		t.Fatal(err)
	}
	mapping = got.(map[interface{}]interface{})
	if mapping["id"] != "first" || mapping["Id"] != "second" {
		t.Fatalf("ordinary timestamp-key mode = %#v", mapping)
	}
}

func TestSkippedMergedValuesStillValidated(t *testing.T) {
	doc := "value: explicit\n<<: {value: {<<: invalid}}\n"
	for _, out := range []interface{}{new(map[string]interface{}), new(MapSlice), new(struct {
		Value string `yaml:"value"`
	}), new(struct {
		Extra map[string]interface{} `yaml:",inline"`
	})} {
		if err := Unmarshal([]byte(doc), out); err == nil {
			t.Errorf("%T accepted malformed overridden merge", out)
		}
	}
}

func TestBOMInFlowAndPlainScalars(t *testing.T) {
	for _, doc := range []string{"[\n\ufeff---\n]", "foo\ufeffbar", "foo\n \ufeffbar"} {
		var out interface{}
		if err := Unmarshal([]byte(doc), &out); err == nil {
			t.Errorf("accepted BOM in %q: %#v", doc, out)
		}
	}
	for _, doc := range []string{"[one, two]", "\"foo\ufeffbar\""} {
		var out interface{}
		if err := Unmarshal([]byte(doc), &out); err != nil {
			t.Errorf("%q: %v", doc, err)
		}
	}
}

type cyclicDecodedKey struct {
	Value int
	Next  *cyclicDecodedKey
}

func (k *cyclicDecodedKey) UnmarshalYAML(unmarshal func(interface{}) error) error {
	if err := unmarshal(&k.Value); err != nil {
		return err
	}
	k.Next = k
	return nil
}

func TestCyclicDecodedMappingKeys(t *testing.T) {
	var out map[*cyclicDecodedKey]int
	if err := UnmarshalStrict([]byte("1: 1\n1: 2\n"), &out); err == nil {
		t.Fatal("equivalent cyclic keys accepted")
	}
	out = nil
	if err := UnmarshalStrict([]byte("1: 1\n2: 2\n"), &out); err != nil || len(out) != 2 {
		t.Fatalf("distinct cyclic keys = %#v, %v", out, err)
	}
}

type recursiveMapMarshaler int
type recursiveSliceMarshaler int

func (recursiveMapMarshaler) MarshalYAML() (interface{}, error) {
	value := map[string]interface{}{}
	value["self"] = value
	return value, nil
}

func (recursiveSliceMarshaler) MarshalYAML() (interface{}, error) {
	value := make([]interface{}, 1)
	value[0] = value
	return value, nil
}

func TestCyclicReplacementMappingKeys(t *testing.T) {
	for _, key := range []interface{}{recursiveMapMarshaler(1), recursiveSliceMarshaler(1)} {
		if _, err := Marshal(MapSlice{{Key: key, Value: 1}}); err == nil || !strings.Contains(err.Error(), "cyclic mapping key") {
			t.Errorf("%T: %v", key, err)
		}
	}
}

func TestParserErrorLineNumbers(t *testing.T) {
	p := parser{}
	yaml_parser_set_parser_error(&p.parser, "test problem", yaml_mark_t{})
	var err error
	func() {
		defer handleErr(&err)
		p.fail()
	}()
	if err == nil || !strings.Contains(err.Error(), "line 1:") {
		t.Fatalf("parser error: %v", err)
	}
	var out interface{}
	if err := Unmarshal([]byte("@bad"), &out); err == nil || !strings.Contains(err.Error(), "line 1:") {
		t.Fatalf("scanner error: %v", err)
	}
}

func TestFlowMappingEmptyValueMark(t *testing.T) {
	input := "[? key: , next]"
	p := newParser([]byte(input))
	defer p.destroy()
	for {
		var event yaml_event_t
		if !yaml_parser_parse(&p.parser, &event) {
			t.Fatalf("parse error: %s", p.parser.problem)
		}
		if event.typ == yaml_SCALAR_EVENT && len(event.value) == 0 {
			if event.start_mark.column != strings.Index(input, ":")+1 {
				t.Fatalf("empty value mark = %d", event.start_mark.column)
			}
			return
		}
		if event.typ == yaml_STREAM_END_EVENT {
			t.Fatal("empty value event not found")
		}
	}
}

type countedSortKey struct {
	value int
	calls *int
}

func (k countedSortKey) MarshalYAML() (interface{}, error) {
	*k.calls++
	return k.value, nil
}

func TestComplexMapKeyMarshaledOnce(t *testing.T) {
	firstCalls, secondCalls := 0, 0
	input := map[countedSortKey]string{
		{value: 1, calls: &firstCalls}:  "one",
		{value: 2, calls: &secondCalls}: "two",
	}
	if _, err := Marshal(input); err != nil || firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("marshal error = %v, calls = %d, %d", err, firstCalls, secondCalls)
	}
}

func TestDecoderRetainsAliasParseError(t *testing.T) {
	d := NewDecoder(strings.NewReader("*missing\n---\nnext\n"))
	var out interface{}
	first := d.Decode(&out)
	if first == nil || !strings.Contains(first.Error(), "unknown anchor") {
		t.Fatalf("unknown alias error = %v", first)
	}
	if next := d.Decode(&out); next != first {
		t.Fatalf("subsequent error = %v, want %v", next, first)
	}
	d = NewDecoder(strings.NewReader("wrong\n---\n42\n"))
	var number int
	if err := d.Decode(&number); err == nil {
		t.Fatal("expected type error")
	}
	if err := d.Decode(&number); err != nil || number != 42 {
		t.Fatalf("next document = %d, %v", number, err)
	}
}

func TestBOMBetweenSequenceEntries(t *testing.T) {
	var out []string
	if err := Unmarshal([]byte("- one\n\ufeff- two\n"), &out); err == nil {
		t.Fatalf("BOM between entries accepted: %#v", out)
	}
	var value map[string]string
	if err := Unmarshal([]byte("# comment\n\ufeffkey: value\n"), &value); err != nil || value["key"] != "value" {
		t.Fatalf("BOM before first document = %#v, %v", value, err)
	}
}

func TestQuotedNELAfterSpace(t *testing.T) {
	var out string
	if err := Unmarshal([]byte("\"a \u0085b\""), &out); err != nil || out != "a b" {
		t.Fatalf("quoted NEL = %q, %v", out, err)
	}
}

func TestCompoundKeyCollisionMatching(t *testing.T) {
	first := map[string]int{"x": 1}
	second := map[string]int{"x": 2}
	left := MapSlice{{Key: first, Value: "a"}, {Key: second, Value: "b"}, {Key: first, Value: "a"}}
	reordered := MapSlice{{Key: first, Value: "a"}, {Key: first, Value: "a"}, {Key: second, Value: "b"}}
	different := MapSlice{{Key: first, Value: "a"}, {Key: second, Value: "b"}, {Key: second, Value: "b"}}
	if !equivalentYAMLKey(left, reordered) || equivalentYAMLKey(left, different) {
		t.Fatal("compound key collisions or one-to-one matching handled incorrectly")
	}
}

func TestNestedKeyFingerprintsAndCollisions(t *testing.T) {
	nested := interface{}(strings.Repeat("x", 4096))
	for i := 0; i < 128; i++ {
		nested = []interface{}{nested}
		if got := len(yamlKeyFingerprint(nested)); got != 32 {
			t.Fatalf("depth %d: fingerprint length = %d", i+1, got)
		}
	}

	first := []interface{}{map[string]int{"x": 1}}
	second := []interface{}{map[string]int{"x": 2}}
	if yamlKeyFingerprint(first) != yamlKeyFingerprint(second) {
		t.Fatal("expected unsupported map values to share a fingerprint")
	}
	var keys mapSliceKeys
	keys.add(first)
	if keys.contains(second) {
		t.Fatal("different keys in the same fingerprint bucket compared equal")
	}
	keys.add(second)
	if !keys.contains(first) || !keys.contains(second) {
		t.Fatal("colliding keys could not be looked up")
	}

	key := strings.Repeat("[", 100) + "value" + strings.Repeat("]", 100)
	var out MapSlice
	if err := UnmarshalStrict([]byte("? "+key+"\n: first\n? "+key+"\n: second\n"), &out); err == nil {
		t.Fatal("duplicate nested keys accepted")
	}
	other := strings.Repeat("[", 100) + "other" + strings.Repeat("]", 100)
	if err := UnmarshalStrict([]byte("? "+key+"\n: first\n? "+other+"\n: second\n"), &out); err != nil || len(out) != 2 {
		t.Fatalf("distinct nested keys = %#v, %v", out, err)
	}
}

func TestLargeIntegerGenericValue(t *testing.T) {
	for _, doc := range []string{"18446744073709551616", "-9223372036854775809"} {
		var generic interface{}
		if err := Unmarshal([]byte(doc), &generic); err != nil {
			t.Fatal(err)
		}
		if _, ok := generic.(float64); !ok {
			t.Fatalf("%s = %#v (%T), want float64", doc, generic, generic)
		}
		var signed int64
		if err := Unmarshal([]byte(doc), &signed); err == nil {
			t.Fatalf("%s accepted as int64", doc)
		}
		var unsigned uint64
		if err := Unmarshal([]byte(doc), &unsigned); err == nil {
			t.Fatalf("%s accepted as uint64", doc)
		}
	}
}

func TestInvalidEncodingAfterDocument(t *testing.T) {
	for _, prefix := range []string{"---\na: 1\n...\n", "---\na: 1\n---\n"} {
		data := prefix + string([]byte{0xff})
		for _, unmarshal := range []func([]byte, interface{}) error{
			Unmarshal,
			UnmarshalStrict,
			func(in []byte, out interface{}) error { return UnmarshalForJSON(in, out, false) },
		} {
			out := map[string]int{"existing": 2}
			err := unmarshal([]byte(data), &out)
			if err == nil || !strings.Contains(err.Error(), "invalid leading UTF-8 octet") || !reflect.DeepEqual(out, map[string]int{"existing": 2}) {
				t.Fatalf("byte input = %#v, %v", out, err)
			}
		}
		for _, reader := range []io.Reader{strings.NewReader(data), iotest.OneByteReader(strings.NewReader(data))} {
			decoder := NewDecoder(reader)
			var out map[string]int
			if err := decoder.Decode(&out); err != nil || out["a"] != 1 {
				t.Fatalf("first decode = %#v, %v", out, err)
			}
			err := decoder.Decode(&out)
			if err == nil || errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "invalid leading UTF-8 octet") {
				t.Fatalf("second decode error = %v", err)
			}
			if decoder.parser.parser.problem_offset != len(data)-1 {
				t.Fatalf("encoding error offset = %d, want %d", decoder.parser.parser.problem_offset, len(data)-1)
			}
		}
	}
}

func TestDocumentEndValidatesClosingLine(t *testing.T) {
	for _, input := range []string{"---\na: 1\n... \xff\n", "---\na: 1\n... # comment \xff\n", "---\na: 1\n... extra\n"} {
		var out map[string]int
		if err := Unmarshal([]byte(input), &out); err == nil {
			t.Errorf("invalid closing line accepted: %q", input)
		}
	}
	d := NewDecoder(strings.NewReader("---\na: 1\n... # valid\n\xff"))
	var out map[string]int
	if err := d.Decode(&out); err != nil || out["a"] != 1 {
		t.Fatalf("first document = %#v, %v", out, err)
	}
	if err := d.Decode(&out); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("next document error = %v", err)
	}
}

func BenchmarkDecodeLargeScalar(b *testing.B) {
	data := []byte(strings.Repeat("x", 1<<20))
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out string
		if err := Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func TestScalarWhitespaceAndNEL(t *testing.T) {
	backing := make([]byte, 0, 8)
	parser := yaml_parser_t{buffer: []byte(" "), unread: 1}
	got := read(&parser, backing)
	if &got[0] != &backing[:cap(backing)][0] || string(got) != " " {
		t.Fatal("scanner did not reuse whitespace buffer")
	}
	for _, tt := range []struct{ doc, want string }{
		{"\"a\u0085b\"", "a b"},
		{`"a\Nb"`, "a\u0085b"},
		{"'a\u0085b'", "a b"},
		{"a\u0085b", "a b"},
	} {
		var value string
		if err := Unmarshal([]byte(tt.doc), &value); err != nil || value != tt.want {
			t.Fatalf("%q = %q, %v", tt.doc, value, err)
		}
	}
	var comments map[string]int
	if err := Unmarshal([]byte("a: 1 # comment\u0085b: 2\n"), &comments); err != nil || len(comments) != 2 || comments["b"] != 2 {
		t.Fatalf("NEL comment = %#v, %v", comments, err)
	}
	for _, doc := range []string{"value: |\n  \ttext\n", "value: |2\n  \ttext\n"} {
		var out map[string]string
		if err := Unmarshal([]byte(doc), &out); err != nil || out["value"] != "\ttext\n" {
			t.Fatalf("%q = %#v, %v", doc, out, err)
		}
	}
	var out map[string]string
	if err := Unmarshal([]byte("value: |2\n \ttext\n"), &out); err == nil {
		t.Fatal("tab before required indentation accepted")
	}
}

type recursiveKey struct{ Next interface{} }

func TestCyclicAndSharedMappingKeys(t *testing.T) {
	key := &recursiveKey{}
	key.Next = key
	if _, err := Marshal(MapSlice{{Key: key, Value: 1}}); err == nil {
		t.Fatal("cyclic struct key accepted")
	}
	if _, err := Marshal(map[*recursiveKey]int{key: 1}); err == nil {
		t.Fatal("cyclic struct map key accepted")
	}
	cycle := make(map[string]interface{})
	cycle["self"] = cycle
	if _, err := Marshal(MapSlice{{Key: cycle, Value: 1}}); err == nil {
		t.Fatal("cyclic map key accepted")
	}
	shared := &recursiveKey{Next: "leaf"}
	if _, err := Marshal(MapSlice{{Key: []interface{}{shared, shared}, Value: 1}}); err != nil {
		t.Fatalf("shared acyclic key: %v", err)
	}
}

func TestTerminalEncodeReleasesEmitter(t *testing.T) {
	var out strings.Builder
	e := NewEncoder(&out)
	err := e.Encode(MapSlice{{Key: "a", Value: strings.Repeat("x", 20000)}, {Key: "a", Value: 2}})
	if err == nil || e.encoder != nil {
		t.Fatalf("error = %v, encoder = %#v", err, e.encoder)
	}
	before := out.String()
	if next := e.Encode(map[string]int{"ok": 1}); next != err {
		t.Fatalf("next error = %v, want %v", next, err)
	}
	if closeErr := e.Close(); closeErr != err || out.String() != before {
		t.Fatalf("close error = %v, output changed = %t", closeErr, out.String() != before)
	}
}

func TestDuplicateKeyEncodeIsTerminal(t *testing.T) {
	var out strings.Builder
	e := NewEncoder(&out)
	if err := e.Encode("first"); err != nil {
		t.Fatal(err)
	}
	err := e.Encode(MapSlice{{Key: "a", Value: strings.Repeat("x", 20000)}, {Key: "a", Value: 2}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate key error = %v", err)
	}
	before := out.String()
	if next := e.Encode("later"); next != err {
		t.Fatalf("next encode error = %v, want %v", next, err)
	}
	if closeErr := e.Close(); closeErr != err || out.String() != before {
		t.Fatalf("close error = %v, output changed = %t", closeErr, out.String() != before)
	}
	d := NewDecoder(strings.NewReader(out.String()))
	var first string
	if err := d.Decode(&first); err != nil || first != "first" {
		t.Fatalf("first document = %q, %v", first, err)
	}
	var next interface{}
	if err := d.Decode(&next); err == nil && next == "later" {
		t.Fatalf("unexpected later document: %#v", next)
	}
}

func TestOmitEmptyAdditionalKinds(t *testing.T) {
	value := 0
	data, err := Marshal(struct {
		Array [2]int `yaml:",omitempty"`
		Func  func() `yaml:",omitempty"`
		Ptr   *int   `yaml:",omitempty"`
	}{Ptr: &value})
	if err != nil || string(data) != "ptr: 0\n" {
		t.Fatalf("omitempty = %q, %v", data, err)
	}
}

type nilIsZeroer struct{}

func (*nilIsZeroer) IsZero() bool { panic("IsZero called on nil pointer") }

func TestOmitEmptyInterfaceNilPointers(t *testing.T) {
	var ordinary *int
	var zeroer *nilIsZeroer
	value := struct {
		Ordinary interface{} `yaml:"ordinary,omitempty"`
		Zeroer   interface{} `yaml:"zeroer,omitempty"`
		Empty    interface{} `yaml:"empty,omitempty"`
	}{Ordinary: ordinary, Zeroer: zeroer}
	data, err := Marshal(value)
	if err != nil || string(data) != "ordinary: null\n" {
		t.Fatalf("marshal = %q, %v", data, err)
	}
}

type namedFloat float64
type namedPairs []MapItem

func TestCompoundKeyFingerprintAndNamedMappings(t *testing.T) {
	a := namedPairs{{Key: "a", Value: 1}, {Key: "b", Value: 2}}
	b := MapSlice{{Key: "b", Value: 2}, {Key: "a", Value: 1}}
	var keys mapSliceKeys
	keys.add(a)
	if !keys.contains(b) || yamlKeyFingerprint(a) != yamlKeyFingerprint(b) {
		t.Fatal("reordered named mapping was not recognized")
	}
	if equivalentYAMLKey([]interface{}{"a", "b"}, []interface{}{"b", "a"}) {
		t.Fatal("sequence ordering ignored")
	}
	for i := 0; i < 100; i++ {
		keys.add(MapSlice{{Key: "n", Value: i}})
	}
	if len(keys.complex) < 100 {
		t.Fatalf("compound key fingerprints = %d, want at least 100", len(keys.complex))
	}
	var merged MapSlice
	doc := "<<:\n  ? [a, b]\n  : merged\n  ? [b, a]\n  : other\n? [a, b]\n: explicit\n"
	if err := UnmarshalStrict([]byte(doc), &merged); err != nil || len(merged) != 2 || merged[0].Value != "explicit" || merged[1].Value != "other" {
		t.Fatalf("compound merge = %#v, %v", merged, err)
	}
}

func TestNamedNaNAndPointerMergeKeys(t *testing.T) {
	var nan map[namedFloat]int
	if err := UnmarshalStrict([]byte(".nan: 1\n.NaN: 2\n"), &nan); err == nil {
		t.Fatal("duplicate named NaN accepted")
	}
	nan = nil
	if err := UnmarshalStrict([]byte("1.0: 1\n2.0: 2\n"), &nan); err != nil || len(nan) != 2 {
		t.Fatalf("finite keys = %#v, %v", nan, err)
	}
	for _, decode := range []func([]byte, interface{}) error{Unmarshal, UnmarshalStrict} {
		for _, doc := range []string{"key: 1\n<<: {key: 2}\n", "<<: {key: 2}\nkey: 1\n"} {
			var out map[*string]int
			if err := decode([]byte(doc), &out); err != nil || len(out) != 1 {
				t.Fatalf("pointer merge = %#v, %v", out, err)
			}
			for key, value := range out {
				if *key != "key" || value != 1 {
					t.Fatalf("pointer merge = %#v", out)
				}
			}
		}
	}
}

func TestDecimalFallbackAndRange(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  int
	}{{"08", 8}, {"0208", 208}, {"-08", -8}, {"+08", 8}, {"010", 8}} {
		var got int
		if err := Unmarshal([]byte(tt.input), &got); err != nil || got != tt.want {
			t.Errorf("%s = %d, %v", tt.input, got, err)
		}
	}
	for _, input := range []string{"18446744073709551616", "-9223372036854775809"} {
		var got int64
		if err := Unmarshal([]byte(input), &got); err == nil {
			t.Errorf("out-of-range %s accepted as %d", input, got)
		}
	}
}

type interfaceKey struct{ Value interface{} }

func (k *interfaceKey) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var value interface{}
	if err := unmarshal(&value); err != nil {
		return err
	}
	k.Value = value
	return nil
}

func TestNonComparableMapKey(t *testing.T) {
	var out map[interfaceKey]int
	if err := Unmarshal([]byte("? [a, b]\n: 1\n"), &out); err == nil {
		t.Fatal("non-comparable key accepted")
	}
	if err := Unmarshal([]byte("scalar: 1\n"), &out); err != nil || out[interfaceKey{Value: "scalar"}] != 1 {
		t.Fatalf("comparable key = %#v, %v", out, err)
	}
}

type nilMarshaler struct{ calls *int }

func (m *nilMarshaler) MarshalYAML() (interface{}, error) {
	if m == nil {
		panic("called on nil pointer")
	}
	*m.calls++
	return "value", nil
}

func TestInterfaceNilMarshalerAndDuplicateMapSlice(t *testing.T) {
	var nilValue *nilMarshaler
	calls := 0
	data, err := Marshal(map[string]interface{}{"nil": nilValue, "value": &nilMarshaler{calls: &calls}})
	if err != nil || calls != 1 || !strings.Contains(string(data), "nil: null") {
		t.Fatalf("marshal = %q, %v, calls=%d", data, err, calls)
	}
	if _, err := Marshal(MapSlice{{Key: "a", Value: 1}, {Key: "a", Value: 2}}); err == nil {
		t.Fatal("duplicate MapSlice key accepted")
	}
	data, err = Marshal(MapSlice{{Key: "b", Value: 1}, {Key: "a", Value: 2}})
	if err != nil || string(data) != "b: 1\na: 2\n" {
		t.Fatalf("ordered MapSlice = %q, %v", data, err)
	}
}

func TestCyclicMapKeyRejectedBeforeSorting(t *testing.T) {
	var value interface{}
	value = &value
	m := map[interface{}]int{&value: 1, "other": 2}
	if _, err := Marshal(m); err == nil {
		t.Fatal("cyclic key accepted")
	}
}

type privateInlineMap map[string]int

func TestUnexportedInlineMapIgnored(t *testing.T) {
	var out struct {
		privateInlineMap `yaml:",inline"`
		Known            string `yaml:"known"`
	}
	if err := Unmarshal([]byte("known: ok\nextra: 1\n"), &out); err != nil || out.Known != "ok" || out.privateInlineMap != nil {
		t.Fatalf("out = %#v, %v", out, err)
	}
}

func TestBlockEmptyKeyAndFlowColonValues(t *testing.T) {
	var mapping map[interface{}]interface{}
	if err := Unmarshal([]byte(": value\n"), &mapping); err != nil || mapping[nil] != "value" {
		t.Fatalf("block mapping = %#v, %v", mapping, err)
	}
	for _, doc := range []string{`{k: :ready}`, `{"key": :name}`, `{"key":1}`, `{"key":value}`, `{"key":"build"}`, `{k:}`} {
		var out map[string]interface{}
		err := Unmarshal([]byte(doc), &out)
		want := interface{}(":ready")
		switch doc {
		case `{"key": :name}`:
			want = ":name"
		case `{"key":1}`:
			want = 1
		case `{"key":value}`:
			want = "value"
		case `{"key":"build"}`:
			want = "build"
		case `{k:}`:
			want = nil
		}
		key := "k"
		if strings.Contains(doc, `"key"`) {
			key = "key"
		}
		got, exists := out[key]
		if err != nil || len(out) != 1 || !exists || got != want {
			t.Errorf("%s = %#v, want %#v, %v", doc, out, want, err)
		}
	}
}

type retryDiagnostics struct{}

func (*retryDiagnostics) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var first int
	initial := unmarshal(&first)
	if initial == nil {
		return errors.New("expected first type error")
	}
	message := initial.Error()
	var second bool
	_ = unmarshal(&second)
	if initial.Error() != message {
		return errors.New("first error was modified")
	}
	return nil
}

type ignoreFatalAttempt struct{}

func (*ignoreFatalAttempt) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var dst struct{ A, B int }
	if err := unmarshal(&dst); err == nil || !strings.Contains(err.Error(), "cannot decode !!str `invalid` as a !!int") {
		return fmt.Errorf("expected invalid explicit int error, got %v", err)
	}
	return nil
}

func TestCustomDecoderDiagnosticsIsolation(t *testing.T) {
	var retry retryDiagnostics
	if err := Unmarshal([]byte("not-a-number"), &retry); err != nil {
		t.Fatal(err)
	}
	var ignored ignoreFatalAttempt
	if err := Unmarshal([]byte("a: nope\nb: !!int invalid\n"), &ignored); err != nil {
		t.Fatal(err)
	}
}

func TestCloseUnusedEncoder(t *testing.T) {
	var out strings.Builder
	e := NewEncoder(&out)
	if err := e.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty stream output = %q", out.String())
	}
}

func TestEncoderNormalFlush(t *testing.T) {
	var out strings.Builder
	e := NewEncoder(&out)
	if err := e.Encode("first"); err != nil {
		t.Fatal(err)
	}
	if err := e.Encode("second"); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "first\n--- second\n" {
		t.Fatalf("output = %q", out.String())
	}
}
