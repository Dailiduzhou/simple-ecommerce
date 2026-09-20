// Command openapi-doc turns the raw protoc-gen-openapi output into a document
// that describes what the HTTP server actually does.
//
// protoc-gen-openapi only knows the protos. It cannot see the JWT middleware,
// the kratos error contract, the authorization checks in the service layer, the
// buf.validate constraints or the routes the server registers by hand, so those
// parts are added here:
//
//   - info/servers/securitySchemes and the per-operation security overrides,
//   - the error responses of every operation (kratos error body schema),
//   - required fields and validation keywords derived from buf.validate,
//   - path templates renamed to the snake_case routes kratos registers,
//   - the manually registered routes (payment channel callbacks).
//
// Everything is derived from the protos and the Go sources next to them, so a
// change in the server shows up as a diff instead of as a stale document.
//
// Usage (wired into `make -C app/mall api`):
//
//	go run ./tools/openapi-doc -root . -spec openapi.yaml
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func main() {
	root := flag.String("root", ".", "module root, used to locate the HTTP server and service sources")
	spec := flag.String("spec", "openapi.yaml", "generated OpenAPI document to rewrite in place")
	flag.Parse()

	if err := rewrite(*root, *spec); err != nil {
		fmt.Fprintln(os.Stderr, "openapi-doc:", err)
		os.Exit(1)
	}
}

// rewrite patches spec in place using the protos, the HTTP server routes and
// the service layer of the module rooted at root.
func rewrite(root, spec string) error {
	serverFile := filepath.Join(root, "app/mall/internal/server/http.go")
	serviceDir := filepath.Join(root, "app/mall/internal/service")
	if err := serviceDirExists(serviceDir); err != nil {
		return fmt.Errorf("service sources: %w", err)
	}

	model, err := loadProtoModel()
	if err != nil {
		return err
	}
	public, err := loadPublicOperations(serverFile, model)
	if err != nil {
		return err
	}
	serviceStatus, err := loadServiceStatuses(serviceDir, model)
	if err != nil {
		return err
	}
	documented, err := manualRoutes(serverFile)
	if err != nil {
		return err
	}
	manual := manualRouteDocs()
	if err := checkManualRoutes(documented, manual); err != nil {
		return err
	}
	for _, doc := range manual {
		public[doc.ID] = true
	}

	content, err := os.ReadFile(spec)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return fmt.Errorf("parse %s: %w", spec, err)
	}
	if err := patchSpec(&document, patchInput{
		model:         model,
		public:        public,
		serviceStatus: serviceStatus,
		manual:        manual,
	}); err != nil {
		return err
	}
	encoded, err := encodeDoc(&document)
	if err != nil {
		return err
	}
	return os.WriteFile(spec, encoded, 0o644)
}

// checkManualRoutes keeps the hand written documentation of server-registered
// routes in sync with the code that registers them.
func checkManualRoutes(registered []route, manual []manualRouteDoc) error {
	want := map[string]bool{}
	for _, doc := range manual {
		want[route{Method: doc.Method, Path: doc.Path}.String()] = true
	}
	got := map[string]bool{}
	for _, r := range registered {
		got[r.String()] = true
		if !want[r.String()] {
			return fmt.Errorf("%s is registered by hand but not documented by tools/openapi-doc", r)
		}
	}
	for r := range want {
		if !got[r] {
			return fmt.Errorf("%s is documented but no longer registered by the HTTP server", r)
		}
	}
	return nil
}
