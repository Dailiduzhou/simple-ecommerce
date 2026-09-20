package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

// The tests run from tools/openapi-doc, so the module root is two levels up.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	return root
}

func loadDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return document
}

func pathsOf(t *testing.T, document map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatal("document has no paths")
	}
	out := map[string]map[string]any{}
	for path, item := range raw {
		out[path] = item.(map[string]any)
	}
	return out
}

func operationOf(t *testing.T, document map[string]any, path, method string) map[string]any {
	t.Helper()
	item, ok := pathsOf(t, document)[path]
	if !ok {
		t.Fatalf("document does not document %s", path)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("%s has no %s operation", path, method)
	}
	return op
}

func responseCodes(t *testing.T, op map[string]any) map[string]bool {
	t.Helper()
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		t.Fatalf("operation %v has no responses", op["operationId"])
	}
	codes := map[string]bool{}
	for code := range responses {
		codes[code] = true
	}
	return codes
}

func schemaOf(t *testing.T, document map[string]any, name string) map[string]any {
	t.Helper()
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("document has no schema %s", name)
	}
	return schema
}

func propertyOf(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties")
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema has no property %s", name)
	}
	return property
}

func requiredFields(schema map[string]any) map[string]bool {
	required := map[string]bool{}
	items, _ := schema["required"].([]any)
	for _, item := range items {
		required[item.(string)] = true
	}
	return required
}

// TestCommittedDocumentMatchesPatcher keeps the checked in document in sync:
// if a patch changes, or the protos/server sources move on, this test fails
// until openapi.yaml is regenerated with `make -C app/mall api`.
func TestCommittedDocumentMatchesPatcher(t *testing.T) {
	root := moduleRoot(t)
	committed := filepath.Join(root, "openapi.yaml")
	want, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read committed document: %v", err)
	}

	temp := filepath.Join(t.TempDir(), "openapi.yaml")
	if err := os.WriteFile(temp, want, 0o644); err != nil {
		t.Fatalf("stage document: %v", err)
	}
	if err := rewrite(root, temp); err != nil {
		t.Fatalf("rewrite document: %v", err)
	}
	got, err := os.ReadFile(temp)
	if err != nil {
		t.Fatalf("read rewritten document: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("openapi.yaml is out of date; run: make -C app/mall api")
	}
}

// TestEveryProtoRouteIsDocumented checks that the document describes exactly
// the HTTP routes the protos define, using the same path templates.
func TestEveryProtoRouteIsDocumented(t *testing.T) {
	document := loadDocument(t, filepath.Join(moduleRoot(t), "openapi.yaml"))
	paths := pathsOf(t, document)
	model, err := loadProtoModel()
	if err != nil {
		t.Fatalf("load protos: %v", err)
	}

	documented := map[string]string{}
	for path := range paths {
		documented[normalizePathTemplate(path)] = path
	}
	for _, op := range model.operations {
		if _, ok := documented[normalizePathTemplate(op.Path)]; !ok {
			t.Errorf("%s %s is not documented", op.HTTPMethod, op.Path)
		}
		if _, ok := paths[op.Path]; !ok {
			t.Errorf("document uses a different template for %s", op.Path)
		}
	}
	// The manual routes are the only allowed extra paths.
	extra := map[string]bool{}
	for _, doc := range manualRouteDocs() {
		extra[doc.Path] = true
	}
	for path := range paths {
		matched := extra[path]
		for _, op := range model.operations {
			if op.Path == path {
				matched = true
			}
		}
		if !matched {
			t.Errorf("document path %s is not backed by a proto route or a manual route", path)
		}
	}
}

// TestManualRoutesMatchServerRegistration fails when a route registered with
// srv.Route(...) in server/http.go is not documented here (or vice versa).
func TestManualRoutesMatchServerRegistration(t *testing.T) {
	serverFile := filepath.Join(moduleRoot(t), "app/mall/internal/server/http.go")
	registered, err := manualRoutes(serverFile)
	if err != nil {
		t.Fatalf("read manual routes: %v", err)
	}
	if len(registered) == 0 {
		t.Fatal("no manually registered routes found; the parser or the server changed")
	}
	if err := checkManualRoutes(registered, manualRouteDocs()); err != nil {
		t.Fatal(err)
	}
}

// TestPublicOperationsMatchWhitelist checks that exactly the JWT whitelist and
// the manually registered callbacks are documented without a security scheme.
func TestPublicOperationsMatchWhitelist(t *testing.T) {
	root := moduleRoot(t)
	document := loadDocument(t, filepath.Join(root, "openapi.yaml"))
	model, err := loadProtoModel()
	if err != nil {
		t.Fatalf("load protos: %v", err)
	}
	public, err := loadPublicOperations(filepath.Join(root, "app/mall/internal/server/http.go"), model)
	if err != nil {
		t.Fatalf("read whitelist: %v", err)
	}

	documented := map[string]bool{}
	for _, item := range pathsOf(t, document) {
		for method, raw := range item {
			op := raw.(map[string]any)
			if method == "parameters" {
				continue
			}
			security, hasSecurity := op["security"].([]any)
			if hasSecurity && len(security) == 0 {
				documented[op["operationId"].(string)] = true
			}
			if !hasSecurity && containsString(op, "security") {
				t.Errorf("operation %v has an unexpected security value", op["operationId"])
			}
		}
	}
	for id := range public {
		if !documented[id] {
			t.Errorf("%s is whitelisted by the server but documented as protected", id)
		}
	}
	for id := range documented {
		isPublic := public[id]
		for _, doc := range manualRouteDocs() {
			if doc.ID == id {
				isPublic = true
			}
		}
		if !isPublic {
			t.Errorf("%s is documented as public but the server requires a token", id)
		}
	}
	if len(documented) == 0 {
		t.Fatal("no public operations documented")
	}
}

