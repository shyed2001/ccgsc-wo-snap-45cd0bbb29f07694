package pg

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// N3: Go runs test packages in separate processes. Keep the config-store
// fixture outside public, and never change production DDL or live settings.
// This package owns its randomly named schema; other packages retain theirs.
func TestMain(m *testing.M) { os.Exit(n3RunTests(m)) }

func n3RunTests(m *testing.M) (code int) {
	code = 1
	raw := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if raw == "" {
		return m.Run()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "N3 test schema: PostgreSQL unavailable (details withheld to avoid DSN exposure)")
		return 1
	}
	schema := fmt.Sprintf("pools_t_pg_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	created := false
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		var cleanupErr error
		if created {
			// A fixture may have left an explicit transaction aborted.
			_, _ = conn.Exec(cleanupCtx, "ROLLBACK")
			_, cleanupErr = conn.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE")
		}
		closeErr := conn.Close(cleanupCtx)
		fmt.Fprintf(os.Stderr, "N3 test outcome=%d; private schema cleanup=%v; close=%v\n", code, cleanupErr, closeErr)
		if code == 0 && (cleanupErr != nil || closeErr != nil) {
			code = 1
		}
	}()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		fmt.Fprintln(os.Stderr, "N3 test schema: create failed:", err)
		return 1
	}
	created = true
	// Capture the public catalog before loading the fixtures. Verify after each
	// fixture that none changed any public table/index/sequence definition.
	// A private search_path alone is not evidence of fixture isolation.
	publicBefore, err := n3PublicCatalog(ctx, conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "N3 test schema: read public catalog:", err)
		return 1
	}
	// The schema SQL intentionally ships with public search_path for live
	// deployment. Scope an IN-MEMORY TEST COPY only; never modify the SQL files.
	if _, err = conn.Exec(ctx, "SET search_path TO "+quoted+",pg_temp"); err != nil {
		fmt.Fprintln(os.Stderr, "N3 test schema: search_path failed:", err)
		return 1
	}
	for _, filename := range []string{
		"../../../db/pool_config.sql",
		"../../../db/policy_F5.sql",
		"../../../db/pool_metrics_MVPW.sql",
		"../../../db/redis_admin_T91.sql",
	} {
		sqlBytes, readErr := os.ReadFile(filename)
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "N3 test schema: read fixture failed:", readErr)
			return 1
		}
		sql, scopeErr := n3ScopeFixtureSQL(string(sqlBytes), schema)
		if scopeErr != nil {
			fmt.Fprintln(os.Stderr, "N3 test schema: reject unsafe fixture ("+filename+"):", scopeErr)
			return 1
		}
		if _, er := conn.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); er != nil {
			fmt.Fprintln(os.Stderr, "N3 test schema: fixture apply failed ("+filename+"):", er)
			return 1
		}
		var current string
		if err := conn.QueryRow(ctx, "SELECT current_schema()").Scan(&current); err != nil || current != schema {
			fmt.Fprintln(os.Stderr, "N3 test schema: fixture escaped private schema", filename)
			return 1
		}
		publicAfter, err := n3PublicCatalog(ctx, conn)
		if err != nil || publicBefore != publicAfter {
			fmt.Fprintln(os.Stderr, "N3 test schema: public catalog changed or query failed:", filename, err)
			return 1
		}
	}
	scoped, err := n3ScopedDSN(raw, schema)
	if err != nil {
		fmt.Fprintln(os.Stderr, "N3 test schema: DSN rejected (details withheld)")
		return 1
	}
	if err := os.Setenv("POOLS_PG_DSN", scoped); err != nil {
		fmt.Fprintln(os.Stderr, "N3 test schema: env failed:", err)
		return 1
	}
	defer os.Setenv("POOLS_PG_DSN", raw)
	return m.Run()
}

func n3ScopedDSN(raw, schema string) (string, error) {
	if !n3TestSchemaName.MatchString(schema) {
		return "", fmt.Errorf("N3 invalid private schema")
	}
	var scoped string
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("N3 malformed URL DSN")
		}
		q := u.Query()
		for key := range q {
			if strings.EqualFold(key, "options") {
				return "", fmt.Errorf("N3 competing connection options")
			}
			if strings.EqualFold(key, "search_path") {
				q.Del(key)
			}
		}
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	} else {
		if n3DSNSchemaOverride.MatchString(raw) {
			return "", fmt.Errorf("N3 conflicting search_path/options in PostgreSQL test DSN")
		}
		scoped = raw + " search_path=" + schema
	}
	cfg, err := pgx.ParseConfig(scoped)
	if err != nil {
		return "", fmt.Errorf("N3 cannot parse scoped DSN")
	}
	if cfg.RuntimeParams["search_path"] != schema {
		return "", fmt.Errorf("N3 effective path not private")
	}
	for key := range cfg.RuntimeParams {
		if strings.EqualFold(key, "options") || (strings.EqualFold(key, "search_path") && key != "search_path") {
			return "", fmt.Errorf("N3 competing parsed override")
		}
	}
	return scoped, nil
}

