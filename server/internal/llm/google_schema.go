package llm

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxGeminiSchemaDepth = 64
	maxGeminiSchemaNodes = 4096
)

type geminiSchemaNormalizer struct {
	root      map[string]any
	resolving map[string]bool
	remaining int
}

// normalizeGeminiFunctionSchema converts a general JSON Schema (including the
// schemas commonly returned by MCP servers) into the subset accepted by
// Gemini's FunctionDeclaration.parameters protobuf. The source schema is never
// mutated, so providers with broader JSON Schema support still receive it in
// full.
func normalizeGeminiFunctionSchema(raw json.RawMessage) map[string]any {
	root := decodeJSONSchema(raw)
	if root == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}

	normalizer := geminiSchemaNormalizer{
		root:      root,
		resolving: map[string]bool{},
		remaining: maxGeminiSchemaNodes,
	}
	normalized := normalizer.normalizeNode(root, true, false, 0)
	if normalized == nil {
		normalized = map[string]any{}
	}
	normalized["type"] = "object"
	if _, ok := normalized["properties"]; !ok {
		normalized["properties"] = map[string]any{}
	}
	filterGeminiRequired(normalized)
	return normalized
}

func decodeJSONSchema(raw json.RawMessage) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil
	}
	root, _ := decoded.(map[string]any)
	return root
}

func (n *geminiSchemaNormalizer) normalizeNode(node map[string]any, rootNode, deferNameFiltering bool, depth int) map[string]any {
	if node == nil {
		return nil
	}
	if depth > maxGeminiSchemaDepth {
		return map[string]any{"description": "This deeply nested parameter schema was simplified for Gemini compatibility."}
	}
	if n.remaining <= 0 {
		return map[string]any{"description": "This parameter schema was simplified after reaching Gemini's schema complexity limit."}
	}
	n.remaining--

	out := map[string]any{}
	if ref, ok := node["$ref"].(string); ok {
		if strings.HasPrefix(ref, "#") && !n.resolving[ref] {
			if target := resolveLocalJSONSchemaRef(n.root, ref); target != nil {
				n.resolving[ref] = true
				mergeGeminiSchema(out, n.normalizeNode(target, rootNode, true, depth+1))
				delete(n.resolving, ref)
			} else {
				appendGeminiDescription(out, "A local schema reference could not be resolved; accept the documented value for this parameter.")
			}
		} else if strings.HasPrefix(ref, "#") {
			appendGeminiDescription(out, "A recursive schema reference was simplified for Gemini compatibility.")
		} else {
			appendGeminiDescription(out, "An external schema reference was simplified because Gemini cannot load external definitions.")
		}
	}

	if branches, ok := node["allOf"].([]any); ok {
		for _, branch := range branches {
			if schema, ok := branch.(map[string]any); ok {
				mergeGeminiSchema(out, n.normalizeNode(schema, rootNode, true, depth+1))
			}
		}
	}

	copyGeminiSchemaString(node, out, "title")
	copyGeminiSchemaString(node, out, "description")
	copyGeminiSchemaString(node, out, "format")
	copyGeminiSchemaString(node, out, "pattern")
	copyGeminiSchemaBool(node, out, "nullable")
	copyGeminiSchemaAny(node, out, "default")
	copyGeminiSchemaAny(node, out, "example")
	copyGeminiSchemaNumber(node, out, "minimum")
	copyGeminiSchemaNumber(node, out, "maximum")
	copyGeminiSchemaInt64(node, out, "minItems")
	copyGeminiSchemaInt64(node, out, "maxItems")
	copyGeminiSchemaInt64(node, out, "minLength")
	copyGeminiSchemaInt64(node, out, "maxLength")
	copyGeminiSchemaInt64(node, out, "minProperties")
	copyGeminiSchemaInt64(node, out, "maxProperties")

	if _, exists := out["example"]; !exists {
		if examples, ok := node["examples"].([]any); ok && len(examples) > 0 {
			out["example"] = examples[0]
		}
	}

	normalizeGeminiSchemaType(node["type"], out)
	if rootNode {
		out["type"] = "object"
	}

	normalizeGeminiEnumAndConst(node, out)

	if additional, exists := node["additionalProperties"]; exists {
		switch value := additional.(type) {
		case bool:
			if value {
				appendGeminiDescription(out, "Additional object properties are allowed.")
			}
		case map[string]any:
			normalized := n.normalizeNode(value, false, false, depth+1)
			appendGeminiConstraint(out, "Additional object-property values should follow", normalized)
		}
	}

	if properties, ok := node["properties"].(map[string]any); ok {
		normalizedProperties := make(map[string]any, len(properties))
		for name, value := range properties {
			schema := jsonSchemaObject(value)
			if schema == nil {
				continue
			}
			normalized := n.normalizeNode(schema, false, false, depth+1)
			if normalized == nil {
				normalized = map[string]any{}
			}
			ensureGeminiSchemaShape(normalized, false)
			normalizedProperties[name] = normalized
		}
		mergeGeminiSchema(out, map[string]any{"properties": normalizedProperties})
		if _, exists := out["type"]; !exists {
			out["type"] = "object"
		}
	}

	switch items := node["items"].(type) {
	case map[string]any:
		normalized := n.normalizeNode(items, false, false, depth+1)
		if normalized == nil {
			normalized = map[string]any{}
		}
		ensureGeminiSchemaShape(normalized, false)
		out["items"] = normalized
		if _, exists := out["type"]; !exists {
			out["type"] = "array"
		}
	case []any:
		branches := make([]any, 0, len(items))
		for _, item := range items {
			schema := jsonSchemaObject(item)
			if schema == nil {
				continue
			}
			normalized := n.normalizeNode(schema, false, false, depth+1)
			if normalized == nil {
				normalized = map[string]any{}
			}
			ensureGeminiSchemaShape(normalized, false)
			branches = append(branches, normalized)
		}
		if len(branches) > 0 {
			out["items"] = map[string]any{"anyOf": branches}
			out["type"] = "array"
			appendGeminiConstraint(out, "Tuple item positions should follow", branches)
		}
	}
	n.normalizeTupleItems(node["prefixItems"], out, depth)
	if items, isBool := node["items"].(bool); isBool && !items {
		appendGeminiDescription(out, "The original schema did not allow items beyond the documented tuple positions.")
	}

	branches := node["anyOf"]
	if branches == nil {
		branches = node["oneOf"]
	}
	if list, ok := branches.([]any); ok {
		normalizedBranches := make([]any, 0, len(list))
		for _, branch := range list {
			schema := jsonSchemaObject(branch)
			if schema == nil {
				continue
			}
			normalized := n.normalizeNode(schema, false, false, depth+1)
			if len(normalized) > 0 {
				normalizedBranches = append(normalizedBranches, normalized)
			}
		}
		if len(normalizedBranches) > 0 {
			out["anyOf"] = normalizedBranches
		}
	}

	if ordering, ok := stringSlice(node["propertyOrdering"]); ok {
		out["propertyOrdering"] = ordering
	}
	if required, ok := stringSlice(node["required"]); ok {
		mergeGeminiSchema(out, map[string]any{"required": required})
	}
	if !deferNameFiltering {
		filterGeminiRequired(out)
		filterGeminiPropertyOrdering(out)
	}
	ensureGeminiSchemaShape(out, rootNode)
	return out
}

