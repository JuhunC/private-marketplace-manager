package vsix

import (
	"github.com/JuhunC/private-marketplace-manager/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestInspect(t *testing.T) {
	cases := []struct {
		name, platform string
		pre            bool
		extra          map[string]string
		bad            bool
	}{
		{"universal", "", false, nil, false}, {"arm64", "linux-arm64", true, nil, false},
		{"traversal", "", false, map[string]string{"../bad": "bad"}, true},
		{"backslash", "", false, map[string]string{`extension\bad`: "bad"}, true},
		{"wrong_identity", "", false, map[string]string{"extension/package.json": `{"name":"other","publisher":"test","version":"1.2.3"}`}, true},
		{"xxe", "", false, map[string]string{"extension.vsixmanifest": `<!DOCTYPE foo [<!ENTITY x SYSTEM "file:///etc/passwd">]><foo>&x;</foo>`}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "test.vsix")
			os.WriteFile(filename, testutil.VSIX("hello", "1.2.3", c.platform, c.pre, c.extra), 0600)
			p, e := Inspect(filename)
			if c.bad {
				if e == nil {
					t.Fatal("unsafe archive accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			platform := c.platform
			if platform == "" {
				platform = "universal"
			}
			if p.Key() != "test.hello@1.2.3@"+platform || p.Prerelease != c.pre || len(p.SHA256) != 64 {
				t.Fatalf("unexpected metadata: %+v", p)
			}
		})
	}
}
func TestIdentity(t *testing.T) {
	for _, s := range []string{"../name", "publisher", "a.b.c", "a/b.c", "a.b?"} {
		if ValidID(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	if !ValidIdentity("ms-python.python", "2026.1.0", "linux-arm64") {
		t.Fatal("valid identity rejected")
	}
}
