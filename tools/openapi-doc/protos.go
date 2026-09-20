package main

import (
	"fmt"
	"sort"
	"strings"

	_ "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	_ "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	_ "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	_ "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	_ "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	_ "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	annotations "google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// protoModel is everything the patch step needs from the protobuf definitions.
// Descriptors come from the generated Go packages (imported for their side
// effect of registering themselves), so the tool never has to re-run protoc.
type protoModel struct {
	operations []operation
	byID       map[string]operation
	// byRPCPackageMethod maps "<proto package>.<rpc method>" to an operation,
	// which is how the service-layer scan attaches itself to operations.
	byRPCPackageMethod map[string]string
	messages           map[protoreflect.FullName]protoreflect.MessageDescriptor
	requestMessages    map[protoreflect.FullName]bool
	// pathBoundFields lists the request fields that the router fills in from
	// the URL. They never have to appear in the request body, so they are not
	// marked required there.
	pathBoundFields map[protoreflect.FullName]map[string]bool
	errorCodes      map[string][]int
}

type operation struct {
	ID         string // Mall_ListCategories, matches protoc-gen-go-http
	Service    string // Mall
	Method     string // ListCategories
	Package    string // api.mall.v1
	HTTPMethod string // GET
	Path       string // /v1/categories
	HasBody    bool
	Request    protoreflect.MessageDescriptor
}

func (o operation) key() string { return o.Package + "." + o.Method }

func loadProtoModel() (*protoModel, error) {
	m := &protoModel{
		byID:               map[string]operation{},
		byRPCPackageMethod: map[string]string{},
		messages:           map[protoreflect.FullName]protoreflect.MessageDescriptor{},
		requestMessages:    map[protoreflect.FullName]bool{},
		pathBoundFields:    map[protoreflect.FullName]map[string]bool{},
		errorCodes:         map[string][]int{},
	}
	var loadErr error
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		pkg := string(fd.Package())
		if !strings.HasPrefix(pkg, "api.") {
			return true
		}
		m.collectMessages(fd)
		m.collectErrorCodes(fd)
		if err := m.collectOperations(fd); err != nil {
			loadErr = err
			return false
		}
		return true
	})
	if loadErr != nil {
		return nil, loadErr
	}
	if len(m.operations) == 0 {
		return nil, fmt.Errorf("no HTTP-mapped RPCs found in the api/* packages")
	}
	sort.Slice(m.operations, func(i, j int) bool { return m.operations[i].ID < m.operations[j].ID })
	for _, op := range m.operations {
		m.byID[op.ID] = op
		m.byRPCPackageMethod[op.key()] = op.ID
		m.markRequestMessages(op.Request)
		m.markPathBoundFields(op)
	}
	for pkg, codes := range m.errorCodes {
		sort.Ints(codes)
		m.errorCodes[pkg] = dedupeInts(codes)
	}
	return m, nil
}

// markPathBoundFields records which request fields the route template carries
// as variables (e.g. post_id in /v1/posts/{post_id}/comments).
func (m *protoModel) markPathBoundFields(op operation) {
	for _, variable := range pathVariables(op.Path) {
		fd := requestField(op.Request, variable)
		if fd == nil {
			continue
		}
		if m.pathBoundFields[op.Request.FullName()] == nil {
			m.pathBoundFields[op.Request.FullName()] = map[string]bool{}
		}
		m.pathBoundFields[op.Request.FullName()][fd.JSONName()] = true
	}
}

func (m *protoModel) collectMessages(fd protoreflect.FileDescriptor) {
	messages := fd.Messages()
	for i := range messages.Len() {
		m.collectMessage(messages.Get(i))
	}
}

func (m *protoModel) collectMessage(md protoreflect.MessageDescriptor) {
	m.messages[md.FullName()] = md
	nested := md.Messages()
	for i := range nested.Len() {
		m.collectMessage(nested.Get(i))
	}
}

