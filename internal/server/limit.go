package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
)

// remove deletes packages' files while keeping them indexed as deleted by an operator or the version
// limit. The deletion is recorded first, so if the process stops midway the next scan removes the rest.
// It reports how many files could not be removed yet. Callers hold publishMu.
func (s *Server) remove(targets []vsix.Package, by, detail string) (int, error) {
	keys := make([]string, len(targets))
	for i, p := range targets {
		keys[i] = p.Key()
	}
	names, e := s.db.FilesHolding(keys)
	if e != nil {
		return 0, e
	}
	for _, p := range targets {
		if p.Filename != "" && !slices.Contains(names, p.Filename) {
			names = append(names, p.Filename)
		}
	}
	if e = s.db.MarkDeleted(targets, time.Now().UTC().Format(time.RFC3339), by, detail); e != nil {
		return 0, e
	}
	var removed []string
	remaining := 0
	for _, name := range names {
		if filepath.Base(name) != name {
			continue
		}
		if e := os.Remove(filepath.Join(s.cfg.Extensions, name)); e == nil || os.IsNotExist(e) {
			removed = append(removed, name)
		} else {
			slog.Warn("deleted version's file could not be removed; the next scan retries", "file", name, "error", e)
			remaining++
		}
	}
	if e = s.db.ForgetFiles(removed); e != nil {
		slog.Warn("removed files are still remembered; the next scan forgets them", "error", e)
	}
	return remaining, nil
}

// kept lists an extension's versions that are not deleted, newest first.
func kept(packages []vsix.Package) []string {
	var versions []string
	for _, p := range packages {
		if p.Status != "deleted" && !slices.Contains(versions, p.Version) {
			versions = append(versions, p.Version)
		}
	}
	slices.SortFunc(versions, func(a, b string) int { return vsix.CompareVersions(b, a) })
	return versions
}

// beyond lists the packages of versions older than the newest keep versions; keep 0 keeps all.
func beyond(packages []vsix.Package, keep int) []vsix.Package {
	versions := kept(packages)
	if keep <= 0 || len(versions) <= keep {
		return nil
	}
	cut := versions[keep:]
	var out []vsix.Package
	for _, p := range packages {
		if p.Status != "deleted" && slices.Contains(cut, p.Version) {
			out = append(out, p)
		}
	}
	return out
}

// outside reports whether adding version would leave it older than the newest keep versions.
func outside(packages []vsix.Package, version string, keep int) bool {
	if keep <= 0 {
		return false
	}
	versions := kept(packages)
	if slices.Contains(versions, version) {
		return false
	}
	newer := 0
	for _, v := range versions {
		if vsix.CompareVersions(v, version) > 0 {
			newer++
		}
	}
	return newer >= keep
}

// trim deletes an extension's versions beyond its limit. Callers hold publishMu.
func (s *Server) trim(id string) (int, error) {
	keep, _, e := s.db.Keep(id)
	if e != nil || keep == 0 {
		return 0, e
	}
	packages, _, e := s.db.List(id, 2147483647, 0)
	if e != nil {
		return 0, e
	}
	targets := beyond(packages, keep)
	if len(targets) == 0 {
		return 0, nil
	}
	detail, _ := json.Marshal(map[string]any{"id": id, "keep": keep, "packages": len(targets)})
	if _, e = s.remove(targets, "limit", string(detail)); e != nil {
		return 0, e
	}
	return len(targets), nil
}

// trimmed summarizes what a version limit removes.
type trimmed struct {
	Extensions int      `json:"extensions"`
	Versions   int      `json:"versions"`
	Packages   int      `json:"packages"`
	Bytes      int64    `json:"bytes"`
	Remaining  int      `json:"filesRemaining"`
	Removed    []string `json:"removedVersions,omitempty"` // for a single extension
}

func (t *trimmed) add(targets []vsix.Package) {
	if len(targets) == 0 {
		return
	}
	t.Extensions++
	var versions []string
	for _, p := range targets {
		if !slices.Contains(versions, p.Version) {
			versions = append(versions, p.Version)
		}
		if p.Status == "stored" {
			t.Bytes += p.Size
		}
	}
	t.Versions += len(versions)
	t.Packages += len(targets)
}

type limitRequest struct {
	Keep   *int `json:"keep"`
	DryRun bool `json:"dryRun"`
}

