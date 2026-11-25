# PostgreSQL fixture isolation and orphan-schema inspection

The package test harness creates a unique private schema, scopes only the in-memory fixture copies, checks the effective schema and public catalog after each fixture, and removes only the schema it created. Its deferred cleanup reports the test exit code and cleanup/close errors separately. This is test infrastructure, not production migration logic.

A fatal panic on another test goroutine, an explicit os.Exit, or process termination can bypass Go deferred cleanup. Do not automatically delete schemas on startup or from a name pattern alone.

Owner procedure on the designated test database only:

1. Stop the relevant test processes and confirm no concurrent harness owns the candidate.
2. Inspect candidates, using a read-only query:

   SELECT nspname, pg_get_userbyid(nspowner) AS owner
   FROM pg_namespace
   WHERE nspname ~ '^pools_t_pg_[0-9]+_[0-9]+$'
   ORDER BY nspname;

3. Compare the exact schema name with the failed run and verify the database, owner, contained objects and absence of active tests. Do not infer that a candidate is orphaned from the prefix alone.
4. Only after that verification, an authorized owner may use DROP SCHEMA on that exact quoted schema identifier with CASCADE. Never target public, pg_catalog, unrelated test schemas, or production databases.

The scanner deliberately rejects unfamiliar search-path overrides and explicit public qualifications rather than guessing how to rewrite arbitrary SQL. Current approved fixtures use the known public, pg_temp search-path clause and unqualified objects.
