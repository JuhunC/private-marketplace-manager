package mcpadmin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	if ok, _ := reconcile["ok"].(bool); !ok {
		t.Fatalf("unexpected reconcile: %+v", reconcile)
	}
	list := callToolJSON(t, ctx, clientSession, "manager_inventory", map[string]any{"id": "test.repair"})
	if total, _ := list["total"].(float64); total != 1 {
		t.Fatalf("unexpected inventory: %+v", list)
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
