package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"gopkg.in/yaml.v3"
)

// Name of the schema every error response points at. It is the kratos error
// body (errors.Status), which the HTTP server writes with the negotiated JSON
// codec: {"code":404,"reason":"PRODUCT_NOT_FOUND","message":"...","metadata":{}}.
const errorSchemaName = "errors.Status"

const (
	apiTitle     = "简单电商平台 API"
	apiServerURL = "http://localhost:8000"
	// apiDescription explains the parts of the document that are not a direct
	// translation of the protos, so readers know what they can trust.
	apiDescription = `用户、分类/商品/限时活动、下单与支付、图文社区、图片上传的统一 HTTP API。

本文件由 protoc-gen-openapi 从 api/**/*.proto 生成，再由 tools/openapi-doc 补全
（认证、错误响应、buf.validate 约束、手工注册的回调路由与 info 元数据），
因此与生成器的原始输出不同：请通过 make -C app/mall api 重新生成，不要手工编辑。

## 认证
注册、登录、刷新令牌和支付渠道回调不需要令牌；其余接口都必须携带
Authorization: Bearer <access_token>（HS256 JWT）。需要管理员角色的接口
（商品/分类/活动的写操作、退款、对账任务）以及只能操作自己名下资源的接口
（收货地址、订单、浏览记录）在权限不足时返回 403。

## 错误响应
失败时返回 kratos 统一错误体，HTTP 状态码即 body 的 code 字段：
{"code":404,"reason":"PRODUCT_NOT_FOUND","message":"...","metadata":{}}，
结构见 components.schemas.errors.Status。接口声明的 4xx/5xx 来自该服务
*_error.proto 的错误码集合，以及服务层中实际存在的鉴权分支，属于服务级上界。

## 64 位整数
protojson 把 int64/uint64 编码成 JSON 字符串，所以这类字段是 type: string
加 format: int64，buf.validate 的数值边界以 x-minimum / x-exclusiveMinimum /
x-maximum / x-exclusiveMaximum 给出。`
)

var errorDescriptions = map[int]string{
	400: "请求不合法：报文格式、参数校验或业务规则校验失败",
	401: "未认证：缺少、无效、已过期或已登出的访问令牌",
	403: "无权限：需要管理员角色，或只能操作自己名下的资源",
	404: "目标资源不存在或已被删除",
	408: "请求或资源已超时",
	409: "状态冲突：并发更新、重复提交、库存或支付状态不一致",
	429: "触发限流：操作过于频繁或登录失败次数过多",
	500: "服务内部错误",
	502: "上游渠道（支付网关）返回失败",
	503: "依赖不可用：对象存储、消息队列或第三方支付未配置",
	504: "上游渠道响应超时",
}

var tagDescriptions = map[string]string{
	"Mall":      "分类、商品、限时活动与今日养生卡片（写操作需要管理员角色）",
	"User":      "注册登录、令牌轮转、用户资料、收货地址与商品浏览记录",
	"Order":     "下单、订单查询与取消",
	"Payment":   "支付单、渠道查询/关单/退款、对账任务与支付渠道异步回调",
	"Community": "图文帖子、点赞与评论（含一层楼中楼）",
	"Media":     "图片上传（预签名直传）与资源查询",
}

type patchInput struct {
	model         *protoModel
	public        map[string]bool
	serviceStatus map[string][]int
	manual        []manualRouteDoc
}

// patchSpec rewrites the generated document in place. Every step is
// idempotent, so running the tool twice produces the same bytes.
func patchSpec(doc *yaml.Node, in patchInput) error {
	root := documentRoot(doc)
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("openapi document root is not a mapping")
	}
	if err := patchInfo(root); err != nil {
		return err
	}
	setAfter(root, "info", "servers", seqNode(mapNode(
		keyNode("url"), strNode(apiServerURL),
		keyNode("description"), strNode("本地开发默认地址（server.http.addr）"),
	)))
	patchSecurity(root)

	paths := get(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return fmt.Errorf("openapi document has no paths mapping")
	}
	if err := patchPaths(paths, in); err != nil {
		return err
	}
	if err := patchComponents(root, in); err != nil {
		return err
	}
	patchTags(root)
	return nil
}

