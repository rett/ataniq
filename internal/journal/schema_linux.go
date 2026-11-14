package journal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
)

const applicationID = 0x41544a4c // ATJL
const schemaVersion = 1

// Comparing the complete v1 schema shipped in 0.0.3 refuses partial
// initialization, earlier layouts and added objects. Future evolution requires
// an explicit reviewed path; no real-state migration is performed here.
const metadataDDL = `CREATE TABLE journal_identity (
 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
 authority BLOB NOT NULL CHECK (length(authority) BETWEEN 1 AND 256),
 store_id BLOB NOT NULL CHECK (length(store_id) = 16),
 incarnation_id BLOB NOT NULL CHECK (length(incarnation_id) = 16)
) STRICT`

const attemptsDDL = `CREATE TABLE attempts (
 id BLOB PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 256),
 binding BLOB NOT NULL CHECK (length(binding) <= 1052672),
 expires INTEGER NOT NULL,
 grant_expires INTEGER NOT NULL,
 revoked INTEGER NOT NULL CHECK (revoked IN (0,1)),
 prepared INTEGER NOT NULL CHECK (prepared IN (0,1)),
 uncertain INTEGER NOT NULL CHECK (uncertain IN (0,1)),
 active INTEGER NOT NULL CHECK (active IN (0,1)),
 generation BLOB NOT NULL CHECK (length(generation) = 16),
 state TEXT NOT NULL CHECK (state IN ('prepared','started','denied','uncertain')),
 receipt_id BLOB NOT NULL CHECK (length(receipt_id) <= 256),
 evidence BLOB NOT NULL CHECK (length(evidence) <= 65536)
) STRICT, WITHOUT ROWID`

const grantsDDL = `CREATE TABLE grants (
 id BLOB PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 256),
 binding BLOB NOT NULL CHECK (length(binding) <= 1052672)
) STRICT, WITHOUT ROWID`

const evidenceDDL = `CREATE TABLE evidence (
 sequence INTEGER PRIMARY KEY,
 attempt_id BLOB NOT NULL REFERENCES attempts(id),
 receipt_id BLOB NOT NULL CHECK (length(receipt_id) BETWEEN 1 AND 256),
 state TEXT NOT NULL CHECK (state IN ('prepared','started','denied','uncertain')),
 evidence BLOB NOT NULL CHECK (length(evidence) <= 65536)
) STRICT`

const eventsDDL = `CREATE TABLE events (
 sequence INTEGER PRIMARY KEY,
 attempt_id BLOB NOT NULL REFERENCES attempts(id),
 kind TEXT NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
 detail BLOB NOT NULL CHECK (length(detail) <= 1052672)
) STRICT`

var schema = map[string]string{"journal_identity": metadataDDL, "attempts": attemptsDDL, "grants": grantsDDL, "evidence": evidenceDDL, "events": eventsDDL}

func initialize(ctx context.Context, db *sql.DB, authority string) error {
	tx, err := db.BeginTx(ctx, nil) // driver DSN selects BEGIN IMMEDIATE
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := initializeIdentity(ctx, tx, authority); err != nil {
		return err
	}
	return tx.Commit()
}

func initializeIdentity(ctx context.Context, tx *sql.Tx, authority string) error {
	store, incarnation := make([]byte, 16), make([]byte, 16)
	if _, err := rand.Read(store); err != nil {
		return err
	}
	if _, err := rand.Read(incarnation); err != nil {
		return err
	}
	for _, statement := range []string{metadataDDL, attemptsDDL, grantsDDL, evidenceDDL, eventsDDL, "PRAGMA application_id=1096043084", "PRAGMA user_version=1"} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO journal_identity VALUES (1, ?, ?, ?)", []byte(authority), store, incarnation)
	return err
}

func readIdentity(ctx context.Context, db *sql.DB, authority string) (Info, error) {
	var app, version int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return Info{}, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return Info{}, err
	}
	if app != applicationID || version != schemaVersion {
		return Info{}, errors.New("journal: unknown application or schema version")
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&count); err != nil {
		return Info{}, err
	}
	if count != len(schema) {
		return Info{}, errors.New("journal: incomplete or foreign schema")
	}
	for name, want := range schema {
		var ddl string
		if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type='table' AND name=? AND length(sql) <= 4096", name).Scan(&ddl); err != nil {
			return Info{}, err
		}
		if ddl != want {
			return Info{}, errors.New("journal: incompatible schema")
		}
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM journal_identity").Scan(&count); err != nil {
		return Info{}, err
	}
	if count != 1 {
		return Info{}, errors.New("journal: incomplete or ambiguous identity")
	}
	var stored, store, incarnation []byte
	if err := db.QueryRowContext(ctx, "SELECT authority, store_id, incarnation_id FROM journal_identity WHERE singleton=1 AND length(authority) BETWEEN 1 AND 256 AND length(store_id)=16 AND length(incarnation_id)=16").Scan(&stored, &store, &incarnation); err != nil {
		return Info{}, err
	}
	if string(stored) != authority || allZero(store) || allZero(incarnation) {
		return Info{}, errors.New("journal: authority mismatch or unknown identity")
	}
	return Info{Authority: authority, StoreID: hex.EncodeToString(store), IncarnationID: hex.EncodeToString(incarnation), SchemaVersion: version}, nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
