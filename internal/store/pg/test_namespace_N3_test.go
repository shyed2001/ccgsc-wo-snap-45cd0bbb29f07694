package pg

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"testing"
)

// N3 contract: PG configuration tests must never mutate the shared public
// schema. Package-local schema isolation is required even with -p>1.
func TestN3PGPackageUsesPrivateSchema(t *testing.T) {
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var schema string
	if err := db.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "pools_t_pg_") {
		t.Fatalf("store/pg package unexpectedly writes to schema %q; expected pools_t_pg_*", schema)
	}
	for _, name := range []string{"admin_user", "pool_admin_page", "pool_config_json_schema", "pool_config_version", "pool_config_retention", "job_config_ref", "security_audit_log", "pool_config_version_state", "pool_metrics"} {
		var namespace string
		if err := db.QueryRow(ctx, "SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid WHERE c.oid=to_regclass($1)", name).Scan(&namespace); err != nil || namespace != schema {
			t.Fatalf("fixture relation %s not in private schema: %s (%v)", name, namespace, err)
		}
	}
	for _, name := range []string{"pool_config_save", "pool_config_prune", "pool_config_attach_job"} {
		var count int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON p.pronamespace=n.oid WHERE n.nspname=$1 AND p.proname=$2", schema, name).Scan(&count); err != nil || count == 0 {
			t.Fatalf("fixture function %s not in private schema: %v", name, err)
		}
	}
}