func patchInfo(root *yaml.Node) error {
	info := get(root, "info")
	if info == nil {
		return fmt.Errorf("openapi document has no info block")
	}
	set(info, "title", strNode(apiTitle))
	setAfter(info, "title", "description", strNode(apiDescription))
	return nil
}

func patchSecurity(root *yaml.Node) {
	setAfter(root, "servers", "security", seqNode(mapNode(keyNode("bearerAuth"), seqNode())))

	components := get(root, "components")
	if components == nil {
		components = mapNode()
		set(root, "components", components)
	}
	setFirst(components, "securitySchemes", mapNode(
		keyNode("bearerAuth"), mapNode(
			keyNode("type"), strNode("http"),
			keyNode("scheme"), strNode("bearer"),
			keyNode("bearerFormat"), strNode("JWT"),
			keyNode("description"), strNode("访问令牌（HS256 JWT），登录/刷新接口返回，通过 Authorization: Bearer <token> 传递"),
		),
	))
}

func patchTags(root *yaml.Node) {
	tags := get(root, "tags")
	if tags == nil || tags.Kind != yaml.SequenceNode {
		return
	}
	for _, tag := range tags.Content {
		name := get(tag, "name")
		if name == nil {
			continue
		}
		description, ok := tagDescriptions[name.Value]
		if !ok {
			continue
		}
		set(tag, "description", strNode(description))
	}
}

// patchPaths renames the camelCase path variables of the generated document to
// the snake_case templates the router actually registers, adds the missing
// error responses and metadata to every operation, documents the manually
// registered routes and keeps the mapping sorted.
func patchPaths(paths *yaml.Node, in patchInput) error {
	manual := map[string]manualRouteDoc{}
	for _, doc := range in.manual {
		manual[doc.Path] = doc
	}
	templates, err := pathTemplates(in.model)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		key, item := paths.Content[i], paths.Content[i+1]
		if doc, ok := manual[key.Value]; ok {
			// Hand written routes are re-created from their documentation so a
			// second run cannot drift from it.
			paths.Content[i+1] = mapNode(keyNode(strings.ToLower(doc.Method)), doc.Operation())
			continue
		}
		template, ok := templates[normalizePathTemplate(key.Value)]
		if !ok {
			return fmt.Errorf("no RPC matches documented path %s; regenerate the document", key.Value)
		}
		if err := patchPathItem(item, pathRenames(key.Value, template), in); err != nil {
			return err
		}
		key.Value = template
	}
	for _, doc := range in.manual {
		if get(paths, doc.Path) != nil {
			continue
		}
		paths.Content = append(paths.Content,
			keyNode(doc.Path),
			mapNode(keyNode(strings.ToLower(doc.Method)), doc.Operation()),
		)
	}
	sortKeys(paths)
	return nil
}

// pathTemplates maps a normalized template (variables stripped) to the route
// template the server registers. Two routes that differ only by variable names
// would make the document ambiguous, so that is rejected.
func pathTemplates(model *protoModel) (map[string]string, error) {
	out := map[string]string{}
	for _, op := range model.operations {
		key := normalizePathTemplate(op.Path)
		if existing, ok := out[key]; ok && existing != op.Path {
			return nil, fmt.Errorf("routes %s and %s differ only by variable names", existing, op.Path)
		}
		out[key] = op.Path
	}
	return out, nil
}

// pathRenames maps the variables of a generated template onto the variables of
// the real route template, position by position.
func pathRenames(generated, real string) map[string]string {
	generatedVars := pathVariables(generated)
	realVars := pathVariables(real)
	renames := map[string]string{}
	for i := range generatedVars {
		if i >= len(realVars) {
			break
		}
		renames[generatedVars[i]] = realVars[i]
	}
	return renames
}

