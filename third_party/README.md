# Local YAML forks

The local forks are based on `go.yaml.in/yaml/v2` v2.4.2 and
`sigs.k8s.io/yaml` v1.6.0. They contain compatibility fixes not yet available
in those upstream releases. The root module does not yet use these forks;
the YAML dependency migration will add `replace` directives for them. Once
adopted, edit the forks rather than `vendor` and regenerate with `go mod vendor`.

The go-yaml fork requires Go 1.21 or newer.
Its strict decoder checks overridden merge defaults for duplicate keys and
invalid merge operands, but only decodes effective values into destination
types. Its encoder rejects map keys that produce the same YAML key.
Its JSON conversion fork retains original timestamp key spellings through
the go-yaml fork's string-timestamp-key decoding mode. During unmarshalling,
it also uses merge priority to select explicit values over defaults that
match the same JSON struct field, without collapsing keys in map destinations.

Known limitations of the JSON conversion fork:

- For map keys implementing `encoding.TextUnmarshaler`, strict unmarshalling
  can miss collisions between different spellings that decode to the same key.
  A merged default can also overwrite an explicit value for such keys. Avoid
  using equivalent custom keys with different spellings, including in merges.
- When unmarshalling into an existing destination with aliased sequence
  elements, conversion can use stale destination types if an earlier decoded
  field changes a shared value. Avoid sharing mutable destination values across
  fields when unmarshalling.

Run their regression tests with:

```sh
(cd third_party/go-yaml && go test ./...)
(cd third_party/sigs-yaml && go test ./...)
```
