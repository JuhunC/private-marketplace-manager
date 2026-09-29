package server

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/store"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
)

// Directory entries and package keys handled per batch; each batch is one short transaction,
// so other requests (including the healthcheck) never wait long for the single database connection.
var scanBatch = 4096

// statWorkers overlaps file-metadata lookups, which dominate on network shares.
var statWorkers = 32

// scan reconciles the extension directory with the inventory. The files table remembers each VSIX's
// size, modification time, and identity, so only new or changed files are opened and hashed; an
// unchanged library costs one stat per file. Every batch commits the files it verified together with
// the package keys it affected (the dirty table), so an interrupted scan resumes safely.
type scan struct {
	s           *Server
	progress    func(done, total int)
	found       bitset // files rows whose file is still present
	seen        int
	done, total int
	changed     int
	grew        map[string]bool // extensions that gained stored versions, trimmed to their limit afterwards
}

type fileState struct {
	name        string
	size, mtime int64
	id          int64  // files row, or 0 when the file is new
	previous    string // key the files row held before this scan
}

func (s *Server) reconcileStorage(full bool, progress func(done, total int)) (int, error) {
	sc := &scan{s: s, progress: progress, grew: map[string]bool{}}
	if sc.progress == nil {
		sc.progress = func(int, int) {}
	}
	var trusted int
	if e := s.db.DB.QueryRow(`SELECT count(*) FROM meta WHERE k='files_trusted'`).Scan(&trusted); e != nil {
		return 0, e
	}
	var e error
	switch {
	case full:
		e = sc.forget()
	case trusted == 0:
		e = sc.adopt()
	}
	if e == nil {
		// Expected files: those remembered, or the records when everything is being rehashed.
		e = s.db.DB.QueryRow(`SELECT max((SELECT count(*) FROM files), CASE WHEN ? THEN (SELECT count(*) FROM packages) ELSE 0 END)`, full).Scan(&sc.total)
	}
	if e == nil {
		e = sc.walk()
	}
	if e == nil {
		e = sc.dropVanished()
	}
	if e == nil {
		e = sc.settle()
	}
	for id := range sc.grew {
		if e != nil {
			break
		}
		var n int
		n, e = s.trim(id)
		sc.changed += n
	}
	if e == nil {
		_, e = s.db.DB.Exec(`INSERT OR IGNORE INTO meta VALUES('files_trusted','1')`)
	}
	sc.progress(sc.done, sc.done)
	return sc.changed, e
}

