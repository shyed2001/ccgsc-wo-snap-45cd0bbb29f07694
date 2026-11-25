// Path: internal/store/pg/redis_schema_T91_test.go | Author: Codex | 2026-11-20
// T91: validate both provisioned page schemas and the additive migration offline.
package pg

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestT91RedisSchemas(t *testing.T) {
	sql, err := os.ReadFile("../../../db/pool_config.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../db/redis_admin_T91.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(migration), "$redis$")
	if len(parts) != 3 {
		t.Fatal("missing migration literal")
	}
	var additions map[string]any
	if err = json.Unmarshal([]byte(parts[1]), &additions); err != nil {
		t.Fatal(err)
	}
	standalone, err := os.ReadFile("../../../db/pool_config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	documents := [][]byte{standalone}
	for i, part := range strings.Split(string(sql), "$schema$") {
		if i%2 == 1 {
			documents = append(documents, []byte(part))
		}
	}
	checked := 0
	for _, raw := range documents {
		var doc map[string]any
		if err = json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		page := ""
		switch doc["title"] {
		case "CCGSC thread-pool-admin page config":
			page = "thread-pool-admin"
		case "CCGSC process-pool-admin page config":
			page = "process-pool-admin"
		default:
			continue
		}
		checked++
		properties := doc["properties"].(map[string]any)
		for key, want := range additions {
			if !reflect.DeepEqual(properties[key], want) {
				t.Fatalf("%s differs from migration: %s", page, key)
			}
		}
		validator, err := ParseSchema(raw)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		for _, pc := range validPageConfigs {
			if pc.page == page {
				if err = json.Unmarshal(pc.config, &config); err != nil {
					t.Fatal(err)
				}
			}
		}
		for key, schema := range additions {
			config[key] = schema.(map[string]any)["default"]
		}
		encoded, _ := json.Marshal(config)
		if problems := validator.Validate(encoded); len(problems) > 0 {
			t.Fatalf("%s defaults: %v", page, problems)
		}
		for key, values := range map[string][]any{
			"redis.memory.mode": {"invalid", nil}, "redis.memory.fixed_gb": {0, 5, 1.5, "2"},
			"redis.memory.dynamic_percent": {9, 41}, "redis.memory.interval_min": {0, 61}, "redis.persistence.mode": {"off"},
		} {
			previous := config[key]
			for _, value := range values {
				config[key] = value
				encoded, _ = json.Marshal(config)
				if len(validator.Validate(encoded)) == 0 {
					t.Fatalf("%s accepted %s=%v", page, key, value)
				}
			}
			config[key] = previous
		}
	}
	if checked != 3 {
		t.Fatalf("checked %d schemas, want standalone plus both embedded pages", checked)
	}
}
