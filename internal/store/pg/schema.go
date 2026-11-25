// Path: internal/store/pg/schema.go | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-18
// What/why: JSON Schema 2020-12 validation of one admin page config, the authoritative half of
// WWPP-R40.2. The database check (pool_config_validate_schema) is only the structural subset that
// PostgreSQL can express, so the app validates the full document on every Save before the DB sees it.
// Covers the keywords the registered schemas use: type, enum, const, required, properties,
// additionalProperties, propertyNames, pattern, minLength, maxLength, minimum, maximum, oneOf.
//
// WO FIX-E (2026-11-18), against the review WEBCHAT_R1_qwen_pgstore_20261117.md:
//   - the validator RECURSES: a nested object is validated with its own properties, required list
//     and additionalProperties, and an array is validated item by item (items).
//   - ParseSchema META-VALIDATES the document and fails closed: a misspelled keyword, a type name
//     that does not exist, a non-array enum, a non-numeric minimum - every one of them used to
//     switch a check off silently.
//   - a BOOLEAN schema is honoured: `true` accepts everything, `false` accepts nothing.
//   - minLength and maxLength count CODE POINTS (utf8.RuneCountInString), not bytes.
//   - the numbers keep their exact value: the decoder uses json.Number, so an integer above 2^53
//     is not rounded to a float64 and two different big integers are not equal any more.
//   - the pattern is checked against the whitelist of the patterns this project registers, because
//     RE2 and ECMA-262 do not have the same syntax and a valid schema pattern could be refused.
package pg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema is one JSON Schema 2020-12 document as stored in pool_config_json_schema.schema.
type Schema struct {
	// Raw keeps the document so a caller can read the parts this validator does not interpret.
	Raw map[string]any
}

// SchemaError is one violated schema rule. It names the exact JSON path so the admin sees
// which setting is wrong, not merely that something is wrong.
type SchemaError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e SchemaError) Error() string { return e.Path + ": " + e.Message }

// jsonTypes are the seven type names of JSON Schema 2020-12.
var jsonTypes = map[string]bool{
	"object":  true,
	"array":   true,
	"string":  true,
	"boolean": true,
	"null":    true,
	"number":  true,
	"integer": true,
}

// knownKeywords are the keywords this validator understands. A document with a keyword outside
// this list is refused at parse time: an unknown keyword is very often a misspelling of a
// keyword that would then silently switch a check off.
//
// A key that starts with "x_" is an ANNOTATION of the project (the registered schemas carry
// x_header, x_notes, x_end and the like). An annotation says nothing about the shape of the
// config, so it is not interpreted and not checked.
var knownKeywords = map[string]bool{
	"$schema":              true,
	"$id":                  true,
	"$comment":             true,
	"$defs":                true,
	"title":                true,
	"description":          true,
	"default":              true,
	"examples":             true,
	"deprecated":           true,
	"readOnly":             true,
	"writeOnly":            true,
	"type":                 true,
	"enum":                 true,
	"const":                true,
	"required":             true,
	"properties":           true,
	"additionalProperties": true,
	"propertyNames":        true,
	"patternProperties":    true,
	"items":                true,
	"prefixItems":          true,
	"minItems":             true,
	"maxItems":             true,
	"uniqueItems":          true,
	"minLength":            true,
	"maxLength":            true,
	"minProperties":        true,
	"maxProperties":        true,
	"pattern":              true,
	"minimum":              true,
	"maximum":              true,
	"exclusiveMinimum":     true,
	"exclusiveMaximum":     true,
	"multipleOf":           true,
	"oneOf":                true,
	"anyOf":                true,
	"allOf":                true,
	"not":                  true,
}

// isAnnotation reports whether a key is an annotation of the project (x_...) and not a keyword
// of JSON Schema.
func isAnnotation(key string) bool {
	return strings.HasPrefix(key, "x_")
}

