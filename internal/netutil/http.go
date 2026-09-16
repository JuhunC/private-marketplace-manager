package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func Client(caFile string, marketplace bool) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		b, e := os.ReadFile(caFile)
		if e != nil {
			return nil, e
		}
		pool, e := x509.SystemCertPool()
		if e != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("CA file contains no certificates")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Minute}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !marketplace {
			return fmt.Errorf("API redirects are not allowed; configure its final URL")
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return AssetURL(req.URL.String())
	}
	return client, nil
}
func AssetURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return fmt.Errorf("invalid marketplace asset URL")
	}
	h := strings.ToLower(u.Hostname())
	if h != "marketplace.visualstudio.com" && !strings.HasSuffix(h, ".gallerycdn.vsassets.io") && !strings.HasSuffix(h, ".gallery.vsassets.io") {
		return fmt.Errorf("unrecognized marketplace asset host: %s", h)
	}
	return nil
}
