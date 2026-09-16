package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/netutil"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
	"golang.org/x/mod/semver"
)

const GalleryURL = "https://marketplace.visualstudio.com/_apis/public/gallery/extensionquery"

// IncludeVersions|IncludeFiles|IncludeVersionProperties|IncludeAssetUri. Never use latest-only flags.
const historicalFlags = 1 | 2 | 16 | 128

type Artifact struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	Platform   string `json:"platform"`
	Prerelease bool   `json:"prerelease"`
	URL        string `json:"url"`
	Published  string `json:"published"`
}

func (a Artifact) Key() string { return a.ID + "@" + a.Version + "@" + a.Platform }

type Gallery struct {
	Client   *http.Client
	Endpoint string
	Attempts int
}

func (g Gallery) Discover(ctx context.Context, id string) ([]Artifact, error) {
	endpoint := g.Endpoint
	if endpoint == "" {
		endpoint = GalleryURL
	}
	body, _ := json.Marshal(map[string]any{"filters": []any{map[string]any{"criteria": []any{map[string]any{"filterType": 7, "value": id}}, "pageNumber": 1, "pageSize": 1}}, "assetTypes": []string{"Microsoft.VisualStudio.Services.VSIXPackage"}, "flags": historicalFlags})
	var raw []byte
	e := retry(ctx, g.Attempts, func() (time.Duration, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json;api-version=7.2-preview.1")
		req.Header.Set("User-Agent", "private-marketplace-manager/1")
		resp, e := g.Client.Do(req)
		if e != nil {
			return 0, retryable{e}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return responseError(resp)
		}
		raw, e = io.ReadAll(io.LimitReader(resp.Body, (64<<20)+1))
		if e != nil {
			return 0, retryable{e}
		}
		if len(raw) > 64<<20 {
			return 0, fmt.Errorf("metadata response exceeds 64 MiB; cannot assert full enumeration")
		}
		return 0, nil
	})
	if e != nil {
		return nil, e
	}
	var data struct {
		Results []struct {
			Extensions []struct {
				Name      string `json:"extensionName"`
				Publisher struct {
					Name string `json:"publisherName"`
				} `json:"publisher"`
				Versions []struct {
					Version  string `json:"version"`
					Platform string `json:"targetPlatform"`
					Updated  string `json:"lastUpdated"`
					Files    []struct {
						Type string `json:"assetType"`
						URL  string `json:"source"`
					} `json:"files"`
					Properties []struct {
						Key   string `json:"key"`
						Value string `json:"value"`
					} `json:"properties"`
				} `json:"versions"`
			} `json:"extensions"`
		} `json:"results"`
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		return nil, fmt.Errorf("decode gallery: %w", e)
	}
	result := []Artifact{}
	seen := map[string]Artifact{}
	found := false
	for _, r := range data.Results {
		for _, ext := range r.Extensions {
			if !strings.EqualFold(ext.Publisher.Name+"."+ext.Name, id) {
				continue
			}
			found = true
			for _, v := range ext.Versions {
				a := Artifact{ID: strings.ToLower(id), Version: v.Version, Platform: strings.ToLower(v.Platform), Published: v.Updated}
				if a.Platform == "" {
					a.Platform = "universal"
				}
				if !vsix.ValidIdentity(a.ID, a.Version, a.Platform) {
					return nil, fmt.Errorf("gallery returned invalid identity")
				}
				for _, p := range v.Properties {
					if p.Key == "Microsoft.VisualStudio.Code.PreRelease" {
						a.Prerelease = strings.EqualFold(p.Value, "true")
					}
				}
				for _, f := range v.Files {
					if f.Type == "Microsoft.VisualStudio.Services.VSIXPackage" {
						a.URL = f.URL
						break
					}
				}
				if a.URL == "" {
					return nil, fmt.Errorf("no VSIX asset for %s; enumeration incomplete", a.Key())
				}
				if e = netutil.AssetURL(a.URL); e != nil {
					return nil, e
				}
				if old, ok := seen[a.Key()]; ok {
					if old.URL != a.URL || old.Prerelease != a.Prerelease {
						return nil, fmt.Errorf("conflicting gallery identity %s", a.Key())
					}
					continue
				}
				seen[a.Key()] = a
				result = append(result, a)
			}
		}
	}
	if !found || len(result) == 0 {
		return nil, fmt.Errorf("extension %s has no discoverable packages (unknown, unpublished, or unavailable)", id)
	}
	sort.Slice(result, func(i, j int) bool {
		cmp := semver.Compare("v"+result[i].Version, "v"+result[j].Version)
		if cmp != 0 {
			return cmp < 0
		}
		return result[i].Platform < result[j].Platform
	})
	return result, nil
}