func copyGeminiSchemaString(source, target map[string]any, key string) {
	if value, ok := source[key].(string); ok {
		target[key] = value
	}
}

func copyGeminiSchemaBool(source, target map[string]any, key string) {
	if value, ok := source[key].(bool); ok {
		target[key] = value
	}
}

func copyGeminiSchemaAny(source, target map[string]any, key string) {
	if value, ok := source[key]; ok {
		target[key] = value
	}
}

func copyGeminiSchemaNumber(source, target map[string]any, key string) {
	if value, ok := source[key].(json.Number); ok {
		if _, err := strconv.ParseFloat(value.String(), 64); err == nil {
			target[key] = value
		}
	}
}

func copyGeminiSchemaInt64(source, target map[string]any, key string) {
	value, ok := source[key]
	if !ok {
		return
	}
	if encoded, ok := geminiInt64String(value); ok {
		target[key] = encoded
	}
}

func geminiInt64String(value any) (string, bool) {
	text := ""
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = typed
	default:
		return "", false
	}
	rational, ok := new(big.Rat).SetString(text)
	if !ok || rational.Sign() < 0 || !rational.IsInt() {
		return "", false
	}
	maxInt64 := new(big.Int).SetUint64(^uint64(0) >> 1)
	if rational.Num().Cmp(maxInt64) > 0 {
		return "", false
	}
	return rational.Num().String(), true
}

