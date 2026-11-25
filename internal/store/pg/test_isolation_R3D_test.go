package pg

import (
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestR3DRejectsAlternateSchemaEscapes(t *testing.T) {
	for _, sql := range []string{
		"CREATE TABLE \"public\".escape(id int);",
		"SELECT 1; SET search_path TO public;",
		"SET LOCAL search_path TO public;",
		"SET SESSION \"search_path\" TO public;",
		"RESET search_path;",
		"SELECT set_config('search_path','public',false);",
	} {
		if _, err := n3ScopeFixtureSQL(sql, "pools_t_pg_r3d"); err == nil {
			t.Errorf("unsafe SQL accepted: %q", sql)
		}
	}
}

func TestR3DScopedDSNPrivateEffectiveValue(t *testing.T) {
	const schema = "pools_t_pg_r3d"
	for _, raw := range []string{"postgres://localhost/test?search_path=public&search_path=other", "host=localhost user=test dbname=test"} {
		got, err := n3ScopedDSN(raw, schema)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := pgx.ParseConfig(got)
		if err != nil || cfg.RuntimeParams["search_path"] != schema {
			t.Fatal("effective path not private")
		}
		if strings.Count(got, "search_path=") != 1 {
			t.Fatal("duplicate search path")
		}
	}
	for _, raw := range []string{"postgres://localhost/test?options=-csearch_path%3Dpublic", "host=localhost user=test search_path=public"} {
		if _, err := n3ScopedDSN(raw, schema); err == nil {
			t.Error("competing override accepted")
		}
	}
	if _, err := n3ScopedDSN("host=localhost user=test dbname=test", "public"); err == nil {
		t.Error("public schema accepted")
	}
}