// forget discards the remembered files, so every VSIX is hashed again and every record is settled.
func (sc *scan) forget() error {
	return sc.s.db.InTx(func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM files`, `DELETE FROM meta WHERE k='files_trusted'`, `INSERT OR IGNORE INTO dirty SELECT key FROM packages`} {
			if _, e := tx.Exec(q); e != nil {
				return e
			}
		}
		return nil
	})
}

// unknownTime marks a file adopted from a record; the walk trusts it if its size still matches.
const unknownTime = -1

// adopt runs once after an upgrade: stored records were verified by the previous version, so their files
// are remembered without rehashing. Every other record, including one whose filename another record
// already claims, is settled.
func (sc *scan) adopt() error {
	var last int64
	if e := sc.s.db.DB.QueryRow(`SELECT coalesce(max(rowid),0) FROM packages`).Scan(&last); e != nil {
		return e
	}
	type record struct {
		key, status string
		file        struct {
			Name   string `json:"filename"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		}
	}
	for low := int64(0); low <= last; low += int64(scanBatch) {
		if e := sc.s.ctx.Err(); e != nil {
			return e
		}
		high := low + int64(scanBatch) - 1
		var records []record
		rows, e := sc.s.db.DB.Query(`SELECT key, status, payload FROM packages WHERE rowid BETWEEN ? AND ?`, low, high)
		if e != nil {
			return e
		}
		for rows.Next() {
			var r record
			var payload []byte
			if e = rows.Scan(&r.key, &r.status, &payload); e == nil {
				e = json.Unmarshal(payload, &r.file)
			}
			if e != nil {
				rows.Close()
				return e
			}
			records = append(records, r)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		e = sc.s.db.InTx(func(tx *sql.Tx) error {
			remember, e := tx.Prepare(`INSERT OR IGNORE INTO files(name,size,mtime,key,sha) VALUES(?,?,?,?,?)`)
			if e != nil {
				return e
			}
			defer remember.Close()
			queue, e := tx.Prepare(`INSERT OR IGNORE INTO dirty VALUES(?)`)
			if e != nil {
				return e
			}
			defer queue.Close()
			for _, r := range records {
				if r.status == "stored" && r.file.Name != "" {
					res, e := remember.Exec(r.file.Name, r.file.Size, unknownTime, r.key, r.file.SHA256)
					if e != nil {
						return e
					}
					if n, _ := res.RowsAffected(); n == 1 {
						continue
					}
				}
				if _, e := queue.Exec(r.key); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		sc.progress(int(min(high, last)), int(last))
	}
	return nil
}

// walk streams the directory in batches. Listing and stat calls for the next batch overlap the
// database work on the current one, so a scan takes about as long as the slower of the two.
func (sc *scan) walk() error {
	d, e := os.Open(sc.s.cfg.Extensions)
	if e != nil {
		return e
	}
	type listed struct {
		files []*fileState
		err   error
	}
	batches, stop := make(chan listed, 2), make(chan struct{})
	var lister sync.WaitGroup
	defer lister.Wait()
	defer close(stop)
	lister.Go(func() {
		defer d.Close()
		defer close(batches)
		for {
			entries, err := d.ReadDir(scanBatch)
			if err == io.EOF {
				err = nil
			}
			select {
			case batches <- listed{sc.stat(entries), err}:
			case <-stop:
				return
			}
			if len(entries) == 0 || err != nil {
				return
			}
		}
	})
	for b := range batches {
		if e = sc.s.ctx.Err(); e == nil {
			e = b.err
		}
		if e == nil {
			e = sc.verify(b.files)
		}
		if e != nil {
			return e
		}
	}
	return nil
}

// stat reads the size and modification time of a batch's VSIX files, removing abandoned upload staging files.
func (sc *scan) stat(entries []os.DirEntry) []*fileState {
	var names []string
	for _, ent := range entries {
		name := ent.Name()
		if strings.HasPrefix(name, ".upload-") && strings.HasSuffix(name, ".part") {
			_ = os.Remove(filepath.Join(sc.s.cfg.Extensions, name))
		} else if strings.HasSuffix(strings.ToLower(name), ".vsix") {
			names = append(names, name)
		}
	}
	infos := make([]os.FileInfo, len(names))
	each(len(names), statWorkers, func(i int) { infos[i], _ = os.Lstat(filepath.Join(sc.s.cfg.Extensions, names[i])) })
	var files []*fileState
	for i, info := range infos {
		if info != nil && info.Mode().IsRegular() {
			files = append(files, &fileState{name: names[i], size: info.Size(), mtime: info.ModTime().UnixNano()})
		}
	}
	return files
}

// verify compares one batch of files with what was remembered, then opens and hashes only the
// files that are new or whose size or modification time changed.
func (sc *scan) verify(batch []*fileState) error {
	if len(batch) == 0 {
		return nil
	}
	files := map[string]*fileState{}
	args := make([]any, len(batch))
	for i, f := range batch {
		files[f.name], args[i] = f, f.name
	}
	var adopted []*fileState
	rows, e := sc.s.db.DB.Query(`SELECT id, name, size, mtime, key FROM files WHERE name IN (?`+strings.Repeat(",?", len(args)-1)+`)`, args...)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id, size, mtime int64
		var name, key string
		if e = rows.Scan(&id, &name, &size, &mtime, &key); e != nil {
			rows.Close()
			return e
		}
		f := files[name]
		switch {
		case f.size == size && f.mtime == mtime:
			sc.mark(id)
			sc.done++
			delete(files, name)
		case f.size == size && mtime == unknownTime:
			f.id = id
			adopted = append(adopted, f)
			delete(files, name)
		default:
			f.id, f.previous = id, key
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	changed := make([]*fileState, 0, len(files))
	for _, f := range files {
		changed = append(changed, f)
	}
	slices.SortFunc(changed, func(a, b *fileState) int { return strings.Compare(a.name, b.name) })
	packages := make([]vsix.Package, len(changed))
	problems := make([]error, len(changed))
	each(len(changed), max(2, runtime.GOMAXPROCS(0)), func(i int) {
		packages[i], problems[i] = vsix.Inspect(filepath.Join(sc.s.cfg.Extensions, changed[i].name))
	})
	if len(changed) == 0 && len(adopted) == 0 {
		sc.progress(sc.done, max(sc.total, sc.done))
		return nil
	}
	e = sc.s.db.InTx(func(tx *sql.Tx) error {
		if len(adopted) > 0 {
			slices.SortFunc(adopted, func(a, b *fileState) int { return cmp.Compare(a.id, b.id) })
			update, e := tx.Prepare(`UPDATE files SET mtime=? WHERE id=?`)
			if e != nil {
				return e
			}
			defer update.Close()
			for _, f := range adopted {
				if _, e := update.Exec(f.mtime, f.id); e != nil {
					return e
				}
				sc.mark(f.id)
			}
		}
		for i, f := range changed {
			key, sha := "", ""
			if problems[i] != nil {
				slog.Warn("existing VSIX rejected", "file", f.name, "error", problems[i])
				if e := store.AuditIn(tx, "startup", "invalid_existing", f.name); e != nil {
					return e
				}
			} else {
				key, sha = packages[i].Key(), packages[i].SHA256
				if e := sc.inventory(tx, packages[i], f); e != nil {
					return e
				}
			}
			for _, k := range []string{key, f.previous} {
				if k != "" {
					if _, e := tx.Exec(`INSERT OR IGNORE INTO dirty VALUES(?)`, k); e != nil {
						return e
					}
				}
			}
			var id int64
			if e := tx.QueryRow(`INSERT INTO files(name,size,mtime,key,sha) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET size=excluded.size,mtime=excluded.mtime,key=excluded.key,sha=excluded.sha RETURNING id`, f.name, f.size, f.mtime, key, sha).Scan(&id); e != nil {
				return e
			}
			sc.mark(id)
		}
		return nil
	})
	if e != nil {
		return e
	}
	sc.done += len(changed) + len(adopted)
	sc.progress(sc.done, max(sc.total, sc.done))
	return nil
}

// inventory creates the record for a package seen for the first time, or refreshes the metadata of a
// record with identical bytes. Statuses are settled later, once every file with the same identity is known.
func (sc *scan) inventory(tx *sql.Tx, p vsix.Package, f *fileState) error {
	var payload string
	e := tx.QueryRow(`SELECT payload FROM packages WHERE key=?`, p.Key()).Scan(&payload)
	if errors.Is(e, sql.ErrNoRows) {
		// Pending until settled below, which counts the change once and detects conflicting copies.
		p.Filename, p.Status = f.name, "pending"
		p.StoredAt = time.Unix(0, f.mtime).UTC().Format(time.RFC3339)
		return store.Put(tx, p)
	}
	var old vsix.Package
	if e == nil {
		e = json.Unmarshal([]byte(payload), &old)
	}
	if e != nil || old.SHA256 != p.SHA256 {
		return e
	}
	p.Filename, p.Status, p.Managed, p.Source, p.StoredAt, p.DeletedAt = old.Filename, old.Status, old.Managed, old.Source, old.StoredAt, old.DeletedAt
	if b, _ := json.Marshal(p); string(b) == payload {
		return nil
	}
	return store.Put(tx, p)
}

// dropVanished forgets files that are no longer in the directory and queues their packages.
func (sc *scan) dropVanished() error {
	var rows, last int64
	if e := sc.s.db.DB.QueryRow(`SELECT count(*), coalesce(max(id),0) FROM files`).Scan(&rows, &last); e != nil || rows == int64(sc.seen) {
		return e
	}
	for low := int64(0); low <= last; low += int64(scanBatch) {
		var gone []int64
		var keys []string
		r, e := sc.s.db.DB.Query(`SELECT id, key FROM files WHERE id BETWEEN ? AND ?`, low, low+int64(scanBatch)-1)
		if e != nil {
			return e
		}
		for r.Next() {
			var id int64
			var key string
			if e = r.Scan(&id, &key); e != nil {
				r.Close()
				return e
			}
			if !sc.found.has(id) {
				gone, keys = append(gone, id), append(keys, key)
			}
		}
		r.Close()
		if e = r.Err(); e != nil {
			return e
		}
		if len(gone) == 0 {
			continue
		}
		e = sc.s.db.InTx(func(tx *sql.Tx) error {
			for i, id := range gone {
				if _, e := tx.Exec(`DELETE FROM files WHERE id=?`, id); e != nil {
					return e
				}
				if keys[i] != "" {
					if _, e := tx.Exec(`INSERT OR IGNORE INTO dirty VALUES(?)`, keys[i]); e != nil {
						return e
					}
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}

type remembered struct{ name, sha string }

// settle recomputes the status of every queued package from the files that hold it: stored when
// they all match the record, conflict when any differs, and missing when none remain.
func (sc *scan) settle() error {
	for {
		if e := sc.s.ctx.Err(); e != nil {
			return e
		}
		var keys []any
		rows, e := sc.s.db.DB.Query(`SELECT key FROM dirty ORDER BY key LIMIT ?`, scanBatch)
		if e != nil {
			return e
		}
		for rows.Next() {
			var key string
			if e = rows.Scan(&key); e != nil {
				rows.Close()
				return e
			}
			keys = append(keys, key)
		}
		rows.Close()
		if e = rows.Err(); e != nil || len(keys) == 0 {
			return e
		}
		in := `(?` + strings.Repeat(",?", len(keys)-1) + `)`
		held := map[string][]remembered{}
		rows, e = sc.s.db.DB.Query(`SELECT key, name, sha FROM files WHERE key IN `+in+` ORDER BY key, name`, keys...)
		if e != nil {
			return e
		}
		for rows.Next() {
			var key string
			var f remembered
			if e = rows.Scan(&key, &f.name, &f.sha); e != nil {
				rows.Close()
				return e
			}
			held[key] = append(held[key], f)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		var records []vsix.Package
		rows, e = sc.s.db.DB.Query(`SELECT payload FROM packages WHERE key IN `+in, keys...)
		if e != nil {
			return e
		}
		for rows.Next() {
			var payload string
			var p vsix.Package
			if e = rows.Scan(&payload); e == nil {
				e = json.Unmarshal([]byte(payload), &p)
			}
			if e != nil {
				rows.Close()
				return e
			}
			records = append(records, p)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		e = sc.s.db.InTx(func(tx *sql.Tx) error {
			for _, p := range records {
				if p.Status == "deleted" {
					// Finish a deletion interrupted before its files were removed. Other bytes under
					// this identity are left in place.
					for _, f := range held[p.Key()] {
						if f.sha != p.SHA256 {
							continue
						}
						if e := os.Remove(filepath.Join(sc.s.cfg.Extensions, f.name)); e != nil && !os.IsNotExist(e) {
							return e
						}
						if _, e := tx.Exec(`DELETE FROM files WHERE name=?`, f.name); e != nil {
							return e
						}
					}
					continue
				}
				status, name := p.Status, p.Filename
				p = settled(p, held[p.Key()])
				if p.Status == "stored" && status != "stored" {
					sc.grew[strings.ToLower(p.ID)] = true
				}
				if p.Status != status {
					sc.changed++
					if p.Status == "conflict" {
						if e := store.AuditIn(tx, "startup", "conflict", p.Key()); e != nil {
							return e
						}
					}
				}
				if p.Status != status || p.Filename != name {
					if e := store.Put(tx, p); e != nil {
						return e
					}
				}
			}
			_, e := tx.Exec(`DELETE FROM dirty WHERE key IN `+in, keys...)
			return e
		})
		if e != nil {
			return e
		}
	}
}

// settled derives a record's status from the files holding its identity, sorted by name.
func settled(p vsix.Package, files []remembered) vsix.Package {
	if len(files) == 0 {
		p.Status = "missing"
		return p
	}
	for _, f := range files {
		if f.sha != p.SHA256 {
			p.Status = "conflict"
			return p
		}
	}
	name := files[0].name
	for _, f := range files {
		if f.name == p.Filename {
			name = f.name
		}
	}
	p.Status, p.Filename = "stored", name
	return p
}

func (sc *scan) mark(id int64) {
	if sc.found.set(id) {
		sc.seen++
	}
}

// bitset records which files rows were found; a million files need only 125 KB.
type bitset []uint64

func (b *bitset) set(i int64) bool {
	w := int(i / 64)
	if w >= len(*b) {
		*b = append(*b, make([]uint64, w-len(*b)+1)...)
	}
	m := uint64(1) << (i % 64)
	if (*b)[w]&m != 0 {
		return false
	}
	(*b)[w] |= m
	return true
}
func (b bitset) has(i int64) bool { w := int(i / 64); return w < len(b) && b[w]&(1<<(i%64)) != 0 }

// each calls fn for every index below n on up to workers goroutines.
func each(n, workers int, fn func(i int)) {
	workers = min(workers, n)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := w; i < n; i += workers {
				fn(i)
			}
		})
	}
	wg.Wait()
}
