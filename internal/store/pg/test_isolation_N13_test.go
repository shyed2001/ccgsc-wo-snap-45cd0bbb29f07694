package pg

import (
	"strings"
	"testing"
)

func TestN13FixtureSQLAlwaysScopesKnownPublicSearchPath(t *testing.T) {
	for _, sample := range []string{
		"SET search_path = public, pg_temp;\nCREATE TABLE IF NOT EXISTS n13_table(id int);",
		"set SEARCH_path=public,pg_temp;\nCREATE TABLE n13_table(id int);",
		"SET search_path TO public , pg_temp ;\nCREATE TABLE n13_table(id int);",
		"RETURNS void\nLANGUAGE plpgsql\nSECURITY DEFINER\nSET search_path = public, pg_temp\nAS $fn$",
	} {
		out, err := n3ScopeFixtureSQL(sample, "pools_t_pg_n13")
		if err != nil {
			t.Fatalf("valid fixture variant rejected: %v", err)
		}
		if !strings.Contains(out, "SET search_path TO \"pools_t_pg_n13\", pg_temp") {
			t.Fatalf("fixture not scoped: %q", out)
		}
		if strings.Contains(strings.ToLower(out), "search_path = public") {
			t.Fatalf("unsafe fixture still points public")
		}
	}
}

func TestN13FixtureSQLRefusesUnrecognizedSchemaEscape(t *testing.T) {
	for _, sample := range []string{
		"SET search_path TO attacker, public; CREATE TABLE x(a int);",
		"CREATE TABLE public.n13_escape (id integer);",
		"set search_path = public; CREATE TABLE n13_escape(id int);",
	} {
		if _, err := n3ScopeFixtureSQL(sample, "pools_t_pg_n13"); err == nil {
			t.Errorf("unsafe fixture did not fail closed: %q", sample)
		}
	}
}

func TestN13KeyValueDSNRejectsConflictingSchemaOverrides(t *testing.T) {
	for _, dsn := range []string{
		"host=localhost user=test search_path=public",
		"host=localhost user=test options='-c search_path=public'",
	} {
		if _, err := n3ScopedDSN(dsn, "pools_t_pg_n13"); err == nil {
			t.Errorf("DSN with competing schema override accepted: %q", dsn)
		}
	}
	got, err := n3ScopedDSN("host=localhost user=test dbname=test", "pools_t_pg_n13")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "search_path=pools_t_pg_n13") {
		t.Fatalf("scoped DSN missing: %q", got)
	}
}
