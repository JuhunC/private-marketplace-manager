package server

import (
	"bytes"
	"encoding/json"
	"github.com/JuhunC/private-marketplace-manager/internal/testutil"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const testToken = "0123456789012345678901234567890123456789"

func setup(t *testing.T) (*Server, *httptest.Server, Config) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("receiver is a Linux container; Windows CLI tested separately")
	}
	dir := t.TempDir()
	c := Config{Extensions: filepath.Join(dir, "extensions"), State: filepath.Join(dir, "state"), Token: testToken, Password: "a-long-test-password", PublicURL: "http://localhost", MaxUpload: 1 << 20}
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	h := httptest.NewServer(s.Handler())
	t.Cleanup(func() { h.Close(); s.Close() })
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
func upload(t *testing.T, h *httptest.Server, b []byte, key string) (int, map[string]any) {
	t.Helper()
	r := request(t, h, "POST", "/api/v1/extensions", b, map[string]string{"Authorization": "Bearer " + testToken, "Idempotency-Key": key})
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
	r := request(t, h, "POST", "/api/v1/extensions", b, nil)
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
		{"/api/v1/extensions", []byte("invalid zip"), nil, 422},
		{"/api/v1/extensions?id=test.wrong", b, nil, 422},
		{"/api/v1/extensions", b, map[string]string{"X-Content-SHA256": "bad"}, 422},
		{"/api/v1/extensions", make([]byte, (1<<20)+1), nil, 413},
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
	r := request(t, h, "POST", "/api/v1/login", []byte(`{"password":"a-long-test-password"}`), map[string]string{"Origin": "https://evil.example"})
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
	r = request(t, h, "POST", "/api/v1/login", []byte(`{"password":"a-long-test-password"}`), map[string]string{"Origin": "http://localhost"})
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	cookie := r.Cookies()[0]
	headers := map[string]string{"Cookie": cookie.String(), "Origin": "http://localhost"}
	r = request(t, h, "POST", "/api/v1/logout", nil, headers)
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("missing CSRF accepted")
	}
	headers["X-CSRF-Token"] = body["csrfToken"]
	r = request(t, h, "POST", "/api/v1/logout", nil, headers)
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
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	p, _ = s.db.Get(p.Key())
	if p.Status != "stored" {
		t.Fatal(p.Status)
	}
	os.Remove(filepath.Join(c.Extensions, p.Filename))
	s.Reconcile()
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
	if e := s.Reconcile(); e != nil {
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
	r := request(t, h, "POST", "/api/v1/extensions/check", []byte(`{"keys":["test.hello@1.0.0@universal"]}`), map[string]string{"Authorization": "Bearer " + testToken})
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
	r := request(t, h, "POST", "/api/v1/admin/reconcile", nil, map[string]string{"Authorization": "Bearer " + testToken})
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
	r = request(t, h, "POST", "/api/v1/admin/reconcile", nil, nil)
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatalf("unauthenticated reconcile: %d", r.StatusCode)
	}
}
