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

// Only lifecycle metadata exists at this stage. Comparing the complete schema
// refuses partial initialization, added objects and unrecognized layouts.
const metadataDDL = `CREATE TABLE journal_identity (
 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
 authority BLOB NOT NULL CHECK (length(authority) BETWEEN 1 AND 256),
 store_id BLOB NOT NULL CHECK (length(store_id) = 16),
 incarnation_id BLOB NOT NULL CHECK (length(incarnation_id) = 16)
) STRICT`

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
	for _, statement := range []string{metadataDDL, "PRAGMA application_id=1096043084", "PRAGMA user_version=1"} {
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
	if count != 1 {
		return Info{}, errors.New("journal: incomplete or foreign schema")
	}
	var ddl string
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type='table' AND name='journal_identity' AND length(sql) <= 1024").Scan(&ddl); err != nil {
		return Info{}, err
	}
	if ddl != metadataDDL {
		return Info{}, errors.New("journal: incompatible identity schema")
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
