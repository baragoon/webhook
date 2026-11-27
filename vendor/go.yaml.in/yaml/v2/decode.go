// Modified in the webhook project.
package yaml

import (
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	documentNode = 1 << iota
	mappingNode
	sequenceNode
	scalarNode
	aliasNode
)

type node struct {
	kind         int
	line, column int
	tag          string
	// For an alias node, alias holds the resolved alias.
	alias    *node
	value    string
	implicit bool
	yaml11   bool
	children []*node
	anchors  map[string]*node
}

// ----------------------------------------------------------------------------
// Parser, produces a node tree out of a libyaml event stream.

type parser struct {
	parser   yaml_parser_t
	event    yaml_event_t
	doc      *node
	doneInit bool
}

func newParser(b []byte) *parser {
	p := parser{}
	if !yaml_parser_initialize(&p.parser) {
		panic("failed to initialize YAML emitter")
	}
	if len(b) == 0 {
		b = []byte{'\n'}
	}
	yaml_parser_set_input_string(&p.parser, b)
	return &p
}

func newParserFromReader(r io.Reader) *parser {
	p := parser{}
	if !yaml_parser_initialize(&p.parser) {
		panic("failed to initialize YAML emitter")
	}
	yaml_parser_set_input_reader(&p.parser, r)
	return &p
}

func (p *parser) init() {
	if p.doneInit {
		return
	}
	p.expect(yaml_STREAM_START_EVENT)
	p.doneInit = true
}

func (p *parser) destroy() {
	if p.event.typ != yaml_NO_EVENT {
		yaml_event_delete(&p.event)
	}
	yaml_parser_delete(&p.parser)
}

// expect consumes an event from the event stream and
// checks that it's of the expected type.
func (p *parser) expect(e yaml_event_type_t) {
	if p.event.typ == yaml_NO_EVENT {
		if !yaml_parser_parse(&p.parser, &p.event) {
			p.fail()
		}
	}
	if p.event.typ == yaml_STREAM_END_EVENT {
		failf("attempted to go past the end of stream; corrupted value?")
	}
	if p.event.typ != e {
		p.parser.problem = fmt.Sprintf("expected %s event but got %s", e, p.event.typ)
		p.fail()
	}
	yaml_event_delete(&p.event)
	p.event.typ = yaml_NO_EVENT
}

// peek peeks at the next event in the event stream,
// puts the results into p.event and returns the event type.
func (p *parser) peek() yaml_event_type_t {
	if p.event.typ != yaml_NO_EVENT {
		return p.event.typ
	}
	if !yaml_parser_parse(&p.parser, &p.event) {
		p.fail()
	}
	return p.event.typ
}

func (p *parser) fail() {
	var where string
	var line int
	if p.parser.problem_mark_set {
		line = p.parser.problem_mark.line + 1
	} else if p.parser.context_mark.line != 0 {
		line = p.parser.context_mark.line
	}
	if line != 0 {
		where = "line " + strconv.Itoa(line) + ": "
	}
	var msg string
	if len(p.parser.problem) > 0 {
		msg = p.parser.problem
	} else {
		msg = "unknown problem parsing YAML content"
	}
	failf("%s%s", where, msg)
}

func (p *parser) anchor(n *node, anchor []byte) {
	if anchor != nil {
		p.doc.anchors[string(anchor)] = n
	}
}

func (p *parser) parse() *node {
	p.init()
	switch p.peek() {
	case yaml_SCALAR_EVENT:
		return p.scalar()
	case yaml_ALIAS_EVENT:
		return p.alias()
	case yaml_MAPPING_START_EVENT:
		return p.mapping()
	case yaml_SEQUENCE_START_EVENT:
		return p.sequence()
	case yaml_DOCUMENT_START_EVENT:
		return p.document()
	case yaml_STREAM_END_EVENT:
		// Happens when attempting to decode an empty buffer.
		return nil
	default:
		panic("attempted to parse unknown event: " + p.event.typ.String())
	}
}

func (p *parser) node(kind int) *node {
	return &node{
		kind:   kind,
		line:   p.event.start_mark.line,
		column: p.event.start_mark.column,
		yaml11: p.doc != nil && p.doc.yaml11,
	}
}

func (p *parser) document() *node {
	n := p.node(documentNode)
	if version := p.event.version_directive; version != nil {
		n.yaml11 = version.major == 1 && version.minor == 1
	}
	n.anchors = make(map[string]*node)
	p.doc = n
	defer func() { p.doc = nil }()
	p.expect(yaml_DOCUMENT_START_EVENT)
	n.children = append(n.children, p.parse())
	p.expect(yaml_DOCUMENT_END_EVENT)
	return n
}

func (p *parser) alias() *node {
	n := p.node(aliasNode)
	n.value = string(p.event.anchor)
	n.alias = p.doc.anchors[n.value]
	if n.alias == nil {
		failf("unknown anchor '%s' referenced", n.value)
	}
	p.expect(yaml_ALIAS_EVENT)
	return n
}

func (p *parser) scalar() *node {
	n := p.node(scalarNode)
	n.value = string(p.event.value)
	n.tag = string(p.event.tag)
	n.implicit = p.event.implicit
	p.anchor(n, p.event.anchor)
	p.expect(yaml_SCALAR_EVENT)
	return n
}

func (p *parser) sequence() *node {
	n := p.node(sequenceNode)
	p.anchor(n, p.event.anchor)
	p.expect(yaml_SEQUENCE_START_EVENT)
	for p.peek() != yaml_SEQUENCE_END_EVENT {
		n.children = append(n.children, p.parse())
	}
	p.expect(yaml_SEQUENCE_END_EVENT)
	return n
}

func (p *parser) mapping() *node {
	n := p.node(mappingNode)
	p.anchor(n, p.event.anchor)
	p.expect(yaml_MAPPING_START_EVENT)
	for p.peek() != yaml_MAPPING_END_EVENT {
		n.children = append(n.children, p.parse(), p.parse())
	}
	p.expect(yaml_MAPPING_END_EVENT)
	return n
}