func containsString(op map[string]any, key string) bool {
	_, ok := op[key]
	return ok
}

// TestPrivilegedOperationsDocument403 keeps the 403 responses anchored to the
// authorization checks that exist in the service layer.
func TestPrivilegedOperationsDocument403(t *testing.T) {
	root := moduleRoot(t)
	document := loadDocument(t, filepath.Join(root, "openapi.yaml"))
	model, err := loadProtoModel()
	if err != nil {
		t.Fatalf("load protos: %v", err)
	}
	statuses, err := loadServiceStatuses(filepath.Join(root, "app/mall/internal/service"), model)
	if err != nil {
		t.Fatalf("scan services: %v", err)
	}

	privileged := map[string]bool{}
	for id, codes := range statuses {
		for _, code := range codes {
			if code == 403 {
				privileged[id] = true
			}
		}
	}
	if len(privileged) == 0 {
		t.Fatal("no privileged operations found; the service scan is broken")
	}
	byID := map[string]map[string]any{}
	for path, item := range pathsOf(t, document) {
		for method, raw := range item {
			op := raw.(map[string]any)
			byID[op["operationId"].(string)] = map[string]any{"op": op, "path": path, "method": method}
		}
	}
	for id := range privileged {
		found, ok := byID[id]
		if !ok {
			t.Errorf("%s requires authorization but is not documented", id)
			continue
		}
		if !responseCodes(t, found["op"].(map[string]any))["403"] {
			t.Errorf("%s requires authorization but does not document 403", id)
		}
	}
	// Spot checks so a silently empty scan cannot pass as correct.
	for _, id := range []string{"Mall_CreateProduct", "Payment_RefundPayment", "User_ListShippingAddresses", "User_GetUser"} {
		if !privileged[id] {
			t.Errorf("%s should be detected as privileged", id)
		}
	}
	for _, id := range []string{"Mall_ListProducts", "Mall_GetProduct", "User_Login"} {
		if privileged[id] {
			t.Errorf("%s must not be detected as privileged", id)
		}
		found, ok := byID[id]
		if !ok {
			t.Fatalf("%s is missing from the document", id)
		}
		if responseCodes(t, found["op"].(map[string]any))["403"] {
			t.Errorf("%s documents 403 but no authorization check exists", id)
		}
	}
}

// TestErrorResponsesAreDocumented checks the kratos error contract: every
// operation documents 400/500 and its error body, protected operations
// document 401, and public ones do not.
func TestErrorResponsesAreDocumented(t *testing.T) {
	document := loadDocument(t, filepath.Join(moduleRoot(t), "openapi.yaml"))
	schema := schemaOf(t, document, errorSchemaName)
	for _, field := range []string{"code", "reason", "message", "metadata"} {
		if _, ok := schema["properties"].(map[string]any)[field]; !ok {
			t.Errorf("%s is missing the %s property", errorSchemaName, field)
		}
		if !requiredFields(schema)[field] {
			t.Errorf("%s does not require %s", errorSchemaName, field)
		}
	}

	for path, item := range pathsOf(t, document) {
		for method, raw := range item {
			if method == "parameters" {
				continue
			}
			op := raw.(map[string]any)
			codes := responseCodes(t, op)
			for _, code := range []string{"400", "500"} {
				if !codes[code] {
					t.Errorf("%s %s: %s does not document %s", method, path, op["operationId"], code)
				}
			}
			// Public routes may still answer 401 for bad credentials (login,
			// refresh), but every protected route has to document it.
			public := false
			if security, ok := op["security"].([]any); ok && len(security) == 0 {
				public = true
			}
			if !public && !codes["401"] {
				t.Errorf("%s %s: %s needs a token but does not document 401", method, path, op["operationId"])
			}
		}
	}
}

