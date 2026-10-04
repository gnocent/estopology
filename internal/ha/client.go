package ha

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// Bounds on what a standby reads from its primary in memory. Variables for
// the tests.
var (
	maxJSONBytes   int64 = 16 << 20  // status, cluster and record lists
	maxMetaBytes   int64 = 256 << 20 // configuration snapshot (a year of audit trail)
	maxRecordBytes int64 = 512 << 20 // one compressed record (250k shards: about 10 MB)
)

// stallTimeout is how long a partition download may stay without receiving
// a byte before it is abandoned.
const stallTimeout = 30 * time.Second

// client calls the replication API of a peer.
type client struct {
	base  string
	token string
	http  *http.Client
	stall time.Duration
	err   error // the client could not be configured: every call fails with it
}

// loadCA reads the CA of the peer's certificate. A file that cannot be read
// or holds no certificate is an error: falling back to the system CAs would
// silently trust more than what was configured.
func loadCA(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("replication CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("replication CA %s: no certificate found", path)
	}
	return pool, nil
}

func (m *Manager) client(base string) *client {
	c := &client{base: base, token: m.token, stall: m.stall}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if m.o.CAFile != "" {
		if tc.RootCAs, c.err = loadCA(m.o.CAFile); c.err != nil {
			return c
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tc
	tr.ResponseHeaderTimeout = 5 * time.Minute // partition snapshots take a while to start
	c.http = &http.Client{
		Transport: tr,
		// The token is for the configured peer only: a redirect is not followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c
}

func (c *client) get(ctx context.Context, path string, hdr map[string]string) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, b)
	}
	return resp, nil
}

// readAll reads a body of at most max bytes.
func readAll(r io.Reader, max int64, what string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err == nil && int64(len(b)) > max {
		err = fmt.Errorf("%s: more than %d bytes", what, max)
	}
	return b, err
}

func (c *client) json(ctx context.Context, path string, v any) error {
	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	resp, err := c.get(sctx, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := readAll(resp.Body, maxJSONBytes, "GET "+path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (c *client) status(ctx context.Context) (remoteStatus, error) {
	var s remoteStatus
	err := c.json(ctx, "/api/v1/replication/status", &s)
	return s, err
}

// meta returns the snapshot, or changed=false when etag still matches.
func (c *client) meta(ctx context.Context, etag string) (data []byte, newTag string, changed bool, err error) {
	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-None-Match"] = etag
	}
	resp, err := c.get(sctx, "/api/v1/replication/meta", hdr)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, false, nil
	}
	data, err = readAll(resp.Body, maxMetaBytes, "configuration snapshot")
	return data, resp.Header.Get("ETag"), true, err
}

func (c *client) clusters(ctx context.Context) ([]remoteCluster, error) {
	var out []remoteCluster
	err := c.json(ctx, "/api/v1/replication/clusters", &out)
	return out, err
}

func (c *client) records(ctx context.Context, cluster string, after int64, limit int) ([]remoteRecord, error) {
	var out []remoteRecord
	err := c.json(ctx, "/api/v1/replication/records/"+url.PathEscape(cluster)+"?after="+strconv.FormatInt(after, 10)+
		"&limit="+strconv.Itoa(limit), &out)
	return out, err
}

func (c *client) record(ctx context.Context, cluster, name string) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	resp, err := c.get(sctx, "/api/v1/replication/records/"+url.PathEscape(cluster)+"/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readAll(resp.Body, maxRecordBytes, "record "+cluster+"/"+name)
}

// stallReader cancels a download that receives nothing for too long: a
// partition takes minutes to copy, so the deadline is on each read, not on
// the whole body.
type stallReader struct {
	r     io.Reader
	timer *time.Timer // cancels the request when it fires
	d     time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	s.timer.Reset(s.d)
	n, err := s.r.Read(p)
	s.timer.Stop()
	return n, err
}

// partition downloads a partition snapshot into dst.
func (c *client) partition(ctx context.Context, cluster, week, dst string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resp, err := c.get(ctx, "/api/v1/replication/partitions/"+url.PathEscape(cluster)+"/"+url.PathEscape(week), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength < 0 {
		return fmt.Errorf("partition %s/%s: size not announced", cluster, week)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	var stalled atomic.Bool
	timer := time.AfterFunc(c.stall, func() { stalled.Store(true); cancel() })
	timer.Stop()
	defer timer.Stop()
	// Never more than what was announced, whatever the peer sends.
	n, err := io.Copy(f, io.LimitReader(&stallReader{r: resp.Body, timer: timer, d: c.stall}, resp.ContentLength))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case stalled.Load():
		err = fmt.Errorf("partition %s/%s: nothing received for %s after %d of %d bytes", cluster, week, c.stall, n, resp.ContentLength)
	case err == nil && n != resp.ContentLength:
		err = fmt.Errorf("partition %s/%s: %d bytes received, %d announced", cluster, week, n, resp.ContentLength)
	}
	return err
}
