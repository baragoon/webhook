package hook

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/unicode"
)

func TestParseXMLPayload(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name: "nested elements, attributes and repeated elements",
			body: `<app><users><user id="1" name="Jeff"/><user id="2" name="Sally"/></users><message id="1"> Hello!! </message></app>`,
			want: map[string]interface{}{"app": map[string]interface{}{
				"users": map[string]interface{}{"user": []interface{}{
					map[string]interface{}{"-id": "1", "-name": "Jeff"},
					map[string]interface{}{"-id": "2", "-name": "Sally"},
				}},
				"message": map[string]interface{}{"-id": "1", "#text": "Hello!!"},
			}},
		},
		{
			name: "simple empty and repeated values",
			body: `<root><item> 1 </item><item>2</item><item/><empty attr=""/><blank>   </blank></root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"item":  []interface{}{"1", "2", ""},
				"empty": map[string]interface{}{"-attr": ""},
				"blank": "",
			}},
		},
		{
			name: "namespaces use qualified names",
			body: `<p:root xmlns:p="urn:example"><p:item p:id="a">ok</p:item></p:root>`,
			want: map[string]interface{}{"{urn%3Aexample}root": map[string]interface{}{
				"-{http%3A%2F%2Fwww%2Ew3%2Eorg%2F2000%2Fxmlns%2F}p": "urn:example",
				"{urn%3Aexample}item":                               map[string]interface{}{"-{urn%3Aexample}id": "a", "#text": "ok"},
			}},
		},
		{
			name: "distinct namespaces and equivalent prefixes",
			body: `<root xmlns:a="urn:a" xmlns:b="urn:b" xmlns:c="urn:a" id="plain" a:id="A" b:id="B"><a:item>one</a:item><b:item>two</b:item><c:item>three</c:item><item>plain</item></root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"-{http%3A%2F%2Fwww%2Ew3%2Eorg%2F2000%2Fxmlns%2F}a": "urn:a",
				"-{http%3A%2F%2Fwww%2Ew3%2Eorg%2F2000%2Fxmlns%2F}b": "urn:b",
				"-{http%3A%2F%2Fwww%2Ew3%2Eorg%2F2000%2Fxmlns%2F}c": "urn:a",
				"-id": "plain", "-{urn%3Aa}id": "A", "-{urn%3Ab}id": "B",
				"{urn%3Aa}item": []interface{}{"one", "three"}, "{urn%3Ab}item": "two", "item": "plain",
			}},
		},
		{
			name: "default namespace and scoped rebinding",
			body: `<root xmlns="urn:a" xmlns:p="xmlns" p:p="value"><item id="1" p:id="2"/><child xmlns="urn:b"><item/></child><plain xmlns=""><item/></plain></root>`,
			want: map[string]interface{}{"{urn%3Aa}root": map[string]interface{}{
				"-xmlns": "urn:a", "-{http%3A%2F%2Fwww%2Ew3%2Eorg%2F2000%2Fxmlns%2F}p": "xmlns",
				"-{xmlns}p":      "value",
				"{urn%3Aa}item":  map[string]interface{}{"-id": "1", "-{xmlns}id": "2"},
				"{urn%3Ab}child": map[string]interface{}{"-xmlns": "urn:b", "{urn%3Ab}item": ""},
				"plain":          map[string]interface{}{"-xmlns": "", "item": ""},
			}},
		},
		{
			name: "mixed text before and after child",
			body: `<root>before<child/>after</root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"child": "", "#text": "beforeafter",
			}},
		},
		{
			name: "mixed text after child",
			body: `<root><child/>after</root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"child": "", "#text": "after",
			}},
		},
		{
			name: "attributes with mixed text",
			body: `<root id="1">before<child/>after</root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"-id": "1", "child": "", "#text": "beforeafter",
			}},
		},
		{
			name: "entities and comments",
			body: `<root> first <!-- comment -->second <?pi data?> &amp; third </root>`,
			want: map[string]interface{}{"root": "first second  & third"},
		},
		{
			name: "text boundaries preserve spaces",
			body: `<root> first <![CDATA[and]]> <!-- comment --> last <child/> tail </root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"child": "", "#text": "first and  last  tail",
			}},
		},
		{
			name: "UTF-8 declaration",
			body: `<?xml version="1.0" encoding="UTF-8"?><root>café</root>`,
			want: map[string]interface{}{"root": "café"},
		},
		{
			name: "trailing whitespace comments and instructions",
			body: "<root>first</root> \n<!-- comment --><?pi data?>\t",
			want: map[string]interface{}{"root": "first"},
		},
		{
			name:    "second root rejected",
			body:    `<root>first</root><root>second</root>`,
			wantErr: true,
		},
		{name: "trailing text", body: `<root/>text`, wantErr: true},
		{name: "trailing malformed XML", body: `<root/><`, wantErr: true},
		{name: "trailing directive", body: `<root/><!DOCTYPE root>`, wantErr: true},
		{name: "trailing invalid whitespace", body: "<root/>\u00a0", wantErr: true},
		{name: "latin1", body: "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><root>caf\xe9</root>", want: map[string]interface{}{"root": "café"}},
		{name: "unknown encoding", body: `<?xml version="1.0" encoding="unknown-encoding"?><root/>`, wantErr: true},
		{name: "undeclared namespace prefix", body: `<root><a:item/></root>`, wantErr: true},
		{name: "undeclared attribute prefix", body: `<root a:id="1"/>`, wantErr: true},
		{name: "duplicate expanded attribute", body: `<root xmlns:a="urn:a" xmlns:b="urn:a" a:id="1" b:id="2"/>`, wantErr: true},
		{name: "reserved namespace declaration", body: `<root xmlns:a="http://www.w3.org/2000/xmlns/"/>`, wantErr: true},
		{name: "unclosed root", body: `<root>`, wantErr: true},
		{name: "empty input", body: ``, wantErr: true},
		{name: "malformed XML", body: `<root><child></root>`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Request{Body: []byte(tt.body), Payload: map[string]interface{}{"old": "value"}}
			err := r.ParseXMLPayload()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "error parsing XML payload:") {
					t.Fatalf("expected XML parse error, got %v", err)
				}
				if r.Payload != nil {
					t.Errorf("payload after error = %#v, want nil", r.Payload)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.Payload, tt.want) {
				t.Errorf("payload = %#v, want %#v", r.Payload, tt.want)
			}
		})
	}
}

