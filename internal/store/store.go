package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// Memory-mapped reads and a 64 MiB page cache keep a scan of millions of files from re-reading pages.
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL; PRAGMA cache_size=-65536; PRAGMA mmap_size=1073741824;
 CREATE TABLE IF NOT EXISTS packages (key TEXT PRIMARY KEY, id TEXT NOT NULL, version TEXT NOT NULL, platform TEXT NOT NULL, status TEXT NOT NULL, payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS packages_id ON packages(id);
 CREATE TABLE IF NOT EXISTS uploads (key TEXT PRIMARY KEY, sha TEXT NOT NULL, package_key TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS audit (id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, detail TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, updated TEXT NOT NULL, payload TEXT NOT NULL);
 -- files remembers each VSIX's size, modification time, and identity (empty key for an invalid file),
 -- so a scan only reopens files that changed. dirty queues package keys the next scan must settle.
 CREATE TABLE IF NOT EXISTS files (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, size INTEGER NOT NULL, mtime INTEGER NOT NULL, key TEXT NOT NULL, sha TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS files_key ON files(key);
 CREATE TABLE IF NOT EXISTS dirty (key TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
 -- limits holds per-extension version limits (0 keeps every version); meta 'keep' holds the library default.
 CREATE TABLE IF NOT EXISTS limits (id TEXT PRIMARY KEY, keep INTEGER NOT NULL);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.DB.Close() }

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *Store) Put(p vsix.Package) error { return Put(s.DB, p) }

// Put writes a package record through x, which may be a transaction.
func Put(x Execer, p vsix.Package) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = x.Exec(`INSERT INTO packages VALUES(?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,payload=excluded.payload`, p.Key(), p.ID, p.Version, p.Platform, p.Status, string(b))
	return e
}

// Journal records a publication in progress and queues its key, so the next scan settles it
// if the process stops before Publish.
func (s *Store) Journal(p vsix.Package) error {
	return s.InTx(func(tx *sql.Tx) error {
		if e := Put(tx, p); e != nil {
			return e
		}
		_, e := tx.Exec(`INSERT OR IGNORE INTO dirty VALUES(?)`, p.Key())
		return e
	})
}

// Publish records a stored package together with its file's modification time,
// so the next scan can trust the unchanged file without rehashing it.
func (s *Store) Publish(p vsix.Package, mtime int64) error {
	return s.InTx(func(tx *sql.Tx) error {
		if e := Put(tx, p); e != nil {
			return e
		}
		return RememberFile(tx, p.Filename, p.Size, mtime, p.Key(), p.SHA256)
	})
}

// RememberFile records what a VSIX file contains. If the name previously held another package,
// that package is queued for the next scan.
func RememberFile(tx *sql.Tx, name string, size, mtime int64, key, sha string) error {
	var previous string
	if e := tx.QueryRow(`SELECT key FROM files WHERE name=?`, name).Scan(&previous); e == nil && previous != key && previous != "" {
		if _, e = tx.Exec(`INSERT OR IGNORE INTO dirty VALUES(?)`, previous); e != nil {
			return e
		}
	} else if e != nil && e != sql.ErrNoRows {
		return e
	}
	_, e := tx.Exec(`INSERT INTO files(name,size,mtime,key,sha) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET size=excluded.size,mtime=excluded.mtime,key=excluded.key,sha=excluded.sha`, name, size, mtime, key, sha)
	return e
}

// InTx runs f in one transaction, committing only if it succeeds.
func (s *Store) InTx(f func(*sql.Tx) error) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	if e = f(tx); e != nil {
		tx.Rollback()
		return e
	}
	return tx.Commit()
}

// MarkDeleted records that these packages' files are being deleted by an operator or the version limit,
// keeping them in the index, and queues them so the next scan removes any file left behind if the
// process stops mid-deletion.
func (s *Store) MarkDeleted(packages []vsix.Package, at, by, detail string) error {
	return s.InTx(func(tx *sql.Tx) error {
		for _, p := range packages {
			p.Status, p.DeletedAt, p.DeletedBy = "deleted", at, by
			if e := Put(tx, p); e != nil {
				return e
			}
			if _, e := tx.Exec(`INSERT OR IGNORE INTO dirty VALUES(?)`, p.Key()); e != nil {
				return e
			}
		}
		return AuditIn(tx, by, "deleted_versions", detail)
	})
}

