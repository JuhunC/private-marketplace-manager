package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/testutil"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
)

type transport func(*http.Request) (*http.Response, error)

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }
func TestReadListAndConfig(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "extensions.txt")
	os.WriteFile(list, []byte("\ufeff# identifiers\r\nTest.One\r\ntest.one\n test.two \n"), 0600)
	ids, h, e := ReadList(list)
	if e != nil || len(ids) != 2 || len(h) != 64 {
		t.Fatal(ids, h, e)
	}
	os.WriteFile(list, []byte("test.one\nbad\n"), 0600)
	if _, _, e = ReadList(list); e == nil {
		t.Fatal("invalid list accepted")
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"tokenFile":"secret","workDir":"work"}`), 0600)
	c, e := LoadConfig(filepath.Join(dir, "config.json"))
	if e != nil || c.TokenFile != filepath.Join(dir, "secret") {
		t.Fatal(c, e)
	}
}
func galleryHandler(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var body struct {
		Flags   int `json:"flags"`
		Filters []struct {
			Criteria []struct {
				Value string `json:"value"`
			} `json:"criteria"`
		} `json:"filters"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Flags != 147 {
		t.Errorf("latest-only filtering: %d", body.Flags)
	}
	id := body.Filters[0].Criteria[0].Value
	name := strings.TrimPrefix(id, "test.")
	json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"extensions": []any{map[string]any{"extensionName": name, "publisher": map[string]any{"publisherName": "test"}, "versions": []any{
		map[string]any{"version": "2.0.0", "targetPlatform": "linux-arm64", "files": []any{map[string]any{"assetType": "Microsoft.VisualStudio.Services.VSIXPackage", "source": "https://test.gallerycdn.vsassets.io/" + name + "/2"}}},
		map[string]any{"version": "1.0.0", "files": []any{map[string]any{"assetType": "Microsoft.VisualStudio.Services.VSIXPackage", "source": "https://test.gallerycdn.vsassets.io/" + name + "/1"}}},
	}}}}}})
}
func TestDiscoveryFullHistoryAndPlatforms(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { galleryHandler(t, w, r) }))
	defer h.Close()
	g := Gallery{Client: h.Client(), Endpoint: h.URL, Attempts: 1}
	a, e := g.Discover(context.Background(), "test.one")
	if e != nil || len(a) != 2 {
		t.Fatal(a, e)
	}
	if a[0].Version != "1.0.0" || a[0].Platform != "universal" || a[1].Platform != "linux-arm64" {
		t.Fatal(a)
	}
}
func TestRetryClassification(t *testing.T) {
	calls := 0
	e := retry(context.Background(), 4, func() (time.Duration, error) { calls++; return 0, io.ErrUnexpectedEOF })
	if e == nil || calls != 1 {
		t.Fatal("permanent error retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	retry(ctx, 4, func() (time.Duration, error) { calls++; return 0, nil })
	if calls != 0 {
		t.Fatal("canceled job ran")
	}
}
func TestMigrationAndChangingList(t *testing.T) {
	var mu sync.Mutex
	stored := map[string]vsix.Package{}
	downloads := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gallery" {
			galleryHandler(t, w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/asset/") {
			parts := strings.Split(r.URL.Path, "/")
			name, v := parts[2], parts[3]
			platform := ""
			if v == "2" {
				platform = "linux-arm64"
			}
			mu.Lock()
			downloads++
			mu.Unlock()
			w.Write(testutil.VSIX(name, v+".0.0", platform, false, nil))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("missing token")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/status":
			io.WriteString(w, `{"apiVersion":1}`)
		case "/api/v1/extensions/check":
			mu.Lock()
			defer mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"packages": stored})
		case "/api/v1/extensions":
			f, e := os.CreateTemp(t.TempDir(), "upload")
			if e != nil {
				t.Error(e)
				return
			}
			io.Copy(f, r.Body)
			f.Close()
			p, e := vsix.Inspect(f.Name())
			if e != nil {
				t.Error(e)
				w.WriteHeader(422)
				return
			}
			mu.Lock()
			stored[p.Key()] = p
			mu.Unlock()
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"package": p})
		case "/api/v1/sync-runs":
			io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer h.Close()
	base := t.TempDir()
	token := filepath.Join(base, "token")
	os.WriteFile(token, []byte(strings.Repeat("x", 32)), 0600)
	list := filepath.Join(base, "extensions.txt")
	os.WriteFile(list, []byte("test.one\n"), 0600)
	run := func(work string) Report {
		c := Config{ServerURL: h.URL, TokenFile: token, WorkDir: filepath.Join(base, work), Concurrency: 2, MaxDownloadBytes: 1 << 20, Retries: 1, AllowInsecureHTTP: true}
		r, e := New(c)
		if e != nil {
			t.Fatal(e)
		}
		r.Log = io.Discard
		r.Gallery.Endpoint = h.URL + "/gallery"
		underlying := http.DefaultTransport
		r.Gallery.Client.Transport = transport(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			if clone.URL.Host == "test.gallerycdn.vsassets.io" {
				u := *clone.URL
				u.Scheme = "http"
				u.Host = strings.TrimPrefix(h.URL, "http://")
				u.Path = "/asset" + u.Path
				clone.URL = &u
			}
			return underlying.RoundTrip(clone)
		})
		report, e := r.Run(context.Background(), list, false)
		if e != nil {
			t.Fatal(e)
		}
		return report
	}
	if p := run("first"); p.Stored != 2 {
		t.Fatal(p)
	}
	if p := run("new-machine"); p.Stored != 0 || p.Skipped != 2 {
		t.Fatal(p)
	}
	os.WriteFile(list, []byte("test.one\ntest.two\n"), 0600)
	if p := run("third"); p.Stored != 2 || p.Skipped != 2 {
		t.Fatal(p)
	}
	os.WriteFile(list, []byte("test.two\n"), 0600)
	if p := run("fourth"); p.Stored != 0 || p.Skipped != 2 {
		t.Fatal(p)
	}
	if len(stored) != 4 || downloads != 4 {
		t.Fatalf("lost or duplicate transfers: %d %d", len(stored), downloads)
	}
}
