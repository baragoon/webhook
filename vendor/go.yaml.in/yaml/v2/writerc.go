package yaml

import "fmt"

// Set the writer error and return false.
func yaml_emitter_set_writer_error(emitter *yaml_emitter_t, err error) bool {
	emitter.error = yaml_WRITER_ERROR
	emitter.problem = "write error: " + err.Error()
	emitter.write_error = fmt.Errorf("yaml: write error: %w", err)
	return false
}

// Flush the output buffer.
func yaml_emitter_flush(emitter *yaml_emitter_t) bool {
	if emitter.write_error != nil {
		return false
	}
	if emitter.write_handler == nil {
		panic("write handler not set")
	}

	// Check if the buffer is empty.
	if emitter.buffer_pos == 0 {
		return true
	}

	if err := emitter.write_handler(emitter, emitter.buffer[:emitter.buffer_pos]); err != nil {
		return yaml_emitter_set_writer_error(emitter, err)
	}
	emitter.buffer_pos = 0
	return true
}