func normalizeGeminiSchemaType(value any, target map[string]any) {
	switch typed := value.(type) {
	case string:
		if schemaType := geminiSchemaType(typed); schemaType != "" {
			target["type"] = schemaType
		}
	case []any:
		types := make([]string, 0, len(typed))
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				continue
			}
			if strings.EqualFold(name, "null") {
				target["nullable"] = true
				continue
			}
			if schemaType := geminiSchemaType(name); schemaType != "" {
				types = append(types, schemaType)
			}
		}
		if len(types) == 1 {
			target["type"] = types[0]
		} else if len(types) > 1 {
			branches := make([]any, 0, len(types))
			for _, schemaType := range types {
				branches = append(branches, map[string]any{"type": schemaType})
			}
			target["anyOf"] = branches
		}
	}
}

func geminiSchemaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "object", "array", "string", "number", "integer", "boolean":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeGeminiEnumAndConst(source, target map[string]any) {
	if value, exists := source["const"]; exists {
		normalizeGeminiAllowedValues(target, []any{value}, "Required constant value")
		return
	}
	values, exists := source["enum"].([]any)
	if !exists || len(values) == 0 {
		return
	}
	normalizeGeminiAllowedValues(target, values, "Allowed values")
}

func normalizeGeminiAllowedValues(target map[string]any, values []any, label string) {
	schemaType, nullable, homogeneous := inferGeminiValueType(values)
	if nullable {
		target["nullable"] = true
	}
	if homogeneous && schemaType != "" && canNarrowGeminiSchemaType(target, schemaType) {
		target["type"] = schemaType
		delete(target, "anyOf")
	}
	if schemaType == "string" && target["type"] == "string" {
		enum := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				enum = append(enum, text)
			}
		}
		if len(enum) > 0 {
			target["enum"] = enum
			return
		}
	}
	appendGeminiConstraint(target, label, valuesForConstraint(values, label))
}

func inferGeminiValueType(values []any) (string, bool, bool) {
	inferred := ""
	nullable := false
	nonNull := 0
	for _, value := range values {
		if value == nil {
			nullable = true
			continue
		}
		nonNull++
		valueType := geminiValueType(value)
		if valueType == "" {
			return "", nullable, false
		}
		if inferred == "" {
			inferred = valueType
			continue
		}
		if (inferred == "integer" && valueType == "number") || (inferred == "number" && valueType == "integer") {
			inferred = "number"
			continue
		}
		if inferred != valueType {
			return "", nullable, false
		}
	}
	return inferred, nullable, nonNull > 0
}

func geminiValueType(value any) string {
	switch typed := value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if rational, ok := new(big.Rat).SetString(typed.String()); ok && rational.IsInt() {
			return "integer"
		}
		return "number"
	default:
		return ""
	}
}

func canNarrowGeminiSchemaType(schema map[string]any, inferred string) bool {
	if current, ok := schema["type"].(string); ok {
		return current == inferred || (current == "number" && inferred == "integer")
	}
	branches, ok := schema["anyOf"].([]any)
	if !ok || len(branches) == 0 {
		return true
	}
	for _, branch := range branches {
		candidate, ok := branch.(map[string]any)
		if !ok {
			continue
		}
		branchType, _ := candidate["type"].(string)
		if branchType == inferred || (branchType == "number" && inferred == "integer") {
			return true
		}
	}
	return false
}

func valuesForConstraint(values []any, label string) any {
	if label == "Required constant value" && len(values) == 1 {
		return values[0]
	}
	return values
}

func (n *geminiSchemaNormalizer) normalizeTupleItems(value any, target map[string]any, depth int) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return
	}
	branches := make([]any, 0, len(items)+1)
	for _, item := range items {
		schema := jsonSchemaObject(item)
		if schema == nil {
			continue
		}
		normalized := n.normalizeNode(schema, false, false, depth+1)
		if normalized == nil {
			normalized = map[string]any{}
		}
		ensureGeminiSchemaShape(normalized, false)
		branches = append(branches, normalized)
	}
	tupleBranches := append([]any(nil), branches...)
	if existing, ok := target["items"].(map[string]any); ok {
		if existingAnyOf, ok := existing["anyOf"].([]any); ok {
			branches = append(branches, existingAnyOf...)
		} else {
			branches = append(branches, existing)
		}
	}
	if len(branches) == 0 {
		return
	}
	target["type"] = "array"
	target["items"] = map[string]any{"anyOf": branches}
	if len(tupleBranches) > 0 {
		appendGeminiConstraint(target, "Tuple item positions should follow", tupleBranches)
	}
}