func TestParseXMLPayloadUTF16(t *testing.T) {
	for _, tt := range []struct {
		name  string
		order unicode.Endianness
		bom   unicode.BOMPolicy
	}{
		{"big endian BOM", unicode.BigEndian, unicode.UseBOM},
		{"little endian BOM", unicode.LittleEndian, unicode.UseBOM},
		{"big endian no BOM", unicode.BigEndian, unicode.IgnoreBOM},
		{"little endian no BOM", unicode.LittleEndian, unicode.IgnoreBOM},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := unicode.UTF16(tt.order, tt.bom).NewEncoder().Bytes([]byte(`<?xml version="1.0" encoding="UTF-16"?><root>café</root>`))
			if err != nil {
				t.Fatal(err)
			}
			r := &Request{Body: body}
			if err := r.ParseXMLPayload(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.Payload, map[string]interface{}{"root": "café"}) {
				t.Fatalf("payload = %#v", r.Payload)
			}
		})
	}
}

func TestXMLPayloadArgumentAccess(t *testing.T) {
	r := &Request{Body: []byte(`<app><users><user name="Jeff"/><user name="Sally"/></users><message id="1">Hello!!</message></app>`)}
	if err := r.ParseXMLPayload(); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		"app.users.user.0.-name": "Jeff",
		"app.users.user.1.-name": "Sally",
		"app.message.-id":        "1",
		"app.message.#text":      "Hello!!",
	} {
		t.Run(path, func(t *testing.T) {
			got, err := (&Argument{Source: SourcePayload, Name: path}).Get(r)
			if err != nil || got != want {
				t.Errorf("Get() = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestXMLNamespaceArgumentAccess(t *testing.T) {
	r := &Request{Body: []byte(`<root xmlns:a="https://example.com/a" xmlns:b="https://example.com/b"><a:item a:id="A">allowed</a:item><b:item b:id="B">denied</b:item><a:with.dot>dot</a:with.dot></root>`)}
	if err := r.ParseXMLPayload(); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		"root.{https%3A%2F%2Fexample%2Ecom%2Fa}item.#text":                                "allowed",
		"root.{https%3A%2F%2Fexample%2Ecom%2Fa}item.-{https%3A%2F%2Fexample%2Ecom%2Fa}id": "A",
		"root.{https%3A%2F%2Fexample%2Ecom%2Fb}item.#text":                                "denied",
		"root.{https%3A%2F%2Fexample%2Ecom%2Fb}item.-{https%3A%2F%2Fexample%2Ecom%2Fb}id": "B",
		"root.{https%3A%2F%2Fexample%2Ecom%2Fa}with%2Edot":                                "dot",
	} {
		t.Run(path, func(t *testing.T) {
			got, err := (&Argument{Source: SourcePayload, Name: path}).Get(r)
			if err != nil || got != want {
				t.Errorf("Get() = %q, %v; want %q", got, err, want)
			}
		})
	}
	if _, err := (&Argument{Source: SourcePayload, Name: "root.item"}).Get(r); err == nil {
		t.Error("unqualified path must not match namespaced elements")
	}
	rule := MatchRule{
		Type: MatchValue, Value: "denied",
		Parameter: Argument{Source: SourcePayload, Name: "root.{https%3A%2F%2Fexample%2Ecom%2Fa}item.#text"},
	}
	if matched, err := rule.Evaluate(r); err != nil || matched {
		t.Errorf("rule matched a value from a different namespace: matched = %v, err = %v", matched, err)
	}
}

func TestXMLNamespacedArgumentAccess(t *testing.T) {
	r := &Request{Body: []byte(`<root xmlns:a="http://example.com/a" xmlns:b="urn:b" xmlns:c="urn:b"><a:item a:id="A" b:id="B" id="plain">one</a:item><b:item>two</b:item><c:item>three</c:item><item>plain</item><a:with.dot><b:value>nested</b:value></a:with.dot></root>`)}
	if err := r.ParseXMLPayload(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"root.{http%3A%2F%2Fexample%2Ecom%2Fa}item.-{http%3A%2F%2Fexample%2Ecom%2Fa}id": "A",
		"root.{http%3A%2F%2Fexample%2Ecom%2Fa}item.-{urn%3Ab}id":                        "B",
		"root.{http%3A%2F%2Fexample%2Ecom%2Fa}item.-id":                                 "plain",
		"root.{urn%3Ab}item.0": "two",
		"root.{urn%3Ab}item.1": "three",
		"root.item":            "plain",
		"root.{http%3A%2F%2Fexample%2Ecom%2Fa}with%2Edot.{urn%3Ab}value": "nested",
	} {
		t.Run(path, func(t *testing.T) {
			got, err := (&Argument{Source: SourcePayload, Name: path}).Get(r)
			if err != nil || got != want {
				t.Errorf("Get() = %q, %v; want %q", got, err, want)
			}
		})
	}
}
