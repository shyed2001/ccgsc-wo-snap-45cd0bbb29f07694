// Path: cmd/pools-migrate/main.go | Author: Claude Opus 5.5 | 2026-11-19
// What/why: one-shot copy of every SQLite record (all buckets) into the PostgreSQL
// pool_records store, so the live switch SQLite -> PostgreSQL (owner GO 2026-11-19,
// GRILL R42.7 / T86) keeps admin users, API keys, jobs and config versions.
// Safe to re-run: each record is written with Put (same bucket+id overwrites).
// Prints only bucket names and counts, never record bodies (they hold key hashes).
//
// Usage: POOLS_PG_DSN=... pools-migrate -sqlite /path/pools.db [-dry-run]
//
// T93 (2026-11-20, hermes MiniMax-M3):
//   - the SQLite DSN is built with net/url, so a path containing "?", "#" or a
//     space can no longer alter the URI or drop the read-only mode;
//   - the PostgreSQL write and the verification read run under a 2 minute
//     timeout, so a blocked connection cannot hang the one-shot tool;
//   - the verification compares the exact (bucket,id) sets, not only the counts.
//
// Contents: main (flags, read SQLite, write PostgreSQL in one transaction).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"

	_ "modernc.org/sqlite"
)

// pgTimeout bounds the PostgreSQL write and the verification read.
const pgTimeout = 2 * time.Minute

type row struct {
	bucket, id string
	body       json.RawMessage
}

func main() {
	src := flag.String("sqlite", "", "SQLite database file to copy from (opened read-only)")
	dry := flag.Bool("dry-run", false, "count records only, write nothing")
	flag.Parse()
	if err := run(*src, *dry); err != nil {
		fmt.Fprintln(os.Stderr, "pools-migrate:", err)
		os.Exit(1)
	}
}

// sqliteDSN builds the read-only SQLite DSN of a database file. The path is
// escaped as a URI path, so "?", "#", spaces and every other reserved
// character stay part of the file name and can never be read as URI syntax
// that overrides the read-only mode. The empty path is rejected.
//
// The result looks like "file:C:%5Cdata%5Cpools.db?mode=ro" on Windows and
// "file:/var/lib/pools.db?mode=ro" on POSIX. The "//" form is never used:
// SQLite would read the first path segment as a URI authority and fail.
// T93 (agy review): a POSIX path that itself starts with "//" (shell slash
// concatenation, e.g. -sqlite //var/lib/pools.db) would come out as
// "file://var/lib/pools.db?mode=ro" despite OmitHost, so the extra leading
// slashes are stripped before the URL is built.
func sqliteDSN(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("sqlite path is empty")
	}
	for strings.HasPrefix(path, "//") {
		path = path[1:]
	}
	u := url.URL{Scheme: "file", Path: path, OmitHost: true}
	q := url.Values{}
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// bucketIDs maps a bucket name to the set of record ids it holds.
type bucketIDs map[string]map[string]struct{}

// idSetsOfRows collects the (bucket,id) pairs of the SQLite rows.
func idSetsOfRows(rows []row) bucketIDs {
	sets := bucketIDs{}
	for _, r := range rows {
		ids, ok := sets[r.bucket]
		if !ok {
			ids = map[string]struct{}{}
			sets[r.bucket] = ids
		}
		ids[r.id] = struct{}{}
	}
	return sets
}

// idSetsOfRecords collects the (bucket,id) pairs of one PostgreSQL listing.
func idSetsOfRecords(bucket string, records []store.Record) bucketIDs {
	ids := map[string]struct{}{}
	for _, record := range records {
		ids[record.Key] = struct{}{}
	}
	return bucketIDs{bucket: ids}
}

// missingIDSets returns the (bucket,id) pairs of want that got does not hold.
// A pair present in want but absent in got means the record did not land in
// PostgreSQL, whatever the counts say.
func missingIDSets(want, got bucketIDs) []string {
	var missing []string
	buckets := make([]string, 0, len(want))
	for bucket := range want {
		buckets = append(buckets, bucket)
	}
	sort.Strings(buckets)
	for _, bucket := range buckets {
		ids := got[bucket]
		for id := range want[bucket] {
			if _, ok := ids[id]; !ok {
				missing = append(missing, bucket+"/"+id)
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// verifyIDSets fails when any (bucket,id) of the source is missing in the
// target. Extra PostgreSQL records are allowed: the migration overwrites by
// (bucket,id) and never removes anything.
func verifyIDSets(want, got bucketIDs) error {
	missing := missingIDSets(want, got)
	if len(missing) == 0 {
		return nil
	}
	const shown = 5
	if len(missing) > shown {
		return fmt.Errorf("%d record(s) missing in PostgreSQL, for example %s", len(missing), strings.Join(missing[:shown], ", "))
	}
	return fmt.Errorf("%d record(s) missing in PostgreSQL: %s", len(missing), strings.Join(missing, ", "))
}

func run(src string, dry bool) error {
	if src == "" {
		return fmt.Errorf("-sqlite is required")
	}
	dsn, err := sqliteDSN(src)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	rs, err := db.Query(`SELECT bucket, id, body FROM records ORDER BY bucket, id`)
	if err != nil {
		return err
	}
	var rows []row
	counts := map[string]int{}
	for rs.Next() {
		var r row
		var body string
		if err := rs.Scan(&r.bucket, &r.id, &body); err != nil {
			rs.Close()
			return err
		}
		r.body = json.RawMessage(body)
		rows = append(rows, r)
		counts[r.bucket]++
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	names := make([]string, 0, len(counts))
	for b := range counts {
		names = append(names, b)
	}
	sort.Strings(names)
	for _, b := range names {
		fmt.Printf("%-20s %d\n", b, counts[b])
	}
	fmt.Printf("total %d records\n", len(rows))
	if dry {
		return nil
	}
	pgDSN := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if pgDSN == "" {
		return fmt.Errorf("POOLS_PG_DSN is not set")
	}
	st, err := pg.Open(pgDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	// One transaction: either every record lands or none does. The context
	// bounds the write so a blocked connection cannot hang the tool.
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), pgTimeout)
	defer cancelWrite()
	err = st.Update(writeCtx, func(tx store.Tx) error {
		for _, r := range rows {
			if err := tx.Put(r.bucket, r.id, r.body); err != nil {
				return fmt.Errorf("%s/%s: %w", r.bucket, r.id, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Verify by reading the exact id sets back from PostgreSQL: every
	// (bucket,id) of the source must be present, not only the counts.
	want := idSetsOfRows(rows)
	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), pgTimeout)
	defer cancelVerify()
	return st.View(verifyCtx, func(r store.Reader) error {
		got := bucketIDs{}
		for _, b := range names {
			records, err := r.List(b)
			if err != nil {
				return err
			}
			for bucket, ids := range idSetsOfRecords(b, records) {
				got[bucket] = ids
			}
		}
		if err := verifyIDSets(want, got); err != nil {
			return err
		}
		fmt.Println("verify ok: every SQLite (bucket,id) is present in PostgreSQL")
		return nil
	})
}

// End of file: cmd/pools-migrate/main.go
