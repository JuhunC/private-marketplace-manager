package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/JuhunC/private-marketplace-manager/internal/store"
	"github.com/JuhunC/private-marketplace-manager/internal/testutil"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789012345678901234567890123456789"

// unstarted returns a serving manager whose startup scan has not begun.
func unstarted(t *testing.T, options ...func(*Config)) (*Server, *httptest.Server, Config) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("receiver is a Linux container; Windows CLI tested separately")
	}
	dir := t.TempDir()
	c := Config{Extensions: filepath.Join(dir, "extensions"), State: filepath.Join(dir, "state"), Token: testToken, Password: "a-long-test-password", PublicURL: "http://localhost", MaxUpload: 1 << 20}
	for _, option := range options {
		option(&c)
	}
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	h := httptest.NewServer(s.Handler())
	t.Cleanup(func() { h.Close(); s.Close() })
	return s, h, c
}
func setup(t *testing.T) (*Server, *httptest.Server, Config) {
	t.Helper()
	s, h, c := unstarted(t)
	s.Start()
	s.background.Wait()
	return s, h, c
}
func request(t *testing.T, h *httptest.Server, method, path string, b []byte, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(b))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r, e := h.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func getJSON(t *testing.T, h *httptest.Server, path string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	r := request(t, h, "GET", path, nil, headers)
	defer r.Body.Close()
	var result map[string]any
	json.NewDecoder(r.Body).Decode(&result)
	return r.StatusCode, result
}
func upload(t *testing.T, h *httptest.Server, b []byte, key string) (int, map[string]any) {
	t.Helper()
	r := request(t, h, "POST", "/api/v2/extensions", b, map[string]string{"Authorization": "Bearer " + testToken, "Idempotency-Key": key})
	defer r.Body.Close()
	var result map[string]any
	json.NewDecoder(r.Body).Decode(&result)
	return r.StatusCode, result
}
func TestUploadPreservesVersionsAndRetries(t *testing.T) {
	s, h, c := setup(t)
	b := testutil.VSIX("hello", "1.0.0", "linux-x64", false, nil)
	for i, want := range []int{201, 200} {
		code, v := upload(t, h, b, "one")
		if code != want {
			t.Fatalf("attempt %d: %d %+v", i, code, v)
		}
	}
	if code, _ := upload(t, h, testutil.VSIX("hello", "1.0.0", "linux-x64", false, map[string]string{"extension/code.js": "changed"}), "two"); code != 409 {
		t.Fatalf("changed bytes: %d", code)
	}
	if code, _ := upload(t, h, testutil.VSIX("hello", "2.0.0", "darwin-arm64", true, nil), "three"); code != 201 {
		t.Fatal(code)
	}
	if code, _ := upload(t, h, testutil.VSIX("other", "1.0.0", "", false, nil), "one"); code != 409 {
		t.Fatal("idempotency key reused")
	}
	p, _, _ := s.db.List("", 100, 0)
	if len(p) != 2 {
		t.Fatal(len(p))
	}
	for _, pkg := range p {
		b, e := os.ReadFile(filepath.Join(c.Extensions, pkg.Filename))
		if e != nil || len(b) == 0 {
			t.Fatal("missing file")
		}
	}
	entries, _ := os.ReadDir(c.Extensions)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			t.Fatal("staging leak")
		}
	}
}
func TestUploadValidationAndAuthentication(t *testing.T) {
	_, h, _ := setup(t)
	b := testutil.VSIX("hello", "1.0.0", "", false, nil)
	r := request(t, h, "POST", "/api/v2/extensions", b, nil)
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
	for _, tt := range []struct {
		path    string
		body    []byte
		headers map[string]string
		want    int
	}{
		{"/api/v2/extensions", []byte("invalid zip"), nil, 422},
		{"/api/v2/extensions?id=test.wrong", b, nil, 422},
		{"/api/v2/extensions", b, map[string]string{"X-Content-SHA256": "bad"}, 422},
		{"/api/v2/extensions", make([]byte, (1<<20)+1), nil, 413},
	} {
		headers := map[string]string{"Authorization": "Bearer " + testToken}
		for k, v := range tt.headers {
			headers[k] = v
		}
		r = request(t, h, "POST", tt.path, tt.body, headers)
		data, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != tt.want {
			t.Fatalf("want %d got %d %s", tt.want, r.StatusCode, data)
		}
	}
}
func TestCookieOriginAndCSRF(t *testing.T) {
	_, h, _ := setup(t)
	r := request(t, h, "POST", "/api/v2/login", []byte(`{"password":"a-long-test-password"}`), map[string]string{"Origin": "https://evil.example"})
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
	r = request(t, h, "POST", "/api/v2/login", []byte(`{"password":"a-long-test-password"}`), map[string]string{"Origin": "http://localhost"})
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	cookie := r.Cookies()[0]
	headers := map[string]string{"Cookie": cookie.String(), "Origin": "http://localhost"}
	r = request(t, h, "POST", "/api/v2/logout", nil, headers)
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("missing CSRF accepted")
	}
	headers["X-CSRF-Token"] = body["csrfToken"]
	r = request(t, h, "POST", "/api/v2/logout", nil, headers)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
}
func TestConcurrentUploadAndStartupRecovery(t *testing.T) {
	s, h, c := setup(t)
	b := testutil.VSIX("hello", "1.0.0", "", false, nil)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); code, _ := upload(t, h, b, "same"); codes <- code }()
	}
	wg.Wait()
	close(codes)
	sum := 0
	for code := range codes {
		sum += code
	}
	if sum != 401 {
		t.Fatal(sum)
	}
	p, e := s.db.Get("test.hello@1.0.0@universal")
	if e != nil {
		t.Fatal(e)
	}
	p.Status = "pending"
	s.db.Put(p)
	if _, e = s.Reconcile(false, nil); e != nil {
		t.Fatal(e)
	}
	p, _ = s.db.Get(p.Key())
	if p.Status != "stored" {
		t.Fatal(p.Status)
	}
	os.Remove(filepath.Join(c.Extensions, p.Filename))
	s.Reconcile(false, nil)
	p, _ = s.db.Get(p.Key())
	if p.Status != "missing" {
		t.Fatal(p.Status)
	}
	if code, v := upload(t, h, b, "repair"); code != 201 {
		t.Fatalf("repair failed %d %v", code, v)
	}
}
func TestExistingUnmanagedAndSymlink(t *testing.T) {
	s, h, c := setup(t)
	b := testutil.VSIX("hello", "1.0.0", "", false, nil)
	os.WriteFile(filepath.Join(c.Extensions, "custom.vsix"), b, 0644)
	if _, e := s.Reconcile(false, nil); e != nil {
		t.Fatal(e)
	}
	p, _ := s.db.Get("test.hello@1.0.0@universal")
	if p.Managed || p.Filename != "custom.vsix" {
		t.Fatal(p)
	}
	if code, _ := upload(t, h, b, "same"); code != 200 {
		t.Fatal(code)
	}
	other := testutil.VSIX("next", "1.0.0", "", false, nil)
	os.WriteFile(filepath.Join(c.State, "outside"), other, 0600)
	os.Symlink(filepath.Join(c.State, "outside"), filepath.Join(c.Extensions, "test.next-1.0.0-universal.vsix"))
	if code, _ := upload(t, h, other, "other"); code != 409 {
		t.Fatal("symlink destination accepted", code)
	}
}
func TestCheckDoesNotSkipMissingFile(t *testing.T) {
	_, h, c := setup(t)
	upload(t, h, testutil.VSIX("hello", "1.0.0", "", false, nil), "key")
	os.Remove(filepath.Join(c.Extensions, "test.hello-1.0.0-universal.vsix"))
	r := request(t, h, "POST", "/api/v2/extensions/check", []byte(`{"keys":["test.hello@1.0.0@universal"]}`), map[string]string{"Authorization": "Bearer " + testToken})
	defer r.Body.Close()
	var b struct {
		Packages map[string]vsix.Package `json:"packages"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if len(b.Packages) != 0 {
		t.Fatal("missing file skipped")
	}
}

func TestAdminReconcileRepairsMissingAndInventoriesNewFiles(t *testing.T) {
	_, h, c := setup(t)
	first := testutil.VSIX("hello", "1.0.0", "", false, nil)
	if code, _ := upload(t, h, first, "first"); code != 201 {
		t.Fatal(code)
	}
	if e := os.Remove(filepath.Join(c.Extensions, "test.hello-1.0.0-universal.vsix")); e != nil {
		t.Fatal(e)
	}
	second := testutil.VSIX("other", "2.0.0", "linux-x64", false, nil)
	if e := os.WriteFile(filepath.Join(c.Extensions, "external.vsix"), second, 0644); e != nil {
		t.Fatal(e)
	}
	r := request(t, h, "POST", "/api/v2/admin/reconcile", nil, map[string]string{"Authorization": "Bearer " + testToken})
	defer r.Body.Close()
	if r.StatusCode != 200 {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("reconcile: %d %s", r.StatusCode, body)
	}
	var result struct {
		Changed int            `json:"changed"`
		Counts  map[string]int `json:"statusCounts"`
	}
	if e := json.NewDecoder(r.Body).Decode(&result); e != nil {
		t.Fatal(e)
	}
	if result.Changed != 2 || result.Counts["stored"] != 1 || result.Counts["missing"] != 1 {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	r = request(t, h, "POST", "/api/v2/admin/reconcile", nil, nil)
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatalf("unauthenticated reconcile: %d", r.StatusCode)
	}
}

func TestInventoryWaitsForBackgroundStartupScan(t *testing.T) {
	s, h, c := unstarted(t)
	h.Client().Timeout = 5 * time.Second // a refused request must answer at once, not wait for the scan
	if e := os.WriteFile(filepath.Join(c.Extensions, "existing.vsix"), testutil.VSIX("hello", "1.0.0", "", false, nil), 0644); e != nil {
		t.Fatal(e)
	}
	// Hold the scan at its first step, as hashing a large archive would.
	s.maintenance.Lock()
	release := sync.OnceFunc(s.maintenance.Unlock)
	t.Cleanup(release)
	s.Start()
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	if code, body := getJSON(t, h, "/health/ready", nil); code != 200 || body["status"] != "starting" {
		t.Fatalf("readiness while scanning: %d %v", code, body)
	}
	for _, path := range []string{"POST /api/v2/extensions", "GET /api/v2/extensions", "POST /api/v2/extensions/check", "GET /api/v2/packages/download?key=test.hello@1.0.0@universal", "GET /api/v2/uploads/by-key/any", "POST /api/v2/admin/reconcile"} {
		method, target, _ := strings.Cut(path, " ")
		r := request(t, h, method, target, nil, auth)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		r.Body.Close()
		if r.StatusCode != 503 || body["code"] != "starting" || r.Header.Get("Retry-After") == "" {
			t.Fatalf("%s while scanning: %d %v", path, r.StatusCode, body)
		}
	}
	code, status := getJSON(t, h, "/api/v2/status", auth)
	if scan, _ := status["inventory"].(map[string]any); code != 200 || scan["state"] != "scanning" {
		t.Fatalf("status while scanning: %d %v", code, status)
	}
	release()
	s.background.Wait()
	if code, body := getJSON(t, h, "/health/ready", nil); code != 200 || body["status"] != "ready" {
		t.Fatalf("readiness after scan: %d %v", code, body)
	}
	if code, list := getJSON(t, h, "/api/v2/extensions", auth); code != 200 || list["total"] != float64(1) {
		t.Fatalf("inventory after scan: %d %v", code, list)
	}
	_, status = getJSON(t, h, "/api/v2/status", auth)
	if scan, _ := status["inventory"].(map[string]any); scan["state"] != "ready" || scan["scanned"] != float64(1) || scan["total"] != float64(1) {
		t.Fatalf("status after scan: %v", status)
	}
}

func TestFailedStartupScanRecoversThroughReconcile(t *testing.T) {
	s, h, c := unstarted(t)
	// An unmounted or unreadable share makes the directory listing fail.
	if e := os.Remove(c.Extensions); e != nil {
		t.Fatal(e)
	}
	s.Start()
	s.background.Wait()
	if e := os.Mkdir(c.Extensions, 0750); e != nil {
		t.Fatal(e)
	}
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	if code, body := getJSON(t, h, "/health/ready", nil); code != 503 {
		t.Fatalf("readiness after failed scan: %d %v", code, body)
	}
	if code, body := getJSON(t, h, "/api/v2/extensions", auth); code != 503 || body["code"] != "scan_failed" {
		t.Fatalf("inventory after failed scan: %d %v", code, body)
	}
	_, status := getJSON(t, h, "/api/v2/status", auth)
	if scan, _ := status["inventory"].(map[string]any); scan["state"] != "failed" || scan["error"] == nil {
		t.Fatalf("status after failed scan: %v", status)
	}
	r := request(t, h, "POST", "/api/v2/admin/reconcile", nil, auth)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("reconcile retry: %d", r.StatusCode)
	}
	if code, body := getJSON(t, h, "/health/ready", nil); code != 200 || body["status"] != "ready" {
		t.Fatalf("readiness after reconcile: %d %v", code, body)
	}
	if code, _ := getJSON(t, h, "/api/v2/extensions", auth); code != 200 {
		t.Fatalf("inventory after reconcile: %d", code)
	}
}

func TestCatalogGroupsVersionsPerExtension(t *testing.T) {
	s, h, c := setup(t)
	for i, pkg := range []struct {
		name, version, platform string
		prerelease              bool
	}{
		{"hello", "1.9.0", "", false},
		{"hello", "1.10.0", "linux-x64", false},
		{"hello", "1.10.0", "darwin-arm64", false},
		{"hello", "2.0.0", "", true},
		{"other", "0.1.0", "", false},
	} {
		if code, v := upload(t, h, testutil.VSIX(pkg.name, pkg.version, pkg.platform, pkg.prerelease, nil), fmt.Sprint(i)); code != 201 {
			t.Fatalf("seed %v: %d %v", pkg, code, v)
		}
	}
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	catalog := func(query string) (int, []map[string]any, float64) {
		t.Helper()
		code, body := getJSON(t, h, "/api/v2/catalog"+query, auth)
		list, _ := body["extensions"].([]any)
		out := []map[string]any{}
		for _, x := range list {
			out = append(out, x.(map[string]any))
		}
		total, _ := body["total"].(float64)
		return code, out, total
	}
	code, list, total := catalog("")
	if code != 200 || total != 2 || len(list) != 2 {
		t.Fatalf("catalog: %d %v", code, list)
	}
	hello := list[0]
	if hello["id"] != "test.hello" || hello["latestVersion"] != "2.0.0" || hello["versions"] != float64(3) || hello["packages"] != float64(4) || hello["displayName"] != "Test extension" {
		t.Fatalf("hello summary: %v", hello)
	}
	if fmt.Sprint(hello["platforms"]) != "[darwin-arm64 linux-x64 universal]" || hello["statusCounts"].(map[string]any)["stored"] != float64(4) || hello["bytes"].(float64) <= 0 {
		t.Fatalf("hello platforms/status: %v", hello)
	}
	if _, list, _ = catalog("?sort=versions"); list[0]["id"] != "test.hello" {
		t.Fatalf("sort by versions: %v", list)
	}
	if _, list, _ = catalog("?q=OTH"); len(list) != 1 || list[0]["id"] != "test.other" {
		t.Fatalf("search by ID: %v", list)
	}
	if _, list, _ = catalog("?q=test+extension"); len(list) != 2 {
		t.Fatalf("search by display name: %v", list)
	}
	if _, list, _ = catalog("?q=%25"); len(list) != 0 {
		t.Fatalf("LIKE wildcard was not escaped: %v", list)
	}
	if _, list, total = catalog("?limit=1&offset=1"); total != 2 || len(list) != 1 || list[0]["id"] != "test.other" {
		t.Fatalf("pagination: %v", list)
	}
	if code, _, _ = catalog("?sort=random"); code != 400 {
		t.Fatalf("unknown sort: %d", code)
	}
	code, detail := getJSON(t, h, "/api/v2/catalog/TEST.HELLO", auth)
	versions, _ := detail["versions"].([]any)
	if code != 200 || len(versions) != 3 {
		t.Fatalf("detail: %d %v", code, detail)
	}
	order := []string{}
	for _, v := range versions {
		order = append(order, v.(map[string]any)["version"].(string))
	}
	if strings.Join(order, " ") != "2.0.0 1.10.0 1.9.0" {
		t.Fatalf("versions are not newest first: %v", order)
	}
	if v := versions[1].(map[string]any); len(v["packages"].([]any)) != 2 || v["prerelease"] != false || versions[0].(map[string]any)["prerelease"] != true {
		t.Fatalf("version grouping: %v", versions)
	}
	if code, _ = getJSON(t, h, "/api/v2/catalog/test.absent", auth); code != 404 {
		t.Fatalf("absent extension: %d", code)
	}
	if e := os.Remove(filepath.Join(c.Extensions, "test.other-0.1.0-universal.vsix")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Reconcile(false, nil); e != nil {
		t.Fatal(e)
	}
	if _, list, _ = catalog("?attention=true"); len(list) != 1 || list[0]["id"] != "test.other" || list[0]["statusCounts"].(map[string]any)["missing"] != float64(1) {
		t.Fatalf("attention filter: %v", list)
	}
	_, status := getJSON(t, h, "/api/v2/status", auth)
	if status["extensions"] != float64(1) || status["versions"] != float64(3) || status["packages"] != float64(4) || status["attention"] != float64(1) {
		t.Fatalf("status totals: %v", status)
	}
}

func statusOf(t *testing.T, s *Server, key string) string {
	t.Helper()
	p, e := s.db.Get(key)
	if e != nil {
		t.Fatal(e)
	}
	return p.Status
}

// sameSizeEdit replaces a file's bytes without changing its size or modification time.
func sameSizeEdit(t *testing.T, path string, b []byte) {
	t.Helper()
	info, e := os.Stat(path)
	if e != nil || info.Size() != int64(len(b)) {
		t.Fatalf("replacement must keep the size: %v", e)
	}
	if e = os.WriteFile(path, b, 0644); e == nil {
		e = os.Chtimes(path, info.ModTime(), info.ModTime())
	}
	if e != nil {
		t.Fatal(e)
	}
}

func TestScanTrustsUnchangedFilesAndRehashesChangedOnes(t *testing.T) {
	s, h, c := setup(t)
	original := testutil.VSIX("hello", "1.0.0", "", false, map[string]string{"extension/code.js": "aaaa"})
	altered := testutil.VSIX("hello", "1.0.0", "", false, map[string]string{"extension/code.js": "bbbb"})
	if code, _ := upload(t, h, original, "one"); code != 201 {
		t.Fatal(code)
	}
	key, path := "test.hello@1.0.0@universal", filepath.Join(c.Extensions, "test.hello-1.0.0-universal.vsix")
	sameSizeEdit(t, path, altered)
	if _, e := s.Reconcile(false, nil); e != nil || statusOf(t, s, key) != "stored" {
		t.Fatalf("an unchanged size and modification time should be trusted: %v %s", e, statusOf(t, s, key))
	}
	var expected int
	if changed, e := s.Reconcile(true, func(_, n int) { expected = max(expected, n) }); e != nil || changed != 1 || statusOf(t, s, key) != "conflict" || expected != 1 {
		t.Fatalf("full verification should rehash: %d %v %s, expected %d files", changed, e, statusOf(t, s, key), expected)
	}
	if e := os.WriteFile(path, original, 0644); e != nil {
		t.Fatal(e)
	}
	later := time.Now().Add(time.Minute)
	if e := os.Chtimes(path, later, later); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Reconcile(false, nil); e != nil || statusOf(t, s, key) != "stored" {
		t.Fatalf("a changed modification time should be rehashed: %v %s", e, statusOf(t, s, key))
	}
}

func TestScanAdoptsVerifiedRecordsAfterUpgrade(t *testing.T) {
	s, h, c := setup(t)
	for i, name := range []string{"kept", "altered", "removed"} {
		if code, _ := upload(t, h, testutil.VSIX(name, "1.0.0", "", false, map[string]string{"extension/code.js": "aaaa"}), fmt.Sprint(i)); code != 201 {
			t.Fatal(code)
		}
	}
	// A database from the previous version has records but no remembered files.
	if _, e := s.db.DB.Exec(`DELETE FROM files; DELETE FROM meta; DELETE FROM dirty`); e != nil {
		t.Fatal(e)
	}
	sameSizeEdit(t, filepath.Join(c.Extensions, "test.altered-1.0.0-universal.vsix"), testutil.VSIX("altered", "1.0.0", "", false, map[string]string{"extension/code.js": "bbbb"}))
	os.Remove(filepath.Join(c.Extensions, "test.removed-1.0.0-universal.vsix"))
	if e := os.WriteFile(filepath.Join(c.Extensions, "external.vsix"), testutil.VSIX("external", "2.0.0", "", false, nil), 0644); e != nil {
		t.Fatal(e)
	}
	changed, e := s.Reconcile(false, nil)
	if e != nil || changed != 2 {
		t.Fatalf("adoption pass: %d %v", changed, e)
	}
	for key, want := range map[string]string{"test.kept@1.0.0@universal": "stored", "test.altered@1.0.0@universal": "stored", "test.removed@1.0.0@universal": "missing", "test.external@2.0.0@universal": "stored"} {
		if got := statusOf(t, s, key); got != want {
			t.Fatalf("%s: %s, want %s", key, got, want)
		}
	}
	var remembered int
	s.db.DB.QueryRow(`SELECT count(*) FROM files`).Scan(&remembered)
	if remembered != 3 {
		t.Fatalf("remembered files: %d", remembered)
	}
}

func TestScanAcrossManyBatches(t *testing.T) {
	defer func(n int) { scanBatch = n }(scanBatch)
	scanBatch = 3
	s, _, c := setup(t)
	write := func(name string, b []byte) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(c.Extensions, name), b, 0644); e != nil {
			t.Fatal(e)
		}
	}
	for i := range 20 {
		write(fmt.Sprintf("ext%02d.vsix", i), testutil.VSIX(fmt.Sprintf("ext%02d", i), "1.0.0", "", false, nil))
	}
	write("broken.vsix", []byte("not a zip"))
	write("copy-a.vsix", testutil.VSIX("dup", "1.0.0", "", false, map[string]string{"extension/code.js": "aaaa"}))
	write("copy-b.vsix", testutil.VSIX("dup", "1.0.0", "", false, map[string]string{"extension/code.js": "bbbb"}))
	if changed, e := s.Reconcile(false, nil); e != nil || changed != 21 {
		t.Fatalf("first scan: %d %v", changed, e)
	}
	if statusOf(t, s, "test.dup@1.0.0@universal") != "conflict" {
		t.Fatal("different bytes for one identity must conflict")
	}
	invalid := func() (n int) {
		s.db.DB.QueryRow(`SELECT count(*) FROM audit WHERE action='invalid_existing'`).Scan(&n)
		return n
	}
	if invalid() != 1 {
		t.Fatalf("invalid file audits: %d", invalid())
	}
	for _, i := range []int{2, 9, 17} {
		os.Remove(filepath.Join(c.Extensions, fmt.Sprintf("ext%02d.vsix", i)))
	}
	// Removing the copy that disagrees with the record resolves the conflict.
	dup, _ := s.db.Get("test.dup@1.0.0@universal")
	if hash, _, _ := vsix.HashFile(filepath.Join(c.Extensions, "copy-a.vsix")); hash == dup.SHA256 {
		os.Remove(filepath.Join(c.Extensions, "copy-b.vsix"))
	} else {
		os.Remove(filepath.Join(c.Extensions, "copy-a.vsix"))
	}
	var done, total int
	changed, e := s.Reconcile(false, func(d, n int) { done, total = d, n })
	if e != nil || changed != 4 || done != 19 || total != 19 {
		t.Fatalf("second scan: changed %d, progress %d/%d, %v", changed, done, total, e)
	}
	for _, i := range []int{2, 9, 17} {
		if statusOf(t, s, fmt.Sprintf("test.ext%02d@1.0.0@universal", i)) != "missing" {
			t.Fatalf("ext%02d should be missing", i)
		}
	}
	if statusOf(t, s, "test.dup@1.0.0@universal") != "stored" || statusOf(t, s, "test.ext03@1.0.0@universal") != "stored" {
		t.Fatal("remaining files should be stored")
	}
	if invalid() != 1 {
		t.Fatalf("an unchanged invalid file was audited again: %d", invalid())
	}
	var pending int
	s.db.DB.QueryRow(`SELECT count(*) FROM dirty`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("queued keys left after a scan: %d", pending)
	}
}

func TestStatusReportsDiskSpace(t *testing.T) {
	_, h, _ := setup(t)
	_, status := getJSON(t, h, "/api/v2/status", map[string]string{"Authorization": "Bearer " + testToken})
	storage, _ := status["storage"].(map[string]any)
	disk, _ := storage["extensions"].(map[string]any)
	total, _ := disk["total"].(float64)
	used, _ := disk["used"].(float64)
	free, _ := disk["free"].(float64)
	if total <= 0 || free <= 0 || used <= 0 || used+free > total {
		t.Fatalf("extension disk: %v", storage)
	}
	if _, separate := storage["state"]; separate {
		t.Fatalf("state shares the extension folder's filesystem and should not be listed twice: %v", storage)
	}
}

func TestLowDiskSpaceRule(t *testing.T) {
	for _, tt := range []struct {
		total, free, reserve uint64
		low                  bool
	}{
		{1000, 200, 10, false},
		{1000, 99, 10, true},   // under a tenth free
		{1000, 150, 400, true}, // cannot hold two of the largest uploads
	} {
		d := diskSpace{Total: tt.total, Free: tt.free}
		if d.judge(tt.reserve); d.Low != tt.low {
			t.Fatalf("%+v: low=%v", tt, d.Low)
		}
	}
}

func TestDeleteVersionsKeepsThemIndexed(t *testing.T) {
	s, h, c := setup(t)
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	files := map[string][]byte{}
	for i, v := range []struct{ version, platform string }{{"1.0.0", ""}, {"1.5.0", "linux-x64"}, {"1.5.0", "win32-x64"}, {"1.5.1", ""}, {"1.10.0", ""}} {
		b := testutil.VSIX("hello", v.version, v.platform, false, nil)
		files[v.version+v.platform] = b
		if code, _ := upload(t, h, b, fmt.Sprint(i)); code != 201 {
			t.Fatal(code)
		}
	}
	remove := func(body string) (int, map[string]any) {
		t.Helper()
		r := request(t, h, "POST", "/api/v2/catalog/test.hello/delete", []byte(body), auth)
		defer r.Body.Close()
		var out map[string]any
		json.NewDecoder(r.Body).Decode(&out)
		return r.StatusCode, out
	}
	for _, bad := range []string{`{}`, `{"through":"1.0.0","all":true}`, `{"through":"latest"}`} {
		if code, _ := remove(bad); code != 400 {
			t.Fatalf("%s accepted: %d", bad, code)
		}
	}
	if r := request(t, h, "POST", "/api/v2/catalog/test.absent/delete", []byte(`{"all":true}`), auth); r.StatusCode != 404 {
		t.Fatalf("unknown extension: %d", r.StatusCode)
	}
	code, preview := remove(`{"through":"1.5.1","dryRun":true}`)
	if code != 200 || fmt.Sprint(preview["versions"]) != "[1.0.0 1.5.0 1.5.1]" || preview["packages"] != float64(4) {
		t.Fatalf("preview: %d %v", code, preview)
	}
	if _, e := os.Stat(filepath.Join(c.Extensions, "test.hello-1.0.0-universal.vsix")); e != nil {
		t.Fatal("a dry run removed a file")
	}
	if code, done := remove(`{"through":"1.5.1"}`); code != 200 || done["packages"] != float64(4) || done["filesRemaining"] != float64(0) {
		t.Fatalf("delete: %d %v", code, done)
	}
	for _, name := range []string{"test.hello-1.0.0-universal.vsix", "test.hello-1.5.0-linux-x64.vsix", "test.hello-1.5.1-universal.vsix"} {
		if _, e := os.Stat(filepath.Join(c.Extensions, name)); !os.IsNotExist(e) {
			t.Fatalf("%s is still on disk", name)
		}
	}
	p, _ := s.db.Get("test.hello@1.5.0@linux-x64")
	if p.Status != "deleted" || p.DeletedAt == "" || p.SHA256 == "" {
		t.Fatalf("deleted record: %+v", p)
	}
	_, entry := getJSON(t, h, "/api/v2/catalog/test.hello", auth)
	x := entry["extension"].(map[string]any)
	if x["versions"] != float64(1) || x["packages"] != float64(1) || x["latestVersion"] != "1.10.0" || x["statusCounts"].(map[string]any)["deleted"] != float64(4) {
		t.Fatalf("summary after delete: %v", x)
	}
	if versions := entry["versions"].([]any); len(versions) != 4 {
		t.Fatalf("deleted versions should stay listed: %v", versions)
	}
	_, status := getJSON(t, h, "/api/v2/status", auth)
	if status["attention"] != float64(0) || status["deleted"] != float64(4) || status["packages"] != float64(1) {
		t.Fatalf("status after delete: %v", status)
	}
	r := request(t, h, "POST", "/api/v2/extensions/check", []byte(`{"keys":["test.hello@1.0.0@universal","test.hello@1.10.0@universal"]}`), auth)
	var check struct {
		Packages map[string]vsix.Package `json:"packages"`
		Deleted  []string                `json:"deleted"`
	}
	json.NewDecoder(r.Body).Decode(&check)
	r.Body.Close()
	if len(check.Packages) != 1 || fmt.Sprint(check.Deleted) != "[test.hello@1.0.0@universal]" {
		t.Fatalf("check: %+v", check)
	}
	if r = request(t, h, "GET", "/api/v2/packages/download?key=test.hello@1.0.0@universal", nil, auth); r.StatusCode != 404 {
		t.Fatalf("download of a deleted version: %d", r.StatusCode)
	}
	// A restart keeps the index; a file left by an interrupted deletion is removed.
	os.WriteFile(filepath.Join(c.Extensions, "test.hello-1.5.1-universal.vsix"), files["1.5.1"], 0644)
	if _, e := s.Reconcile(false, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(c.Extensions, "test.hello-1.5.1-universal.vsix")); !os.IsNotExist(e) || statusOf(t, s, "test.hello@1.5.1@universal") != "deleted" {
		t.Fatal("scan should finish an interrupted deletion and keep the record deleted")
	}
	// Any upload brings a deleted version back while it is within the version limit.
	if code, v := upload(t, h, files["1.0.0"], "again"); code != 201 || statusOf(t, s, "test.hello@1.0.0@universal") != "stored" {
		t.Fatalf("re-upload of a deleted version: %d %v", code, v)
	}
	if p, _ = s.db.Get("test.hello@1.0.0@universal"); p.DeletedAt != "" || p.DeletedBy != "" {
		t.Fatalf("a re-uploaded version still carries its deletion: %+v", p)
	}
	if code, done := remove(`{"all":true}`); code != 200 || done["packages"] != float64(2) {
		t.Fatalf("delete all: %d %v", code, done)
	}
	_, status = getJSON(t, h, "/api/v2/status", auth)
	if status["extensions"] != float64(0) || status["deleted"] != float64(5) {
		t.Fatalf("status after deleting everything: %v", status)
	}
}

func TestVersionLimit(t *testing.T) {
	s, h, c := setup(t)
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	for i, v := range []struct{ name, version, platform string }{
		{"hello", "1.0.0", "linux-x64"}, {"hello", "1.0.0", "win32-x64"}, {"hello", "1.1.0", ""}, {"hello", "1.2.0", ""}, {"hello", "2.0.0", ""},
		{"other", "1.0.0", ""}, {"other", "2.0.0", ""}, {"other", "3.0.0", ""},
	} {
		if code, _ := upload(t, h, testutil.VSIX(v.name, v.version, v.platform, false, nil), fmt.Sprint(i)); code != 201 {
			t.Fatal(code)
		}
	}
	put := func(path, body string) (int, map[string]any) {
		t.Helper()
		r := request(t, h, "PUT", path, []byte(body), auth)
		defer r.Body.Close()
		var out map[string]any
		json.NewDecoder(r.Body).Decode(&out)
		return r.StatusCode, out
	}
	for path, body := range map[string]string{"/api/v2/limit": `{"keep":null}`, "/api/v2/catalog/test.hello/limit": `{"keep":-1}`} {
		if code, _ := put(path, body); code != 400 {
			t.Fatalf("%s %s accepted: %d", path, body, code)
		}
	}
	code, preview := put("/api/v2/limit", `{"keep":2,"dryRun":true}`)
	if p := preview["trimmed"].(map[string]any); code != 200 || p["extensions"] != float64(2) || p["versions"] != float64(3) || p["packages"] != float64(4) {
		t.Fatalf("library preview: %d %v", code, preview)
	}
	if statusOf(t, s, "test.hello@1.0.0@linux-x64") != "stored" {
		t.Fatal("a preview deleted a version")
	}
	if code, _ := put("/api/v2/catalog/test.other/limit", `{"keep":0}`); code != 200 {
		t.Fatalf("keep-all override: %d", code)
	}
	if code, done := put("/api/v2/limit", `{"keep":2}`); code != 200 || done["trimmed"].(map[string]any)["packages"] != float64(3) {
		t.Fatalf("apply library limit: %d %v", code, done)
	}
	for key, want := range map[string]string{"test.hello@1.0.0@linux-x64": "deleted", "test.hello@1.1.0@universal": "deleted", "test.hello@1.2.0@universal": "stored", "test.hello@2.0.0@universal": "stored", "test.other@1.0.0@universal": "stored"} {
		if got := statusOf(t, s, key); got != want {
			t.Fatalf("%s: %s, want %s", key, got, want)
		}
	}
	if p, _ := s.db.Get("test.hello@1.1.0@universal"); p.DeletedBy != "limit" {
		t.Fatalf("deleted by: %q", p.DeletedBy)
	}
	if _, e := os.Stat(filepath.Join(c.Extensions, "test.hello-1.1.0-universal.vsix")); !os.IsNotExist(e) {
		t.Fatal("a version beyond the limit is still on disk")
	}
	if code, v := upload(t, h, testutil.VSIX("hello", "1.1.5", "", false, nil), "older"); code != 409 || v["code"] != "retention" {
		t.Fatalf("upload older than the limit: %d %v", code, v)
	}
	if code, v := upload(t, h, testutil.VSIX("hello", "1.1.0", "", false, nil), "deleted-older"); code != 409 || v["code"] != "retention" {
		t.Fatalf("re-uploading a deleted version beyond the limit: %d %v", code, v)
	}
	if code, v := upload(t, h, testutil.VSIX("hello", "2.1.0", "", false, nil), "newer"); code != 201 || statusOf(t, s, "test.hello@1.2.0@universal") != "deleted" || fmt.Sprint(v["removedVersions"]) != "[1.2.0]" {
		t.Fatalf("a newer upload should push the oldest kept version out and say so: %d %v", code, v)
	}
	r := request(t, h, "POST", "/api/v2/extensions/check", []byte(`{"keys":["test.hello@2.1.0@universal","test.other@1.0.0@universal"]}`), auth)
	var check struct {
		Limits map[string]int `json:"limits"`
	}
	json.NewDecoder(r.Body).Decode(&check)
	r.Body.Close()
	if check.Limits["test.hello"] != 2 || check.Limits["test.other"] != 0 {
		t.Fatalf("check limits: %v", check.Limits)
	}
	_, entry := getJSON(t, h, "/api/v2/catalog/test.other", auth)
	if x := entry["extension"].(map[string]any); x["keep"] != float64(0) || x["keepSource"] != "extension" {
		t.Fatalf("override in catalog: %v", x)
	}
	if code, done := put("/api/v2/catalog/test.other/limit", `{"keep":null}`); code != 200 || done["keepSource"] != "library" || done["trimmed"].(map[string]any)["packages"] != float64(1) {
		t.Fatalf("follow the library default again: %d %v", code, done)
	}
	_, limits := getJSON(t, h, "/api/v2/limit", auth)
	if limits["keep"] != float64(2) || len(limits["extensionLimits"].(map[string]any)) != 0 {
		t.Fatalf("limits: %v", limits)
	}
	// A file dropped into the folder by hand is inventoried, then trimmed to the limit.
	if e := os.WriteFile(filepath.Join(c.Extensions, "manual.vsix"), testutil.VSIX("hello", "0.9.0", "", false, nil), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Reconcile(false, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(c.Extensions, "manual.vsix")); !os.IsNotExist(e) || statusOf(t, s, "test.hello@0.9.0@universal") != "deleted" {
		t.Fatal("a scan should trim versions beyond the limit")
	}
	_, status := getJSON(t, h, "/api/v2/status", auth)
	if status["limit"].(map[string]any)["keep"] != float64(2) || status["apiVersion"] != float64(2) {
		t.Fatalf("status: %v", status)
	}
	// Raising the limit lets a version the limit deleted come back.
	if code, _ := put("/api/v2/limit", `{"keep":3}`); code != 200 {
		t.Fatalf("raise the limit: %d", code)
	}
	if code, v := upload(t, h, testutil.VSIX("hello", "1.2.0", "", false, nil), "back"); code != 201 || statusOf(t, s, "test.hello@1.2.0@universal") != "stored" {
		t.Fatalf("re-upload after raising the limit: %d %v", code, v)
	}
}

func TestAPIv1IsRetired(t *testing.T) {
	_, h, _ := setup(t)
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	if code, body := getJSON(t, h, "/api/v1/status", auth); code != 200 || body["apiVersion"] != float64(2) {
		t.Fatalf("v1 status should tell old clients about version 2: %d %v", code, body)
	}
	for _, method := range []string{"GET", "POST"} {
		r := request(t, h, method, "/api/v1/extensions", nil, auth)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		r.Body.Close()
		if r.StatusCode != 410 || body["code"] != "api_version" {
			t.Fatalf("%s v1: %d %v", method, r.StatusCode, body)
		}
	}
}

func TestAPIErrorsAreJSON(t *testing.T) {
	_, h, _ := setup(t)
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	for _, c := range []struct {
		method, path string
		status       int
		code, allow  string
	}{
		{"GET", "/api/v2/nope", 404, "not_found", ""},
		{"POST", "/api/v2/nope", 404, "not_found", ""},
		{"DELETE", "/api/v2/limit", 405, "method_not_allowed", "GET, HEAD, PUT"},
		{"PUT", "/api/v2/status", 405, "method_not_allowed", "GET, HEAD"},
		{"DELETE", "/api/v2/catalog/test.x/limit", 405, "method_not_allowed", "PUT"},
	} {
		r := request(t, h, c.method, c.path, nil, auth)
		var body map[string]string
		e := json.NewDecoder(r.Body).Decode(&body)
		r.Body.Close()
		if e != nil || r.StatusCode != c.status || body["code"] != c.code || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("Allow") != c.allow {
			t.Fatalf("%s %s: %d %v %q allow=%q (%v)", c.method, c.path, r.StatusCode, body, r.Header.Get("Content-Type"), r.Header.Get("Allow"), e)
		}
	}
	if r := request(t, h, "GET", "/no-such-page", nil, nil); r.StatusCode != 404 || strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("pages outside the API keep the file server's answer: %d %q", r.StatusCode, r.Header.Get("Content-Type"))
	}
}

func TestPageSizeAndAuditFilters(t *testing.T) {
	_, h, _ := setup(t)
	auth := map[string]string{"Authorization": "Bearer " + testToken}
	for i, name := range []string{"alpha", "beta", "gamma"} {
		if code, _ := upload(t, h, testutil.VSIX(name, "1.0.0", "", false, nil), fmt.Sprint(i)); code != 201 {
			t.Fatal(code)
		}
	}
	for _, path := range []string{"/api/v2/catalog?pageSize=2", "/api/v2/catalog?limit=2"} {
		_, body := getJSON(t, h, path, auth)
		if len(body["extensions"].([]any)) != 2 || body["pageSize"] != float64(2) || body["total"] != float64(3) {
			t.Fatalf("%s: %v", path, body)
		}
	}
	events := func(query string) []any {
		t.Helper()
		r := request(t, h, "GET", "/api/v2/audit-events"+query, nil, auth)
		defer r.Body.Close()
		var out []any
		json.NewDecoder(r.Body).Decode(&out)
		return out
	}
	stored := events("?action=stored&actor=sync-token")
	if len(stored) != 3 {
		t.Fatalf("stored events: %v", stored)
	}
	first := events("?action=stored&pageSize=2")
	older := events(fmt.Sprintf("?action=stored&pageSize=2&before=%v", first[1].(map[string]any)["id"]))
	if len(first) != 2 || len(older) != 1 || older[0].(map[string]any)["detail"] != "test.alpha@1.0.0@universal" {
		t.Fatalf("paging: %v then %v", first, older)
	}
	if none := events("?action=stored&actor=operator"); len(none) != 0 {
		t.Fatalf("actor filter: %v", none)
	}
}

func TestLoginThrottlingUsesTrustedProxyClients(t *testing.T) {
	loginFrom := func(h *httptest.Server, client string) int {
		t.Helper()
		r := request(t, h, "POST", "/api/v2/login", []byte(`{"password":"wrong-password-here"}`), map[string]string{"Origin": "http://localhost", "X-Forwarded-For": client})
		r.Body.Close()
		return r.StatusCode
	}
	// Behind a trusted proxy, one client's failures do not lock out another.
	_, proxied, _ := unstarted(t, func(c *Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	})
	for range 10 {
		loginFrom(proxied, "203.0.113.5")
	}
	if code := loginFrom(proxied, "203.0.113.5"); code != 429 {
		t.Fatalf("eleventh failure from one client: %d", code)
	}
	if code := loginFrom(proxied, "198.51.100.7, 203.0.113.99"); code != 401 {
		t.Fatalf("another client behind the proxy was throttled: %d", code)
	}
	if code := loginFrom(proxied, "198.51.100.8:4321"); code != 401 {
		t.Fatalf("a client recorded as address:port was throttled with the proxy: %d", code)
	}
	// Without a trusted proxy, a forged X-Forwarded-For cannot dodge throttling.
	_, direct, _ := unstarted(t)
	for i := range 10 {
		loginFrom(direct, fmt.Sprintf("192.0.2.%d", i))
	}
	if code := loginFrom(direct, "192.0.2.200"); code != 429 {
		t.Fatalf("forged addresses escaped throttling: %d", code)
	}
}

func TestSummariesAreBuiltAfterUpgrade(t *testing.T) {
	s, h, c := setup(t)
	for i, v := range []string{"1.0.0", "2.0.0"} {
		if code, _ := upload(t, h, testutil.VSIX("hello", v, "", false, nil), fmt.Sprint(i)); code != 201 {
			t.Fatal(code)
		}
	}
	// A database from v0.8.1 has packages but no summaries.
	if _, e := s.db.DB.Exec(`DELETE FROM summaries; DELETE FROM summary_dirty; DELETE FROM meta WHERE k='summaries'`); e != nil {
		t.Fatal(e)
	}
	h.Close()
	s.Close()
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Start()
	s.background.Wait()
	x, total, e := s.db.Catalog(store.CatalogQuery{Limit: 10})
	if e != nil || total != 1 || x[0].Versions != 2 || x[0].LatestVersion != "2.0.0" {
		t.Fatalf("catalog after upgrade: %d %+v %v", total, x, e)
	}
}

func TestStatePathWithQuestionMarkIsRefused(t *testing.T) {
	dir := t.TempDir()
	_, e := New(Config{Extensions: filepath.Join(dir, "extensions"), State: filepath.Join(dir, "state?x"), Token: testToken, Password: "a-long-test-password", PublicURL: "http://localhost", MaxUpload: 1 << 20})
	if e == nil || !strings.Contains(e.Error(), "must not contain '?'") {
		t.Fatalf("state path with '?': %v", e)
	}
}
