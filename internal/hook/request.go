package hook

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
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
		text   string
	}

	decoder := xml.NewDecoder(bytes.NewReader(body))
	var stack []element
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		switch token := token.(type) {
		case xml.StartElement:
			node := element{name: token.Name.Local, fields: make(map[string]interface{})}
			for _, attr := range token.Attr {
				node.fields["-"+attr.Name.Local] = attr.Value
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			text := strings.Trim(string(token), "\t\r\b\n ")
			if text == "" {
				continue
			}
			node := &stack[len(stack)-1]
			if len(node.fields) == 0 {
				node.text = text
			} else {
				node.fields["#text"] = text
			}
		case xml.EndElement:
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			var value interface{} = node.text
			if len(node.fields) > 0 {
				if node.text != "" {
					// Preserve text preceding child elements for compatibility.
					node.fields["#text"] = node.text
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