// collectErrorCodes records the HTTP status codes an api package declares in
// its *_error.proto enum. The enum is the service-level error contract: the biz
// layer returns those reasons, so every operation of the service can produce
// one of them.
func (m *protoModel) collectErrorCodes(fd protoreflect.FileDescriptor) {
	pkg := string(fd.Package())
	enums := fd.Enums()
	for i := range enums.Len() {
		enum := enums.Get(i)
		if opts := enum.Options(); opts != nil {
			if code, ok := proto.GetExtension(opts, kratoserrors.E_DefaultCode).(int32); ok && code > 0 {
				m.addErrorCode(pkg, int(code))
			}
		}
		values := enum.Values()
		for j := range values.Len() {
			opts := values.Get(j).Options()
			if opts == nil {
				continue
			}
			code, ok := proto.GetExtension(opts, kratoserrors.E_Code).(int32)
			if !ok || code <= 0 {
				continue
			}
			m.addErrorCode(pkg, int(code))
		}
	}
}

func (m *protoModel) addErrorCode(pkg string, code int) {
	for _, existing := range m.errorCodes[pkg] {
		if existing == code {
			return
		}
	}
	m.errorCodes[pkg] = append(m.errorCodes[pkg], code)
}

func (m *protoModel) collectOperations(fd protoreflect.FileDescriptor) error {
	services := fd.Services()
	for i := range services.Len() {
		svc := services.Get(i)
		methods := svc.Methods()
		for j := range methods.Len() {
			method := methods.Get(j)
			httpMethod, path, body, ok := httpRule(method)
			if !ok {
				// No google.api.http rule means protoc-gen-go-http did not
				// route it either; the document does not need it.
				continue
			}
			op := operation{
				ID:         string(svc.Name()) + "_" + string(method.Name()),
				Service:    string(svc.Name()),
				Method:     string(method.Name()),
				Package:    string(fd.Package()),
				HTTPMethod: httpMethod,
				Path:       path,
				HasBody:    body != "",
				Request:    method.Input(),
			}
			if _, dup := m.byID[op.ID]; dup {
				return fmt.Errorf("duplicate operation id %s", op.ID)
			}
			m.operations = append(m.operations, op)
		}
	}
	return nil
}

func httpRule(method protoreflect.MethodDescriptor) (httpMethod, path, body string, ok bool) {
	opts := method.Options()
	if opts == nil {
		return "", "", "", false
	}
	rule, isRule := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	if !isRule || rule == nil {
		return "", "", "", false
	}
	switch pattern := rule.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		return "GET", pattern.Get, rule.GetBody(), true
	case *annotations.HttpRule_Post:
		return "POST", pattern.Post, rule.GetBody(), true
	case *annotations.HttpRule_Put:
		return "PUT", pattern.Put, rule.GetBody(), true
	case *annotations.HttpRule_Patch:
		return "PATCH", pattern.Patch, rule.GetBody(), true
	case *annotations.HttpRule_Delete:
		return "DELETE", pattern.Delete, rule.GetBody(), true
	default:
		// Custom verbs have no OpenAPI representation.
		return "", "", "", false
	}
}

// markRequestMessages records every message reachable from a request, i.e. the
// schemas where a buf.validate rule really means "the caller must send this".
func (m *protoModel) markRequestMessages(md protoreflect.MessageDescriptor) {
	if md == nil || m.requestMessages[md.FullName()] {
		return
	}
	m.requestMessages[md.FullName()] = true
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.IsMap() {
			m.markRequestMessages(fd.MapValue().Message())
			continue
		}
		m.markRequestMessages(fd.Message())
	}
}

func dedupeInts(codes []int) []int {
	if len(codes) == 0 {
		return codes
	}
	out := codes[:1]
	for _, code := range codes[1:] {
		if code != out[len(out)-1] {
			out = append(out, code)
		}
	}
	return out
}