// KeepDefault is the library's version limit; 0 keeps every version.
func (s *Store) KeepDefault() (int, error) {
	var keep int
	e := s.DB.QueryRow(`SELECT coalesce((SELECT CAST(v AS INTEGER) FROM meta WHERE k='keep'),0)`).Scan(&keep)
	return keep, e
}
func (s *Store) SetKeepDefault(keep int) error {
	_, e := s.DB.Exec(`INSERT INTO meta VALUES('keep',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, fmt.Sprint(keep))
	return e
}

// Keep reports an extension's version limit and whether it is the extension's own or the library default.
func (s *Store) Keep(id string) (keep int, own bool, e error) {
	e = s.DB.QueryRow(`SELECT keep FROM limits WHERE id=?`, id).Scan(&keep)
	if e == nil {
		return keep, true, nil
	}
	if e != sql.ErrNoRows {
		return 0, false, e
	}
	keep, e = s.KeepDefault()
	return keep, false, e
}

// SetKeep gives an extension its own version limit, or with nil makes it follow the library default.
func (s *Store) SetKeep(id string, keep *int) error {
	if keep == nil {
		_, e := s.DB.Exec(`DELETE FROM limits WHERE id=?`, id)
		return e
	}
	_, e := s.DB.Exec(`INSERT INTO limits VALUES(?,?) ON CONFLICT(id) DO UPDATE SET keep=excluded.keep`, id, *keep)
	return e
}

// Overrides lists the extensions with their own version limit.
func (s *Store) Overrides() (map[string]int, error) {
	rows, e := s.DB.Query(`SELECT id, keep FROM limits`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var keep int
		if e = rows.Scan(&id, &keep); e != nil {
			return nil, e
		}
		out[id] = keep
	}
	return out, rows.Err()
}

// Extensions streams every extension's packages, one extension at a time in ID order.
func (s *Store) Extensions(f func(id string, packages []vsix.Package) error) error {
	after := ""
	for {
		var ids []string
		rows, e := s.DB.Query(`SELECT DISTINCT id FROM packages WHERE id>? ORDER BY id LIMIT 1000`, after)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		rows.Close()
		if e = rows.Err(); e != nil || len(ids) == 0 {
			return e
		}
		for _, id := range ids {
			packages, _, e := s.List(id, 2147483647, 0)
			if e == nil {
				e = f(id, packages)
			}
			if e != nil {
				return e
			}
		}
		after = ids[len(ids)-1]
	}
}

// FilesHolding lists the VSIX files remembered for these package keys.
func (s *Store) FilesHolding(keys []string) ([]string, error) {
	var names []string
	for low := 0; low < len(keys); low += 1000 {
		batch := keys[low:min(low+1000, len(keys))]
		args := make([]any, len(batch))
		for i, k := range batch {
			args[i] = k
		}
		rows, e := s.DB.Query(`SELECT name FROM files WHERE key IN (?`+strings.Repeat(",?", len(args)-1)+`)`, args...)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				rows.Close()
				return nil, e
			}
			names = append(names, name)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return nil, e
		}
	}
	return names, nil
}

// ForgetFiles drops the remembered state of files that were removed.
func (s *Store) ForgetFiles(names []string) error {
	return s.InTx(func(tx *sql.Tx) error {
		for _, name := range names {
			if _, e := tx.Exec(`DELETE FROM files WHERE name=?`, name); e != nil {
				return e
			}
		}
		return nil
	})
}

func (s *Store) StatusCounts() (map[string]int, error) {
	rows, e := s.DB.Query(`SELECT status, count(*) FROM packages GROUP BY status`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if e = rows.Scan(&status, &n); e != nil {
			return nil, e
		}
		out[status] = n
	}
	return out, rows.Err()
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

// Totals counts stored content, the package records that need attention (missing, pending, or conflict),
// and those an administrator deleted.
type Totals struct {
	Extensions   int   `json:"extensions"`
	Versions     int   `json:"versions"`
	Packages     int   `json:"packages"`
	Bytes        int64 `json:"bytes"`
	Attention    int   `json:"attention"`
	Deleted      int   `json:"deleted"`
	DeletedBytes int64 `json:"deletedBytes"`
}

func (s *Store) Totals() (Totals, error) {
	var t Totals
	e := s.DB.QueryRow(`SELECT count(DISTINCT CASE WHEN status='stored' THEN id END), count(DISTINCT CASE WHEN status='stored' THEN id||'@'||version END),
	 coalesce(sum(status='stored'),0), coalesce(sum(CASE WHEN status='stored' THEN json_extract(payload,'$.size') END),0), coalesce(sum(status NOT IN ('stored','deleted')),0),
	 coalesce(sum(status='deleted'),0), coalesce(sum(CASE WHEN status='deleted' THEN json_extract(payload,'$.size') END),0) FROM packages`).Scan(&t.Extensions, &t.Versions, &t.Packages, &t.Bytes, &t.Attention, &t.Deleted, &t.DeletedBytes)
	return t, e
}

// Extension summarizes every package record that shares an extension ID.
type Extension struct {
	ID            string         `json:"id"`
	DisplayName   string         `json:"displayName"`
	LatestVersion string         `json:"latestVersion"`
	Versions      int            `json:"versions"`
	Platforms     []string       `json:"platforms"`
	Packages      int            `json:"packages"`
	Bytes         int64          `json:"bytes"`
	UpdatedAt     string         `json:"updatedAt,omitempty"`
	StatusCounts  map[string]int `json:"statusCounts"`
	Keep          int            `json:"keep"`       // newest versions kept; 0 keeps all
	KeepSource    string         `json:"keepSource"` // "library" default or the "extension"'s own
}

// CatalogQuery selects extension summaries. ID matches exactly; Search matches part of an ID or display name.
type CatalogQuery struct {
	ID, Search    string
	Sort          string // name, versions, size, or updated
	Attention     bool   // only extensions with records that are not stored
	Limit, Offset int
}

var catalogOrder = map[string]string{"name": "id", "versions": "versions DESC, id", "size": "bytes DESC, id", "updated": "updated DESC, id"}
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (s *Store) Catalog(q CatalogQuery) ([]Extension, int, error) {
	order, ok := catalogOrder[q.Sort]
	if !ok {
		order = catalogOrder["name"]
	}
	where, args := "", []any{}
	if q.ID != "" {
		where, args = " WHERE id=?", append(args, q.ID)
	} else if q.Search != "" {
		// Select whole extensions, so versions whose display name differs still count.
		pattern := "%" + likeEscaper.Replace(q.Search) + "%"
		where = ` WHERE id IN (SELECT id FROM packages WHERE id LIKE ? ESCAPE '\' OR json_extract(payload,'$.displayName') LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern)
	}
	having := ""
	if q.Attention {
		having = " HAVING sum(status NOT IN ('stored','deleted'))>0"
	}
	// Deleted versions stay listed in statusCounts but are not counted as versions, packages, or platforms.
	groups := `SELECT id, count(DISTINCT CASE WHEN status<>'deleted' THEN version END) AS versions, sum(status<>'deleted') AS packages,
	 coalesce(sum(CASE WHEN status='stored' THEN json_extract(payload,'$.size') END),0) AS bytes,
	 coalesce(max(json_extract(payload,'$.storedAt')),'') AS updated, coalesce(group_concat(DISTINCT CASE WHEN status<>'deleted' THEN platform END),'') FROM packages` + where + " GROUP BY id" + having
	var total int
	if e := s.DB.QueryRow("SELECT count(*) FROM ("+groups+")", args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := s.DB.Query(groups+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(args, q.Limit, q.Offset)...)
	if e != nil {
		return nil, 0, e
	}
	out := []Extension{}
	index := map[string]int{}
	for rows.Next() {
		var x Extension
		var platforms string
		if e = rows.Scan(&x.ID, &x.Versions, &x.Packages, &x.Bytes, &x.UpdatedAt, &platforms); e != nil {
			rows.Close()
			return nil, 0, e
		}
		x.Platforms = []string{}
		if platforms != "" {
			x.Platforms = strings.Split(platforms, ",")
			sort.Strings(x.Platforms)
		}
		x.StatusCounts = map[string]int{}
		index[x.ID] = len(out)
		out = append(out, x)
	}
	rows.Close()
	if e = rows.Err(); e != nil || len(out) == 0 {
		return out, total, e
	}
	// Latest version and statuses need semantic version order, which SQL cannot provide.
	ids := make([]any, 0, len(out))
	for _, x := range out {
		ids = append(ids, x.ID)
	}
	library, e := s.KeepDefault()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		out[i].Keep, out[i].KeepSource = library, "library"
	}
	limits, e := s.DB.Query(`SELECT id, keep FROM limits WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, ids...)
	if e != nil {
		return nil, 0, e
	}
	for limits.Next() {
		var id string
		var keep int
		if e = limits.Scan(&id, &keep); e != nil {
			limits.Close()
			return nil, 0, e
		}
		out[index[id]].Keep, out[index[id]].KeepSource = keep, "extension"
	}
	limits.Close()
	if e = limits.Err(); e != nil {
		return nil, 0, e
	}
	rows, e = s.DB.Query(`SELECT id, version, status, coalesce(json_extract(payload,'$.displayName'),'') FROM packages WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, ids...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	// The latest version is the newest one not deleted, or the newest deleted one if all are.
	kept := make([]bool, len(out))
	for rows.Next() {
		var id, version, status, name string
		if e = rows.Scan(&id, &version, &status, &name); e != nil {
			return nil, 0, e
		}
		i := index[id]
		x := &out[i]
		x.StatusCounts[status]++
		live := status != "deleted"
		if x.LatestVersion == "" || live && !kept[i] || live == kept[i] && vsix.CompareVersions(version, x.LatestVersion) > 0 {
			x.LatestVersion, x.DisplayName, kept[i] = version, name, live
		}
	}
	return out, total, rows.Err()
}
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
	return AuditIn(s.DB, actor, action, detail)
}

// AuditIn records an audit event through x, which may be a transaction.
func AuditIn(x Execer, actor, action, detail string) error {
	_, e := x.Exec(`INSERT INTO audit(at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339), actor, action, detail)
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
