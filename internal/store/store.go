package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/halfisnine/upstream-gateway/internal/interceptlog"
	_ "github.com/lib/pq"
	"time"
)

var ErrConflict = errors.New("version_conflict")
var ErrNodeConflict = errors.New("node_id_in_use")

type Version struct {
	ID      int64     `json:"id"`
	Digest  string    `json:"digest"`
	Note    string    `json:"note"`
	Created time.Time `json:"created_at"`
	Data    []byte    `json:"-"`
}
type Node struct {
	ID       string             `json:"id"`
	Instance string             `json:"instance"`
	Desired  int64              `json:"desired_version"`
	Applied  int64              `json:"applied_version"`
	Digest   string             `json:"digest"`
	Error    string             `json:"error"`
	Seen     time.Time          `json:"last_seen"`
	Inflight int64              `json:"inflight"`
	Online   bool               `json:"online"`
	Logs     interceptlog.Stats `json:"logs"`
}
type Store struct {
	DB    *sql.DB
	LogDB *sql.DB
}

func (s *Store) Close() {
	s.DB.Close()
	if s.LogDB != nil {
		s.LogDB.Close()
	}
}

func Connect(dsn string) (*Store, error) {
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		return nil, errors.New("invalid_database_configuration")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	logs, e := sql.Open("postgres", dsn)
	if e != nil {
		db.Close()
		return nil, errors.New("invalid_database_configuration")
	}
	logs.SetMaxOpenConns(1)
	logs.SetMaxIdleConns(1)
	logs.SetConnMaxLifetime(5 * time.Minute)
	return &Store{DB: db, LogDB: logs}, nil
}
func (s *Store) Init(ctx context.Context) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `
SELECT pg_advisory_xact_lock(726318011);
CREATE TABLE IF NOT EXISTS gateway_versions (
 id BIGINT PRIMARY KEY CHECK (id > 0), digest TEXT NOT NULL,
 note TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), data BYTEA NOT NULL
);
CREATE TABLE IF NOT EXISTS gateway_nodes (
 id TEXT PRIMARY KEY, instance TEXT NOT NULL, desired BIGINT NOT NULL,
 applied BIGINT NOT NULL, digest TEXT NOT NULL, error TEXT NOT NULL,
 seen TIMESTAMPTZ NOT NULL, inflight BIGINT NOT NULL
);
ALTER TABLE gateway_nodes ADD COLUMN IF NOT EXISTS log_stats JSONB NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS gateway_interceptions (
 id TEXT PRIMARY KEY, occurred_at TIMESTAMPTZ NOT NULL, request_id TEXT NOT NULL,
 channel TEXT NOT NULL, rule_id TEXT NOT NULL, node TEXT NOT NULL, actual_status INTEGER NOT NULL,
 event JSONB NOT NULL CHECK (octet_length(event::text)<=2048)
);
ALTER TABLE gateway_interceptions DROP CONSTRAINT IF EXISTS gateway_interceptions_event_check;
ALTER TABLE gateway_interceptions ADD CONSTRAINT gateway_interceptions_event_check CHECK(octet_length(event::text)<=8192);
CREATE INDEX IF NOT EXISTS gateway_interceptions_time ON gateway_interceptions(occurred_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS gateway_interceptions_request ON gateway_interceptions(request_id);
CREATE TABLE IF NOT EXISTS gateway_log_maintenance(id INTEGER PRIMARY KEY CHECK(id=1),last_run TIMESTAMPTZ NOT NULL);
INSERT INTO gateway_log_maintenance VALUES(1,'epoch') ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION gateway_reject_version_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'published_snapshot_is_immutable'; END; $$;
DROP TRIGGER IF EXISTS gateway_version_immutable ON gateway_versions;
CREATE TRIGGER gateway_version_immutable BEFORE UPDATE OR DELETE ON gateway_versions
FOR EACH ROW EXECUTE FUNCTION gateway_reject_version_mutation();
`)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Latest(ctx context.Context) (int64, error) {
	var n int64
	e := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM gateway_versions`).Scan(&n)
	return n, e
}
func (s *Store) Version(ctx context.Context, id int64) (Version, error) {
	var v Version
	e := s.DB.QueryRowContext(ctx, `SELECT id,digest,note,created_at,data FROM gateway_versions WHERE id=$1`, id).Scan(&v.ID, &v.Digest, &v.Note, &v.Created, &v.Data)
	return v, e
}
func (s *Store) Versions(ctx context.Context, before int64) ([]Version, error) {
	if before <= 0 {
		before = 9223372036854775807
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT id,digest,note,created_at FROM gateway_versions WHERE id<$1 ORDER BY id DESC LIMIT 100`, before)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		if e = rows.Scan(&v.ID, &v.Digest, &v.Note, &v.Created); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Publish(ctx context.Context, base int64, data []byte, digest, note string) (Version, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Version{}, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(726318012)`); e != nil {
		return Version{}, e
	}
	var current int64
	if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM gateway_versions`).Scan(&current); e != nil {
		return Version{}, e
	}
	if current != base {
		return Version{}, ErrConflict
	}
	v := Version{ID: current + 1, Digest: digest, Note: note}
	if e = tx.QueryRowContext(ctx, `INSERT INTO gateway_versions(id,digest,note,data) VALUES($1,$2,$3,$4) RETURNING created_at`, v.ID, digest, note, data).Scan(&v.Created); e != nil {
		return Version{}, e
	}
	return v, tx.Commit()
}
func (s *Store) Report(ctx context.Context, n Node) error {
	stats, _ := json.Marshal(n.Logs)
	result, e := s.DB.ExecContext(ctx, `INSERT INTO gateway_nodes(id,instance,desired,applied,digest,error,seen,inflight,log_stats) VALUES($1,$2,$3,$4,$5,$6,now(),$7,$8) ON CONFLICT(id) DO UPDATE SET instance=EXCLUDED.instance, desired=EXCLUDED.desired, applied=EXCLUDED.applied, digest=EXCLUDED.digest,error=EXCLUDED.error,seen=now(),inflight=EXCLUDED.inflight,log_stats=EXCLUDED.log_stats WHERE gateway_nodes.instance=EXCLUDED.instance OR gateway_nodes.seen < now()-interval '20 seconds'`, n.ID, n.Instance, n.Desired, n.Applied, n.Digest, n.Error, n.Inflight, string(stats))
	if e != nil {
		return e
	}
	count, e := result.RowsAffected()
	if e == nil && count == 0 {
		return ErrNodeConflict
	}
	return e
}
func (s *Store) Nodes(ctx context.Context) ([]Node, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,desired,applied,digest,error,seen,inflight,seen>now()-interval '20 seconds',instance,log_stats FROM gateway_nodes ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var n Node
		var raw []byte
		if e = rows.Scan(&n.ID, &n.Desired, &n.Applied, &n.Digest, &n.Error, &n.Seen, &n.Inflight, &n.Online, &n.Instance, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &n.Logs); e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Release only the caller's lease; a rejected duplicate cannot evict its owner.
func (s *Store) Release(ctx context.Context, id, instance string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE gateway_nodes SET seen=now()-interval '21 seconds',error='stopped',inflight=0 WHERE id=$1 AND instance=$2`, id, instance)
	return err
}
