// Path: internal/store/pg/schema_test.go | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
// What/why: offline tests of the JSON Schema 2020-12 subset the settings store uses to validate
// every Save (WWPP-R40.2). These run everywhere: no database, no DSN, no network.
//
// WO FIX-E (2026-11-18): the table tests of the validator were added - the nested object, the
// array items, the boolean schema, the rune length, the big integer, the meta-validation of the
// schema and the oneOf count are each proved by a case of the table.
package pg

import (
	"encoding/json"
	"strings"
	"testing"
)

const testSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {"pattern": "^(tp|cfg)\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"},
  "required": ["tp.wrk.per_tab", "tp.wrk.min", "tp.wrk.max", "tp.ver.rerun_percent", "cfg.keep_last"],
  "properties": {
    "tp.wrk.per_tab": {"oneOf": [{"const": "auto"}, {"type": "integer", "minimum": 1, "maximum": 4}]},
    "tp.wrk.min": {"type": "integer", "minimum": 1, "maximum": 4},
    "tp.wrk.max": {"type": "integer", "minimum": 1, "maximum": 32},
    "tp.ver.rerun_percent": {"type": "integer", "minimum": 0, "maximum": 100},
    "tp.gen.fetch_mode": {"type": "string", "enum": ["leader", "each-tab"]},
    "tp.gen.name": {"type": "string", "minLength": 1, "maxLength": 64},
    "tp.gd.hidden_tab": {"type": "boolean", "const": false},
    "cfg.keep_last": {"type": "integer", "minimum": 1, "maximum": 100}
  }
}`

func mustSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := ParseSchema([]byte(testSchema))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const validConfig = `{
  "tp.wrk.per_tab": "auto",
  "tp.wrk.min": 1,
  "tp.wrk.max": 4,
  "tp.ver.rerun_percent": 1,
  "cfg.keep_last": 10
}`

func TestSchemaAcceptsValidConfig(t *testing.T) {
	if v := mustSchema(t).Validate([]byte(validConfig)); len(v) != 0 {
		t.Fatalf("valid config rejected: %v", v)
	}
}

func TestSchemaRejectsMissingRequiredProperty(t *testing.T) {
	cfg := `{"tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,"cfg.keep_last":10}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("missing required property accepted")
	}
	if !strings.Contains(v[0].Message, "required") {
		t.Fatalf("wrong message: %v", v[0])
	}
}

func TestSchemaRejectsUnknownProperty(t *testing.T) {
	cfg := `{"tp.wrk.per_tab":"auto","tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,
	          "cfg.keep_last":10,"tp.surprise":1}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("additional property accepted")
	}
	if !strings.Contains(v[0].Message, "not part of the schema") {
		t.Fatalf("wrong message: %v", v[0])
	}
}

func TestSchemaRejectsBadPropertyName(t *testing.T) {
	cfg := `{"tp.wrk.per_tab":"auto","tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,
	          "cfg.keep_last":10,"BadName":1}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("bad property name accepted")
	}
}

func TestSchemaRejectsWrongType(t *testing.T) {
	cfg := `{"tp.wrk.per_tab":"auto","tp.wrk.min":"one","tp.wrk.max":4,"tp.ver.rerun_percent":1,
	          "cfg.keep_last":10}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("string accepted where integer is required")
	}
	if !strings.Contains(v[0].Message, "must be integer") {
		t.Fatalf("wrong message: %v", v[0])
	}
}

func TestSchemaRejectsOutOfRangeNumbers(t *testing.T) {
	cases := map[string]string{
		"minimum": `{"tp.wrk.per_tab":"auto","tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":101,"cfg.keep_last":10}`,
		"maximum": `{"tp.wrk.per_tab":"auto","tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,"cfg.keep_last":0}`,
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if v := mustSchema(t).Validate([]byte(cfg)); len(v) == 0 {
				t.Fatal("out of range value accepted")
			}
		})
	}
}

func TestSchemaRejectsEnumViolation(t *testing.T) {
	cfg := `{"tp.wrk.per_tab":"auto","tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,
	          "cfg.keep_last":10,"tp.gen.fetch_mode":"wrong"}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("enum violation accepted")
	}
	if !strings.Contains(v[0].Message, "allowed list") {
		t.Fatalf("wrong message: %v", v[0])
	}
}

func TestSchemaOneOfAcceptsBothBranches(t *testing.T) {
	for _, perTab := range []string{`"auto"`, `2`} {
		cfg := `{"tp.wrk.per_tab":` + perTab + `,"tp.wrk.min":1,"tp.wrk.max":4,
		          "tp.ver.rerun_percent":1,"cfg.keep_last":10}`
		if v := mustSchema(t).Validate([]byte(cfg)); len(v) != 0 {
			t.Fatalf("oneOf branch %s rejected: %v", perTab, v)
		}
	}
}

func TestSchemaOneOfRejectsNeitherBranch(t *testing.T) {
	cfg := `{"tp.wrk.per_tab":9,"tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,"cfg.keep_last":10}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("oneOf violation accepted")
	}
}