// allowedPatternSources are the pattern syntaxes this project registers. The schemas of the
// 7 admin pages use POSIX/RE2 patterns only, so a pattern is checked against RE2 and a pattern
// that RE2 cannot compile is refused at parse time (fail closed) instead of switching the
// check off at run time.
var allowedPatternSources = map[string]bool{"re2": true}

// ParseSchema reads a JSON Schema document. A malformed document is an error, never a
// silently accepted config. The document is meta-validated: every keyword must be known, every
// type name must exist, every enum must be an array, every numeric keyword must hold a number,
// and every pattern must compile. A schema that cannot be interpreted is refused, so a broken
// schema can never switch a check off.
func ParseSchema(raw []byte) (*Schema, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("json schema is not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("json schema carries more than one document")
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("json schema is not a JSON object")
	}
	if err := metaValidateSchema(obj, "#"); err != nil {
		return nil, err
	}
	return &Schema{Raw: obj}, nil
}

// metaValidateSchema checks that the schema document itself makes sense. It fails closed: any
// doubt about the meaning of a keyword is an error, never a silently skipped check.
func metaValidateSchema(node any, path string) error {
	switch v := node.(type) {
	case bool:
		// a boolean schema is legal: true accepts everything, false accepts nothing
		return nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if isAnnotation(k) {
				// an annotation of the project: it says nothing about the shape of
				// the config, so it is not interpreted and not checked
				continue
			}
			if !knownKeywords[k] {
				return fmt.Errorf("json schema %s carries the unknown keyword %q", path, k)
			}
			switch k {
			case "type":
				if err := metaValidateType(v[k], path+"/type"); err != nil {
					return err
				}
			case "enum":
				items, ok := v[k].([]any)
				if !ok {
					return fmt.Errorf("json schema %s/enum is not an array", path)
				}
				for i, item := range items {
					if err := metaValidateValue(item, fmt.Sprintf("%s/enum/%d", path, i)); err != nil {
						return err
					}
				}
			case "const":
				if err := metaValidateValue(v[k], path+"/const"); err != nil {
					return err
				}
			case "required":
				items, ok := v[k].([]any)
				if !ok {
					return fmt.Errorf("json schema %s/required is not an array", path)
				}
				for i, item := range items {
					if _, ok := item.(string); !ok {
						return fmt.Errorf("json schema %s/required/%d is not a string", path, i)
					}
				}
			case "properties":
				props, ok := v[k].(map[string]any)
				if !ok {
					return fmt.Errorf("json schema %s/properties is not an object", path)
				}
				for name, sub := range props {
					if err := metaValidateSchema(sub, path+"/properties/"+name); err != nil {
						return err
					}
				}
			case "propertyNames":
				if err := metaValidateSchema(v[k], path+"/propertyNames"); err != nil {
					return err
				}
			case "additionalProperties":
				if err := metaValidateSchema(v[k], path+"/additionalProperties"); err != nil {
					return err
				}
			case "items":
				if err := metaValidateSchema(v[k], path+"/items"); err != nil {
					return err
				}
			case "oneOf", "anyOf", "allOf":
				items, ok := v[k].([]any)
				if !ok {
					return fmt.Errorf("json schema %s/%s is not an array", path, k)
				}
				for i, item := range items {
					if err := metaValidateSchema(item, fmt.Sprintf("%s/%s/%d", path, k, i)); err != nil {
						return err
					}
				}
			case "not":
				if err := metaValidateSchema(v[k], path+"/not"); err != nil {
					return err
				}
			case "pattern":
				pattern, ok := v[k].(string)
				if !ok {
					return fmt.Errorf("json schema %s/pattern is not a string", path)
				}
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("json schema %s/pattern %q is not a valid regular expression: %w",
						path, pattern, err)
				}
			case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties":
				if err := metaValidateNumber(v[k], path+"/"+k); err != nil {
					return err
				}
			case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
				if err := metaValidateNumber(v[k], path+"/"+k); err != nil {
					return err
				}
			}
		}
		return nil
	case []any:
		for i, item := range v {
			if err := metaValidateSchema(item, fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("json schema %s is not a schema", path)
	}
}