var n3TestSchemaName = regexp.MustCompile(`^pools_t_pg_[a-zA-Z0-9_]+$`)
var n3SearchPathStatement = regexp.MustCompile(`(?im)^[ \t]*SET[ \t]+search_path[ \t]*(?:=|TO)[ \t]*([^\r\n;]+)(;?)`)
var n3KnownPublicPath = regexp.MustCompile(`(?i)^\s*public\s*,\s*pg_temp\s*$`)
var n3AnySearchPath = regexp.MustCompile(`(?i)\b(?:SET\s+(?:(?:LOCAL|SESSION)\s+)?|RESET\s+)(?:search_path\b|"search_path")`)
var n3QualifiedPublic = regexp.MustCompile(`(?i)(?:\bpublic\b|"public")\s*\.`)
var n3OtherPathOverride = regexp.MustCompile(`(?i)\b(?:set_config\s*\(|RESET\s+ALL\b|DISCARD\s+ALL\b)`)
var n3DSNSchemaOverride = regexp.MustCompile(`(?i)(^|\s)(?:search_path|options)\s*=`)

// n3ScopeFixtureSQL scopes copies of production SQL entirely inside the
// package-owned test schema. Unknown search-path changes fail closed; never
// execute potentially unscoped DDL against a shared public schema.
func n3ScopeFixtureSQL(sql, schema string) (string, error) {
	if !n3TestSchemaName.MatchString(schema) {
		return "", fmt.Errorf("N3 invalid private schema identifier")
	}
	quoted := pgx.Identifier{schema}.Sanitize()
	matches := n3SearchPathStatement.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		if !n3KnownPublicPath.MatchString(match[1]) {
			return "", fmt.Errorf("N3 unrecognized SET search_path, refuse fixture")
		}
	}
	rewritten := n3SearchPathStatement.ReplaceAllStringFunc(sql, func(statement string) string {
		suffix := ""
		if strings.HasSuffix(strings.TrimSpace(statement), ";") {
			suffix = ";"
		}
		return "SET search_path TO " + quoted + ", pg_temp" + suffix
	})
	if len(n3AnySearchPath.FindAllStringIndex(rewritten, -1)) != len(matches) {
		return "", fmt.Errorf("N3 unrecognized search_path syntax in fixture")
	}
	if n3OtherPathOverride.MatchString(rewritten) {
		return "", fmt.Errorf("N3 unsupported session configuration override")
	}
	if n3QualifiedPublic.MatchString(rewritten) {
		return "", fmt.Errorf("N3 public-qualified object reference in fixture")
	}
	return rewritten, nil
}

// n3PublicCatalog is an identity snapshot of public relations including their
// column/index metadata. It must not change while private test fixtures load.
func n3PublicCatalog(ctx context.Context, conn *pgx.Conn) (string, error) {
	rows, err := conn.Query(ctx, `
		SELECT c.oid::text, c.relname, c.relkind::text, c.relnatts::text,
		       COALESCE(a.attname, '') || ':' ||
		       COALESCE(a.atttypid::text, '') || ':' ||
		       COALESCE(a.attnotnull::text, '') || ':' ||
		       COALESCE(d.adbin::text, '')
		FROM pg_catalog.pg_class AS c
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		LEFT JOIN pg_catalog.pg_attrdef AS d ON d.adrelid = c.oid AND d.adnum = a.attnum
		WHERE n.nspname = 'public'
		ORDER BY c.oid, a.attnum`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var oid, name, kind, cols, def string
		if err := rows.Scan(&oid, &name, &kind, &cols, &def); err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "%s|%s|%s|%s|%s\n", oid, name, kind, cols, def)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	details, err := conn.Query(ctx, `SELECT kind, name, def FROM (
	SELECT 'function' kind, p.oid::text name, concat_ws('|',p.proname,p.prosrc,p.proargtypes::text,p.prorettype::text,p.proconfig::text,p.prosecdef::text) def FROM pg_proc p JOIN pg_namespace n ON p.pronamespace=n.oid WHERE n.nspname='public'
	UNION ALL SELECT 'constraint', c.oid::text, concat_ws('|',c.conname,c.contype::text,c.conrelid::text,c.confrelid::text,c.conkey::text,c.confkey::text,c.conbin::text,c.convalidated::text,c.condeferrable::text) FROM pg_constraint c JOIN pg_namespace n ON c.connamespace=n.oid WHERE n.nspname='public'
	UNION ALL SELECT 'index', c.oid::text, concat_ws('|',i.indrelid::text,i.indkey::text,i.indclass::text,i.indcollation::text,i.indoption::text,i.indexprs::text,i.indpred::text,i.indisunique::text,i.indisvalid::text) FROM pg_class c JOIN pg_index i ON i.indexrelid=c.oid JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='public' AND c.relkind IN ('i','I')
	UNION ALL SELECT 'trigger', t.oid::text, concat_ws('|',t.tgname,t.tgfoid::text,t.tgtype::text,t.tgenabled::text,t.tgargs::text,t.tgqual::text) FROM pg_trigger t JOIN pg_class c ON t.tgrelid=c.oid JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='public'
	UNION ALL SELECT 'view', c.oid::text, concat_ws('|',r.rulename,r.ev_action::text,r.ev_qual::text) FROM pg_class c JOIN pg_rewrite r ON r.ev_class=c.oid JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='public' AND c.relkind IN ('v','m')
	) AS d ORDER BY kind,name`)
	if err != nil {
		return "", err
	}
	defer details.Close()
	for details.Next() {
		var kind, name, def string
		if err := details.Scan(&kind, &name, &def); err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "%q|%q|%q\n", kind, name, def)
	}
	return out.String(), details.Err()
}
