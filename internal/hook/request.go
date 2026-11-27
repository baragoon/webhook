package hook

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/text/encoding/ianaindex"
	textunicode "golang.org/x/text/encoding/unicode"
)

// Request represents a webhook request.
type Request struct {
	// The request ID set by the RequestID middleware.
	ID string

	// The Content-Type of the request.
	ContentType string

	// The raw request body.
	Body []byte

	// Headers is a map of the parsed headers.
	Headers map[string]interface{}

	// Query is a map of the parsed URL query values.
	Query map[string]interface{}

	// Payload is a map of the parsed payload.
	Payload map[string]interface{}

	// The underlying HTTP request.
	RawRequest *http.Request

	// Treat signature errors as simple validate failures.
	AllowSignatureErrors bool
}

func (r *Request) ParseJSONPayload() error {
	decoder := json.NewDecoder(bytes.NewReader(r.Body))
	decoder.UseNumber()

	var firstChar byte
	for i := 0; i < len(r.Body); i++ {
		if unicode.IsSpace(rune(r.Body[i])) {
			continue
		}
		firstChar = r.Body[i]
		break
	}

	if firstChar == byte('[') {
		var arrayPayload interface{}
		err := decoder.Decode(&arrayPayload)
		if err != nil {
			return fmt.Errorf("error parsing JSON array payload %+v", err)
		}

		r.Payload = make(map[string]interface{}, 1)
		r.Payload["root"] = arrayPayload
	} else {
		err := decoder.Decode(&r.Payload)
		if err != nil {
			return fmt.Errorf("error parsing JSON payload %+v", err)
		}
	}

	return nil
}

func (r *Request) ParseHeaders(headers map[string][]string) {
	r.Headers = make(map[string]interface{}, len(headers))

	for k, v := range headers {
		if len(v) > 0 {
			r.Headers[k] = v[0]
		}
	}
}

func (r *Request) ParseQuery(query map[string][]string) {
	r.Query = make(map[string]interface{}, len(query))

	for k, v := range query {
		if len(v) > 0 {
			r.Query[k] = v[0]
		}
	}
}

func (r *Request) ParseFormPayload() error {
	fd, err := url.ParseQuery(string(r.Body))
	if err != nil {
		return fmt.Errorf("error parsing form payload %+v", err)
	}

	r.Payload = make(map[string]interface{}, len(fd))

	for k, v := range fd {
		if len(v) > 0 {
			r.Payload[k] = v[0]
		}
	}

	return nil
}

func (r *Request) ParseXMLPayload() error {
	payload, err := xmlPayload(r.Body)
	if err != nil {
		r.Payload = nil
		return fmt.Errorf("error parsing XML payload: %w", err)
	}

	r.Payload = payload
	return nil
}