func pathVariables(path string) []string {
	var out []string
	for {
		start := strings.Index(path, "{")
		if start < 0 {
			return out
		}
		end := strings.Index(path[start:], "}")
		if end < 0 {
			return out
		}
		out = append(out, path[start+1:start+end])
		path = path[start+end+1:]
	}
}

// normalizePathTemplate replaces every path variable with an empty pair of
// braces, so two templates can be compared without depending on variable names.
func normalizePathTemplate(path string) string {
	var out strings.Builder
	for {
		start := strings.Index(path, "{")
		if start < 0 {
			out.WriteString(path)
			return out.String()
		}
		end := strings.Index(path[start:], "}")
		if end < 0 {
			out.WriteString(path)
			return out.String()
		}
		out.WriteString(path[:start])
		out.WriteString("{}")
		path = path[start+end+1:]
	}
}

func patchPathItem(item *yaml.Node, renames map[string]string, in patchInput) error {
	for i := 0; i+1 < len(item.Content); i += 2 {
		op := item.Content[i+1]
		id := get(op, "operationId")
		if id == nil {
			return fmt.Errorf("operation without operationId under a documented path")
		}
		operation, ok := in.model.byID[id.Value]
		if !ok {
			return fmt.Errorf("operation %s is not defined by any proto", id.Value)
		}
		if err := renamePathParams(op, renames); err != nil {
			return err
		}
		if err := patchParameters(op, operation.Request); err != nil {
			return err
		}
		setAfter(op, "tags", "summary", strNode(summaryFor(op, id.Value)))
		isPublic := in.public[id.Value]
		mergeResponses(op, errorStatuses(operation, in), isPublic)
		if isPublic {
			set(op, "security", seqNode())
		}
	}
	return nil
}

func renamePathParams(op *yaml.Node, renames map[string]string) error {
	params := get(op, "parameters")
	if params == nil {
		return nil
	}
	for _, param := range params.Content {
		in := get(param, "in")
		name := get(param, "name")
		if in == nil || name == nil || in.Value != "path" {
			continue
		}
		renamed, ok := renames[name.Value]
		if !ok {
			return fmt.Errorf("path parameter %s has no matching route variable", name.Value)
		}
		name.Value = renamed
	}
	return nil
}

// patchParameters merges the request field rules into the path and query
// parameters of an operation. The generator derives the parameters from the
// request message, so every documented parameter must resolve back to a field.
func patchParameters(op *yaml.Node, request protoreflect.MessageDescriptor) error {
	params := get(op, "parameters")
	if params == nil || request == nil {
		return nil
	}
	for _, param := range params.Content {
		name := get(param, "name")
		schema := get(param, "schema")
		if name == nil || schema == nil {
			continue
		}
		fd := requestField(request, name.Value)
		if fd == nil {
			return fmt.Errorf("%s: parameter %s does not match a request field", request.FullName(), name.Value)
		}
		rules := fieldRules(fd)
		applyPairs(schema, elementKeywords(fd.Kind(), elementRules(fd, rules)))
		if ruleForcesValue(fd, rules) {
			set(param, "required", boolNode(true))
		}
	}
	return nil
}

// requestField resolves a documented parameter back to its proto field, either
// by proto name (path variables, e.g. post_id) or by JSON name (query
// parameters, e.g. pageSize).
func requestField(md protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	if fd := md.Fields().ByName(protoreflect.Name(name)); fd != nil {
		return fd
	}
	return md.Fields().ByJSONName(name)
}