// ----------------------------------------------------------------------------
// Decoder, unmarshals a node into a provided value.

type decoder struct {
	doc                 *node
	aliases             map[*node]bool
	mapType             reflect.Type
	terrors             []string
	strict              bool
	stringTimestampKeys bool
	identityScalars     bool
	jsonMergeKeys       bool
	mergePriority       *int
	mergeTarget         reflect.Value
	mergedKeys          *mapSliceKeys
	destinationKeys     map[interface{}]interface{}
	mergedItems         *mapSliceKeys

	decodeCount int
	aliasCount  int
	aliasDepth  int
}

var (
	mapItemType    = reflect.TypeOf(MapItem{})
	durationType   = reflect.TypeOf(time.Duration(0))
	defaultMapType = reflect.TypeOf(map[interface{}]interface{}{})
	ifaceType      = defaultMapType.Elem()
	timeType       = reflect.TypeOf(time.Time{})
	ptrTimeType    = reflect.TypeOf(&time.Time{})
)

func newDecoder(strict bool) *decoder {
	d := &decoder{mapType: defaultMapType, strict: strict}
	d.aliases = make(map[*node]bool)
	return d
}

func (d *decoder) terror(n *node, tag string, out reflect.Value) {
	if n.tag != "" {
		tag = n.tag
	}
	value := n.value
	if tag != yaml_SEQ_TAG && tag != yaml_MAP_TAG {
		if len(value) > 10 {
			value = " `" + value[:7] + "...`"
		} else {
			value = " `" + value + "`"
		}
	}
	d.terrors = append(d.terrors, fmt.Sprintf("line %d: cannot unmarshal %s%s into %s", n.line+1, shortTag(tag), value, out.Type()))
}

func (d *decoder) callUnmarshaler(n *node, u Unmarshaler) (good bool) {
	terrlen := len(d.terrors)
	err := u.UnmarshalYAML(func(v interface{}) (err error) {
		defer func() { d.terrors = d.terrors[:terrlen] }()
		defer handleErr(&err)
		d.unmarshal(n, reflect.ValueOf(v))
		if len(d.terrors) > terrlen {
			issues := append([]string(nil), d.terrors[terrlen:]...)
			return &TypeError{issues}
		}
		return nil
	})
	if e, ok := err.(*TypeError); ok {
		d.terrors = append(d.terrors, e.Errors...)
		return false
	}
	if err != nil {
		fail(err)
	}
	return true
}

// d.prepare initializes and dereferences pointers and calls UnmarshalYAML
// if a value is found to implement it.
// It returns the initialized and dereferenced out value, whether
// unmarshalling was already done by UnmarshalYAML, and if so whether
// its types unmarshalled appropriately.
//
// If n holds a null value, prepare returns before doing anything.
func (d *decoder) prepare(n *node, out reflect.Value) (newout reflect.Value, unmarshaled, good bool) {
	if n.kind == scalarNode {
		if n.tag == yaml_NULL_TAG {
			return out, false, false
		}
		if n.tag == "" && n.implicit {
			if item, ok := resolveMap[n.value]; ok && item.tag == yaml_NULL_TAG {
				return out, false, false
			}
		}
	}
	again := true
	for again {
		again = false
		if out.Kind() == reflect.Ptr {
			if out.IsNil() {
				out.Set(reflect.New(out.Type().Elem()))
			}
			out = out.Elem()
			again = true
		}
		if out.CanAddr() {
			if u, ok := out.Addr().Interface().(Unmarshaler); ok {
				good = d.callUnmarshaler(n, u)
				return out, true, good
			}
		}
	}
	return out, false, false
}

const (
	// 400,000 decode operations is ~500kb of dense object declarations, or
	// ~5kb of dense object declarations with 10000% alias expansion
	alias_ratio_range_low = 400000

	// 4,000,000 decode operations is ~5MB of dense object declarations, or
	// ~4.5MB of dense object declarations with 10% alias expansion
	alias_ratio_range_high = 4000000

	// alias_ratio_range is the range over which we scale allowed alias ratios
	alias_ratio_range = float64(alias_ratio_range_high - alias_ratio_range_low)
)

func allowedAliasRatio(decodeCount int) float64 {
	switch {
	case decodeCount <= alias_ratio_range_low:
		// allow 99% to come from alias expansion for small-to-medium documents
		return 0.99
	case decodeCount >= alias_ratio_range_high:
		// allow 10% to come from alias expansion for very large documents
		return 0.10
	default:
		// scale smoothly from 99% down to 10% over the range.
		// this maps to 396,000 - 400,000 allowed alias-driven decodes over the range.
		// 400,000 decode operations is ~100MB of allocations in worst-case scenarios (single-item maps).
		return 0.99 - 0.89*(float64(decodeCount-alias_ratio_range_low)/alias_ratio_range)
	}
}

func (d *decoder) unmarshal(n *node, out reflect.Value) (good bool) {
	d.countDecode()
	switch n.kind {
	case documentNode:
		return d.document(n, out)
	case aliasNode:
		return d.alias(n, out)
	}
	out, unmarshaled, good := d.prepare(n, out)
	if unmarshaled {
		return good
	}
	switch n.kind {
	case scalarNode:
		good = d.scalar(n, out)
	case mappingNode:
		good = d.mapping(n, out)
	case sequenceNode:
		good = d.sequence(n, out)
	default:
		panic("internal error: unknown node kind: " + strconv.Itoa(n.kind))
	}
	return good
}

func (d *decoder) countDecode() {
	d.decodeCount++
	if d.aliasDepth > 0 {
		d.aliasCount++
	}
	if d.aliasCount > 100 && d.decodeCount > 1000 && float64(d.aliasCount)/float64(d.decodeCount) > allowedAliasRatio(d.decodeCount) {
		failf("document contains excessive aliasing")
	}
}

