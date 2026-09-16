package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }

func Open(dir string) (*Store, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", filepath.Join(dir, "manager.db"))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;
 CREATE TABLE IF NOT EXISTS packages (key TEXT PRIMARY KEY, id TEXT NOT NULL, version TEXT NOT NULL, platform TEXT NOT NULL, status TEXT NOT NULL, payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS packages_id ON packages(id);
 CREATE TABLE IF NOT EXISTS uploads (key TEXT PRIMARY KEY, sha TEXT NOT NULL, package_key TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS audit (id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, detail TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, updated TEXT NOT NULL, payload TEXT NOT NULL);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Put(p vsix.Package) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(`INSERT INTO packages VALUES(?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,payload=excluded.payload`, p.Key(), p.ID, p.Version, p.Platform, p.Status, string(b))
	return e
}
func (s *Store) Get(key string) (vsix.Package, error) {
	var b string
	var p vsix.Package
	e := s.DB.QueryRow(`SELECT payload FROM packages WHERE key=?`, key).Scan(&b)
	if e == nil {
		e = json.Unmarshal([]byte(b), &p)
	}
	return p, e
}
func (s *Store) List(id string, limit, offset int) ([]vsix.Package, int, error) {
	args := []any{}
	where := ""
	if id != "" {
		where = " WHERE id=?"
		args = append(args, id)
	}
	var total int
	e := s.DB.QueryRow("SELECT count(*) FROM packages"+where, args...).Scan(&total)
	if e != nil {
		return nil, 0, e
	}
	rows, e := s.DB.Query("SELECT payload FROM packages"+where+" ORDER BY id,version,platform LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	out := []vsix.Package{}
	for rows.Next() {
		var b string
		var p vsix.Package
		if e = rows.Scan(&b); e != nil {
			return nil, 0, e
		}
		if e = json.Unmarshal([]byte(b), &p); e != nil {
			return nil, 0, e
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}
func (s *Store) All() ([]vsix.Package, error) { p, _, e := s.List("", 2147483647, 0); return p, e }
func (s *Store) Upload(key, sha, pkg string) error {
	_, e := s.DB.Exec(`INSERT INTO uploads VALUES(?,?,?) ON CONFLICT(key) DO NOTHING`, key, sha, pkg)
	return e
}
func (s *Store) LookupUpload(key string) (string, string, error) {
	var sha, p string
	e := s.DB.QueryRow(`SELECT sha,package_key FROM uploads WHERE key=?`, key).Scan(&sha, &p)
	return sha, p, e
}
func (s *Store) Audit(actor, action, detail string) error {
	_, e := s.DB.Exec(`INSERT INTO audit(at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339), actor, action, detail)
	return e
}
func (s *Store) Events() ([]map[string]any, error) {
	rows, e := s.DB.Query(`SELECT id,at,actor,action,detail FROM audit ORDER BY id DESC LIMIT 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int
		var at, actor, action, detail string
		if e = rows.Scan(&id, &at, &actor, &action, &detail); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "at": at, "actor": actor, "action": action, "detail": detail})
	}
	return out, rows.Err()
}
func (s *Store) PutRun(id string, b []byte) error {
	if !json.Valid(b) {
		return fmt.Errorf("invalid JSON")
	}
	_, e := s.DB.Exec(`INSERT INTO runs VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET updated=excluded.updated,payload=excluded.payload`, id, time.Now().UTC().Format(time.RFC3339), string(b))
	return e
}
func (s *Store) Runs() ([]json.RawMessage, error) {
	rows, e := s.DB.Query(`SELECT payload FROM runs ORDER BY updated DESC LIMIT 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b string
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}
