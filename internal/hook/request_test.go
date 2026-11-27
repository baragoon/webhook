package hook

import (
	"reflect"
	"strings"
	"testing"
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
			name: "namespaces use expanded names",
			body: `<p:root xmlns:p="urn:example"><p:item p:id="a">ok</p:item></p:root>`,
			want: map[string]interface{}{"{urn%3Aexample}root": map[string]interface{}{
				"-{xmlns}p":           "urn:example",
				"{urn%3Aexample}item": map[string]interface{}{"-{urn%3Aexample}id": "a", "#text": "ok"},
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
			body: `<root>first<!-- comment -->second &amp; third</root>`,
			want: map[string]interface{}{"root": "firstsecond & third"},
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
			name: "first document only",
			body: `<root>first</root><root>second</root>`,
			want: map[string]interface{}{"root": "first"},
		},
		{name: "empty input", body: ``, wantErr: true},
		{name: "malformed XML", body: `<root><child></root>`, wantErr: true},
		{name: "unsupported encoding", body: "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><root>caf\xe9</root>", wantErr: true},
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

func TestXMLPayloadUnsupportedEncoding(t *testing.T) {
	r := &Request{Body: []byte(`<?xml version="1.0" encoding="ISO-8859-1"?><root/>`)}
	err := r.ParseXMLPayload()
	if err == nil || !strings.Contains(err.Error(), `unsupported XML encoding "ISO-8859-1"`) {
		t.Fatalf("expected unsupported encoding error, got %v", err)
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
