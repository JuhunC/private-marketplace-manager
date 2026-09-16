// Package vsix inspects archives without extracting or executing their contents.
package vsix

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

var component = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?(?:\+[a-zA-Z0-9.-]+)?$`)
var platformRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Package uniquely identifies original VSIX bytes. Channel is metadata, not a filename suffix.
type Package struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	Platform      string   `json:"platform"`
	Prerelease    bool     `json:"prerelease"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	Engine        string   `json:"engine"`
	Dependencies  []string `json:"dependencies,omitempty"`
	ExtensionPack []string `json:"extensionPack,omitempty"`
	SHA256        string   `json:"sha256"`
	Size          int64    `json:"size"`
	Filename      string   `json:"filename,omitempty"`
	Managed       bool     `json:"managed"`
	StoredAt      string   `json:"storedAt,omitempty"`
	Source        string   `json:"source,omitempty"`
	Status        string   `json:"status"`
}

func ValidID(id string) bool {
	p := strings.Split(id, ".")
	return len(p) == 2 && component.MatchString(p[0]) && component.MatchString(p[1])
}
func ValidIdentity(id, version, platform string) bool {
	return ValidID(id) && versionRE.MatchString(version) && platformRE.MatchString(platform)
}
func (p Package) Key() string { return strings.ToLower(p.ID) + "@" + p.Version + "@" + p.Platform }
func (p Package) CanonicalName() string {
	return strings.ToLower(p.ID) + "-" + p.Version + "-" + p.Platform + ".vsix"
}
func HashFile(filename string) (string, int64, error) {
	f, e := os.Open(filename)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func Inspect(filename string) (Package, error) {
	var p Package
	z, e := zip.OpenReader(filename)
	if e != nil {
		return p, fmt.Errorf("invalid ZIP: %w", e)
	}
	defer z.Close()
	if len(z.File) > 100000 {
		return p, fmt.Errorf("too many archive entries")
	}
	seen := map[string]bool{}
	var total uint64
	var manifest, pkg []byte
	for _, f := range z.File {
		n := f.Name
		clean := strings.TrimSuffix(n, "/")
		if n == "" || strings.ContainsAny(n, "\\\x00:") || strings.HasPrefix(n, "/") || path.Clean(clean) != clean || strings.HasPrefix(clean, "../") || clean == ".." || f.Mode()&os.ModeSymlink != 0 {
			return p, fmt.Errorf("unsafe archive entry")
		}
		lower := strings.ToLower(n)
		if seen[lower] {
			return p, fmt.Errorf("duplicate archive entry: %s", n)
		}
		seen[lower] = true
		if f.UncompressedSize64 > 4<<30 || total > 4<<30-f.UncompressedSize64 {
			return p, fmt.Errorf("uncompressed archive exceeds 4 GiB")
		}
		total += f.UncompressedSize64
		if f.UncompressedSize64 > 10<<20 && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > 1000) {
			return p, fmt.Errorf("excessive compression ratio")
		}
		// Stream every entry to verify ZIP checksums; never extract files to disk.
		if !f.FileInfo().IsDir() && lower != "extension/package.json" && lower != "extension.vsixmanifest" {
			entry, err := f.Open()
			if err != nil {
				return p, err
			}
			n, err := io.Copy(io.Discard, io.LimitReader(entry, int64(f.UncompressedSize64)+1))
			entry.Close()
			if err != nil || n != int64(f.UncompressedSize64) {
				return p, fmt.Errorf("corrupt archive entry: %s", f.Name)
			}
		}
		if lower == "extension/package.json" || lower == "extension.vsixmanifest" {
			if f.UncompressedSize64 > 2<<20 {
				return p, fmt.Errorf("manifest exceeds 2 MiB")
			}
			r, err := f.Open()
			if err != nil {
				return p, err
			}
			b, err := io.ReadAll(io.LimitReader(r, (2<<20)+1))
			r.Close()
			if err != nil {
				return p, err
			}
			if len(b) > 2<<20 {
				return p, fmt.Errorf("oversized manifest")
			}
			if lower == "extension/package.json" {
				pkg = b
			} else {
				manifest = b
			}
		}
	}
	if len(pkg) == 0 || len(manifest) == 0 {
		return p, fmt.Errorf("missing extension/package.json or extension.vsixmanifest")
	}
	var j struct {
		Name, Publisher, Version, DisplayName, Description string
		Engines                                            map[string]string
		ExtensionDependencies, ExtensionPack               []string
	}
	if e = json.Unmarshal(pkg, &j); e != nil {
		return p, fmt.Errorf("invalid package.json: %w", e)
	}
	var x struct {
		Metadata struct {
			Identity struct {
				ID        string `xml:"Id,attr"`
				Publisher string `xml:"Publisher,attr"`
				Version   string `xml:"Version,attr"`
				Platform  string `xml:"TargetPlatform,attr"`
			} `xml:"Identity"`
			Properties []struct {
				ID    string `xml:"Id,attr"`
				Value string `xml:"Value,attr"`
			} `xml:"Properties>Property"`
		} `xml:"Metadata"`
	}
	if strings.Contains(strings.ToUpper(string(manifest)), "<!DOCTYPE") || strings.Contains(strings.ToUpper(string(manifest)), "<!ENTITY") {
		return p, fmt.Errorf("XML declarations are not allowed")
	}
	if e = xml.Unmarshal(manifest, &x); e != nil {
		return p, fmt.Errorf("invalid VSIX manifest: %w", e)
	}
	i := x.Metadata.Identity
	if !strings.EqualFold(j.Name, i.ID) || !strings.EqualFold(j.Publisher, i.Publisher) || j.Version != i.Version {
		return p, fmt.Errorf("package and VSIX identities disagree")
	}
	p.ID = strings.ToLower(j.Publisher + "." + j.Name)
	p.Version = j.Version
	p.Platform = strings.ToLower(i.Platform)
	if p.Platform == "" {
		p.Platform = "universal"
	}
	if !ValidIdentity(p.ID, p.Version, p.Platform) {
		return p, fmt.Errorf("invalid package identity")
	}
	for _, v := range x.Metadata.Properties {
		if v.ID == "Microsoft.VisualStudio.Code.PreRelease" {
			p.Prerelease = strings.EqualFold(v.Value, "true")
		}
	}
	p.DisplayName = j.DisplayName
	p.Description = j.Description
	p.Engine = j.Engines["vscode"]
	p.Dependencies = j.ExtensionDependencies
	p.ExtensionPack = j.ExtensionPack
	p.SHA256, p.Size, e = HashFile(filename)
	p.Status = "stored"
	return p, e
}
