package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/config"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Kratos' default resolver leaves environment substitutions as strings, while
// protobuf JSON requires boolean tokens. Walk the descriptor and normalize only
// fields that are actually bool: a new bool config field is handled
// automatically, while global type coercion would corrupt numeric-looking app
// IDs or credentials. Unknown keys are handled by DiscardUnknown below.
func normalizeBools(node any, md protoreflect.MessageDescriptor, path string) error {
	values, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	fields := md.Fields()
	for name, value := range values {
		fd := fields.ByName(protoreflect.Name(name))
		if fd == nil {
			fd = fields.ByJSONName(name)
		}
		if fd == nil {
			continue // unknown key; protojson.DiscardUnknown drops it
		}
		child := string(fd.Name())
		if path != "" {
			child = path + "." + child
		}
		if fd.Message() != nil && !fd.IsMap() && !fd.IsList() {
			if e := normalizeBools(value, fd.Message(), child); e != nil {
				return e
			}
			continue
		}
		if fd.Kind() != protoreflect.BoolKind {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue // YAML literal bools are already typed
		}
		if text == "" {
			text = "false"
		}
		flag, e := strconv.ParseBool(text)
		if e != nil {
			return fmt.Errorf("%s must be a boolean", child)
		}
		values[name] = flag
	}
	return nil
}

func scanBootstrap(c config.Config, out *conf.Bootstrap) error {
	var values map[string]any
	if e := c.Scan(&values); e != nil {
		return e
	}
	md := (&conf.Bootstrap{}).ProtoReflect().Descriptor()
	if e := normalizeBools(values, md, ""); e != nil {
		return e
	}
	raw, e := json.Marshal(values)
	if e != nil {
		return e
	}
	// The env source loads every process variable as a top-level key, so unknown
	// keys must be discarded; known fields still fail on type errors.
	return (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, out)
}
