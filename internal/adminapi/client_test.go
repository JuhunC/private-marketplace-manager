package adminapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigResolvesFiles(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "mcp.json")
	data := []byte(`{"serverUrl":"https://manager.example","tokenFile":"token","caFile":"corp.pem","maxUploadBytes":123}`)
	if e := os.WriteFile(filename, data, 0600); e != nil {
		t.Fatal(e)
	}
	c, e := LoadConfig(filename)
	if e != nil {
		t.Fatal(e)
	}
	if c.TokenFile != filepath.Join(dir, "token") || c.CAFile != filepath.Join(dir, "corp.pem") || c.MaxUploadBytes != 123 {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestNewRejectsInsecureRemoteHTTP(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if e := os.WriteFile(token, []byte("0123456789012345678901234567890123456789"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := New(Config{ServerURL: "http://manager.example", TokenFile: token, AllowInsecureHTTP: true, MaxUploadBytes: 1}); e == nil {
		t.Fatal("remote HTTP accepted")
	}
}