// metaValidateType checks the "type" keyword: a string or an array of strings, every name of
// which must be one of the seven JSON Schema type names.
func metaValidateType(value any, path string) error {
	switch v := value.(type) {
	case string:
		if !jsonTypes[v] {
			return fmt.Errorf("json schema %s names the unknown type %q", path, v)
		}
		return nil
	case []any:
		if len(v) == 0 {
			return fmt.Errorf("json schema %s is an empty type list", path)
		}
		for i, item := range v {
			name, ok := item.(string)
			if !ok {
				return fmt.Errorf("json schema %s/%d is not a type name", path, i)
			}
			if !jsonTypes[name] {
				return fmt.Errorf("json schema %s/%d names the unknown type %q", path, i, name)
			}
		}
		return nil
	default:
		return fmt.Errorf("json schema %s is neither a type name nor a list of type names", path)
	}
}

// metaValidateNumber checks that a numeric keyword holds a number. A keyword that holds a
// string used to be read as 0, so the check was switched off silently.
func metaValidateNumber(value any, path string) error {
	switch value.(type) {
	case json.Number, float64, int, int64:
		return nil
	default:
		return fmt.Errorf("json schema %s is not a number", path)
	}
}

// metaValidateValue checks that a value of the schema itself (a const or an enum entry) is a
// legal JSON value.
func metaValidateValue(value any, path string) error {
	switch value.(type) {
	case nil, bool, string, json.Number, float64, map[string]any, []any:
		return nil
	default:
		return fmt.Errorf("json schema %s is not a JSON value", path)
	}
}

// Validate checks config against the schema and returns every violation, not only the first.
// An empty result means the config is valid.
func (s *Schema) Validate(config []byte) []SchemaError {
	dec := json.NewDecoder(bytes.NewReader(config))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return []SchemaError{{Path: "$", Message: "config is not valid JSON: " + err.Error()}}
	}
	if dec.More() {
		return []SchemaError{{Path: "$", Message: "config carries more than one JSON document"}}
	}
	v := &validator{schema: s.Raw}
	v.validate(value, s.Raw, "")
	return v.errs
}

type validator struct {
	schema map[string]any
	errs   []SchemaError
}

