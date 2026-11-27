package yaml

import "testing"

func TestDocumentStartMarkers(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		documents int
		wantError bool
	}{
		{"empty stream", "", 0, false},
		{"initial implicit", "first\n", 1, false},
		{"explicit documents", "---\nfirst\n...\n---\nsecond\n", 2, false},
		{"implicit then explicit", "first\n...\n---\nsecond\n", 2, false},
		{"extra end markers", "first\n...\n...\n---\nsecond\n", 2, false},
		{"explicit then implicit", "---\nfirst\n...\nsecond\n", 1, true},
		{"implicit then implicit", "first\n...\nsecond\n", 1, true},
		{"extra end then implicit", "first\n...\n...\nsecond\n", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parser yaml_parser_t
			yaml_parser_initialize(&parser)
			defer yaml_parser_delete(&parser)
			yaml_parser_set_input_string(&parser, []byte(tt.input))

			documents := 0
			for {
				var event yaml_event_t
				if !yaml_parser_parse(&parser, &event) {
					if !tt.wantError || parser.problem != "did not find expected <document start>" {
						t.Fatalf("unexpected parser error: %s", parser.problem)
					}
					break
				}
				if event.typ == yaml_DOCUMENT_START_EVENT {
					documents++
				}
				if event.typ == yaml_STREAM_END_EVENT {
					if tt.wantError {
						t.Fatal("expected parser error")
					}
					break
				}
			}
			if documents != tt.documents {
				t.Errorf("got %d documents, want %d", documents, tt.documents)
			}
		})
	}
}
