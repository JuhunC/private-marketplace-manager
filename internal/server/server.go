package server

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/buildinfo"
	"github.com/JuhunC/private-marketplace-manager/internal/store"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
	"github.com/gofrs/flock"
)

//go:embed web/*
var web embed.FS

type Config struct {
	Extensions, State, Token, Password, PublicURL string
	MaxUpload                                     int64
}
type session struct {
	CSRF  string
	Until time.Time
}
type attempt struct {
	Count int
	Until time.Time
}
type Server struct {
	cfg          Config
	db           *store.Store
	lock         *flock.Flock
	mux          *http.ServeMux
	tokenHash    [32]byte
	passwordHash []byte
	salt         []byte
	mu           sync.Mutex
	publishMu    sync.Mutex
	sessions     map[string]session
	attempts     map[string]attempt
	slots        chan struct{}
}

func New(c Config) (*Server, error) {
	if len(c.Token) < 32 || len(c.Password) < 12 {
		return nil, fmt.Errorf("API token must be at least 32 characters and admin password at least 12")
	}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("PUBLIC_URL must be an http(s) origin without a path")
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.MaxUpload <= 0 {
		c.MaxUpload = 2 << 30
	}
	for _, dir := range []string{c.Extensions, c.State} {
		if e = os.MkdirAll(dir, 0750); e != nil {
			return nil, e
		}
	}
	c.Extensions, e = filepath.Abs(c.Extensions)
	if e != nil {
		return nil, e
	}
	c.State, e = filepath.Abs(c.State)
	if e != nil {
		return nil, e
	}
	if c.State == c.Extensions {
		return nil, fmt.Errorf("state and extension directories must differ")
	}
	lock := flock.New(filepath.Join(c.State, "manager.lock"))
	ok, e := lock.TryLock()
	if e != nil || !ok {
		return nil, fmt.Errorf("another manager owns this state directory")
	}
	db, e := store.Open(c.State)
	if e != nil {
		lock.Unlock()
		return nil, e
	}
	s := &Server{cfg: c, db: db, lock: lock, mux: http.NewServeMux(), tokenHash: sha256.Sum256([]byte(c.Token)), sessions: map[string]session{}, attempts: map[string]attempt{}, slots: make(chan struct{}, 4)}
	s.salt = make([]byte, 32)
	if _, e = rand.Read(s.salt); e != nil {
		s.Close()
		return nil, e
	}
	s.passwordHash, e = pbkdf2.Key(sha256.New, c.Password, s.salt, 210000, 32)
	if e != nil {
		s.Close()
		return nil, e
	}
	s.cfg.Password = ""
	s.cfg.Token = ""
	if e = s.Reconcile(); e != nil {
		s.Close()
		return nil, e
	}
	s.routes()
	return s, nil
}
func (s *Server) Close() error { e := s.db.Close(); s.lock.Unlock(); return e }
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Request-ID", randomID())
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		s.mux.ServeHTTP(w, r)
	})
}
func randomID() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, msg string) {
	jsonResponse(w, status, map[string]string{"code": code, "error": msg, "requestId": w.Header().Get("X-Request-ID")})
}
func (s *Server) routes() {
	s.mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"status": "alive", "version": buildinfo.Version})
	})
	s.mux.HandleFunc("GET /health/ready", s.ready)
	s.mux.HandleFunc("POST /api/v1/login", s.login)
	s.mux.HandleFunc("GET /api/v1/me", s.auth(s.me))
	s.mux.HandleFunc("POST /api/v1/logout", s.auth(s.logout))
	s.mux.HandleFunc("GET /api/v1/status", s.auth(s.status))
	s.mux.HandleFunc("GET /api/v1/extensions", s.auth(s.list))
	s.mux.HandleFunc("POST /api/v1/extensions", s.auth(s.upload))
	s.mux.HandleFunc("POST /api/v1/extensions/check", s.auth(s.check))
	s.mux.HandleFunc("GET /api/v1/packages/download", s.auth(s.download))
	s.mux.HandleFunc("GET /api/v1/uploads/by-key/{key}", s.auth(s.uploadStatus))
	s.mux.HandleFunc("GET /api/v1/audit-events", s.auth(s.events))
	s.mux.HandleFunc("POST /api/v1/sync-runs", s.auth(s.report))
	s.mux.HandleFunc("GET /api/v1/sync-runs", s.auth(s.runs))
	sub, _ := fs.Sub(web, "web")
	s.mux.Handle("GET /", http.FileServer(http.FS(sub)))
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if e := s.db.DB.PingContext(r.Context()); e != nil {
		fail(w, 503, "not_ready", "database unavailable")
		return
	}
	f, e := os.CreateTemp(s.cfg.Extensions, ".ready-*.part")
	if e != nil {
		fail(w, 503, "not_ready", "extension storage is not writable")
		return
	}
	f.Close()
	os.Remove(f.Name())
	jsonResponse(w, 200, map[string]string{"status": "ready"})
}
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			h := sha256.Sum256([]byte(strings.TrimPrefix(a, "Bearer ")))
			if subtle.ConstantTimeCompare(h[:], s.tokenHash[:]) == 1 {
				next(w, r)
				return
			}
			fail(w, 401, "unauthorized", "invalid API credential")
			return
		}
		c, e := r.Cookie("marketplace_session")
		if e != nil {
			fail(w, 401, "unauthorized", "sign in or use a bearer token")
			return
		}
		s.mu.Lock()
		sess, ok := s.sessions[c.Value]
		s.mu.Unlock()
		if !ok || time.Now().After(sess.Until) {
			fail(w, 401, "unauthorized", "session expired")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("Origin") != s.cfg.PublicURL || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRF)) != 1 {
				fail(w, 403, "csrf", "invalid request origin or CSRF token")
				return
			}
		}
		next(w, r)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.cfg.PublicURL {
		fail(w, 403, "origin", "invalid login origin; check PUBLIC_URL")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	a := s.attempts[ip]
	if time.Now().After(a.Until) {
		a = attempt{Until: time.Now().Add(time.Minute)}
	}
	a.Count++
	s.attempts[ip] = a
	if len(s.attempts) > 10000 {
		for k, v := range s.attempts {
			if time.Now().After(v.Until) {
				delete(s.attempts, k)
			}
		}
	}
	s.mu.Unlock()
	if a.Count > 10 {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limited", "wait before retrying login")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
		fail(w, 400, "bad_request", "invalid login")
		return
	}
	h, e := pbkdf2.Key(sha256.New, body.Password, s.salt, 210000, 32)
	if e != nil || subtle.ConstantTimeCompare(h, s.passwordHash) != 1 {
		fail(w, 401, "unauthorized", "invalid password")
		return
	}
	id, csrf := randomID(), randomID()
	until := time.Now().Add(8 * time.Hour)
	s.mu.Lock()
	for k, v := range s.sessions {
		if time.Now().After(v.Until) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) > 10000 {
		s.mu.Unlock()
		fail(w, 503, "busy", "too many sessions")
		return
	}
	s.sessions[id] = session{csrf, until}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "marketplace_session", Value: id, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"), SameSite: http.SameSiteStrictMode, Expires: until})
	_ = s.db.Audit("operator", "login", "")
	jsonResponse(w, 200, map[string]string{"csrfToken": csrf})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	csrf := ""
	if c, e := r.Cookie("marketplace_session"); e == nil {
		s.mu.Lock()
		csrf = s.sessions[c.Value].CSRF
		s.mu.Unlock()
	}
	jsonResponse(w, 200, map[string]string{"csrfToken": csrf, "version": buildinfo.Version})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("marketplace_session"); e == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "marketplace_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"), SameSite: http.SameSiteStrictMode})
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func actor(r *http.Request) string {
	if r.Header.Get("Authorization") != "" {
		return "sync-token"
	}
	return "operator"
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	p, total, e := s.db.List(strings.ToLower(r.URL.Query().Get("id")), limit, offset)
	if e != nil {
		fail(w, 500, "database", "inventory unavailable")
		return
	}
	jsonResponse(w, 200, map[string]any{"packages": p, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Keys []string `json:"keys"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); e != nil || len(b.Keys) > 1000 {
		fail(w, 400, "bad_request", "send at most 1000 keys")
		return
	}
	out := map[string]vsix.Package{}
	for _, k := range b.Keys {
		p, e := s.db.Get(k)
		if e == nil && p.Status == "stored" {
			info, err := os.Lstat(filepath.Join(s.cfg.Extensions, p.Filename))
			if err == nil && info.Mode().IsRegular() && info.Size() == p.Size {
				out[k] = p
			}
		}
	}
	jsonResponse(w, 200, map[string]any{"packages": out})
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	var n, bytes int64
	_ = s.db.DB.QueryRow(`SELECT count(*) FROM packages WHERE status='stored'`).Scan(&n)
	p, _, _ := s.db.List("", 2147483647, 0)
	for _, v := range p {
		if v.Status == "stored" {
			bytes += v.Size
		}
	}
	jsonResponse(w, 200, map[string]any{"version": buildinfo.Version, "packages": n, "bytes": bytes, "maxUploadBytes": s.cfg.MaxUpload, "marketplaceVisibility": "unverified", "apiVersion": 1})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	v, e := s.db.Events()
	if e != nil {
		fail(w, 500, "database", "audit unavailable")
		return
	}
	jsonResponse(w, 200, v)
}
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if e != nil {
		fail(w, 413, "size", "report too large")
		return
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &v) != nil || len(v.ID) < 1 || len(v.ID) > 128 {
		fail(w, 400, "bad_request", "run id is required")
		return
	}
	if e = s.db.PutRun(v.ID, b); e != nil {
		fail(w, 500, "database", "report could not be saved")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	v, e := s.db.Runs()
	if e != nil {
		fail(w, 500, "database", "runs unavailable")
		return
	}
	jsonResponse(w, 200, v)
}
func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request) {
	_, key, e := s.db.LookupUpload(r.PathValue("key"))
	if e != nil {
		fail(w, 404, "not_found", "upload not found")
		return
	}
	p, e := s.db.Get(key)
	if e != nil {
		fail(w, 404, "not_found", "package not found")
		return
	}
	jsonResponse(w, 200, map[string]any{"package": p})
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	p, e := s.db.Get(r.URL.Query().Get("key"))
	if e != nil || p.Status != "stored" {
		fail(w, 404, "not_found", "package not found")
		return
	}
	filename := filepath.Join(s.cfg.Extensions, p.Filename)
	fi, e := os.Lstat(filename)
	if e != nil || !fi.Mode().IsRegular() {
		fail(w, 404, "not_found", "package file not found")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", p.Filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filename)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		fail(w, 429, "busy", "four uploads already active")
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) > 256 {
		fail(w, 400, "bad_key", "idempotency key too long")
		return
	}
	f, e := os.CreateTemp(s.cfg.Extensions, ".upload-*.part")
	if e != nil {
		fail(w, 507, "storage", "cannot stage upload")
		return
	}
	filename := f.Name()
	defer os.Remove(filename)
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	_, e = io.Copy(f, r.Body)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		var max *http.MaxBytesError
		if errors.As(e, &max) {
			fail(w, 413, "size", "VSIX exceeds upload limit")
		} else {
			fail(w, 507, "storage", "upload interrupted or storage unavailable")
		}
		return
	}
	p, e := vsix.Inspect(filename)
	if e != nil {
		_ = s.db.Audit(actor(r), "rejected", e.Error())
		fail(w, 422, "invalid_vsix", e.Error())
		return
	}
	for field, want := range map[string]string{"id": p.ID, "version": p.Version, "platform": p.Platform} {
		if got := r.URL.Query().Get(field); got != "" && got != want {
			fail(w, 422, "identity_mismatch", field+" disagrees with VSIX")
			return
		}
	}
	if hash := r.Header.Get("X-Content-SHA256"); hash != "" && !strings.EqualFold(hash, p.SHA256) {
		fail(w, 422, "checksum", "checksum mismatch")
		return
	}
	p.Filename = p.CanonicalName()
	p.Managed = true
	p.StoredAt = time.Now().UTC().Format(time.RFC3339)
	p.Source = r.Header.Get("X-Package-Source")
	if len(p.Source) > 2048 {
		p.Source = p.Source[:2048]
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if idem != "" {
		sha, key, err := s.db.LookupUpload(idem)
		if err == nil && (sha != p.SHA256 || key != p.Key()) {
			fail(w, 409, "idempotency_conflict", "key was used for different content")
			return
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fail(w, 500, "database", "cannot check idempotency key")
			return
		}
	}
	old, err := s.db.Get(p.Key())
	if err == nil {
		if old.Status == "conflict" {
			fail(w, 409, "content_conflict", "conflicting files require operator reconciliation")
			return
		}
		if old.SHA256 != p.SHA256 {
			fail(w, 409, "content_conflict", "identity already exists with different bytes")
			return
		}
		dest := filepath.Join(s.cfg.Extensions, old.Filename)
		info, se := os.Lstat(dest)
		if se == nil {
			if !info.Mode().IsRegular() {
				fail(w, 409, "unsafe_destination", "existing destination is not a regular file")
				return
			}
			hash, _, he := vsix.HashFile(dest)
			if he != nil || hash != p.SHA256 {
				fail(w, 409, "content_conflict", "stored file changed outside manager")
				return
			}
			old.Status = "stored"
			if e = s.db.Put(old); e != nil {
				fail(w, 500, "database", "cannot confirm existing file")
				return
			}
			if idem != "" {
				if e = s.db.Upload(idem, p.SHA256, p.Key()); e != nil {
					fail(w, 500, "database", "cannot save upload receipt")
					return
				}
			}
			jsonResponse(w, 200, map[string]any{"package": old, "duplicate": true})
			return
		}
		if !os.IsNotExist(se) {
			fail(w, 507, "storage", "cannot access destination")
			return
		}
		p.Filename = old.Filename
		p.Managed = old.Managed
	} else if !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "database", "cannot check package identity")
		return
	}
	dest := filepath.Join(s.cfg.Extensions, p.Filename)
	// Journal before publishing: startup reconciliation recovers a link completed before the final commit.
	p.Status = "pending"
	if e = s.db.Put(p); e != nil {
		fail(w, 500, "database", "cannot journal upload")
		return
	}
	if e = os.Chmod(filename, 0644); e == nil {
		e = os.Link(filename, dest)
	}
	if e != nil {
		fail(w, 409, "publication_failed", "destination already exists or atomic publication failed")
		return
	}
	if d, err := os.Open(s.cfg.Extensions); err == nil {
		e = d.Sync()
		d.Close()
	} else {
		e = err
	}
	if e != nil {
		fail(w, 507, "storage", "publication needs reconciliation after directory flush failure")
		return
	}
	p.Status = "stored"
	if e = s.db.Put(p); e != nil {
		fail(w, 500, "database", "file published; retry to reconcile its receipt")
		return
	}
	if idem != "" {
		if e = s.db.Upload(idem, p.SHA256, p.Key()); e != nil {
			fail(w, 500, "database", "file stored; retry to save its receipt")
			return
		}
	}
	_ = s.db.Audit(actor(r), "stored", p.Key())
	jsonResponse(w, 201, map[string]any{"package": p, "duplicate": false})
}

// Reconcile verifies existing files and repairs pending publications. It never removes VSIXs.
func (s *Server) Reconcile() error {
	entries, e := os.ReadDir(s.cfg.Extensions)
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	conflicts := map[string]bool{}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".upload-") && strings.HasSuffix(ent.Name(), ".part") {
			_ = os.Remove(filepath.Join(s.cfg.Extensions, ent.Name()))
			continue
		}
		if !strings.HasSuffix(strings.ToLower(ent.Name()), ".vsix") {
			continue
		}
		info, err := ent.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		p, err := vsix.Inspect(filepath.Join(s.cfg.Extensions, ent.Name()))
		if err != nil {
			slog.Warn("existing VSIX rejected", "file", ent.Name(), "error", err)
			_ = s.db.Audit("startup", "invalid_existing", ent.Name())
			continue
		}
		old, err := s.db.Get(p.Key())
		if err == nil {
			if old.SHA256 != p.SHA256 || conflicts[p.Key()] {
				conflicts[p.Key()] = true
				old.Status = "conflict"
				_ = s.db.Put(old)
				seen[p.Key()] = true
				_ = s.db.Audit("startup", "conflict", p.Key())
				continue
			}
			p.Managed = old.Managed
			p.Source = old.Source
			p.StoredAt = old.StoredAt
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p.Filename = ent.Name()
		if p.StoredAt == "" {
			p.StoredAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		p.Status = "stored"
		if e = s.db.Put(p); e != nil {
			return e
		}
		seen[p.Key()] = true
	}
	all, e := s.db.All()
	if e != nil {
		return e
	}
	for _, p := range all {
		if !seen[p.Key()] {
			p.Status = "missing"
			if e = s.db.Put(p); e != nil {
				return e
			}
		}
	}
	return nil
}
