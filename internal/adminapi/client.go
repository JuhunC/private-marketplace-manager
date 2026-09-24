// Package adminapi provides the authenticated manager client used by the MCP server.
package adminapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/JuhunC/private-marketplace-manager/internal/netutil"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
)

type Config struct {
	ServerURL         string `json:"serverUrl"`
	TokenFile         string `json:"tokenFile"`
	CAFile            string `json:"caFile,omitempty"`
	AllowInsecureHTTP bool   `json:"allowInsecureHttp,omitempty"`
	MaxUploadBytes    int64  `json:"maxUploadBytes,omitempty"`
}

func LoadConfig(filename string) (Config, error) {
	c := Config{MaxUploadBytes: 2 << 30}
	if filename == "" {
		return c, fmt.Errorf("MCP configuration path is required")
	}
	f, e := os.Open(filename)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return c, fmt.Errorf("configuration contains trailing data")
	}
	base := filepath.Dir(filename)
	for _, p := range []*string{&c.TokenFile, &c.CAFile} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	if c.MaxUploadBytes <= 0 {
		return c, fmt.Errorf("maxUploadBytes must be positive")
	}
	return c, nil
}

type Client struct {
	Config Config
	HTTP   *http.Client
	token  string
}

func New(c Config) (*Client, error) {
	u, e := url.Parse(c.ServerURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("serverUrl must be an HTTPS origin without a path")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && c.AllowInsecureHTTP && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("HTTPS is required (HTTP allowed only for explicit loopback development)")
	}
	c.ServerURL = strings.TrimRight(c.ServerURL, "/")
	info, e := os.Stat(c.TokenFile)
	if e != nil {
		return nil, fmt.Errorf("read token file: %w", e)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("token file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("token file is accessible to other users; use chmod 600")
	}
	b, e := os.ReadFile(c.TokenFile)
	if e != nil {
		return nil, e
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 {
		return nil, fmt.Errorf("API token must be at least 32 characters")
	}
	h, e := netutil.Client(c.CAFile, false)
	if e != nil {
		return nil, e
	}
	return &Client{Config: c, HTTP: h, token: token}, nil
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("manager HTTP %d (%s): %s [request %s]", e.Status, e.Code, e.Message, e.RequestID)
	}
	return fmt.Sprintf("manager HTTP %d (%s): %s", e.Status, e.Code, e.Message)
}

func (c *Client) JSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.Config.ServerURL+path, reader)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<20))
		return e
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func decodeError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	v := struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"requestId"`
	}{}
	_ = json.Unmarshal(b, &v)
	if v.Code == "" {
		v.Code = http.StatusText(resp.StatusCode)
	}
	if v.Error == "" {
		v.Error = strings.TrimSpace(string(b))
	}
	if v.Error == "" {
		v.Error = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Code: v.Code, Message: v.Error, RequestID: v.RequestID}
}

type UploadResult struct {
	Package   vsix.Package `json:"package"`
	Duplicate bool         `json:"duplicate"`
}

func (c *Client) UploadVSIX(ctx context.Context, filename string) (UploadResult, error) {
	var result UploadResult
	abs, e := filepath.Abs(filename)
	if e != nil {
		return result, e
	}
	info, e := os.Lstat(abs)
	if e != nil {
		return result, e
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("VSIX path must be a regular file")
	}
	if info.Size() > c.Config.MaxUploadBytes {
		return result, fmt.Errorf("VSIX exceeds configured upload limit")
	}
	p, e := vsix.Inspect(abs)
	if e != nil {
		return result, fmt.Errorf("validate VSIX: %w", e)
	}
	f, e := os.Open(abs)
	if e != nil {
		return result, e
	}
	defer f.Close()
	q := url.Values{"id": {p.ID}, "version": {p.Version}, "platform": {p.Platform}}
	req, e := http.NewRequestWithContext(ctx, "POST", c.Config.ServerURL+"/api/v1/extensions?"+q.Encode(), f)
	if e != nil {
		return result, e
	}
	req.ContentLength = p.Size
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Content-SHA256", p.SHA256)
	req.Header.Set("X-Package-Source", "mcp-local-file")
	idem := sha256.Sum256([]byte("mcp@" + p.Key() + "@" + p.SHA256))
	req.Header.Set("Idempotency-Key", hex.EncodeToString(idem[:]))
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return result, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return result, decodeError(resp)
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); e != nil {
		return result, e
	}
	if result.Package.Status != "stored" || result.Package.Key() != p.Key() || result.Package.SHA256 != p.SHA256 {
		return result, fmt.Errorf("manager did not confirm matching durable storage")
	}
	return result, nil
}
