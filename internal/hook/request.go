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
		name   string
		fields map[string]interface{}
		text   []string
	}

	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.CharsetReader = func(charset string, _ io.Reader) (io.Reader, error) {
		return nil, fmt.Errorf("unsupported XML encoding %q (only UTF-8 is supported)", charset)
	}
	var stack []element
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		switch token := token.(type) {
		case xml.StartElement:
			node := element{name: xmlName(token.Name), fields: make(map[string]interface{})}
			for _, attr := range token.Attr {
				node.fields["-"+xmlName(attr.Name)] = attr.Value
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			node := &stack[len(stack)-1]
			node.text = append(node.text, string(token))
		case xml.EndElement:
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			text := strings.Trim(strings.Join(node.text, ""), "\t\r\b\n ")
			var value interface{} = text
			if len(node.fields) > 0 {
				if text != "" {
					node.fields["#text"] = text
				}
				value = node.fields
			}
			if len(stack) == 0 {
				return map[string]interface{}{node.name: value}, nil
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
		}
	}
}

func xmlName(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	namespace := strings.ReplaceAll(url.QueryEscape(name.Space), ".", "%2E")
	local := strings.ReplaceAll(name.Local, ".", "%2E")
	return "{" + namespace + "}" + local
}