// errorStatuses is the set of failure codes an operation can answer with: 400
// and 500 always (codec/validation failures, internal errors), 401 whenever the
// route sits behind the JWT middleware, plus the codes the service declares in
// its *_error.proto and the ones its handlers raise themselves.
func errorStatuses(op operation, in patchInput) []int {
	codes := map[int]bool{400: true, 500: true}
	if !in.public[op.ID] {
		codes[401] = true
	}
	for _, code := range in.model.errorCodes[op.Package] {
		codes[code] = true
	}
	for _, code := range in.serviceStatus[op.ID] {
		codes[code] = true
	}
	out := make([]int, 0, len(codes))
	for code := range codes {
		out = append(out, code)
	}
	sort.Ints(out)
	return out
}

func mergeResponses(op *yaml.Node, codes []int, public bool) {
	responses := get(op, "responses")
	if responses == nil {
		responses = mapNode()
		set(op, "responses", responses)
	}
	for _, code := range codes {
		setResponse(responses, code, errorResponse(code, public))
	}
	sortResponses(responses)
}

// setResponse writes an error response, replacing an earlier version so that
// improved descriptions show up on regeneration.
func setResponse(responses *yaml.Node, code int, node *yaml.Node) {
	key := strconv.Itoa(code)
	for i := 0; i+1 < len(responses.Content); i += 2 {
		if responses.Content[i].Value == key {
			responses.Content[i+1] = node
			return
		}
	}
	responses.Content = append(responses.Content, quotedKeyNode(key), node)
}

func errorResponse(code int, public bool) *yaml.Node {
	description, ok := errorDescriptions[code]
	if !ok {
		description = fmt.Sprintf("请求失败（HTTP %d）", code)
	}
	if code == 401 && public {
		// Public routes never fail on a missing token; login and refresh report
		// invalid credentials with 401, so the wording stays neutral. The code
		// itself comes from the service error contract.
		description = "凭证校验失败：账号/密码不正确或令牌无效"
	}
	node := mapNode(keyNode("description"), strNode(description))
	node.Content = append(node.Content, jsonResponse(schemasRef(errorSchemaName)).Content...)
	return node
}

func sortResponses(responses *yaml.Node) {
	type pair struct {
		key  *yaml.Node
		val  *yaml.Node
		code int
	}
	pairs := make([]pair, 0, len(responses.Content)/2)
	for i := 0; i+1 < len(responses.Content); i += 2 {
		code, err := strconv.Atoi(responses.Content[i].Value)
		if err != nil {
			code = 1 << 30
		}
		pairs = append(pairs, pair{responses.Content[i], responses.Content[i+1], code})
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].code < pairs[j].code })
	responses.Content = responses.Content[:0]
	for _, p := range pairs {
		responses.Content = append(responses.Content, p.key, p.val)
	}
}

func patchComponents(root *yaml.Node, in patchInput) error {
	components := get(root, "components")
	if components == nil {
		return fmt.Errorf("openapi document has no components block")
	}
	schemas := get(components, "schemas")
	if schemas == nil || schemas.Kind != yaml.MappingNode {
		return fmt.Errorf("openapi document has no components.schemas mapping")
	}
	if err := patchSchemas(schemas, in.model); err != nil {
		return err
	}
	set(schemas, errorSchemaName, errorSchema())
	sortKeys(schemas)
	return nil
}