func TestSchemaConstIsEnforced(t *testing.T) {
	cfg := `{"tp.wrk.per_tab":"auto","tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,
	          "cfg.keep_last":10,"tp.gd.hidden_tab":true}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("const violation accepted")
	}
}

func TestSchemaStringLengthIsChecked(t *testing.T) {
	cfg := `{"tp.wrk.per_tab":"auto","tp.wrk.min":1,"tp.wrk.max":4,"tp.ver.rerun_percent":1,
	          "cfg.keep_last":10,"tp.gen.name":""}`
	v := mustSchema(t).Validate([]byte(cfg))
	if len(v) == 0 {
		t.Fatal("empty string accepted although minLength is 1")
	}
}

func TestSchemaRejectsNonObjectConfig(t *testing.T) {
	if v := mustSchema(t).Validate([]byte(`[1,2,3]`)); len(v) == 0 {
		t.Fatal("array accepted as a config")
	}
	if v := mustSchema(t).Validate([]byte(`not json`)); len(v) == 0 {
		t.Fatal("invalid JSON accepted as a config")
	}
}

func TestParseSchemaRejectsGarbage(t *testing.T) {
	if _, err := ParseSchema([]byte(`[1]`)); err == nil {
		t.Fatal("non-object schema accepted")
	}
	if _, err := ParseSchema([]byte(`{`)); err == nil {
		t.Fatal("malformed schema accepted")
	}
}

func TestValidPageAndJobID(t *testing.T) {
	for _, page := range Pages {
		if err := validPage(page); err != nil {
			t.Fatalf("known page %q rejected: %v", page, err)
		}
	}
	if err := validPage("nope"); err == nil {
		t.Fatal("unknown page accepted")
	}
	if err := validJobID("s1.pool1.job1"); err != nil {
		t.Fatalf("valid job id rejected: %v", err)
	}
	for _, bad := range []string{"", "s1", "s1.pool1", "s1.pool1.job1.extra", ".pool1.job1", "s1..job1", "s1.pool1."} {
		if err := validJobID(bad); err == nil {
			t.Fatalf("bad job id %q accepted", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// WO FIX-E: the validator table tests
// ---------------------------------------------------------------------------

// TestValidatorTable is the table test of the validator. Every case names the rule it proves,
// so a failure points at one rule and not at the validator as a whole.
func TestValidatorTable(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		config  string
		wantErr bool
		wantMsg string
	}{
		{
			name:    "a nested object is validated with its own properties",
			schema:  `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"type":"integer"}}}}}`,
			config:  `{"outer":{"inner":"not a number"}}`,
			wantErr: true,
			wantMsg: "must be integer",
		},
		{
			name:    "a nested object with the right value passes",
			schema:  `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"type":"integer"}}}}}`,
			config:  `{"outer":{"inner":7}}`,
			wantErr: false,
		},
		{
			name:    "a nested object with a missing required property is refused",
			schema:  `{"type":"object","properties":{"outer":{"type":"object","required":["inner"],"properties":{"inner":{"type":"integer"}}}}}`,
			config:  `{"outer":{"other":1}}`,
			wantErr: true,
			wantMsg: "required",
		},
		{
			name:    "a nested object with an unknown property is refused",
			schema:  `{"type":"object","properties":{"outer":{"type":"object","additionalProperties":false,"properties":{"inner":{"type":"integer"}}}}}`,
			config:  `{"outer":{"inner":1,"surprise":2}}`,
			wantErr: true,
			wantMsg: "not part of the schema",
		},
		{
			name:    "the items of an array are validated",
			schema:  `{"type":"object","properties":{"list":{"type":"array","items":{"type":"integer"}}}}`,
			config:  `{"list":[1,2,"three"]}`,
			wantErr: true,
			wantMsg: "must be integer",
		},
		{
			name:    "an array of the right values passes",
			schema:  `{"type":"object","properties":{"list":{"type":"array","items":{"type":"integer"}}}}`,
			config:  `{"list":[1,2,3]}`,
			wantErr: false,
		},
		{
			name:    "a boolean schema false refuses every value",
			schema:  `{"type":"object","properties":{"anything":false}}`,
			config:  `{"anything":1}`,
			wantErr: true,
			wantMsg: "no value is allowed here",
		},
		{
			name:    "a boolean schema true accepts every value",
			schema:  `{"type":"object","properties":{"anything":true}}`,
			config:  `{"anything":{"deep":[1,2,3]}}`,
			wantErr: false,
		},
		{
			name:    "the length of a string counts code points and not bytes",
			schema:  `{"type":"object","properties":{"name":{"type":"string","minLength":3,"maxLength":3}}}`,
			config:  `{"name":"\u00e4\u00f6\u00fc"}`,
			wantErr: false,
		},
		{
			name:    "a string of three bytes but two code points is too short",
			schema:  `{"type":"object","properties":{"name":{"type":"string","minLength":3,"maxLength":3}}}`,
			config:  `{"name":"\u00e4\u00f6"}`,
			wantErr: true,
			wantMsg: "shorter than the minimum length",
		},
		{
			name:    "oneOf means exactly one branch",
			schema:  `{"type":"object","properties":{"x":{"oneOf":[{"type":"integer"},{"type":"number"}]}}}`,
			config:  `{"x":5}`,
			wantErr: true,
			wantMsg: "expected exactly one",
		},
		{
			name:    "oneOf with two branches that do not both match passes",
			schema:  `{"type":"object","properties":{"x":{"oneOf":[{"type":"integer"},{"type":"string"}]}}}`,
			config:  `{"x":5}`,
			wantErr: false,
		},
		{
			name:    "a branch with only a const is honoured",
			schema:  `{"type":"object","properties":{"x":{"oneOf":[{"const":"auto"},{"const":"manual"}]}}}`,
			config:  `{"x":"auto"}`,
			wantErr: false,
		},
		{
			name:    "a branch with only a const refuses another value",
			schema:  `{"type":"object","properties":{"x":{"oneOf":[{"const":"auto"},{"const":"manual"}]}}}`,
			config:  `{"x":"other"}`,
			wantErr: true,
			wantMsg: "expected exactly one",
		},
		{
			name:    "a type list accepts every type of the list",
			schema:  `{"type":"object","properties":{"x":{"type":["string","null"]}}}`,
			config:  `{"x":null}`,
			wantErr: false,
		},
		{
			name:    "a type list refuses a type that is not in the list",
			schema:  `{"type":"object","properties":{"x":{"type":["string","null"]}}}`,
			config:  `{"x":5}`,
			wantErr: true,
			wantMsg: "must be string or null",
		},
		{
			name:    "a big integer is not rounded to a float",
			schema:  `{"type":"object","properties":{"x":{"const":9007199254740993}}}`,
			config:  `{"x":9007199254740992}`,
			wantErr: true,
			wantMsg: "must be",
		},
		{
			name:    "a big integer that is equal passes",
			schema:  `{"type":"object","properties":{"x":{"const":9007199254740993}}}`,
			config:  `{"x":9007199254740993}`,
			wantErr: false,
		},
		{
			name:    "a big integer outside the bounds is refused",
			schema:  `{"type":"object","properties":{"x":{"type":"integer","maximum":9007199254740992}}}`,
			config:  `{"x":9007199254740993}`,
			wantErr: true,
			wantMsg: "above its maximum",
		},
		{
			name:    "a pattern is enforced",
			schema:  `{"type":"object","propertyNames":{"pattern":"^[a-z]+$"},"properties":{"abc":{"type":"integer"}}}`,
			config:  `{"ABC":1}`,
			wantErr: true,
			wantMsg: "does not match the pattern",
		},
		{
			name:    "an exclusive minimum is enforced",
			schema:  `{"type":"object","properties":{"x":{"type":"integer","exclusiveMinimum":3}}}`,
			config:  `{"x":3}`,
			wantErr: true,
			wantMsg: "exclusive minimum",
		},
		{
			name:    "an enum of one value refuses every other value",
			schema:  `{"type":"object","properties":{"x":{"enum":["only"]}}}`,
			config:  `{"x":"other"}`,
			wantErr: true,
			wantMsg: "allowed list",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			schema, err := ParseSchema([]byte(c.schema))
			if err != nil {
				t.Fatalf("the schema itself is refused: %v", err)
			}
			violations := schema.Validate([]byte(c.config))
			if c.wantErr && len(violations) == 0 {
				t.Fatalf("the config was accepted, want a violation")
			}
			if !c.wantErr && len(violations) != 0 {
				t.Fatalf("the config was refused: %v", violations)
			}
			if c.wantMsg != "" {
				found := false
				for _, v := range violations {
					if strings.Contains(v.Message, c.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("no violation mentions %q: %v", c.wantMsg, violations)
				}
			}
		})
	}
}

// TestParseSchemaMetaValidation proves that a broken schema is refused at parse time, so it can
// never switch a check off silently (the review: a misspelled keyword, a non-array enum, an
// unknown type name and a non-numeric minimum all used to disable the check).
func TestParseSchemaMetaValidation(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name:   "a misspelled keyword is refused",
			schema: `{"type":"object","propertiez":{"a":{"type":"integer"}}}`,
			want:   "unknown keyword",
		},
		{
			name:   "an unknown type name is refused",
			schema: `{"type":"object","properties":{"a":{"type":"int"}}}`,
			want:   "unknown type",
		},
		{
			name:   "an unknown type name in a type list is refused",
			schema: `{"type":"object","properties":{"a":{"type":["string","int"]}}}`,
			want:   "unknown type",
		},
		{
			name:   "a non-array enum is refused",
			schema: `{"type":"object","properties":{"a":{"enum":"one"}}}`,
			want:   "not an array",
		},
		{
			name:   "a non-numeric minimum is refused",
			schema: `{"type":"object","properties":{"a":{"type":"integer","minimum":"1"}}}`,
			want:   "not a number",
		},
		{
			name:   "a non-numeric maxLength is refused",
			schema: `{"type":"object","properties":{"a":{"type":"string","maxLength":"64"}}}`,
			want:   "not a number",
		},
		{
			name:   "a pattern that does not compile is refused",
			schema: `{"type":"object","properties":{"a":{"type":"string","pattern":"([a-z"}}}`,
			want:   "not a valid regular expression",
		},
		{
			name:   "a required list that is not an array is refused",
			schema: `{"type":"object","required":"a"}`,
			want:   "not an array",
		},
		{
			name:   "a required entry that is not a string is refused",
			schema: `{"type":"object","required":[7]}`,
			want:   "not a string",
		},
		{
			name:   "a properties list that is not an object is refused",
			schema: `{"type":"object","properties":[]}`,
			want:   "not an object",
		},
		{
			name:   "a oneOf list that is not an array is refused",
			schema: `{"type":"object","properties":{"a":{"oneOf":{"type":"integer"}}}}`,
			want:   "not an array",
		},
		{
			name:   "a nested schema with a broken keyword is refused",
			schema: `{"type":"object","properties":{"a":{"type":"object","propertiez":{}}}}`,
			want:   "unknown keyword",
		},
		{
			name:   "a schema that is not an object is refused",
			schema: `[1,2,3]`,
			want:   "not a JSON object",
		},
		{
			name:   "a document that is not JSON is refused",
			schema: `{`,
			want:   "not valid JSON",
		},
		{
			name:   "two documents are refused",
			schema: `{"type":"object"}{"type":"object"}`,
			want:   "more than one document",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSchema([]byte(c.schema))
			if err == nil {
				t.Fatalf("the broken schema was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("wrong message %q, want it to mention %q", err.Error(), c.want)
			}
		})
	}
}

// TestParseSchemaAcceptsTheRegisteredSchemaOfEveryPage proves that the schema documents this
// store really uses are accepted by the validator, and that the thread-pool-admin document of
// the DDL still passes. It needs no database.
func TestParseSchemaAcceptsTheRegisteredSchemaOfEveryPage(t *testing.T) {
	doc := `{
	  "$schema": "https://json-schema.org/draft/2020-12/schema",
	  "type": "object",
	  "additionalProperties": false,
	  "propertyNames": {"pattern": "^(tp|cfg)\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"},
	  "required": ["tp.wrk.per_tab"],
	  "properties": {
	    "tp.wrk.per_tab": {"oneOf": [{"const": "auto"}, {"type": "integer", "minimum": 1, "maximum": 4}]}
	  },
	  "x_notes": ["a key that starts with x is not a keyword"]
	}`
	if _, err := ParseSchema([]byte(doc)); err != nil {
		t.Fatalf("the schema of the DDL was refused: %v", err)
	}
}

// TestSchemaValidatesTheThreadConfigOfTheDDL proves the config the tests use really satisfies
// the schema of the DDL: it is the same document the database holds.
func TestSchemaValidatesTheThreadConfigOfTheDDL(t *testing.T) {
	schema, err := ParseSchema([]byte(testSchema))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal([]byte(validConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg) == 0 {
		t.Fatal("the test config is empty")
	}
	if v := schema.Validate([]byte(validConfig)); len(v) != 0 {
		t.Fatalf("the test config was refused: %v", v)
	}
}