func (d *decoder) document(n *node, out reflect.Value) (good bool) {
	if len(n.children) == 1 {
		d.doc = n
		d.unmarshal(n.children[0], out)
		return true
	}
	return false
}

func (d *decoder) alias(n *node, out reflect.Value) (good bool) {
	return d.withAlias(n, func() bool { return d.unmarshal(n.alias, out) })
}

func (d *decoder) withAlias(n *node, decode func() bool) bool {
	if d.aliases[n] {
		// TODO this could actually be allowed in some circumstances.
		failf("anchor '%s' value contains itself", n.value)
	}
	d.aliases[n] = true
	d.aliasDepth++
	defer func() {
		d.aliasDepth--
		delete(d.aliases, n)
	}()
	return decode()
}

var zeroValue reflect.Value

func resetMap(out reflect.Value) {
	out.Clear()
}

func (d *decoder) scalar(n *node, out reflect.Value) bool {
	var tag string
	var resolved interface{}
	if n.tag == "" && !n.implicit {
		tag = yaml_STR_TAG
		resolved = n.value
	} else {
		tag, resolved = resolveForDocument(n.tag, n.value, n.yaml11)
		if tag == yaml_BINARY_TAG {
			data, err := base64.StdEncoding.DecodeString(resolved.(string))
			if err != nil {
				failf("!!binary value contains invalid base64 data")
			}
			resolved = string(data)
		}
	}
	if d.identityScalars && out.Kind() == reflect.Interface {
		out.Set(reflect.ValueOf(sourceYAMLScalarKey{Tag: tag, Value: mappingKeyIdentity(resolved)}))
		return true
	}
	if resolved == nil {
		if out.Kind() == reflect.Map && !out.CanAddr() {
			resetMap(out)
		} else {
			out.Set(reflect.Zero(out.Type()))
		}
		return true
	}
	if resolvedv := reflect.ValueOf(resolved); out.Type() == resolvedv.Type() {
		// We've resolved to exactly the type we want, so use that.
		out.Set(resolvedv)
		return true
	}
	// Perhaps we can use the value as a TextUnmarshaler to
	// set its value.
	if out.CanAddr() {
		u, ok := out.Addr().Interface().(encoding.TextUnmarshaler)
		if ok {
			var text []byte
			if tag == yaml_BINARY_TAG {
				text = []byte(resolved.(string))
			} else {
				// We let any value be unmarshaled into TextUnmarshaler.
				// That might be more lax than we'd like, but the
				// TextUnmarshaler itself should bowl out any dubious values.
				text = []byte(n.value)
			}
			err := u.UnmarshalText(text)
			if err != nil {
				fail(err)
			}
			return true
		}
	}
	switch out.Kind() {
	case reflect.String:
		if tag == yaml_BINARY_TAG {
			out.SetString(resolved.(string))
			return true
		}
		out.SetString(n.value)
		return true
	case reflect.Interface:
		if tag == yaml_TIMESTAMP_TAG {
			// It looks like a timestamp but for backward compatibility
			// reasons we set it as a string, so that code that unmarshals
			// timestamp-like values into interface{} will continue to
			// see a string and not a time.Time.
			// TODO(v3) Drop this.
			value := reflect.ValueOf(n.value)
			if !value.Type().AssignableTo(out.Type()) {
				break
			}
			out.Set(value)
		} else {
			value := reflect.ValueOf(resolved)
			if !value.Type().AssignableTo(out.Type()) {
				break
			}
			out.Set(value)
		}
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch resolved := resolved.(type) {
		case int:
			if !out.OverflowInt(int64(resolved)) {
				out.SetInt(int64(resolved))
				return true
			}
		case int64:
			if !out.OverflowInt(resolved) {
				out.SetInt(resolved)
				return true
			}
		case uint64:
			if resolved <= math.MaxInt64 && !out.OverflowInt(int64(resolved)) {
				out.SetInt(int64(resolved))
				return true
			}
		case float64:
			if n.tag == "" && yamlStyleFloat.MatchString(strings.ReplaceAll(n.value, "_", "")) && !strings.ContainsAny(n.value, ".eE") {
				if value, err := strconv.ParseInt(strings.ReplaceAll(n.value, "_", ""), 10, 64); err == nil && !out.OverflowInt(value) {
					out.SetInt(value)
					return true
				}
				break
			}
			if resolved >= -0x1p63 && resolved < 0x1p63 && !out.OverflowInt(int64(resolved)) {
				out.SetInt(int64(resolved))
				return true
			}
		case string:
			if out.Type() == durationType {
				d, err := time.ParseDuration(resolved)
				if err == nil {
					out.SetInt(int64(d))
					return true
				}
			}
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		switch resolved := resolved.(type) {
		case int:
			if resolved >= 0 && !out.OverflowUint(uint64(resolved)) {
				out.SetUint(uint64(resolved))
				return true
			}
		case int64:
			if resolved >= 0 && !out.OverflowUint(uint64(resolved)) {
				out.SetUint(uint64(resolved))
				return true
			}
		case uint64:
			if !out.OverflowUint(uint64(resolved)) {
				out.SetUint(uint64(resolved))
				return true
			}
		case float64:
			if n.tag == "" && yamlStyleFloat.MatchString(strings.ReplaceAll(n.value, "_", "")) && !strings.ContainsAny(n.value, ".eE") {
				if value, err := strconv.ParseUint(strings.TrimPrefix(strings.ReplaceAll(n.value, "_", ""), "+"), 10, 64); err == nil && !out.OverflowUint(value) {
					out.SetUint(value)
					return true
				}
				break
			}
			if resolved >= 0 && resolved < 0x1p64 && !out.OverflowUint(uint64(resolved)) {
				out.SetUint(uint64(resolved))
				return true
			}
		}
	case reflect.Bool:
		switch resolved := resolved.(type) {
		case bool:
			out.SetBool(resolved)
			return true
		}
	case reflect.Float32, reflect.Float64:
		switch resolved := resolved.(type) {
		case int:
			out.SetFloat(float64(resolved))
			return true
		case int64:
			out.SetFloat(float64(resolved))
			return true
		case uint64:
			out.SetFloat(float64(resolved))
			return true
		case float64:
			out.SetFloat(resolved)
			return true
		case string:
			if n.tag == "" && n.implicit {
				plain := strings.ReplaceAll(resolved, "_", "")
				if yamlStyleFloat.MatchString(plain) {
					if value, err := strconv.ParseFloat(plain, 64); err == nil {
						out.SetFloat(value)
						return true
					}
				}
			}
		}
	case reflect.Struct:
		if resolvedv := reflect.ValueOf(resolved); out.Type() == resolvedv.Type() {
			out.Set(resolvedv)
			return true
		}
	case reflect.Ptr:
		if out.Type().Elem() == reflect.TypeOf(resolved) {
			// TODO DOes this make sense? When is out a Ptr except when decoding a nil value?
			elem := reflect.New(out.Type().Elem())
			elem.Elem().Set(reflect.ValueOf(resolved))
			out.Set(elem)
			return true
		}
	}
	d.terror(n, tag, out)
	return false
}

func settableValueOf(i interface{}) reflect.Value {
	v := reflect.ValueOf(i)
	sv := reflect.New(v.Type()).Elem()
	sv.Set(v)
	return sv
}

func (d *decoder) sequence(n *node, out reflect.Value) (good bool) {
	l := len(n.children)

	var iface reflect.Value
	switch out.Kind() {
	case reflect.Slice:
		out.Set(reflect.MakeSlice(out.Type(), l, l))
	case reflect.Array:
		if l != out.Len() {
			failf("invalid array: want %d elements but got %d", out.Len(), l)
		}
	case reflect.Interface:
		// No type hints. Will have to use a generic sequence.
		iface = out
		out = settableValueOf(make([]interface{}, l))
		if !out.Type().AssignableTo(iface.Type()) {
			d.terror(n, yaml_SEQ_TAG, iface)
			return false
		}
	default:
		d.terror(n, yaml_SEQ_TAG, out)
		return false
	}
	et := out.Type().Elem()

	j := 0
	for i := 0; i < l; i++ {
		e := reflect.New(et).Elem()
		if ok := d.unmarshal(n.children[i], e); ok {
			out.Index(j).Set(e)
			j++
		}
	}
	if out.Kind() != reflect.Array {
		out.Set(out.Slice(0, j))
	}
	if iface.IsValid() {
		iface.Set(out)
	}
	return true
}

func (d *decoder) mapping(n *node, out reflect.Value) (good bool) {
	switch out.Kind() {
	case reflect.Struct:
		return d.mappingStruct(n, out)
	case reflect.Slice:
		return d.mappingSlice(n, out)
	case reflect.Map:
		// okay
	case reflect.Interface:
		if d.mapType.Kind() == reflect.Map {
			iface := out
			out = reflect.MakeMap(d.mapType)
			if !out.Type().AssignableTo(iface.Type()) {
				d.terror(n, yaml_MAP_TAG, iface)
				return false
			}
			iface.Set(out)
		} else {
			slicev := reflect.New(d.mapType).Elem()
			if !d.mappingSlice(n, slicev) {
				return false
			}
			if !slicev.Type().AssignableTo(out.Type()) {
				d.terror(n, yaml_MAP_TAG, out)
				return false
			}
			out.Set(slicev)
			return true
		}
	default:
		d.terror(n, yaml_MAP_TAG, out)
		return false
	}
	outt := out.Type()
	kt := outt.Key()
	et := outt.Elem()

	mapType := d.mapType
	defer func() { d.mapType = mapType }()
	if outt.Key() == ifaceType && outt.Elem() == ifaceType {
		d.mapType = outt
	}

	if out.IsNil() {
		out.Set(reflect.MakeMap(outt))
	}
	var explicit, blocked mapSliceKeys
	type pendingEntry struct {
		key, value reflect.Value
		identity   interface{}
	}
	var pending []pendingEntry
	previousTarget, previousKeys, previousPriority, previousDestinations := d.mergeTarget, d.mergedKeys, d.mergePriority, d.destinationKeys
	if !d.mergeTarget.IsValid() || d.mergeTarget != out {
		d.mergedKeys = new(mapSliceKeys)
		d.destinationKeys = make(map[interface{}]interface{})
		if d.jsonMergeKeys {
			d.mergePriority = new(int)
		}
	} else if d.jsonMergeKeys {
		(*d.mergePriority)++
	}
	priority := 0
	if d.jsonMergeKeys {
		priority = *d.mergePriority
	}
	d.mergeTarget = out
	defer func() {
		d.mergeTarget, d.mergedKeys, d.mergePriority, d.destinationKeys = previousTarget, previousKeys, previousPriority, previousDestinations
	}()
	l := len(n.children)
	var mergeNodes []*node
	for i := 0; i < l; i += 2 {
		if isMerge(n.children[i]) {
			if d.strict && len(mergeNodes) != 0 {
				d.terrors = append(d.terrors, fmt.Sprintf("line %d: merge key already set in map", n.children[i].line+1))
			} else {
				mergeNodes = append(mergeNodes, n.children[i+1])
			}
			continue
		}
		k := reflect.New(kt).Elem()
		if d.unmarshal(n.children[i], k) {
			if !k.Comparable() {
				failf("invalid map key: %#v", k.Interface())
			}
			key := d.mappingNodeKeyIdentity(n.children[i], k)
			index, duplicate := explicit.index(key)
			if duplicate {
				if d.strict {
					d.terrors = append(d.terrors, fmt.Sprintf("line %d: key %#v already set in map", n.children[i+1].line+1, key))
					continue
				}
				if blocked.contains(key) {
					d.validateSkipped(n.children[i+1])
					continue
				}
			} else {
				explicit.add(key)
				if d.mergedKeys.contains(key) {
					blocked.add(key)
					pending = append(pending, pendingEntry{})
					d.validateSkipped(n.children[i+1])
					continue
				}
				d.mergedKeys.add(key)
				pending = append(pending, pendingEntry{})
			}
			e := reflect.New(et).Elem()
			if d.unmarshal(n.children[i+1], e) {
				if priority > 0 && kt.Kind() == reflect.Interface && reflect.TypeOf(JSONMergeKey{}).AssignableTo(kt) {
					k.Set(reflect.ValueOf(JSONMergeKey{Key: k.Interface(), Priority: priority}))
				}
				pending[index] = pendingEntry{key: k, value: e, identity: key}
			}
		}
	}
	for _, entry := range pending {
		if entry.value.IsValid() {
			destination := entry.key.Interface()
			if previous, exists := d.destinationKeys[destination]; exists && !equivalentYAMLKey(previous, entry.identity) {
				failf("distinct YAML keys map to the same destination key: %#v", destination)
			}
			d.destinationKeys[destination] = entry.identity
			out.SetMapIndex(entry.key, entry.value)
		}
	}
	for _, mergeNode := range mergeNodes {
		d.merge(mergeNode, out)
	}
	return true
}

func (d *decoder) setMapIndex(n *node, out, k, v reflect.Value) {
	if d.strict && out.MapIndex(k) != zeroValue {
		d.terrors = append(d.terrors, fmt.Sprintf("line %d: key %#v already set in map", n.line+1, k.Interface()))
		return
	}
	out.SetMapIndex(k, v)
}

func (d *decoder) mappingSlice(n *node, out reflect.Value) (good bool) {
	outt := out.Type()
	if outt.Elem() != mapItemType {
		d.terror(n, yaml_MAP_TAG, out)
		return false
	}

	mapType := d.mapType
	d.mapType = outt
	defer func() { d.mapType = mapType }()

	tracking := d.strict || d.mergeTarget.IsValid() && d.mergeTarget == out
	if !tracking {
		for i := 0; i < len(n.children); i += 2 {
			if isMerge(n.children[i]) {
				tracking = true
				break
			}
		}
	}
	previousTarget, previousItems := d.mergeTarget, d.mergedItems
	if !d.mergeTarget.IsValid() || d.mergeTarget != out {
		d.mergedItems = new(mapSliceKeys)
		out.Set(reflect.Zero(out.Type()))
	}
	d.mergeTarget = out
	defer func() { d.mergeTarget, d.mergedItems = previousTarget, previousItems }()

	var explicit, blocked mapSliceKeys
	var mergeNodes []*node
	l := len(n.children)
	for i := 0; i < l; i += 2 {
		if isMerge(n.children[i]) {
			if d.strict && len(mergeNodes) != 0 {
				d.terrors = append(d.terrors, fmt.Sprintf("line %d: merge key already set in map", n.children[i].line+1))
			} else {
				mergeNodes = append(mergeNodes, n.children[i+1])
			}
			continue
		}
		item := MapItem{}
		k := reflect.ValueOf(&item.Key).Elem()
		if d.unmarshal(n.children[i], k) {
			if tracking {
				key := d.mappingNodeKeyIdentity(n.children[i], k)
				if explicit.contains(key) {
					if d.strict {
						d.terrors = append(d.terrors, fmt.Sprintf("line %d: key %#v already set in map", n.children[i+1].line+1, item.Key))
						continue
					}
					if blocked.contains(key) {
						d.validateSkipped(n.children[i+1])
						continue
					}
				} else {
					explicit.add(key)
					if d.mergedItems.contains(key) {
						blocked.add(key)
						d.validateSkipped(n.children[i+1])
						continue
					}
					d.mergedItems.add(key)
				}
			}
			v := reflect.ValueOf(&item.Value).Elem()
			if d.unmarshal(n.children[i+1], v) {
				out.Set(reflect.Append(out, reflect.ValueOf(item)))
			}
		}
	}
	for _, mergeNode := range mergeNodes {
		d.merge(mergeNode, out)
	}
	return true
}

type mapSliceKeys struct {
	scalars map[interface{}]int
	complex map[string][]indexedYAMLKey
	count   int
}

type sourceYAMLScalarKey struct {
	Tag   string
	Value interface{}
}

func (d *decoder) mappingNodeKeyIdentity(n *node, out reflect.Value) interface{} {
	if d.identityScalars {
		return mappingKeyIdentity(out.Interface())
	}
	for n.kind == aliasNode && n.alias != nil {
		n = n.alias
	}
	if n.kind == scalarNode {
		tag, value := yaml_STR_TAG, interface{}(n.value)
		if n.tag != "" || n.implicit {
			tag, value = resolveForDocument(n.tag, n.value, n.yaml11)
			if tag == yaml_TIMESTAMP_TAG && d.stringTimestampKeys {
				value = n.value
			}
			if tag == yaml_BINARY_TAG {
				data, err := base64.StdEncoding.DecodeString(value.(string))
				if err != nil {
					failf("!!binary value contains invalid base64 data")
				}
				value = string(data)
			}
		}
		return sourceYAMLScalarKey{Tag: tag, Value: mappingKeyIdentity(value)}
	}
	if n.kind == sequenceNode || n.kind == mappingNode {
		identityDecoder := newDecoder(false)
		identityDecoder.identityScalars = true
		identityDecoder.mapType = d.mapType
		if out.Kind() != reflect.Interface {
			// Typed keys may contain source mappings with collection keys.
			identityDecoder.mapType = reflect.TypeOf(MapSlice{})
		}
		var identity interface{}
		if !identityDecoder.unmarshal(n, reflect.ValueOf(&identity).Elem()) {
			failf("cannot determine YAML mapping key identity")
		}
		if len(identityDecoder.terrors) != 0 {
			fail(&TypeError{identityDecoder.terrors})
		}
		return identity
	}
	return mappingKeyIdentity(out.Interface())
}

type indexedYAMLKey struct {
	key   interface{}
	index int
}

type nanMappingKey struct{}

func mappingKeyIdentity(key interface{}) interface{} {
	value := reflect.ValueOf(key)
	var seen map[interface{}]bool
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) && !value.IsNil() {
		if value.Kind() == reflect.Ptr {
			if seen == nil {
				seen = make(map[interface{}]bool)
			}
			pointer := value.Interface()
			if seen[pointer] {
				return key
			}
			seen[pointer] = true
		}
		value = value.Elem()
	}
	if value.IsValid() && (value.Kind() == reflect.Float32 || value.Kind() == reflect.Float64) && math.IsNaN(value.Float()) {
		return nanMappingKey{}
	}
	if value.IsValid() && value.CanInterface() {
		if timestamp, ok := value.Interface().(time.Time); ok {
			return timestamp.UTC()
		}
		return value.Interface()
	}
	return key
}

