package authoritystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/d115"
)

var d115MigrationMarker = schemaObject{"schema_migrations", "table", strings.Replace(
	strings.Replace(routeIndexMigrationMarker.sql, "21)", "21, 22)", 1),
	"name = 'm6-step-11-normalized-repo-route-index'))",
	"name = 'm6-step-11-normalized-repo-route-index') OR (version = 22 AND name = 'd115-create-domain-trust-and-replay'))", 1)}

var d115Schema = []schemaObject{
	{"d115_provisioning_realms", "table", `CREATE TABLE d115_provisioning_realms (
  provisioning_realm_id TEXT PRIMARY KEY,
  schema TEXT NOT NULL CHECK(schema='wipd.create-domain-trust/1'),
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  generation INTEGER NOT NULL CHECK(generation > 0)
) STRICT`},
	{"d115_provisioning_realm_update", "trigger", `CREATE TRIGGER d115_provisioning_realm_update BEFORE UPDATE ON d115_provisioning_realms
WHEN NEW.provisioning_realm_id != OLD.provisioning_realm_id OR NEW.schema != OLD.schema OR NEW.generation != OLD.generation+1
BEGIN SELECT RAISE(ABORT,'invalid D115 realm update'); END`},
	{"d115_provisioning_realm_no_delete", "trigger", `CREATE TRIGGER d115_provisioning_realm_no_delete BEFORE DELETE ON d115_provisioning_realms BEGIN SELECT RAISE(ABORT,'immutable D115 realm'); END`},
	{"d115_operator_keys", "table", `CREATE TABLE d115_operator_keys (
  key_id TEXT PRIMARY KEY,
  provisioning_realm_id TEXT NOT NULL REFERENCES d115_provisioning_realms(provisioning_realm_id) ON DELETE RESTRICT,
  public_key BLOB NOT NULL CHECK(length(public_key)=32),
  scope TEXT NOT NULL CHECK(scope='repo.create-local@v1:new-default-domain+initial-repo-membership'),
  audience TEXT NOT NULL,
  not_before TEXT NOT NULL,
  not_after TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('active','revoked')),
  CHECK(audience=provisioning_realm_id),
  UNIQUE(provisioning_realm_id,key_id)
) STRICT`},
	{"d115_operator_key_update", "trigger", `CREATE TRIGGER d115_operator_key_update BEFORE UPDATE ON d115_operator_keys
WHEN OLD.state != 'active' OR NEW.state != 'revoked' OR NEW.key_id != OLD.key_id OR NEW.provisioning_realm_id != OLD.provisioning_realm_id OR
  NEW.public_key != OLD.public_key OR NEW.scope != OLD.scope OR NEW.audience != OLD.audience OR NEW.not_before != OLD.not_before OR NEW.not_after != OLD.not_after
BEGIN SELECT RAISE(ABORT,'D115 operator key is immutable except revocation'); END`},
	{"d115_operator_key_no_delete", "trigger", `CREATE TRIGGER d115_operator_key_no_delete BEFORE DELETE ON d115_operator_keys BEGIN SELECT RAISE(ABORT,'retained D115 operator key'); END`},
	{"d115_one_active_operator_key", "index", `CREATE UNIQUE INDEX d115_one_active_operator_key ON d115_operator_keys(provisioning_realm_id,scope,audience) WHERE state='active'`},
	{"d115_grant_consumptions", "table", `CREATE TABLE d115_grant_consumptions (
  provisioning_realm_id TEXT NOT NULL REFERENCES d115_provisioning_realms(provisioning_realm_id) ON DELETE RESTRICT,
  grant_id TEXT NOT NULL,
  grant_digest TEXT NOT NULL,
  signer_key_id TEXT NOT NULL,
  owner_root_key_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  committed_at TEXT NOT NULL,
  PRIMARY KEY(provisioning_realm_id,grant_id),
  UNIQUE(provisioning_realm_id,command_id)
) STRICT`},
	{"d115_create_domain_commands", "table", `CREATE TABLE d115_create_domain_commands (
  provisioning_realm_id TEXT NOT NULL REFERENCES d115_provisioning_realms(provisioning_realm_id) ON DELETE RESTRICT,
  command_id TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  grant_digest TEXT NOT NULL,
  owner_root_public_key BLOB NOT NULL CHECK(length(owner_root_public_key)=32),
  owner_root_key_id TEXT NOT NULL,
  receipt BLOB NOT NULL CHECK(length(receipt) BETWEEN 1 AND 8192),
  PRIMARY KEY(provisioning_realm_id,command_id)
) STRICT`},
	{"d115_create_domain_command_immutable", "trigger", `CREATE TRIGGER d115_create_domain_command_immutable BEFORE UPDATE ON d115_create_domain_commands BEGIN SELECT RAISE(ABORT,'immutable D115 command receipt'); END`},
	{"d115_create_domain_command_no_delete", "trigger", `CREATE TRIGGER d115_create_domain_command_no_delete BEFORE DELETE ON d115_create_domain_commands BEGIN SELECT RAISE(ABORT,'retained D115 command receipt'); END`},
	{"d115_grant_consumption_immutable", "trigger", `CREATE TRIGGER d115_grant_consumption_immutable BEFORE UPDATE ON d115_grant_consumptions BEGIN SELECT RAISE(ABORT,'immutable D115 grant consumption'); END`},
	{"d115_grant_consumption_no_delete", "trigger", `CREATE TRIGGER d115_grant_consumption_no_delete BEFORE DELETE ON d115_grant_consumptions BEGIN SELECT RAISE(ABORT,'retained D115 grant consumption'); END`},
	{"d115_domain_genesis", "table", `CREATE TABLE d115_domain_genesis (
  domain_id TEXT PRIMARY KEY REFERENCES domains(domain_id) ON DELETE RESTRICT,
  event_count INTEGER NOT NULL CHECK(event_count=0),
  event_id TEXT,
  prefix_digest TEXT NOT NULL
) STRICT`},
	{"d115_domain_genesis_immutable", "trigger", `CREATE TRIGGER d115_domain_genesis_immutable BEFORE UPDATE ON d115_domain_genesis BEGIN SELECT RAISE(ABORT,'immutable D115 genesis'); END`},
	{"d115_domain_genesis_no_delete", "trigger", `CREATE TRIGGER d115_domain_genesis_no_delete BEFORE DELETE ON d115_domain_genesis BEGIN SELECT RAISE(ABORT,'retained D115 genesis'); END`},
}

