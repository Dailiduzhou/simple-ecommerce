package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The service layer is the only place that knows which operations need an
// admin role or resource ownership, so the document derives the extra error
// statuses from there instead of duplicating a hand written list.
//
// The scan is deliberately shallow: it looks at the RPC methods of
// app/mall/internal/service/*.go, resolves the proto package from the request
// type they use, and collects the kratos error kinds the method can return
// (either directly or through the requireAdmin / requireResourceOwner helpers
// that sit next to it in authorization.go).

var kratosErrorStatus = map[string]int{
	"BadRequest":          400,
	"Unauthorized":        401,
	"Forbidden":           403,
	"NotFound":            404,
	"RequestTimeout":      408,
	"Conflict":            409,
	"TooManyRequests":     429,
	"InternalServerError": 500,
	"BadGateway":          502,
	"ServiceUnavailable":  503,
	"GatewayTimeout":      504,
}

// authzHelpers are the local helpers that reject callers for authorization
// reasons. Their bodies live in the same package, so a method calling one of
// them can return the statuses they produce.
var authzHelpers = map[string]int{
	"requireAdmin":         403,
	"requireResourceOwner": 403,
	"authenticatedClaims":  401,
}

// loadServiceStatuses returns, per operation ID, the extra HTTP status codes
// the service layer can produce for it.
func loadServiceStatuses(serviceDir string, model *protoModel) (map[string][]int, error) {
	paths, err := filepath.Glob(filepath.Join(serviceDir, "*.go"))
	if err != nil {
		return nil, err
	}
	statuses := map[string][]int{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		imports := importAliases(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			pkg := protoPackageOfRequest(imports, fn)
			if pkg == "" {
				continue
			}
			opID, ok := model.byRPCPackageMethod[pkg+"."+fn.Name.Name]
			if !ok {
				continue
			}
			codes := collectStatuses(imports, fn.Body)
			if len(codes) == 0 {
				continue
			}
			statuses[opID] = append(statuses[opID], codes...)
		}
	}
	if len(statuses) == 0 {
		return nil, fmt.Errorf("no authorization checks found under %s", serviceDir)
	}
	for id := range statuses {
		sort.Ints(statuses[id])
		statuses[id] = dedupeInts(statuses[id])
	}
	return statuses, nil
}

func importAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = path
	}
	return aliases
}

// protoPackageOfRequest finds the api package an RPC method talks in by looking
// at the request type in its signature, e.g. `req *pb.CreateProductRequest`
// with `pb` imported from api/mall/v1 resolves to api.mall.v1.
func protoPackageOfRequest(imports map[string]string, fn *ast.FuncDecl) string {
	for _, param := range fn.Type.Params.List {
		selector := selectorOf(param.Type)
		if selector == nil {
			continue
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			continue
		}
		pkg := protoPackageOfPath(imports[ident.Name])
		if pkg != "" {
			return pkg
		}
	}
	return ""
}

// protoPackageOfPath turns ".../api/mall/v1" into "api.mall.v1".
func protoPackageOfPath(path string) string {
	if !strings.Contains(path, "/api/") || !strings.HasSuffix(path, "/v1") {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return ""
	}
	service, version := parts[len(parts)-2], parts[len(parts)-1]
	return "api." + service + "." + version
}

func selectorOf(expr ast.Expr) *ast.SelectorExpr {
	switch node := expr.(type) {
	case *ast.StarExpr:
		return selectorOf(node.X)
	case *ast.SelectorExpr:
		return node
	case *ast.Ellipsis:
		return selectorOf(node.Elt)
	default:
		return nil
	}
}

func collectStatuses(imports map[string]string, body *ast.BlockStmt) []int {
	var codes []int
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if code, ok := authzHelpers[fun.Name]; ok {
				codes = append(codes, code)
			}
		case *ast.SelectorExpr:
			ident, ok := fun.X.(*ast.Ident)
			if !ok || !strings.HasSuffix(imports[ident.Name], "go-kratos/kratos/v2/errors") {
				return true
			}
			if code, ok := kratosErrorStatus[fun.Sel.Name]; ok {
				codes = append(codes, code)
			}
		}
		return true
	})
	sort.Ints(codes)
	return dedupeInts(codes)
}

// loadPublicOperations reads the JWT whitelist from the HTTP server so the
// document cannot drift from the middleware that actually guards the routes.
func loadPublicOperations(serverFile string, model *protoModel) (map[string]bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), serverFile, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", serverFile, err)
	}
	names, err := whitelistConstants(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", serverFile, err)
	}
	public := map[string]bool{}
	for _, name := range names {
		// The generated constants are named Operation<Service><Method>.
		opID := strings.TrimPrefix(name, "Operation")
		opID = camelToOperationID(opID)
		if _, ok := model.byID[opID]; !ok {
			return nil, fmt.Errorf("%s: whitelist entry %s does not match a documented operation (%s)", serverFile, name, opID)
		}
		public[opID] = true
	}
	if len(public) == 0 {
		return nil, fmt.Errorf("%s: no JWT whitelist entries found", serverFile)
	}
	return public, nil
}

func whitelistConstants(file *ast.File) ([]string, error) {
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newWhiteListMatcher" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			composite, ok := node.(*ast.CompositeLit)
			if !ok || len(composite.Elts) == 0 {
				return true
			}
			for _, elt := range composite.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if ident, ok := kv.Key.(*ast.Ident); ok && strings.HasPrefix(ident.Name, "Operation") {
					names = append(names, ident.Name)
				}
			}
			return true
		})
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no whiteList entries found in newWhiteListMatcher")
	}
	return names, nil
}

// camelToOperationID converts "UserRefreshToken" into "User_RefreshToken",
// which is the protoc-gen-go-http operation naming for service + method.
func camelToOperationID(name string) string {
	for i := 1; i < len(name); i++ {
		if name[i] >= 'A' && name[i] <= 'Z' {
			return name[:i] + "_" + name[i:]
		}
	}
	return name
}

// manualRoutes reads the routes the server registers by hand, i.e. the ones
// protoc-gen-go-http cannot generate because their handlers work on the raw
// request. Keeping the list derived means a new manual route fails the
// documentation test instead of silently disappearing from the document.
func manualRoutes(serverFile string) ([]route, error) {
	file, err := parser.ParseFile(token.NewFileSet(), serverFile, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", serverFile, err)
	}
	var routes []route
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		// srv.Route("/").POST("/v1/...", handler)
		inner, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		innerSel, ok := inner.Fun.(*ast.SelectorExpr)
		if !ok || innerSel.Sel.Name != "Route" {
			return true
		}
		path, ok := call.Args[0].(*ast.BasicLit)
		if !ok || path.Kind != token.STRING {
			return true
		}
		routes = append(routes, route{Method: sel.Sel.Name, Path: strings.Trim(path.Value, `"`)})
		return true
	})
	return routes, nil
}

type route struct {
	Method string
	Path   string
}

func (r route) String() string { return r.Method + " " + r.Path }

// serviceDirExists guards the caller against a wrong -root argument, which
// would otherwise look like "no operations are authorized".
func serviceDirExists(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}