func scalarKey(key interface{}) bool {
	switch key.(type) {
	case nil, string, bool, int, int64, uint64, float64, encodedScalar, nanMappingKey:
		return true
	}
	value := reflect.ValueOf(key)
	if value.IsValid() {
		return reflexiveValueKey(value)
	}
	return false
}

func reflexiveValueKey(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return value.Equal(value)
	case reflect.Array, reflect.Struct:
		if !value.Comparable() || !value.Equal(value) {
			return false
		}
		n := 0
		if value.Kind() == reflect.Struct {
			n = value.NumField()
		} else {
			n = value.Len()
		}
		for i := 0; i < n; i++ {
			part := value
			if value.Kind() == reflect.Array {
				part = value.Index(i)
			} else {
				part = value.Field(i)
			}
			if !reflexiveValueKey(part) {
				return false
			}
		}
		return true
	}
	return false
}

func (keys *mapSliceKeys) contains(key interface{}) bool {
	_, ok := keys.index(key)
	return ok
}

func (keys *mapSliceKeys) index(key interface{}) (int, bool) {
	key = mappingKeyIdentity(key)
	if scalarKey(key) {
		index, ok := keys.scalars[key]
		if ok {
			return index, true
		}
		return keys.count, false
	}
	for _, existing := range keys.complex[yamlKeyFingerprint(key)] {
		if equivalentYAMLKey(existing.key, key) {
			return existing.index, true
		}
	}
	return keys.count, false
}