// patchSchemas merges the buf.validate rules of every generated schema. It is
// strict on purpose: a schema that no longer matches a proto message means the
// document is stale and must be regenerated.
func patchSchemas(schemas *yaml.Node, model *protoModel) error {
	for i := 0; i+1 < len(schemas.Content); i += 2 {
		key, schema := schemas.Content[i], schemas.Content[i+1]
		if key.Value == errorSchemaName {
			continue
		}
		md, ok := model.messages[protoreflect.FullName(key.Value)]
		if !ok {
			return fmt.Errorf("schema %s does not match any proto message", key.Value)
		}
		props := get(schema, "properties")
		if props == nil {
			continue
		}
		var required []*yaml.Node
		for _, patch := range schemaPatchesFor(md) {
			prop := get(props, patch.Field)
			if prop == nil {
				return fmt.Errorf("%s: schema %s has no property %s", md.FullName(), key.Value, patch.Field)
			}
			applyPairs(prop, patch.Pairs)
			if len(patch.Elements) > 0 {
				element := get(prop, "items")
				if element == nil {
					element = get(prop, "additionalProperties")
				}
				if element == nil {
					return fmt.Errorf("%s.%s: no items/additionalProperties to constrain", md.FullName(), patch.Field)
				}
				applyPairs(element, patch.Elements)
			}
			if patch.Required && model.requestMessages[md.FullName()] && !model.pathBoundFields[md.FullName()][patch.Field] {
				required = append(required, strNode(patch.Field))
			}
		}
		if len(required) > 0 {
			set(schema, "required", seqNode(required...))
		}
	}
	return nil
}

func applyPairs(target *yaml.Node, pairs []*yaml.Node) {
	for i := 0; i+1 < len(pairs); i += 2 {
		set(target, pairs[i].Value, pairs[i+1])
	}
}

func errorSchema() *yaml.Node {
	return mapNode(
		keyNode("type"), strNode("object"),
		keyNode("description"), strNode("kratos 统一错误体，HTTP 状态码等于 code"),
		keyNode("properties"), mapNode(
			keyNode("code"), mapNode(
				keyNode("type"), strNode("integer"),
				keyNode("format"), strNode("int32"),
				keyNode("description"), strNode("HTTP 状态码"),
			),
			keyNode("reason"), mapNode(
				keyNode("type"), strNode("string"),
				keyNode("description"), strNode("机器可读的错误原因，如 PRODUCT_NOT_FOUND"),
			),
			keyNode("message"), mapNode(
				keyNode("type"), strNode("string"),
				keyNode("description"), strNode("人类可读的错误说明"),
			),
			keyNode("metadata"), mapNode(
				keyNode("type"), strNode("object"),
				keyNode("additionalProperties"), mapNode(keyNode("type"), strNode("string")),
			),
		),
		keyNode("required"), seqNode(strNode("code"), strNode("reason"), strNode("message"), strNode("metadata")),
	)
}

// summaryFor prefers the proto comment the generator already copied into
// description, and falls back to the RPC name.
func summaryFor(op *yaml.Node, operationID string) string {
	if description := get(op, "description"); description != nil && description.Kind == yaml.ScalarNode {
		if summary := firstSentence(description.Value); summary != "" {
			return summary
		}
	}
	_, method, found := strings.Cut(operationID, "_")
	if !found {
		method = operationID
	}
	return humanize(method)
}

func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	if line, _, found := strings.Cut(text, "\n"); found {
		text = strings.TrimSpace(line)
	}
	if end := strings.Index(text, "。"); end >= 0 {
		text = text[:end+len("。")]
	}
	if len(text) > 160 {
		text = strings.TrimSpace(text[:160]) + "…"
	}
	return text
}

// humanize turns an RPC name such as ListCommentReplies into "List comment
// replies", splitting on camelCase word boundaries.
func humanize(name string) string {
	var words []string
	start := 0
	for i := 1; i <= len(name); i++ {
		if i == len(name) {
			words = append(words, name[start:i])
			break
		}
		if name[i] < 'A' || name[i] > 'Z' {
			continue
		}
		prevUpper := name[i-1] >= 'A' && name[i-1] <= 'Z'
		nextLower := i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z'
		if !prevUpper || nextLower {
			words = append(words, name[start:i])
			start = i
		}
	}
	if len(words) == 0 {
		return name
	}
	words[0] = strings.ToUpper(words[0][:1]) + strings.ToLower(words[0][1:])
	for i := 1; i < len(words); i++ {
		words[i] = strings.ToLower(words[i])
	}
	return strings.Join(words, " ")
}