// TestValidationKeywordsDocumented pins the buf.validate translation to a few
// concrete rules so a broken rule mapping cannot pass unnoticed.
func TestValidationKeywordsDocumented(t *testing.T) {
	document := loadDocument(t, filepath.Join(moduleRoot(t), "openapi.yaml"))

	register := schemaOf(t, document, "api.user.v1.RegisterRequest")
	password := propertyOf(t, register, "password")
	if password["minLength"] != 8 || password["maxLength"] != 72 {
		t.Errorf("RegisterRequest.password = %v, want minLength 8 and maxLength 72", password)
	}
	if !requiredFields(register)["password"] {
		t.Error("RegisterRequest should require password")
	}

	upload := schemaOf(t, document, "api.media.v1.CreateImageUploadRequest")
	contentType := propertyOf(t, upload, "contentType")
	if got := contentType["enum"]; got == nil {
		t.Error("CreateImageUploadRequest.contentType should list the accepted media types")
	}
	sizeBytes := propertyOf(t, upload, "sizeBytes")
	if sizeBytes["x-exclusiveMinimum"] != 0 || sizeBytes["x-maximum"] != 10485760 {
		t.Errorf("CreateImageUploadRequest.sizeBytes = %v, want x-exclusiveMinimum 0 and x-maximum 10485760", sizeBytes)
	}
	if !requiredFields(upload)["contentType"] || !requiredFields(upload)["sizeBytes"] {
		t.Error("CreateImageUploadRequest should require contentType and sizeBytes")
	}

	// id is filled in from the path, so the body must not require it.
	updatePost := schemaOf(t, document, "api.community.v1.UpdatePostRequest")
	required := requiredFields(updatePost)
	if required["id"] {
		t.Error("UpdatePostRequest.id comes from the path and must not be required in the body")
	}
	if !required["expectedVersion"] {
		t.Error("UpdatePostRequest should require expectedVersion")
	}
	imageIds := propertyOf(t, updatePost, "imageIds")
	if imageIds["maxItems"] != 9 || imageIds["uniqueItems"] != true {
		t.Errorf("UpdatePostRequest.imageIds = %v, want maxItems 9 and uniqueItems true", imageIds)
	}
	items := imageIds["items"].(map[string]any)
	if items["format"] != "int64" || items["x-exclusiveMinimum"] != 0 {
		t.Errorf("UpdatePostRequest.imageIds.items = %v, want int64 above 0", items)
	}

	op := operationOf(t, document, "/v1/users/me/browsing-history", "get")
	var pageSize map[string]any
	for _, raw := range op["parameters"].([]any) {
		param := raw.(map[string]any)
		if param["name"] == "pageSize" {
			pageSize = param["schema"].(map[string]any)
		}
	}
	if pageSize == nil || pageSize["maximum"] != 50 {
		t.Errorf("pageSize query parameter = %v, want maximum 50", pageSize)
	}
}

// TestInfoAndTagsAreFilledIn covers the metadata the generator leaves empty.
func TestInfoAndTagsAreFilledIn(t *testing.T) {
	document := loadDocument(t, filepath.Join(moduleRoot(t), "openapi.yaml"))
	info := document["info"].(map[string]any)
	if info["title"] == "" || info["title"] == nil {
		t.Error("info.title is empty")
	}
	if info["description"] == nil {
		t.Error("info.description is missing")
	}
	securitySchemes, ok := document["components"].(map[string]any)["securitySchemes"].(map[string]any)
	if !ok {
		t.Fatal("components.securitySchemes is missing")
	}
	if _, ok := securitySchemes["bearerAuth"]; !ok {
		t.Error("bearerAuth security scheme is missing")
	}
	tags := document["tags"].([]any)
	for _, raw := range tags {
		tag := raw.(map[string]any)
		if tag["description"] == nil || tag["description"] == "" {
			t.Errorf("tag %v has no description", tag["name"])
		}
	}
}

func TestHumanizeAndNormalize(t *testing.T) {
	for input, want := range map[string]string{
		"ListCategories":        "List categories",
		"GetMQJob":              "Get mq job",
		"GetTodayWellness":      "Get today wellness",
		"ListCommentReplies":    "List comment replies",
		"CreatePaymentCheckJob": "Create payment check job",
	} {
		if got := humanize(input); got != want {
			t.Errorf("humanize(%q) = %q, want %q", input, got, want)
		}
	}
	for input, want := range map[string]string{
		"/v1/posts/{postId}/comments":       "/v1/posts/{}/comments",
		"/v1/users/{userId}/addresses/{id}": "/v1/users/{}/addresses/{}",
		"/v1/payments/mq/jobs/{jobId}":      "/v1/payments/mq/jobs/{}",
		"/v1/categories":                    "/v1/categories",
	} {
		if got := normalizePathTemplate(input); got != want {
			t.Errorf("normalizePathTemplate(%q) = %q, want %q", input, got, want)
		}
	}
	if got := pathRenames("/v1/users/{userId}/addresses/{id}", "/v1/users/{user_id}/addresses/{id}"); got["userId"] != "user_id" || got["id"] != "id" {
		t.Errorf("pathRenames = %v, want userId -> user_id", got)
	}
}

// TestDocumentHasNoCamelCaseRouteVariables guards against a partial rename: the
// router uses snake_case variables, so a documented template that still carries
// a JSON style variable would not match the registered route.
func TestDocumentHasNoCamelCaseRouteVariables(t *testing.T) {
	document := loadDocument(t, filepath.Join(moduleRoot(t), "openapi.yaml"))
	camelVariable := regexp.MustCompile(`\{[a-z]+[A-Z][A-Za-z]*\}`)
	for path := range pathsOf(t, document) {
		if camelVariable.MatchString(path) {
			t.Errorf("path %s still uses a camelCase variable", path)
		}
	}
}