func equivalentYAMLKey(a, b interface{}) bool {
	return equivalentYAMLKeyActive(a, b, make(map[yamlKeyPair]bool))
}

type yamlKeyPair struct {
	left, right interface{}
}

func equivalentYAMLKeyActive(a, b interface{}, active map[yamlKeyPair]bool) bool {
	leftValue, rightValue := reflect.ValueOf(a), reflect.ValueOf(b)
	if leftValue.IsValid() && rightValue.IsValid() &&
		leftValue.Kind() == reflect.Ptr && rightValue.Kind() == reflect.Ptr &&
		!leftValue.IsNil() && !rightValue.IsNil() {
		pair := yamlKeyPair{a, b}
		if active[pair] {
			return true
		}
		active[pair] = true
		defer delete(active, pair)
	}
	a, b = mappingKeyIdentity(a), mappingKeyIdentity(b)
	if left, ok := asMapSlice(a); ok {
		right, ok := asMapSlice(b)
		if !ok || len(left) != len(right) {
			return false
		}
		buckets := make(map[string][]int, len(right))
		for i, item := range right {
			fingerprint := yamlItemFingerprint(item)
			buckets[fingerprint] = append(buckets[fingerprint], i)
		}
		used := make([]bool, len(right))
		for _, item := range left {
			found := false
			for _, i := range buckets[yamlItemFingerprint(item)] {
				other := right[i]
				if !used[i] && equivalentYAMLKeyActive(item.Key, other.Key, active) && equivalentYAMLKeyActive(item.Value, other.Value, active) {
					used[i] = true
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	if left, ok := a.([]interface{}); ok {
		right, ok := b.([]interface{})
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !equivalentYAMLKeyActive(left[i], right[i], active) {
				return false
			}
		}
		return true
	}
	left, right := reflect.ValueOf(a), reflect.ValueOf(b)
	if left.IsValid() && right.IsValid() && left.Type() == right.Type() &&
		(left.Kind() == reflect.Array || left.Kind() == reflect.Struct) && left.Type() != timeType {
		return equivalentYAMLKeyFields(left, right, active)
	}
	if scalarKey(a) && scalarKey(b) {
		return mappingKeyIdentity(a) == mappingKeyIdentity(b)
	}
	return reflect.DeepEqual(a, b)
}

func equivalentYAMLKeyFields(left, right reflect.Value, active map[yamlKeyPair]bool) bool {
	var n int
	if left.Kind() == reflect.Struct {
		n = left.NumField()
	} else {
		n = left.Len()
	}
	for i := 0; i < n; i++ {
		var a, b reflect.Value
		if left.Kind() == reflect.Struct {
			a, b = left.Field(i), right.Field(i)
		} else {
			a, b = left.Index(i), right.Index(i)
		}
		if a.CanInterface() && b.CanInterface() {
			if !equivalentYAMLKeyActive(a.Interface(), b.Interface(), active) {
				return false
			}
		} else if a.Kind() == reflect.Array || a.Kind() == reflect.Struct {
			if !equivalentYAMLKeyFields(a, b, active) {
				return false
			}
		} else if (a.Kind() == reflect.Float32 || a.Kind() == reflect.Float64) && math.IsNaN(a.Float()) && math.IsNaN(b.Float()) {
			continue
		} else if !a.Comparable() || !b.Comparable() || !a.Equal(b) {
			return false
		}
	}
	return true
}

func asMapSlice(value interface{}) (MapSlice, bool) {
	v := reflect.ValueOf(value)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.Type().Elem() != mapItemType {
		return nil, false
	}
	return v.Convert(reflect.TypeOf(MapSlice{})).Interface().(MapSlice), true
}

func yamlKeyFingerprint(key interface{}) string {
	return yamlKeyFingerprintActive(key, make(map[interface{}]bool))
}

func fixedYAMLFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return string(sum[:])
}

func yamlKeyFingerprintActive(key interface{}, active map[interface{}]bool) (fingerprint string) {
	defer func() { fingerprint = fixedYAMLFingerprint(fingerprint) }()
	value := reflect.ValueOf(key)
	if value.IsValid() && value.Kind() == reflect.Ptr && !value.IsNil() {
		if active[key] {
			return fmt.Sprintf("cycle:%T", key)
		}
		active[key] = true
		defer delete(active, key)
	}
	key = mappingKeyIdentity(key)
	if timestamp, ok := key.(time.Time); ok {
		return "time:" + timestamp.Format(time.RFC3339Nano)
	}
	if items, ok := asMapSlice(key); ok {
		parts := make([]string, len(items))
		for i, item := range items {
			parts[i] = yamlItemFingerprintActive(item, active)
		}
		sort.Strings(parts)
		return "map:" + strings.Join(parts, "")
	}
	if items, ok := key.([]interface{}); ok {
		var result strings.Builder
		result.WriteString("seq:")
		for _, item := range items {
			part := yamlKeyFingerprintActive(item, active)
			result.WriteString(strconv.Itoa(len(part)))
			result.WriteByte(':')
			result.WriteString(part)
		}
		return result.String()
	}
	if value := reflect.ValueOf(key); value.IsValid() && (value.Kind() == reflect.Array || value.Kind() == reflect.Struct) {
		return yamlCompositeFingerprint(value, active)
	}
	if scalarKey(key) {
		v := reflect.ValueOf(key)
		if v.IsValid() && (v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64) && v.Float() == 0 {
			return fmt.Sprintf("%T:0", key)
		}
		if v.IsValid() && (v.Kind() == reflect.Complex64 || v.Kind() == reflect.Complex128) {
			return yamlPrivateKeyFingerprint(v, active)
		}
		return fmt.Sprintf("%T:%#v", key, key)
	}
	return fmt.Sprintf("%T", key)
}

func yamlCompositeFingerprint(value reflect.Value, active map[interface{}]bool) (fingerprint string) {
	defer func() { fingerprint = fixedYAMLFingerprint(fingerprint) }()
	var result strings.Builder
	fmt.Fprintf(&result, "%s:", value.Type())
	var n int
	if value.Kind() == reflect.Struct {
		n = value.NumField()
	} else {
		n = value.Len()
	}
	for i := 0; i < n; i++ {
		var field reflect.Value
		if value.Kind() == reflect.Struct {
			field = value.Field(i)
		} else {
			field = value.Index(i)
		}
		var part string
		switch {
		case field.CanInterface():
			part = yamlKeyFingerprintActive(field.Interface(), active)
		case field.Kind() == reflect.Array || field.Kind() == reflect.Struct:
			part = yamlCompositeFingerprint(field, active)
		default:
			part = yamlPrivateKeyFingerprint(field, active)
		}
		result.WriteString(strconv.Itoa(len(part)))
		result.WriteByte(':')
		result.WriteString(part)
	}
	return result.String()
}

func yamlPrivateKeyFingerprint(value reflect.Value, active map[interface{}]bool) (fingerprint string) {
	defer func() { fingerprint = fixedYAMLFingerprint(fingerprint) }()
	prefix := value.Type().String() + ":"
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return prefix + "nil"
		}
		return prefix + yamlPrivateKeyFingerprint(value.Elem(), active)
	case reflect.Array, reflect.Struct:
		return yamlCompositeFingerprint(value, active)
	case reflect.Float32, reflect.Float64:
		f := value.Float()
		if math.IsNaN(f) {
			return prefix + "nan"
		}
		if f == 0 {
			return prefix + "0"
		}
		return prefix + strconv.FormatFloat(f, 'g', -1, value.Type().Bits())
	case reflect.Complex64, reflect.Complex128:
		number := value.Complex()
		realPart, imaginaryPart := real(number), imag(number)
		if realPart == 0 {
			realPart = 0
		}
		if imaginaryPart == 0 {
			imaginaryPart = 0
		}
		bits := value.Type().Bits() / 2
		return prefix + strconv.FormatFloat(realPart, 'g', -1, bits) + ":" + strconv.FormatFloat(imaginaryPart, 'g', -1, bits)
	case reflect.String:
		return prefix + value.String()
	case reflect.Bool:
		return prefix + strconv.FormatBool(value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return prefix + strconv.FormatInt(value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return prefix + strconv.FormatUint(value.Uint(), 10)
	case reflect.Ptr, reflect.Chan, reflect.UnsafePointer:
		return prefix + strconv.FormatUint(uint64(value.Pointer()), 16)
	default:
		return prefix
	}
}

func yamlItemFingerprint(item MapItem) string {
	return yamlItemFingerprintActive(item, make(map[interface{}]bool))
}

func yamlItemFingerprintActive(item MapItem, active map[interface{}]bool) (fingerprint string) {
	defer func() { fingerprint = fixedYAMLFingerprint(fingerprint) }()
	k, v := yamlKeyFingerprintActive(item.Key, active), yamlKeyFingerprintActive(item.Value, active)
	return strconv.Itoa(len(k)) + ":" + k + strconv.Itoa(len(v)) + ":" + v
}

func (keys *mapSliceKeys) add(key interface{}) {
	key = mappingKeyIdentity(key)
	if scalarKey(key) {
		if keys.scalars == nil {
			keys.scalars = make(map[interface{}]int)
		}
		keys.scalars[key] = keys.count
	} else {
		if keys.complex == nil {
			keys.complex = make(map[string][]indexedYAMLKey)
		}
		fingerprint := yamlKeyFingerprint(key)
		keys.complex[fingerprint] = append(keys.complex[fingerprint], indexedYAMLKey{key, keys.count})
	}
	keys.count++
}

func (d *decoder) mappingStruct(n *node, out reflect.Value) (good bool) {
	sinfo, err := getStructInfo(out.Type())
	if err != nil {
		panic(err)
	}
	name := settableValueOf("")
	l := len(n.children)

	var inlineMap reflect.Value
	var elemType reflect.Type
	if sinfo.InlineMap != nil {
		inlineMap = out.FieldByIndex(sinfo.InlineMap)
		if !d.mergeTarget.IsValid() || d.mergeTarget != out {
			inlineMap.Set(reflect.New(inlineMap.Type()).Elem())
		}
		elemType = inlineMap.Type().Elem()
	}

	var doneFields []bool
	var doneInline map[string]bool
	var mergeNodes []*node
	seen := make(map[string]bool)
	blocked := make(map[string]bool)
	previousTarget, previousKeys := d.mergeTarget, d.mergedKeys
	if !d.mergeTarget.IsValid() || d.mergeTarget != out {
		d.mergedKeys = new(mapSliceKeys)
	}
	d.mergeTarget = out
	defer func() { d.mergeTarget, d.mergedKeys = previousTarget, previousKeys }()
	if d.strict {
		doneFields = make([]bool, len(sinfo.FieldsList))
		doneInline = make(map[string]bool)
	}
	for i := 0; i < l; i += 2 {
		ni := n.children[i]
		if isMerge(ni) {
			if d.strict && len(mergeNodes) != 0 {
				d.terrors = append(d.terrors, fmt.Sprintf("line %d: merge key already set in map", ni.line+1))
			} else {
				mergeNodes = append(mergeNodes, n.children[i+1])
			}
			continue
		}
		if !d.unmarshal(ni, name) {
			continue
		}
		if info, ok := sinfo.FieldsMap[name.String()]; ok {
			if d.strict {
				if doneFields[info.Id] {
					d.terrors = append(d.terrors, fmt.Sprintf("line %d: field %s already set in type %s", ni.line+1, name.String(), out.Type()))
					continue
				}
				doneFields[info.Id] = true
			}
			var field reflect.Value
			if info.Inline == nil {
				field = out.Field(info.Num)
			} else {
				field = out.FieldByIndex(info.Inline)
			}
			if !d.selectStructKey(name.String(), seen, blocked) {
				d.validateSkipped(n.children[i+1])
				continue
			}
			d.unmarshal(n.children[i+1], field)
		} else if sinfo.InlineMap != nil {
			if inlineMap.IsNil() {
				inlineMap.Set(reflect.MakeMap(inlineMap.Type()))
			}
			if d.strict {
				if doneInline[name.String()] {
					d.terrors = append(d.terrors, fmt.Sprintf("line %d: key %#v already set in map", n.children[i+1].line+1, name.Interface()))
					continue
				}
				doneInline[name.String()] = true
			}
			if !d.selectStructKey(name.String(), seen, blocked) {
				d.validateSkipped(n.children[i+1])
				continue
			}
			value := reflect.New(elemType).Elem()
			d.unmarshal(n.children[i+1], value)
			if d.strict {
				inlineMap.SetMapIndex(name, value)
			} else {
				d.setMapIndex(n.children[i+1], inlineMap, name, value)
			}
		} else if d.strict {
			d.terrors = append(d.terrors, fmt.Sprintf("line %d: field %s not found in type %s", ni.line+1, name.String(), out.Type()))
		}
	}
	for _, mergeNode := range mergeNodes {
		d.merge(mergeNode, out)
	}
	return true
}

func (d *decoder) selectStructKey(key string, seen, blocked map[string]bool) bool {
	if seen[key] {
		return !blocked[key]
	}
	seen[key] = true
	if d.mergedKeys.contains(key) {
		blocked[key] = true
		return false
	}
	d.mergedKeys.add(key)
	return true
}

func failWantMap() {
	failf("map merge requires map or sequence of maps as the value")
}

func (d *decoder) validateSkipped(n *node) {
	mapType := d.mapType
	d.mapType = reflect.TypeOf(MapSlice{})
	defer func() { d.mapType = mapType }()
	var ignored interface{}
	d.unmarshal(n, reflect.ValueOf(&ignored).Elem())
}

func (d *decoder) merge(n *node, out reflect.Value) {
	switch n.kind {
	case mappingNode:
		d.unmarshal(n, out)
	case aliasNode:
		if n.alias != nil && n.alias.kind == sequenceNode {
			d.countDecode()
			d.withAlias(n, func() bool {
				d.countDecode()
				d.merge(n.alias, out)
				return true
			})
			return
		}
		if n.alias != nil && n.alias.kind != mappingNode {
			failWantMap()
		}
		d.unmarshal(n, out)
	case sequenceNode:
		for _, ni := range n.children {
			if ni.kind == aliasNode {
				if ni.alias != nil && ni.alias.kind != mappingNode {
					failWantMap()
				}
			} else if ni.kind != mappingNode {
				failWantMap()
			}
			d.unmarshal(ni, out)
		}
	default:
		failWantMap()
	}
}

func isMerge(n *node) bool {
	for n.kind == aliasNode && n.alias != nil {
		n = n.alias
	}
	return n.kind == scalarNode && n.value == "<<" && (n.implicit && n.tag == "" || n.tag == yaml_MERGE_TAG)
}
