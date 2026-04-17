package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// validateToolArgs validates args against a JSON Schema-like map.
// schema is expected to have optional keys: "properties", "required", "additionalProperties".
func validateToolArgs(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	if args == nil {
		args = map[string]any{}
	}

	if err := checkRequired(schema, args); err != nil {
		return err
	}

	propsRaw, ok := schema["properties"]
	if !ok {
		return nil // no properties defined — accept any args
	}

	props, ok := propsRaw.(map[string]any)
	if !ok {
		return nil
	}

	additional := allowsAdditional(schema)

	for key, val := range args {
		propSchemaRaw, known := props[key]
		if !known {
			if !additional {
				return fmt.Errorf("unexpected property %q", key)
			}
			continue
		}
		propSchema, ok := propSchemaRaw.(map[string]any)
		if !ok {
			continue // can't validate without a proper schema map
		}
		if err := checkType(key, val, propSchema); err != nil {
			return err
		}
	}

	return nil
}

// prepareToolArgsForValidation repairs common model formatting mistakes before
// strict schema validation. It is intentionally conservative: it only unwraps a
// nested JSON object when required top-level fields are missing.
//
// Some tools (notably exec) should never auto-repair concatenated JSON objects:
// a malformed payload like {"path":"..."}{"command":"rm ..."} likely means
// the model attempted to emit multiple tool calls in one function invocation.
// For those tools we fail closed instead of guessing.
func prepareToolArgsForValidation(toolName string, schema map[string]any, args map[string]any) (map[string]any, bool, error) {
	if args == nil {
		return map[string]any{}, false, nil
	}

	missing := missingRequiredFields(schema, args)
	if len(missing) == 0 {
		return args, false, nil
	}

	props, _ := schema["properties"].(map[string]any)
	additional := allowsAdditional(schema)

	type candidate struct {
		sourceKey string
		payload   string
	}

	candidates := make([]candidate, 0, len(args)+1)
	if raw, ok := args["raw"].(string); ok {
		candidates = append(candidates, candidate{sourceKey: "raw", payload: raw})
	}
	for key, val := range args {
		strVal, ok := val.(string)
		if !ok || !looksLikeJSONObjectString(strVal) {
			continue
		}
		candidates = append(candidates, candidate{sourceKey: key, payload: strVal})
	}

	for _, cand := range candidates {
		parsedCandidates := parseJSONObjectCandidates(cand.payload)
		if len(parsedCandidates) > 1 && disallowConcatenatedJSONAutoRepair(toolName) {
			return args, false, fmt.Errorf(
				"multiple concatenated JSON objects detected for tool %q; send exactly one tool payload per call",
				toolName,
			)
		}

		for _, parsed := range parsedCandidates {
			merged := make(map[string]any, len(parsed)+len(args))
			for k, v := range parsed {
				merged[k] = v
			}
			for k, v := range args {
				if k == cand.sourceKey || k == "raw" {
					continue
				}
				merged[k] = v
			}

			if !additional && len(props) > 0 && hasUnknownProperties(merged, props) {
				continue
			}
			if len(missingRequiredFields(schema, merged)) > 0 {
				continue
			}
			return merged, true, nil
		}
	}

	return args, false, nil
}

func disallowConcatenatedJSONAutoRepair(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "exec":
		return true
	default:
		return false
	}
}

// checkRequired verifies that every field listed in schema["required"] is present in args.
func checkRequired(schema map[string]any, args map[string]any) error {
	reqRaw, ok := schema["required"]
	if !ok {
		return nil
	}

	var required []string

	switch r := reqRaw.(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			s, ok := v.(string)
			if ok {
				required = append(required, s)
			}
		}
	default:
		return nil
	}

	for _, field := range required {
		if _, present := args[field]; !present {
			return fmt.Errorf("missing required property %q", field)
		}
	}
	return nil
}

func missingRequiredFields(schema map[string]any, args map[string]any) []string {
	reqRaw, ok := schema["required"]
	if !ok {
		return nil
	}

	var required []string
	switch r := reqRaw.(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			if s, ok := v.(string); ok {
				required = append(required, s)
			}
		}
	default:
		return nil
	}

	missing := make([]string, 0, len(required))
	for _, field := range required {
		if _, present := args[field]; !present {
			missing = append(missing, field)
		}
	}
	return missing
}

