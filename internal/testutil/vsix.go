package testutil

import (
	"archive/zip"
	"bytes"
	"fmt"
)

func VSIX(id, version, platform string, prerelease bool, extra map[string]string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	files := map[string]string{"extension/package.json": fmt.Sprintf(`{"name":%q,"publisher":"test","version":%q,"displayName":"Test extension","engines":{"vscode":"^1.50.0"}}`, id, version), "extension.vsixmanifest": fmt.Sprintf(`<PackageManifest><Metadata><Identity Id=%q Publisher="test" Version=%q TargetPlatform=%q/><Properties><Property Id="Microsoft.VisualStudio.Code.PreRelease" Value="%t"/></Properties></Metadata></PackageManifest>`, id, version, platform, prerelease)}
	for k, v := range extra {
		files[k] = v
	}
	for _, n := range []string{"extension/package.json", "extension.vsixmanifest"} {
		f, _ := w.Create(n)
		f.Write([]byte(files[n]))
		delete(files, n)
	}
	for n, v := range files {
		f, _ := w.Create(n)
		f.Write([]byte(v))
	}
	w.Close()
	return b.Bytes()
}
