package syncer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/buildinfo"
	"github.com/JuhunC/private-marketplace-manager/internal/netutil"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
	"github.com/gofrs/flock"
)

type Config struct {
	ServerURL         string `json:"serverUrl"`
	TokenFile         string `json:"tokenFile"`
	WorkDir           string `json:"workDir"`
	CAFile            string `json:"caFile"`
	Concurrency       int    `json:"concurrency"`
	MaxDownloadBytes  int64  `json:"maxDownloadBytes"`
	Retries           int    `json:"retries"`
	AllowInsecureHTTP bool   `json:"allowInsecureHttp"`
}

func LoadConfig(filename string) (Config, error) {
	c := Config{WorkDir: "work", Concurrency: 2, MaxDownloadBytes: 2 << 30, Retries: 4}
	base := "."
	if filename != "" {
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
		base = filepath.Dir(filename)
	}
	for _, p := range []*string{&c.WorkDir, &c.TokenFile, &c.CAFile} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	if c.Concurrency < 1 || c.Concurrency > 8 || c.MaxDownloadBytes < 1 || c.Retries < 1 || c.Retries > 10 {
		return c, fmt.Errorf("concurrency must be 1..8, retries 1..10, and download limit positive")
	}
	return c, nil
}
func ReadList(filename string) ([]string, string, error) {
	f, e := os.Open(filename)
	if e != nil {
		return nil, "", e
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 64<<10)
	seen := map[string]bool{}
	out := []string{}
	line := 0
	for scan.Scan() {
		line++
		id := strings.TrimSpace(strings.TrimPrefix(scan.Text(), "\ufeff"))
		if id == "" || strings.HasPrefix(id, "#") {
			continue
		}
		if !vsix.ValidID(id) {
			return nil, "", fmt.Errorf("invalid extension identifier at line %d", line)
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if e = scan.Err(); e != nil {
		return nil, "", e
	}
	sort.Strings(out)
	h := sha256.Sum256([]byte(strings.Join(out, "\n")))
	return out, hex.EncodeToString(h[:]), nil
}

type Failure struct {
	Package string `json:"package"`
	Error   string `json:"error"`
}
type Report struct {
	ID         string     `json:"id"`
	Started    string     `json:"started"`
	Finished   string     `json:"finished,omitempty"`
	Version    string     `json:"version"`
	ListHash   string     `json:"listHash"`
	Extensions []string   `json:"extensions"`
	Discovered int        `json:"discovered"`
	Stored     int        `json:"stored"`
	Skipped    int        `json:"skipped"`
	Failed     int        `json:"failed"`
	Status     string     `json:"status"`
	Errors     []Failure  `json:"errors"`
	Artifacts  []Artifact `json:"artifacts,omitempty"`
}
type Runner struct {
	Config  Config
	Gallery Gallery
	API     *http.Client
	Log     io.Writer
	token   string
}

func New(c Config) (*Runner, error) {
	source, e := netutil.Client(c.CAFile, true)
	if e != nil {
		return nil, e
	}
	api, e := netutil.Client(c.CAFile, false)
	if e != nil {
		return nil, e
	}
	return &Runner{Config: c, Gallery: Gallery{Client: source, Attempts: c.Retries}, API: api, Log: os.Stderr}, nil
}
func (r *Runner) authenticate() error {
	u, e := url.Parse(r.Config.ServerURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("serverUrl must be an HTTPS origin without a path")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && r.Config.AllowInsecureHTTP && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return fmt.Errorf("HTTPS is required (HTTP allowed only for explicit loopback development)")
	}
	r.Config.ServerURL = strings.TrimRight(r.Config.ServerURL, "/")
	info, e := os.Stat(r.Config.TokenFile)
	if e != nil {
		return fmt.Errorf("read token file: %w", e)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("token file is accessible to other users; use chmod 600")
	}
	b, e := os.ReadFile(r.Config.TokenFile)
	if e != nil {
		return e
	}
	r.token = strings.TrimSpace(string(b))
	if len(r.token) < 32 {
		return fmt.Errorf("API token must be at least 32 characters")
	}
	return nil
}
func (r *Runner) apiJSON(ctx context.Context, method, path string, body any, out any) error {
	data, e := json.Marshal(body)
	if e != nil {
		return e
	}
	return retry(ctx, r.Config.Retries, func() (time.Duration, error) {
		req, e := http.NewRequestWithContext(ctx, method, r.Config.ServerURL+path, bytes.NewReader(data))
		if e != nil {
			return 0, e
		}
		req.Header.Set("Authorization", "Bearer "+r.token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := r.API.Do(req)
		if e != nil {
			return 0, retryable{e}
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return responseError(resp)
		}
		if out != nil {
			e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
		}
		return 0, e
	})
}
func (r *Runner) Run(ctx context.Context, listPath string, discover bool) (Report, error) {
	var report Report
	ids, hash, e := ReadList(listPath)
	if e != nil {
		return report, e
	}
	if e = os.MkdirAll(r.Config.WorkDir, 0700); e != nil {
		return report, e
	}
	lock := flock.New(filepath.Join(r.Config.WorkDir, "sync.lock"))
	ok, e := lock.TryLock()
	if e != nil || !ok {
		return report, fmt.Errorf("another sync is using this work directory")
	}
	defer lock.Unlock()
	b := make([]byte, 16)
	if _, e = rand.Read(b); e != nil {
		return report, e
	}
	report = Report{ID: hex.EncodeToString(b), Started: time.Now().UTC().Format(time.RFC3339), Version: buildinfo.Version, ListHash: hash, Extensions: ids, Status: "running", Errors: []Failure{}}
	if !discover {
		if e = r.authenticate(); e != nil {
			return report, e
		}
		var status struct {
			APIVersion int `json:"apiVersion"`
		}
		if e = r.apiJSON(ctx, "GET", "/api/v1/status", nil, &status); e != nil {
			return report, e
		}
		if status.APIVersion != 1 {
			return report, fmt.Errorf("unsupported manager API version %d", status.APIVersion)
		}
	}
	// Every pass starts from the current list and authoritative remote inventory; local state is optional.
	for _, id := range ids {
		if e = ctx.Err(); e != nil {
			report.Failed++
			report.Errors = append(report.Errors, Failure{id, e.Error()})
			break
		}
		fmt.Fprintf(r.Log, "Discovering %s (all versions and platforms)\n", id)
		artifacts, err := r.Gallery.Discover(ctx, id)
		if err != nil {
			report.Failed++
			report.Errors = append(report.Errors, Failure{id, err.Error()})
			continue
		}
		report.Discovered += len(artifacts)
		if discover {
			report.Artifacts = append(report.Artifacts, artifacts...)
			continue
		}
		for start := 0; start < len(artifacts); start += 500 {
			end := min(start+500, len(artifacts))
			batch := artifacts[start:end]
			keys := []string{}
			for _, a := range batch {
				keys = append(keys, a.Key())
			}
			var check struct {
				Packages map[string]vsix.Package `json:"packages"`
			}
			if err = r.apiJSON(ctx, "POST", "/api/v1/extensions/check", map[string]any{"keys": keys}, &check); err != nil {
				report.Failed += len(batch)
				report.Errors = append(report.Errors, Failure{id, err.Error()})
				continue
			}
			jobs := make(chan Artifact)
			var wg sync.WaitGroup
			var mu sync.Mutex
			for i := 0; i < r.Config.Concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for a := range jobs {
						if p, ok := check.Packages[a.Key()]; ok && p.Status == "stored" && p.Prerelease == a.Prerelease {
							mu.Lock()
							report.Skipped++
							mu.Unlock()
							continue
						}
						err := r.transfer(ctx, a)
						mu.Lock()
						if err != nil {
							report.Failed++
							if len(report.Errors) < 200 {
								report.Errors = append(report.Errors, Failure{a.Key(), err.Error()})
							}
						} else {
							report.Stored++
							fmt.Fprintf(r.Log, "Stored %s\n", a.Key())
						}
						mu.Unlock()
					}
				}()
			}
			for _, a := range batch {
				jobs <- a
			}
			close(jobs)
			wg.Wait()
		}
	}
	report.Finished = time.Now().UTC().Format(time.RFC3339)
	report.Status = "complete"
	if len(ids) == 0 {
		report.Status = "empty-list"
	}
	if report.Failed > 0 {
		report.Status = "partial"
	}
	if discover {
		if report.Failed == 0 && len(ids) > 0 {
			report.Status = "discovered"
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(r.Config.WorkDir, "report-"+report.ID+".json"), data, 0600)
	}
	if err != nil {
		return report, err
	}
	if !discover {
		if err = r.apiJSON(ctx, "POST", "/api/v1/sync-runs", report, nil); err != nil {
			return report, fmt.Errorf("files processed but run report could not be saved: %w", err)
		}
	}
	if report.Failed > 0 {
		return report, fmt.Errorf("%d items failed; see report-%s.json", report.Failed, report.ID)
	}
	return report, nil
}
func (r *Runner) transfer(ctx context.Context, a Artifact) error {
	f, e := os.CreateTemp(r.Config.WorkDir, "download-*.part")
	if e != nil {
		return e
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	e = retry(ctx, r.Config.Retries, func() (time.Duration, error) {
		if err := netutil.AssetURL(a.URL); err != nil {
			return 0, err
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
		req.Header.Set("User-Agent", "private-marketplace-manager/1")
		resp, e := r.Gallery.Client.Do(req)
		if e != nil {
			return 0, retryable{e}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return responseError(resp)
		}
		if resp.ContentLength > r.Config.MaxDownloadBytes {
			return 0, fmt.Errorf("download exceeds configured limit")
		}
		dst, e := os.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0600)
		if e != nil {
			return 0, e
		}
		n, e := io.Copy(dst, io.LimitReader(resp.Body, r.Config.MaxDownloadBytes+1))
		closeErr := dst.Close()
		if n > r.Config.MaxDownloadBytes {
			return 0, fmt.Errorf("download exceeds configured limit")
		}
		if e != nil {
			return 0, retryable{e}
		}
		return 0, closeErr
	})
	if e != nil {
		return e
	}
	p, e := vsix.Inspect(name)
	if e != nil {
		return e
	}
	if p.Key() != a.Key() || p.Prerelease != a.Prerelease {
		return fmt.Errorf("downloaded VSIX identity/channel does not match source record")
	}
	keyHash := sha256.Sum256([]byte(p.Key() + "@" + p.SHA256))
	idem := hex.EncodeToString(keyHash[:])
	return retry(ctx, r.Config.Retries, func() (time.Duration, error) {
		file, e := os.Open(name)
		if e != nil {
			return 0, e
		}
		defer file.Close()
		q := url.Values{"id": {p.ID}, "version": {p.Version}, "platform": {p.Platform}}
		req, e := http.NewRequestWithContext(ctx, "POST", r.Config.ServerURL+"/api/v1/extensions?"+q.Encode(), file)
		if e != nil {
			return 0, e
		}
		req.ContentLength = p.Size
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Authorization", "Bearer "+r.token)
		req.Header.Set("X-Content-SHA256", p.SHA256)
		req.Header.Set("Idempotency-Key", idem)
		req.Header.Set("X-Package-Source", a.URL)
		resp, e := r.API.Do(req)
		if e != nil {
			return 0, retryable{e}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			return responseError(resp)
		}
		var result struct {
			Package vsix.Package `json:"package"`
		}
		if e = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); e != nil {
			return 0, retryable{e}
		}
		if result.Package.Status != "stored" || result.Package.Key() != p.Key() || result.Package.SHA256 != p.SHA256 {
			return 0, fmt.Errorf("server did not confirm matching durable storage")
		}
		return 0, nil
	})
}

type retryable struct{ error }

func retry(ctx context.Context, attempts int, fn func() (time.Duration, error)) error {
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		if e := ctx.Err(); e != nil {
			return e
		}
		delay, e := fn()
		if e == nil {
			return nil
		}
		last = e
		var r retryable
		if !errors.As(e, &r) {
			return e
		}
		if i+1 == attempts {
			break
		}
		if delay <= 0 {
			b := make([]byte, 1)
			rand.Read(b)
			delay = time.Second*time.Duration(1<<min(i, 6)) + time.Millisecond*time.Duration(b[0])*4
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return last
}
func responseError(resp *http.Response) (time.Duration, error) {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var msg struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &msg)
	message := http.StatusText(resp.StatusCode)
	if msg.Error != "" {
		message = msg.Error
	}
	e := fmt.Errorf("HTTP %d: %s", resp.StatusCode, message)
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		delay := time.Duration(0)
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			delay = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			delay = time.Until(date)
		}
		return delay, retryable{e}
	}
	return 0, e
}