// allowsAdditional returns true when the schema explicitly sets
// "additionalProperties" to true, or when the key is absent (default: reject extras).
func allowsAdditional(schema map[string]any) bool {
	v, ok := schema["additionalProperties"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func looksLikeJSONObjectString(s string) bool {
	trimmed := strings.TrimSpace(s)
	return strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")
}

func parseJSONObjectString(s string) (map[string]any, bool) {
	if !looksLikeJSONObjectString(s) {
		return nil, false
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil || parsed == nil {
		return nil, false
	}
	return parsed, true
}

func parseJSONObjectCandidates(s string) []map[string]any {
	if parsed, ok := parseJSONObjectString(s); ok {
		return []map[string]any{parsed}
	}

	trimmed := strings.TrimSpace(s)
	if !looksLikeJSONObjectString(trimmed) {
		return nil
	}

	dec := json.NewDecoder(strings.NewReader(trimmed))
	var out []map[string]any
	for {
		var parsed map[string]any
		if err := dec.Decode(&parsed); err != nil {
			return nil
		}
		if parsed != nil {
			out = append(out, parsed)
		}

		if onlyWhitespaceRemaining(trimmed, dec.InputOffset()) {
			return out
		}
	}
}

func onlyWhitespaceRemaining(s string, offset int64) bool {
	if offset < 0 || offset > int64(len(s)) {
		return false
	}
	return len(bytes.TrimSpace([]byte(s[offset:]))) == 0
}

func hasUnknownProperties(args map[string]any, props map[string]any) bool {
	for key := range args {
		if _, ok := props[key]; !ok {
			return true
		}
	}
	return false
}

// checkType validates that val matches the JSON Schema type declared in propSchema.
func checkType(key string, val any, propSchema map[string]any) error {
	typeRaw, ok := propSchema["type"]
	if !ok {
		return nil // no type constraint
	}
	typeName, ok := typeRaw.(string)
	if !ok {
		return nil
	}

	switch typeName {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("property %q: expected string, got %T", key, val)
		}
	case "integer":
		switch v := val.(type) {
		case float64:
			if v != math.Trunc(v) {
				return fmt.Errorf("property %q: expected integer, got float64 with fractional part", key)
			}
		case int:
			// ok
		case int64:
			// ok
		default:
			return fmt.Errorf("property %q: expected integer, got %T", key, val)
		}
	case "number":
		switch val.(type) {
		case float64, int, int64:
			// ok
		default:
			return fmt.Errorf("property %q: expected number, got %T", key, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("property %q: expected boolean, got %T", key, val)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return fmt.Errorf("property %q: expected array, got %T", key, val)
		}
		if err := checkArrayItems(key, arr, propSchema); err != nil {
			return err
		}
	case "object":
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("property %q: expected object, got %T", key, val)
		}
		if err := validateToolArgs(propSchema, obj); err != nil {
			return fmt.Errorf("property %q: %w", key, err)
		}
	}

	if err := checkEnum(key, val, propSchema); err != nil {
		return err
	}

	return nil
}

// checkArrayItems validates each element of arr against the "items" sub-schema.
func checkArrayItems(key string, arr []any, propSchema map[string]any) error {
	itemsRaw, ok := propSchema["items"]
	if !ok {
		return nil
	}
	itemSchema, ok := itemsRaw.(map[string]any)
	if !ok {
		return nil
	}
	for i, elem := range arr {
		elemKey := fmt.Sprintf("%s[%d]", key, i)
		if err := checkType(elemKey, elem, itemSchema); err != nil {
			return err
		}
	}
	return nil
}

// checkEnum validates that val is one of the allowed enum values in propSchema.
func checkEnum(key string, val any, propSchema map[string]any) error {
	enumRaw, ok := propSchema["enum"]
	if !ok {
		return nil
	}

	switch ev := enumRaw.(type) {
	case []any:
		for _, allowed := range ev {
			if val == allowed {
				return nil
			}
		}
	case []string:
		s, ok := val.(string)
		if ok {
			for _, allowed := range ev {
				if s == allowed {
					return nil
				}
			}
		}
	default:
		return nil // unknown enum format, skip
	}

	return fmt.Errorf("property %q: value %v is not in enum", key, val)
}