func installD115(db *sql.DB) error {
	realmID, err := authorityULID(time.Now())
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 21 {
		return fmt.Errorf("%w: D115 registry requires schema 21", ErrInvalidStore)
	}
	for _, object := range d115Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, d115MigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations`, `DROP TABLE old_schema_migrations`,
		`INSERT INTO schema_migrations VALUES(22,'d115-create-domain-trust-and-replay')`,
		`PRAGMA user_version=22`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO d115_provisioning_realms(provisioning_realm_id,schema,enabled,generation) VALUES(?,?,0,1)`, realmID, "wipd.create-domain-trust/1"); err != nil {
		return err
	}
	return tx.Commit()
}

func authorityULID(now time.Time) (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var data [16]byte
	millis := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		data[i] = byte(millis)
		millis >>= 8
	}
	if _, err := rand.Read(data[6:]); err != nil {
		return "", err
	}
	value := new(big.Int).SetBytes(data[:])
	base := big.NewInt(32)
	var result [26]byte
	for i := len(result) - 1; i >= 0; i-- {
		q, r := new(big.Int), new(big.Int)
		q.QuoRem(value, base, r)
		result[i] = alphabet[r.Int64()]
		value = q
	}
	return string(result[:]), nil
}

func checkD115State(db *sql.DB) error {
	var realmID, schema string
	var enabled, generation int64
	if err := db.QueryRow(`SELECT provisioning_realm_id,schema,enabled,generation FROM d115_provisioning_realms`).Scan(&realmID, &schema, &enabled, &generation); err != nil {
		return fmt.Errorf("%w: missing D115 realm", ErrInvalidStore)
	}
	if !ulid.MatchString(realmID) || schema != "wipd.create-domain-trust/1" || (enabled != 0 && enabled != 1) || generation < 1 {
		return fmt.Errorf("%w: invalid D115 realm registry", ErrInvalidStore)
	}
	var realms int
	if err := db.QueryRow(`SELECT count(*) FROM d115_provisioning_realms`).Scan(&realms); err != nil || realms != 1 {
		return fmt.Errorf("%w: D115 registry must have exactly one realm", ErrInvalidStore)
	}
	var active int
	if err := db.QueryRow(`SELECT count(*) FROM d115_operator_keys WHERE provisioning_realm_id=? AND state='active'`, realmID).Scan(&active); err != nil || active > 1 || (enabled == 1 && active != 1) {
		return fmt.Errorf("%w: invalid active D115 operator-key set", ErrInvalidStore)
	}
	rows, err := db.Query(`SELECT key_id,public_key,scope,audience,not_before,not_after,state FROM d115_operator_keys WHERE provisioning_realm_id=?`, realmID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var keyID, scope, audience, notBefore, notAfter, state string
		var publicKey []byte
		if err = rows.Scan(&keyID, &publicKey, &scope, &audience, &notBefore, &notAfter, &state); err != nil {
			return err
		}
		if len(publicKey) != 32 || scope != "repo.create-local@v1:new-default-domain+initial-repo-membership" || audience != realmID ||
			(state != "active" && state != "revoked") || !canonicalD115Time(notBefore) || !canonicalD115Time(notAfter) {
			return fmt.Errorf("%w: invalid D115 operator-key record", ErrInvalidStore)
		}
		computedKeyID, keyErr := d115.KeyID(ed25519.PublicKey(publicKey))
		if keyErr != nil || computedKeyID != keyID {
			return fmt.Errorf("%w: mismatched D115 operator-key identity", ErrInvalidStore)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	return nil
}