func stringSlice(value any) ([]string, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if ok {
			out = append(out, text)
		}
	}
	return out, true
}

func filterGeminiRequired(schema map[string]any) {
	required, ok := schema["required"].([]string)
	if !ok {
		return
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		delete(schema, "required")
		return
	}
	filtered := required[:0]
	for _, name := range required {
		if _, exists := properties[name]; exists {
			filtered = append(filtered, name)
		}
	}
	if len(filtered) == 0 {
		delete(schema, "required")
		return
	}
	schema["required"] = filtered
}

func filterGeminiPropertyOrdering(schema map[string]any) {
	ordering, ok := schema["propertyOrdering"].([]string)
	if !ok {
		return
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		delete(schema, "propertyOrdering")
		return
	}
	filtered := ordering[:0]
	for _, name := range ordering {
		if _, exists := properties[name]; exists {
			filtered = append(filtered, name)
		}
	}
	if len(filtered) == 0 {
		delete(schema, "propertyOrdering")
		return
	}
	schema["propertyOrdering"] = filtered
}

func ensureGeminiSchemaShape(schema map[string]any, rootNode bool) {
	if schema == nil {
		return
	}
	if rootNode {
		schema["type"] = "object"
		return
	}
	if schemaType, hasType := schema["type"].(string); hasType {
		if schemaType == "array" {
			if _, hasItems := schema["items"]; !hasItems {
				schema["items"] = map[string]any{"type": "string"}
			}
		}
		return
	}
	if branches, hasAnyOf := schema["anyOf"].([]any); hasAnyOf && len(branches) > 0 {
		return
	}
	if _, hasProperties := schema["properties"]; hasProperties {
		schema["type"] = "object"
		return
	}
	if _, hasItems := schema["items"]; hasItems {
		schema["type"] = "array"
		return
	}
	schema["type"] = "string"
}

func jsonSchemaObject(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case bool:
		if typed {
			return map[string]any{}
		}
		return map[string]any{"description": "The original JSON Schema disallowed every value; this constraint was simplified for Gemini compatibility."}
	default:
		return nil
	}
}

func appendGeminiDescription(schema map[string]any, addition string) {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return
	}
	current, _ := schema["description"].(string)
	current = strings.TrimSpace(current)
	if current == "" {
		schema["description"] = addition
		return
	}
	schema["description"] = current + " " + addition
}

func appendGeminiConstraint(schema map[string]any, label string, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	const maxConstraintBytes = 512
	if len(encoded) > maxConstraintBytes {
		encoded = append(append([]byte(nil), encoded[:maxConstraintBytes]...), '.', '.', '.')
	}
	appendGeminiDescription(schema, strings.TrimSpace(label)+": "+string(encoded)+".")
}

func resolveLocalJSONSchemaRef(root map[string]any, ref string) map[string]any {
	if ref == "#" {
		return root
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var current any = root
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		decoded, err := url.PathUnescape(token)
		if err != nil {
			return nil
		}
		token = decoded
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[token]
			if !ok {
				return nil
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) {
				return nil
			}
			current = value[index]
		default:
			return nil
		}
	}
	resolved, _ := current.(map[string]any)
	return resolved
}

func mergeGeminiSchema(target, source map[string]any) {
	for key, value := range source {
		if key == "properties" {
			sourceProperties, sourceOK := value.(map[string]any)
			targetProperties, targetOK := target[key].(map[string]any)
			if sourceOK && targetOK {
				for name, property := range sourceProperties {
					if sourceProperty, sourceIsSchema := property.(map[string]any); sourceIsSchema {
						if targetProperty, targetIsSchema := targetProperties[name].(map[string]any); targetIsSchema {
							mergeGeminiSchema(targetProperty, sourceProperty)
							continue
						}
					}
					targetProperties[name] = property
				}
				continue
			}
		}
		if key == "required" {
			sourceRequired, sourceOK := value.([]string)
			targetRequired, targetOK := target[key].([]string)
			if sourceOK && targetOK {
				seen := make(map[string]bool, len(targetRequired)+len(sourceRequired))
				merged := make([]string, 0, len(targetRequired)+len(sourceRequired))
				for _, name := range append(targetRequired, sourceRequired...) {
					if !seen[name] {
						seen[name] = true
						merged = append(merged, name)
					}
				}
				target[key] = merged
				continue
			}
		}
		target[key] = value
	}
}
