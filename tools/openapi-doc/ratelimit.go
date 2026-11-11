package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// loadRateLimitedOperations reads the only source that knows which operations
// have a write quota. The limiter runs as transport middleware, before the
// service method, so its 429/503 never appear in the service-layer scan.
// Reading the switch in biz.RateLimitCategory keeps those responses attached to
// the routes that actually have a quota instead of a hand maintained list.
func loadRateLimitedOperations(path string, model *protoModel) (map[string]bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var categories *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "RateLimitCategory" {
			categories = fn
			break
		}
	}
	if categories == nil || categories.Body == nil {
		return nil, fmt.Errorf("%s no longer defines RateLimitCategory", path)
	}
	byConstant := map[string]string{}
	for _, op := range model.operations {
		key := strings.ReplaceAll(op.ID, "_", "")
		if existing, ok := byConstant[key]; ok && existing != op.ID {
			return nil, fmt.Errorf("operations %s and %s share the constant suffix %s", existing, op.ID, key)
		}
		byConstant[key] = op.ID
	}

	limited := map[string]bool{}
	var unknown []string
	ast.Inspect(categories.Body, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(selector.Sel.Name, "Operation") {
			return true
		}
		suffix := strings.TrimPrefix(selector.Sel.Name, "Operation")
		id, ok := byConstant[suffix]
		if !ok {
			unknown = append(unknown, selector.Sel.Name)
			return true
		}
		limited[id] = true
		return true
	})
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%s references %s, which has no HTTP route", path, strings.Join(unknown, ", "))
	}
	if len(limited) == 0 {
		return nil, fmt.Errorf("no rate-limited operations found in %s; the parser or the limiter changed", path)
	}
	return limited, nil
}