func xmlPayload(body []byte) (map[string]interface{}, error) {
	type element struct {
		name       string
		rawName    xml.Name
		namespaces map[string]string
		fields     map[string]interface{}
		text       []byte
	}

	var input io.Reader = bytes.NewReader(body)
	var utf16Order string
	if len(body) >= 2 {
		switch {
		case bytes.HasPrefix(body, []byte{0xfe, 0xff}):
			utf16Order, body = "utf-16be", body[2:]
		case bytes.HasPrefix(body, []byte{0xff, 0xfe}):
			utf16Order, body = "utf-16le", body[2:]
		case len(body) >= 4 && body[0] == 0 && body[1] == '<' && body[2] == 0:
			utf16Order = "utf-16be"
		case len(body) >= 4 && body[0] == '<' && body[1] == 0 && body[3] == 0:
			utf16Order = "utf-16le"
		}
	}
	if utf16Order != "" {
		order := textunicode.BigEndian
		if utf16Order == "utf-16le" {
			order = textunicode.LittleEndian
		}
		input = textunicode.UTF16(order, textunicode.IgnoreBOM).NewDecoder().Reader(bytes.NewReader(body))
	}
	decoder := xml.NewDecoder(input)
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if utf16Order != "" {
			if strings.EqualFold(label, "utf-16") || strings.EqualFold(label, utf16Order) {
				return input, nil
			}
			return nil, fmt.Errorf("XML encoding %q does not match %s input", label, utf16Order)
		}
		encoding, err := ianaindex.IANA.Encoding(label)
		if err != nil {
			return nil, err
		}
		if encoding == nil {
			return nil, fmt.Errorf("unsupported XML encoding %q", label)
		}
		return encoding.NewDecoder().Reader(input), nil
	}
	var payload map[string]interface{}
	var stack []element
	for {
		token, err := decoder.RawToken()
		if err != nil {
			if err == io.EOF {
				if len(stack) != 0 {
					return nil, io.ErrUnexpectedEOF
				}
				if payload != nil {
					return payload, nil
				}
			}
			return nil, err
		}

		switch token := token.(type) {
		case xml.StartElement:
			if payload != nil {
				return nil, fmt.Errorf("multiple XML root elements")
			}
			namespaces := map[string]string{"xml": "http://www.w3.org/XML/1998/namespace"}
			if len(stack) > 0 {
				for prefix, uri := range stack[len(stack)-1].namespaces {
					namespaces[prefix] = uri
				}
			}
			for _, attr := range token.Attr {
				if attr.Name.Space == "xmlns" {
					if attr.Name.Local == "xmlns" || attr.Value == "" ||
						(attr.Name.Local == "xml") != (attr.Value == "http://www.w3.org/XML/1998/namespace") ||
						attr.Value == "http://www.w3.org/2000/xmlns/" {
						return nil, fmt.Errorf("invalid XML namespace declaration for %q", attr.Name.Local)
					}
					namespaces[attr.Name.Local] = attr.Value
				} else if attr.Name.Space == "" && attr.Name.Local == "xmlns" {
					if attr.Value == "http://www.w3.org/2000/xmlns/" || attr.Value == "http://www.w3.org/XML/1998/namespace" {
						return nil, fmt.Errorf("invalid default XML namespace")
					}
					namespaces[""] = attr.Value
				}
			}
			resolve := func(name xml.Name, defaultNamespace bool) (xml.Name, error) {
				if name.Space == "" && !defaultNamespace {
					return name, nil
				}
				uri, ok := namespaces[name.Space]
				if !ok && name.Space != "" {
					return xml.Name{}, fmt.Errorf("undeclared XML namespace prefix %q", name.Space)
				}
				return xml.Name{Space: uri, Local: name.Local}, nil
			}
			name, err := resolve(token.Name, true)
			if err != nil {
				return nil, err
			}
			node := element{name: xmlName(name), rawName: token.Name, namespaces: namespaces, fields: make(map[string]interface{})}
			for _, attr := range token.Attr {
				var name xml.Name
				if attr.Name.Space == "xmlns" {
					name = xml.Name{Space: "http://www.w3.org/2000/xmlns/", Local: attr.Name.Local}
				} else {
					name, err = resolve(attr.Name, false)
					if err != nil {
						return nil, err
					}
				}
				key := "-" + xmlName(name)
				if _, exists := node.fields[key]; exists {
					return nil, fmt.Errorf("duplicate XML attribute %q", key)
				}
				node.fields[key] = attr.Value
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) == 0 {
				if payload != nil && strings.Trim(string(token), " \t\r\n") != "" {
					return nil, fmt.Errorf("unexpected text after XML root element")
				}
				continue
			}
			node := &stack[len(stack)-1]
			node.text = append(node.text, token...)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].rawName != token.Name {
				return nil, fmt.Errorf("unexpected XML closing element %q", token.Name.Local)
			}
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			text := strings.Trim(string(node.text), "\t\r\b\n ")
			var value interface{} = text
			if len(node.fields) > 0 {
				if text != "" {
					node.fields["#text"] = text
				}
				value = node.fields
			}
			if len(stack) == 0 {
				payload = map[string]interface{}{node.name: value}
				continue
			}

			parent := &stack[len(stack)-1]
			if previous, ok := parent.fields[node.name]; ok {
				if values, ok := previous.([]interface{}); ok {
					parent.fields[node.name] = append(values, value)
				} else {
					parent.fields[node.name] = []interface{}{previous, value}
				}
			} else {
				parent.fields[node.name] = value
			}
		case xml.Directive:
			if payload != nil {
				return nil, fmt.Errorf("unexpected directive after XML root element")
			}
		}
	}
}

func xmlName(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	escape := func(s string) string {
		return strings.ReplaceAll(url.QueryEscape(s), ".", "%2E")
	}
	return "{" + escape(name.Space) + "}" + escape(name.Local)
}