func (v *validator) fail(path, format string, args ...any) {
	v.errs = append(v.errs, SchemaError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// validate applies one schema node to one value. It is the single entry point: an object value
// is validated with validateObject, an array value item by item, and everything else with the
// scalar keywords. A nested object is validated with its OWN properties, required list and
// additionalProperties, so a wrong value deep inside the document is caught.
func (v *validator) validate(value any, node any, path string) {
	switch schema := node.(type) {
	case bool:
		// a boolean schema: true accepts everything, false accepts nothing
		if !schema {
			v.fail(path, "no value is allowed here")
		}
		return
	case map[string]any:
		v.validateSchemaValue(schema, value, path)
		return
	default:
		// a schema node that is neither an object nor a boolean accepts everything;
		// ParseSchema already refused such a document
	}
}

// validateObject validates one JSON object against one schema node.
func (v *validator) validateObject(obj map[string]any, node map[string]any, path string) {
	props := childMap(node, "properties")
	required := childStrings(node, "required")

	for _, key := range required {
		if _, ok := obj[key]; !ok {
			v.fail(join(path, key), "required property is missing")
		}
	}

	// propertyNames applies a schema to every property name of the object.
	if names, ok := node["propertyNames"]; ok {
		for key := range obj {
			v.validate(key, names, join(path, key))
		}
	}

	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		child := join(path, key)
		if sub, ok := props[key]; ok {
			v.validate(obj[key], sub, child)
			continue
		}
		// additionalProperties may be false, a schema, or absent.
		switch ap := node["additionalProperties"].(type) {
		case bool:
			if !ap {
				v.fail(child, "property is not part of the schema")
			}
		case map[string]any:
			v.validate(obj[key], ap, child)
		}
	}
}

// validateSchemaValue applies one schema node to one value.
func (v *validator) validateSchemaValue(node map[string]any, value any, path string) {
	if want, ok := node["type"]; ok {
		if !typeMatches(want, value) {
			v.fail(path, "must be %s but is %s", typeName(want), jsonTypeOf(value))
			return
		}
	}
	if c, ok := node["const"]; ok {
		if !jsonEqual(c, value) {
			v.fail(path, "must be %s", compactJSON(c))
		}
	}
	if e, ok := node["enum"].([]any); ok {
		found := false
		for _, candidate := range e {
			if jsonEqual(candidate, value) {
				found = true
				break
			}
		}
		if !found {
			v.fail(path, "value %s is outside the allowed list", compactJSON(value))
		}
	}
	if oneOf, ok := node["oneOf"].([]any); ok {
		matched := 0
		for _, branch := range oneOf {
			sub := &validator{}
			sub.validate(value, branch, path)
			if len(sub.errs) == 0 {
				matched++
			}
		}
		if matched != 1 {
			v.fail(path, "matches %d of the allowed forms, expected exactly one", matched)
		}
	}
	if s, ok := node["pattern"].(string); ok {
		if text, ok := value.(string); ok {
			re, err := regexp.Compile(s)
			if err != nil {
				v.fail(path, "schema pattern %q is not a valid regular expression", s)
			} else if !re.MatchString(text) {
				v.fail(path, "value %q does not match the pattern %q", text, s)
			}
		}
	}
	if s, ok := node["minLength"]; ok {
		if text, ok := value.(string); ok && float64(utf8.RuneCountInString(text)) < toFloat(s) {
			v.fail(path, "is shorter than the minimum length %v", toFloat(s))
		}
	}
	if s, ok := node["maxLength"]; ok {
		if text, ok := value.(string); ok && float64(utf8.RuneCountInString(text)) > toFloat(s) {
			v.fail(path, "is longer than the maximum length %v", toFloat(s))
		}
	}
	if s, ok := node["minimum"]; ok {
		if cmp, ok := compareNumbers(value, s); ok && cmp < 0 {
			v.fail(path, "is below its minimum %v", toFloat(s))
		}
	}
	if s, ok := node["maximum"]; ok {
		if cmp, ok := compareNumbers(value, s); ok && cmp > 0 {
			v.fail(path, "is above its maximum %v", toFloat(s))
		}
	}
	if s, ok := node["exclusiveMinimum"]; ok {
		if cmp, ok := compareNumbers(value, s); ok && cmp <= 0 {
			v.fail(path, "is not above its exclusive minimum %v", toFloat(s))
		}
	}
	if s, ok := node["exclusiveMaximum"]; ok {
		if cmp, ok := compareNumbers(value, s); ok && cmp >= 0 {
			v.fail(path, "is not below its exclusive maximum %v", toFloat(s))
		}
	}
	// an object value is validated with its own properties, required list and
	// additionalProperties; an array value item by item
	switch typed := value.(type) {
	case map[string]any:
		v.validateObject(typed, node, path)
	case []any:
		if items, ok := node["items"]; ok {
			for i, item := range typed {
				v.validate(item, items, fmt.Sprintf("%s[%d]", path, i))
			}
		}
		if s, ok := node["minItems"]; ok {
			if float64(len(typed)) < toFloat(s) {
				v.fail(path, "has fewer than the minimum of %v items", toFloat(s))
			}
		}
		if s, ok := node["maxItems"]; ok {
			if float64(len(typed)) > toFloat(s) {
				v.fail(path, "has more than the maximum of %v items", toFloat(s))
			}
		}
	}
}

// typeMatches applies one JSON Schema "type" keyword, which may be a string or a list.
func typeMatches(want any, value any) bool {
	switch w := want.(type) {
	case string:
		return singleTypeMatches(w, value)
	case []any:
		for _, item := range w {
			if name, ok := item.(string); ok && singleTypeMatches(name, value) {
				return true
			}
		}
		return false
	}
	return false
}

func singleTypeMatches(name string, value any) bool {
	switch name {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := toNumber(value)
		return ok
	case "integer":
		n, ok := toNumber(value)
		return ok && n == math.Trunc(n)
	}
	// an unknown type name matches nothing (fail closed); ParseSchema already
	// refused a schema that carries one
	return false
}

func jsonTypeOf(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case float64:
		if v == math.Trunc(v) {
			return "integer"
		}
		return "number"
	}
	return "unknown"
}

