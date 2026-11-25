// Path: internal/store/pg/policy_schema_F5_test.go | Codex | 2026-11-18
// T42 F5: verify the optional SQL schema extension with the production validator.
package pg

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestF5SchemaExtension(t *testing.T) {
	sql, err := os.ReadFile("../../../db/policy_F5.sql")
	if err != nil {
		t.Fatal(err)
	}
	pieces := strings.Split(string(sql), "$policy$")
	if len(pieces) != 3 {
		t.Fatal("missing policy schema literal")
	}
	var policy any
	if err = json.Unmarshal([]byte(pieces[1]), &policy); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../db/pool_config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	schema["properties"].(map[string]any)["tp.policy"] = policy
	raw, _ = json.Marshal(schema)
	validator, err := ParseSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(validThreadConfig, &config); err != nil {
		t.Fatal(err)
	}
	config["tp.policy"] = map[string]any{"adminLimits": map[string]any{"maxAttempts": map[string]any{"allowOverride": true, "min": 1, "max": 5}}, "poolDefault": map[string]any{"maxAttempts": 2}}
	raw, _ = json.Marshal(config)
	if violations := validator.Validate(raw); len(violations) != 0 {
		t.Fatalf("valid policy %v", violations)
	}
	config["tp.policy"].(map[string]any)["unknown"] = true
	raw, _ = json.Marshal(config)
	if len(validator.Validate(raw)) == 0 {
		t.Fatal("unknown nested policy field accepted")
	}
}