func readLimit(w http.ResponseWriter, r *http.Request, allowInherit bool) (limitRequest, bool) {
	var req limitRequest
	e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	if e != nil || req.Keep == nil && !allowInherit || req.Keep != nil && *req.Keep < 0 {
		message := `send {"keep": N} where N is 0 (keep every version) or more`
		if allowInherit {
			message = `send {"keep": N} where N is 0 (keep every version) or more, or {"keep": null} to follow the library default`
		}
		fail(w, 400, "bad_request", message)
		return req, false
	}
	return req, true
}

func (s *Server) libraryLimit(w http.ResponseWriter, r *http.Request) {
	keep, e := s.db.KeepDefault()
	overrides, err := s.db.Overrides()
	if e != nil || err != nil {
		fail(w, 500, "database", "limits unavailable")
		return
	}
	jsonResponse(w, 200, map[string]any{"keep": keep, "extensionLimits": overrides})
}

// setLibraryLimit changes the default limit and trims every extension that follows it.
// dryRun reports what the new limit would remove.
func (s *Server) setLibraryLimit(w http.ResponseWriter, r *http.Request) {
	req, ok := readLimit(w, r, false)
	if !ok {
		return
	}
	s.maintenance.RLock()
	defer s.maintenance.RUnlock()
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	overrides, e := s.db.Overrides()
	if e != nil {
		fail(w, 500, "database", "limits unavailable")
		return
	}
	if !req.DryRun {
		if e = s.db.SetKeepDefault(*req.Keep); e != nil {
			fail(w, 500, "database", "limit could not be saved")
			return
		}
	}
	var t trimmed
	e = s.db.Extensions(func(id string, packages []vsix.Package) error {
		if _, own := overrides[id]; own {
			return nil
		}
		targets := beyond(packages, *req.Keep)
		t.add(targets)
		if req.DryRun || len(targets) == 0 {
			return nil
		}
		detail, _ := json.Marshal(map[string]any{"id": id, "keep": *req.Keep, "packages": len(targets)})
		remaining, e := s.remove(targets, "limit", string(detail))
		t.Remaining += remaining
		return e
	})
	if e != nil {
		fail(w, 500, "database", "limit could not be applied completely; apply it again")
		return
	}
	jsonResponse(w, 200, map[string]any{"dryRun": req.DryRun, "keep": *req.Keep, "trimmed": t})
}

// setExtensionLimit gives one extension its own limit (or the library default with null) and trims it.
func (s *Server) setExtensionLimit(w http.ResponseWriter, r *http.Request) {
	req, ok := readLimit(w, r, true)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("id"))
	if !vsix.ValidID(id) {
		fail(w, 400, "bad_request", "extension ID must be publisher.extension")
		return
	}
	s.maintenance.RLock()
	defer s.maintenance.RUnlock()
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	keep := 0
	if req.Keep != nil {
		keep = *req.Keep
	} else if library, e := s.db.KeepDefault(); e == nil {
		keep = library
	} else {
		fail(w, 500, "database", "limits unavailable")
		return
	}
	packages, _, e := s.db.List(id, 2147483647, 0)
	if e != nil {
		fail(w, 500, "database", "inventory unavailable")
		return
	}
	targets := beyond(packages, keep)
	var t trimmed
	t.add(targets)
	for _, p := range targets {
		if !slices.Contains(t.Removed, p.Version) {
			t.Removed = append(t.Removed, p.Version)
		}
	}
	slices.SortFunc(t.Removed, vsix.CompareVersions)
	if !req.DryRun {
		if e = s.db.SetKeep(id, req.Keep); e != nil {
			fail(w, 500, "database", "limit could not be saved")
			return
		}
		if len(targets) > 0 {
			detail, _ := json.Marshal(map[string]any{"id": id, "keep": keep, "packages": len(targets)})
			if t.Remaining, e = s.remove(targets, "limit", string(detail)); e != nil {
				fail(w, 500, "database", "limit saved but could not be applied; apply it again")
				return
			}
		}
	}
	source := "extension"
	if req.Keep == nil {
		source = "library"
	}
	jsonResponse(w, 200, map[string]any{"dryRun": req.DryRun, "keep": keep, "keepSource": source, "trimmed": t})
}