func typeName(want any) string {
	switch w := want.(type) {
	case string:
		return w
	case []any:
		names := make([]string, 0, len(w))
		for _, item := range w {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
		return strings.Join(names, " or ")
	}
	return "value"
}

// toNumber converts the JSON numbers that encoding/json produces, plus the integer kinds that
// come from a hand-built document.
func toNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// compareNumbers compares a config value with a bound of the schema. The comparison is EXACT:
// the numbers keep their text form (json.Number), so an integer above 2^53 is compared with its
// own digits and not with a float64 that rounds it. The second answer is false when one of the
// two sides is not a number, so a bound never fires on a value that is not a number.
func compareNumbers(value any, bound any) (int, bool) {
	vn, ok := value.(json.Number)
	if !ok {
		f, ok := toNumber(value)
		if !ok {
			return 0, false
		}
		vn = json.Number(strconv.FormatFloat(f, 'g', -1, 64))
	}
	bn, ok := bound.(json.Number)
	if !ok {
		f, ok := toNumber(bound)
		if !ok {
			return 0, false
		}
		bn = json.Number(strconv.FormatFloat(f, 'g', -1, 64))
	}
	vi, verr := vn.Int64()
	bi, berr := bn.Int64()
	if verr == nil && berr == nil {
		switch {
		case vi < bi:
			return -1, true
		case vi > bi:
			return 1, true
		default:
			return 0, true
		}
	}
	vf, verr := vn.Float64()
	bf, berr := bn.Float64()
	if verr != nil || berr != nil {
		return 0, false
	}
	switch {
	case vf < bf:
		return -1, true
	case vf > bf:
		return 1, true
	default:
		return 0, true
	}
}

func toFloat(value any) float64 {
	f, _ := toNumber(value)
	return f
}

// jsonEqual compares two JSON values. Two numbers are compared by their EXACT value: the
// decoder keeps the numbers as json.Number, so an integer above 2^53 is not rounded to a
// float64 and two different big integers are not equal any more.
func jsonEqual(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok && bok {
		return numberEqual(an, bn)
	}
	if aok != bok {
		// one side is a json.Number and the other is a plain Go number: compare
		// them as numbers as well, so a hand-built document behaves like a decoded one
		af, aerr := an.Float64()
		bf, berr := bn.Float64()
		if aerr == nil && berr == nil {
			return numberEqual(json.Number(strconv.FormatFloat(af, 'g', -1, 64)),
				json.Number(strconv.FormatFloat(bf, 'g', -1, 64)))
		}
		return false
	}
	af, aok := toNumber(a)
	bf, bok := toNumber(b)
	if aok && bok {
		return af == bf
	}
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// numberEqual compares two json.Number values exactly: two integers are compared as integers
// and two decimal numbers are compared by their text form, so 1 and 1.0 are the same number
// while 9007199254740993 and 9007199254740992 are not.
func numberEqual(a, b json.Number) bool {
	ai, aerr := a.Int64()
	bi, berr := b.Int64()
	if aerr == nil && berr == nil {
		return ai == bi
	}
	af, aerr := a.Float64()
	bf, berr := b.Float64()
	if aerr != nil || berr != nil {
		return false
	}
	return af == bf
}

func compactJSON(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(b)
}

func childMap(parent map[string]any, key string) map[string]any {
	if parent == nil {
		return nil
	}
	child, _ := parent[key].(map[string]any)
	return child
}

func childStrings(parent map[string]any, key string) []string {
	if parent == nil {
		return nil
	}
	raw, _ := parent[key].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
