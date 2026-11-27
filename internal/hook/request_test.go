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
			name: "namespaces use local names",
			body: `<p:root xmlns:p="urn:example"><p:item p:id="a">ok</p:item></p:root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"-p":   "urn:example",
				"item": map[string]interface{}{"-id": "a", "#text": "ok"},
			}},
		},
		{
			name: "mixed text before and after child",
			body: `<root>before<child/>after</root>`,
			want: map[string]interface{}{"root": map[string]interface{}{
				"child": "", "#text": "before",
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
				"-id": "1", "child": "", "#text": "after",
			}},
		},
		{
			name: "entities and comments",
			body: `<root>first<!-- comment -->second &amp; third</root>`,
			want: map[string]interface{}{"root": "second & third"},
		},
		{
			name: "first document only",
			body: `<root>first</root><root>second</root>`,
			want: map[string]interface{}{"root": "first"},
		},
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
