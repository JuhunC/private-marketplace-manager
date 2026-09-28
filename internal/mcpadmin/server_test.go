package mcpadmin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/adminapi"
	"github.com/JuhunC/private-marketplace-manager/internal/server"
	"github.com/JuhunC/private-marketplace-manager/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpTestToken = "mcp-test-token-012345678901234567890123456789"

func TestMCPToolsAnalyzeUploadAndReconcile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("receiver is distributed as a Linux container")
	}
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := t.TempDir()
	manager, e := server.New(server.Config{
		Extensions: filepath.Join(dir, "extensions"),
		State:      filepath.Join(dir, "state"),
		Token:      mcpTestToken,
		Password:   "mcp-test-admin-password",
		PublicURL:  "http://localhost",
		MaxUpload:  1 << 20,
	})
	if e != nil {
		t.Fatal(e)
	}
	httpServer := httptest.NewServer(manager.Handler())
	t.Cleanup(func() { httpServer.Close(); manager.Close() })
	manager.Start()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var ready struct{ Status string }
		if r, e := httpServer.Client().Get(httpServer.URL + "/health/ready"); e == nil {
			json.NewDecoder(r.Body).Decode(&ready)
			r.Body.Close()
		}
		if ready.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager inventory scan did not finish")
		}
	}
	tokenFile := filepath.Join(dir, "token")
	if e = os.WriteFile(tokenFile, []byte(mcpTestToken), 0600); e != nil {
		t.Fatal(e)
	}
	api, e := adminapi.New(adminapi.Config{ServerURL: httpServer.URL, TokenFile: tokenFile, AllowInsecureHTTP: true, MaxUploadBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	mcpServer := New(api, "test")
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serverSession, e := mcpServer.Connect(ctx, serverTransport, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, e := client.Connect(ctx, clientTransport, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer clientSession.Close()
	tools, e := clientSession.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("expected 8 tools, got %d", len(tools.Tools))
	}
	analysis := callToolJSON(t, ctx, clientSession, "manager_analyze", nil)
	if healthy, _ := analysis["healthy"].(bool); !healthy {
		t.Fatalf("unexpected unhealthy analysis: %+v", analysis)
	}
	vsixFile := filepath.Join(dir, "repair.vsix")
	if e = os.WriteFile(vsixFile, testutil.VSIX("repair", "1.2.3", "linux-x64", false, nil), 0600); e != nil {
		t.Fatal(e)
	}
	upload := callToolJSON(t, ctx, clientSession, "manager_upload_vsix", map[string]any{"path": vsixFile})
	pkg, _ := upload["package"].(map[string]any)
	if pkg["id"] != "test.repair" || pkg["status"] != "stored" {
		t.Fatalf("unexpected upload: %+v", upload)
	}
	reconcile := callToolJSON(t, ctx, clientSession, "manager_reconcile_storage", nil)
	if ok, _ := reconcile["ok"].(bool); !ok || reconcile["fullVerify"] != false {
		t.Fatalf("unexpected reconcile: %+v", reconcile)
	}
	reconcile = callToolJSON(t, ctx, clientSession, "manager_reconcile_storage", map[string]any{"fullVerify": true})
	if ok, _ := reconcile["ok"].(bool); !ok || reconcile["fullVerify"] != true {
		t.Fatalf("unexpected full reconcile: %+v", reconcile)
	}
	list := callToolJSON(t, ctx, clientSession, "manager_inventory", map[string]any{"id": "test.repair"})
	if total, _ := list["total"].(float64); total != 1 {
		t.Fatalf("unexpected inventory: %+v", list)
	}
}

func TestAnalyzeReportsStartupScan(t *testing.T) {
	var failed atomic.Bool
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/ready":
			if failed.Load() {
				w.WriteHeader(503)
				io.WriteString(w, `{"code":"not_ready","error":"inventory scan failed; see manager logs"}`)
				return
			}
			io.WriteString(w, `{"status":"starting"}`)
		case "/api/v1/status":
			if failed.Load() {
				io.WriteString(w, `{"apiVersion":1,"inventory":{"state":"failed","scanned":3,"total":10,"error":"open /data/extensions: permission denied"}}`)
				return
			}
			io.WriteString(w, `{"apiVersion":1,"inventory":{"state":"scanning","scanned":3,"total":10}}`)
		default:
			t.Errorf("analysis requested %s during the startup scan", r.URL.Path)
			w.WriteHeader(503)
		}
	}))
	defer manager.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(tokenFile, []byte(mcpTestToken), 0600); e != nil {
		t.Fatal(e)
	}
	api, e := adminapi.New(adminapi.Config{ServerURL: manager.URL, TokenFile: tokenFile, AllowInsecureHTTP: true, MaxUploadBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	out := analyze(context.Background(), api)
	if !out.Healthy || len(out.Findings) != 1 || out.Findings[0].Code != "inventory_scan_running" || out.Findings[0].Evidence != "3 of 10 files examined" {
		t.Fatalf("scanning analysis: %+v", out)
	}
	failed.Store(true)
	out = analyze(context.Background(), api)
	if out.Healthy || len(out.Findings) != 2 || out.Findings[0].Code != "manager_not_ready" || out.Findings[1].Code != "inventory_scan_failed" || out.Findings[1].Evidence != "open /data/extensions: permission denied" {
		t.Fatalf("failed-scan analysis: %+v", out)
	}
}

func TestAnalyzeWarnsAboutLowDiskSpace(t *testing.T) {
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/ready":
			io.WriteString(w, `{"status":"ready"}`)
		case "/api/v1/status":
			io.WriteString(w, `{"apiVersion":1,"inventory":{"state":"ready"},"storage":{"extensions":{"total":2199023255552,"used":2089072092774,"free":107374182400,"low":true}}}`)
		case "/api/v1/extensions":
			io.WriteString(w, `{"packages":[],"total":0}`)
		case "/api/v1/sync-runs":
			io.WriteString(w, `[]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer manager.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(tokenFile, []byte(mcpTestToken), 0600); e != nil {
		t.Fatal(e)
	}
	api, e := adminapi.New(adminapi.Config{ServerURL: manager.URL, TokenFile: tokenFile, AllowInsecureHTTP: true, MaxUploadBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	out := analyze(context.Background(), api)
	if len(out.Findings) != 1 || out.Findings[0].Code != "low_disk_space" || out.Findings[0].Evidence != "100.0 GiB free of 2.0 TiB" {
		t.Fatalf("low disk analysis: %+v", out.Findings)
	}
}

func callToolJSON(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	if result.IsError {
		t.Fatalf("tool %s failed: %+v", name, result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tool %s returned no content", name)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool %s returned non-text content", name)
	}
	var out map[string]any
	if e = json.Unmarshal([]byte(text.Text), &out); e != nil {
		t.Fatalf("tool %s returned invalid JSON: %v", name, e)
	}
	return out
}
