package main

import (
	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gopkg.in/yaml.v3"
)

// buf.validate is the only place that states which request fields the server
// accepts, so those rules become JSON Schema keywords here:
//
//	string.min_len / min_bytes   -> minLength (bytes are approximated as characters)
//	string.max_len / max_bytes   -> maxLength
//	string.in                    -> enum
//	int32 gt/gte/lt/lte          -> minimum / maximum (inclusive bounds)
//	repeated min_items/max_items -> minItems / maxItems
//
// protojson turns 64 bit integers into JSON strings, so those fields keep
// `type: string` and gain `format: int64` plus x-minimum / x-exclusiveMinimum /
// x-maximum / x-exclusiveMaximum, because numeric keywords do not apply to
// strings. A field is also marked required when omitting it (leaving the zero
// value in place) would already fail its own rule, which is what the server
// rejects at runtime.

type schemaPatch struct {
	Field    string // JSON property name
	Required bool
	// Pairs are key/value nodes merged into the property schema.
	Pairs []*yaml.Node
	// Elements are key/value nodes merged into items / additionalProperties,
	// i.e. the element or value schema of a list or map field.
	Elements []*yaml.Node
}

func (p schemaPatch) empty() bool {
	return !p.Required && len(p.Pairs) == 0 && len(p.Elements) == 0
}

func kv(pairs []*yaml.Node, key string, val *yaml.Node) []*yaml.Node {
	return append(pairs, keyNode(key), val)
}

// schemaPatchesFor returns the constraints declared on a message's fields,
// ordered by field declaration so the generated document stays stable.
func schemaPatchesFor(md protoreflect.MessageDescriptor) []schemaPatch {
	fields := md.Fields()
	patches := make([]schemaPatch, 0, fields.Len())
	for i := range fields.Len() {
		fd := fields.Get(i)
		rules := fieldRules(fd)
		patch := schemaPatch{Field: fd.JSONName(), Required: ruleForcesValue(fd, rules)}

		element := elementKeywords(fd.Kind(), elementRules(fd, rules))
		switch {
		case fd.IsList():
			patch.Elements = element
			patch.Pairs = repeatedKeywords(rules)
		case fd.IsMap():
			patch.Elements = element
		default:
			patch.Pairs = element
		}
		if patch.empty() {
			continue
		}
		patches = append(patches, patch)
	}
	return patches
}

// elementRules returns the rules that apply to a single value: list item rules
// for repeated fields and value rules for maps.
func elementRules(fd protoreflect.FieldDescriptor, rules *validate.FieldRules) *validate.FieldRules {
	if rules == nil {
		return nil
	}
	switch {
	case fd.IsList():
		return rules.GetRepeated().GetItems()
	case fd.IsMap():
		return rules.GetMap().GetValues()
	default:
		return rules
	}
}

func fieldRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	opts := fd.Options()
	if opts == nil {
		return nil
	}
	rules, _ := proto.GetExtension(opts, validate.E_Field).(*validate.FieldRules)
	return rules
}

// elementKeywords maps the rules of a single value (a scalar field, a list
// element or a map value) onto JSON Schema keywords.
func elementKeywords(kind protoreflect.Kind, rules *validate.FieldRules) []*yaml.Node {
	var pairs []*yaml.Node
	switch kind {
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		// JSON strings per protojson.
		pairs = kv(pairs, "format", strNode("int64"))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		pairs = kv(pairs, "format", strNode("uint64"))
	}
	if rules == nil {
		return pairs
	}
	switch kind {
	case protoreflect.StringKind:
		return stringKeywords(pairs, rules.GetString())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return int32Keywords(pairs, rules.GetInt32())
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return int64Keywords(pairs, rules.GetInt64())
	default:
		return pairs
	}
}

func stringKeywords(pairs []*yaml.Node, rules *validate.StringRules) []*yaml.Node {
	if rules == nil {
		return pairs
	}
	minLen, maxLen := uint64(0), uint64(0)
	if rules.HasLen() {
		minLen, maxLen = rules.GetLen(), rules.GetLen()
	}
	if rules.HasMinLen() {
		minLen = maxUint64(minLen, rules.GetMinLen())
	}
	if rules.HasMinBytes() {
		// min_bytes bounds bytes, minLength counts characters. For UTF-8 the
		// byte bound is the stricter of the two, so it wins.
		minLen = maxUint64(minLen, rules.GetMinBytes())
	}
	if rules.HasMaxLen() {
		maxLen = clampMax(maxLen, rules.GetMaxLen())
	}
	if rules.HasMaxBytes() {
		maxLen = clampMax(maxLen, rules.GetMaxBytes())
	}
	if minLen > 0 {
		pairs = kv(pairs, "minLength", intNode(int(minLen)))
	}
	if maxLen > 0 {
		pairs = kv(pairs, "maxLength", intNode(int(maxLen)))
	}
	if in := rules.GetIn(); len(in) > 0 {
		items := make([]*yaml.Node, 0, len(in))
		for _, value := range in {
			items = append(items, strNode(value))
		}
		pairs = kv(pairs, "enum", seqNode(items...))
	}
	return pairs
}

