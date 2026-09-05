// Package fetch implements the protocol matrix for retrieving engine
// artifacts: every download in dbpod goes through this layer, which owns
// the network capability, proxy injection, credential handling and audit
// logging. Plugins (wasm/config) never touch the network — they only
// produce refs, which this package resolves.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Result is the audit record of one successful fetch.
type Result struct {
	Scheme   string
	URL      string
	Status   int
	Bytes    int64
	Duration time.Duration
	Proxy    string
}

// Fetcher resolves one or more URL schemes into local files.
type Fetcher interface {
	Schemes() []string
	Fetch(ctx context.Context, u *url.URL, dest string) (Result, error)
}

// Proxy is the transport-level proxy configuration injected into every
// network fetcher.
type Proxy struct {
	HTTP    string // http/https/webdav (http.ProxyURL)
	Socks5  string // ssh/sftp/ftp/smb family (golang.org/x/net/proxy)
	NoProxy []string
}

var (
	fetchers     = map[string]Fetcher{}
	defaultF     Fetcher
	proxyConf    Proxy
	auditOut     io.Writer = os.Stderr
	client       *http.Client
	clients      map[string]*http.Client
	progressHook func(n int64)
)

// SetProgressHook registers a cumulative-bytes progress callback for
// downloads (nil clears it).
func SetProgressHook(fn func(n int64)) { progressHook = fn }

func notifyProgress(n int64) {
	if progressHook != nil {
		progressHook(n)
	}
}

func init() {
	rebuildClients()
	hf := &HTTPFetcher{Client: client}
	Register(hf)
	Register(CopyFetcher{})
	defaultF = hf
}

// rebuildClients re-creates HTTP clients after proxy changes.
func rebuildClients() {
	transport := &http.Transport{}
	if proxyConf.HTTP != "" {
		if pu, err := url.Parse(proxyConf.HTTP); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	client = &http.Client{Transport: transport}
}

// Register adds a fetcher for its schemes.
func Register(f Fetcher) {
	for _, s := range f.Schemes() {
		fetchers[s] = f
	}
}

// SetProxy configures the proxy used by all network fetchers and rebuilds
// the shared HTTP client.
func SetProxy(p Proxy) {
	proxyConf = p
	rebuildClients()
}

// HTTPClient returns the shared HTTP client (proxy-aware) for callers that
// need raw requests beyond Fetch.
func HTTPClient() *http.Client {
	if client == nil {
		rebuildClients()
	}
	return client
}

// FetchBytes GETs a URL and returns the body (for small payloads such as
// repository indexes). hdr may be nil.
func FetchBytes(ctx context.Context, url string, hdr http.Header) ([]byte, Result, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, Result{}, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := HTTPClient().Do(req)
	if err != nil {
		return nil, Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, Result{}, err
	}
	res := Result{Scheme: "https", URL: url, Status: resp.StatusCode,
		Bytes: int64(len(body)), Duration: time.Since(start)}
	logAudit(res, nil)
	if resp.StatusCode != http.StatusOK {
		return nil, res, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return body, res, nil
}

// SetAuditWriter redirects audit records (default: stderr).
func SetAuditWriter(w io.Writer) { auditOut = w }

// NewClient builds an HTTP client with the proxy applied.
func NewClient(p Proxy) *http.Client {
	transport := &http.Transport{}
	if p.HTTP != "" {
		if pu, err := url.Parse(p.HTTP); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{Transport: transport}
}

// RegisterClient binds a dedicated HTTP client to a provider name.
func RegisterClient(name string, c *http.Client) { clients[name] = c }

// ClientFor returns the HTTP client registered for a named provider
// (falling back to the default).
func ClientFor(name string) *http.Client {
	if c, ok := clients[name]; ok {
		return c
	}
	if client == nil {
		rebuildClients()
	}
	return client
}

// Fetch routes ref by scheme to the matching fetcher (http as fallback) and
// writes the content to dest, emitting an audit record either way.
func Fetch(ctx context.Context, rawRef, dest string) (Result, error) {
	u, err := url.Parse(rawRef)
	if err != nil {
		return Result{}, fmt.Errorf("bad ref %q: %w", rawRef, err)
	}
	f, ok := fetchers[u.Scheme]
	if !ok {
		f = defaultF
	}
	if f == nil {
		return Result{}, fmt.Errorf("no fetcher for scheme %q", u.Scheme)
	}

	res, err := f.Fetch(ctx, u, dest)
	res.Scheme = u.Scheme
	res.URL = rawRef
	res.Proxy = proxyConf.HTTP
	logAudit(res, err)
	return res, err
}

func logAudit(res Result, err error) {
	status := fmt.Sprint(res.Status)
	if err != nil {
		status = "ERROR: " + err.Error()
	}
	fmt.Fprintf(auditOut, "[fetch] scheme=%s url=%s status=%s bytes=%d duration=%s proxy=%q\n",
		res.Scheme, res.URL, status, res.Bytes, res.Duration.Truncate(time.Millisecond), res.Proxy)
}

// CopyFetcher implements the local "cp"/"file" schemes.
type CopyFetcher struct{}

func (CopyFetcher) Schemes() []string { return []string{"cp", "file"} }

func (CopyFetcher) Fetch(_ context.Context, u *url.URL, dest string) (Result, error) {
	start := time.Now()
	src := u.Path
	if u.Host != "" {
		src = u.Host + src
	}
	if src == "" {
		src = u.Opaque
	}
	in, err := os.Open(src)
	if err != nil {
		return Result{}, err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return Result{}, err
	}
	defer out.Close()
	n, err := io.Copy(out, in)
	if err != nil {
		return Result{}, err
	}
	return Result{Bytes: n, Duration: time.Since(start)}, nil
}

// HTTPFetcher implements http/https (and webdav GET) via a configurable
// transport — proxy injection lands here.
type HTTPFetcher struct {
	Client *http.Client // if nil, the shared default client is used
}

func (h *HTTPFetcher) Schemes() []string {
	return []string{"http", "https", "webdav"}
}

func (h *HTTPFetcher) Fetch(ctx context.Context, u *url.URL, dest string) (Result, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{}, err
	}
	// NOTE: do NOT spoof a browser User-Agent here — the mysql.com edge
	// rejects UA/TLS-fingerprint mismatches, while the default client UA
	// passes fine (verified against cdn.mysql.com and downloads.mysql.com).
	client := h.Client
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("unexpected status %d for %s", resp.StatusCode, u)
	}

	out, err := os.Create(dest)
	if err != nil {
		return Result{}, err
	}
	var got int64
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		got += int64(n)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return Result{}, werr
			}
			notifyProgress(got)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return Result{}, rerr
		}
	}
	if err := out.Close(); err != nil {
		return Result{}, err
	}
	return Result{Bytes: got, Duration: time.Since(start)}, nil
}

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"
