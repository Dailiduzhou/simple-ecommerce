package main

import (
	"bytes"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Helpers for editing the generated document through yaml.Node. Going through
// the node tree (instead of a map) keeps the key order and lets the untouched
// parts of the file round-trip byte for byte: protoc-gen-openapi writes four
// spaces of indentation, which is also what encodeDoc asks for.

func mapNode(pairs ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: pairs}
}

func seqNode(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

// strNode renders a plain string, switching to a literal block for values that
// span several lines so descriptions stay readable.
func strNode(v string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	if strings.Contains(v, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

func keyNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// quotedKeyNode matches how the generator writes response codes ("200").
func quotedKeyNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}

func intNode(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
}

func boolNode(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
}

func schemasRef(name string) *yaml.Node {
	return mapNode(keyNode("$ref"), strNode("#/components/schemas/"+name))
}

// jsonResponse mirrors the response body the generator emits for a message.
func jsonResponse(schema *yaml.Node) *yaml.Node {
	return mapNode(
		keyNode("content"), mapNode(
			keyNode("application/json"), mapNode(
				keyNode("schema"), schema,
			),
		),
	)
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	return doc
}

// get returns the value stored under key, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// set replaces key in place, keeping its position, and appends it otherwise.
func set(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, keyNode(key), val)
}

// setAfter keeps a mapping in a readable order: the key is created behind
// anchor the first time, and replaced in place afterwards, so the whole
// patching step stays idempotent.
func setAfter(m *yaml.Node, anchor, key string, val *yaml.Node) {
	if get(m, key) != nil {
		set(m, key, val)
		return
	}
	insertAfter(m, anchor, key, val)
}

// setFirst creates a key at the top of a mapping and replaces it in place
// later on.
func setFirst(m *yaml.Node, key string, val *yaml.Node) {
	if get(m, key) != nil {
		set(m, key, val)
		return
	}
	prepend(m, key, val)
}

// prepend adds key in front of the existing entries of a mapping.
func prepend(m *yaml.Node, key string, val *yaml.Node) {
	rest := append([]*yaml.Node{}, m.Content...)
	m.Content = append([]*yaml.Node{keyNode(key), val}, rest...)
}

// insertAfter adds key directly behind anchor, or appends it when the anchor is
// missing.
func insertAfter(m *yaml.Node, anchor, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == anchor {
			rest := append([]*yaml.Node{}, m.Content[i+2:]...)
			m.Content = append(m.Content[:i+2], keyNode(key), val)
			m.Content = append(m.Content, rest...)
			return
		}
	}
	m.Content = append(m.Content, keyNode(key), val)
}

// sortKeys sorts a mapping by its scalar keys, which is the order
// protoc-gen-openapi uses for paths and schemas.
func sortKeys(m *yaml.Node) {
	type pair struct{ k, v *yaml.Node }
	pairs := make([]pair, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		pairs = append(pairs, pair{m.Content[i], m.Content[i+1]})
	}
	// Insertion sort keeps the code dependency free and the mappings small.
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && pairs[j].k.Value < pairs[j-1].k.Value; j-- {
			pairs[j], pairs[j-1] = pairs[j-1], pairs[j]
		}
	}
	m.Content = m.Content[:0]
	for _, p := range pairs {
		m.Content = append(m.Content, p.k, p.v)
	}
}

func encodeDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