func int32Keywords(pairs []*yaml.Node, rules *validate.Int32Rules) []*yaml.Node {
	if rules == nil {
		return pairs
	}
	if rules.HasGte() {
		pairs = kv(pairs, "minimum", intNode(int(rules.GetGte())))
	}
	if rules.HasGt() {
		// Integer types have no exclusive bound in OpenAPI 3.0, so the
		// strictest inclusive one is used instead.
		pairs = kv(pairs, "minimum", intNode(int(rules.GetGt())+1))
	}
	if rules.HasLte() {
		pairs = kv(pairs, "maximum", intNode(int(rules.GetLte())))
	}
	if rules.HasLt() {
		pairs = kv(pairs, "maximum", intNode(int(rules.GetLt())-1))
	}
	return pairs
}

func int64Keywords(pairs []*yaml.Node, rules *validate.Int64Rules) []*yaml.Node {
	if rules == nil {
		return pairs
	}
	// The value is a JSON string, so bounds are recorded as x- extensions that
	// tooling can read without pretending they are JSON numbers.
	if rules.HasGte() {
		pairs = kv(pairs, "x-minimum", intNode(int(rules.GetGte())))
	}
	if rules.HasGt() {
		pairs = kv(pairs, "x-exclusiveMinimum", intNode(int(rules.GetGt())))
	}
	if rules.HasLte() {
		pairs = kv(pairs, "x-maximum", intNode(int(rules.GetLte())))
	}
	if rules.HasLt() {
		pairs = kv(pairs, "x-exclusiveMaximum", intNode(int(rules.GetLt())))
	}
	return pairs
}

func repeatedKeywords(rules *validate.FieldRules) []*yaml.Node {
	if rules == nil {
		return nil
	}
	list := rules.GetRepeated()
	if list == nil {
		return nil
	}
	var pairs []*yaml.Node
	if list.HasMinItems() {
		pairs = kv(pairs, "minItems", intNode(int(list.GetMinItems())))
	}
	if list.HasMaxItems() {
		pairs = kv(pairs, "maxItems", intNode(int(list.GetMaxItems())))
	}
	if list.GetUnique() {
		pairs = kv(pairs, "uniqueItems", boolNode(true))
	}
	return pairs
}

// ruleForcesValue reports whether the zero value of a field would already
// violate its rules, i.e. whether the caller has to send it.
func ruleForcesValue(fd protoreflect.FieldDescriptor, rules *validate.FieldRules) bool {
	if rules == nil {
		return false
	}
	if rules.GetRequired() {
		return true
	}
	if fd.IsList() {
		// Only the list size can make the field mandatory; item rules cannot.
		list := rules.GetRepeated()
		return list != nil && list.HasMinItems() && list.GetMinItems() > 0
	}
	if fd.IsMap() {
		return false
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		return stringForcesValue(rules.GetString())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		signed := rules.GetInt32()
		return signed != nil && ((signed.HasGt() && signed.GetGt() >= 0) || (signed.HasGte() && signed.GetGte() > 0))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		signed := rules.GetInt64()
		return signed != nil && ((signed.HasGt() && signed.GetGt() >= 0) || (signed.HasGte() && signed.GetGte() > 0))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		unsigned := rules.GetUint32()
		return unsigned != nil && (unsigned.HasGt() || (unsigned.HasGte() && unsigned.GetGte() > 0))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		unsigned := rules.GetUint64()
		return unsigned != nil && (unsigned.HasGt() || (unsigned.HasGte() && unsigned.GetGte() > 0))
	case protoreflect.BoolKind:
		boolean := rules.GetBool()
		return boolean != nil && boolean.HasConst() && boolean.GetConst()
	}
	return false
}

func stringForcesValue(rules *validate.StringRules) bool {
	if rules == nil {
		return false
	}
	if rules.HasLen() && rules.GetLen() > 0 {
		return true
	}
	if rules.HasMinLen() && rules.GetMinLen() > 0 {
		return true
	}
	if rules.HasMinBytes() && rules.GetMinBytes() > 0 {
		return true
	}
	if in := rules.GetIn(); len(in) > 0 {
		for _, value := range in {
			if value == "" {
				return false
			}
		}
		return true
	}
	return false
}

func maxUint64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}

func clampMax(current, candidate uint64) uint64 {
	if candidate == 0 {
		return current
	}
	if current == 0 || candidate < current {
		return candidate
	}
	return current
}
