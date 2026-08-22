package dynglance

// LibreSpeed test logic in this file is adapted from librespeed/speedtest-cli
// https://github.com/librespeed/speedtest-cli (GNU LGPL v3.0).

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var speedtestWidgetTemplate = mustParseTemplate("speedtest.html", "widget-base.html")

const (
	speedtestDefaultListURL  = "https://librespeed.org/backend-servers/servers.php"
	speedtestDefaultDlPath   = "backend/garbage.php"
	speedtestDefaultUlPath   = "backend/empty.php"
	speedtestDefaultPingPath = "backend/empty.php"
	speedtestPingCount       = 10
	speedtestStagger         = 200 * time.Millisecond
	speedtestUploadChunk     = 1024 * 1024 // 1 MiB
)

// Shared test runners are keyed by config so that multiple speedtest widgets with
// identical settings share a single in-flight test and a single result, instead of
// each widget hammering the network with its own test.
var (
	speedtestRunnersMu sync.Mutex
	speedtestRunners   = map[string]*speedtestRunner{}
)

func speedtestSharedRunner(server string, duration time.Duration, concurrent int) *speedtestRunner {
	key := fmt.Sprintf("%s|%s|%d", server, duration, concurrent)

	speedtestRunnersMu.Lock()
	defer speedtestRunnersMu.Unlock()

	if r, ok := speedtestRunners[key]; ok {
		return r
	}

	r := &speedtestRunner{
		server:     server,
		duration:   duration,
		concurrent: concurrent,
		// A new test only starts when the previous one finished more than debounce
		// ago, so the wave of widget polls that fire together collapses into one test.
		debounce: 2*duration + 60*time.Second,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: concurrent + 2,
				MaxConnsPerHost:     concurrent + 2,
				Proxy:               http.ProxyFromEnvironment,
			},
		},
	}
	speedtestRunners[key] = r
	return r
}

type speedtestResult struct {
	DownloadMbps float64
	UploadMbps   float64
	PingMs       float64
	ServerName   string
}

type speedtestServer struct {
	Name    string `json:"name"`
	Server  string `json:"server"`
	DlURL   string `json:"dlURL"`
	UlURL   string `json:"ulURL"`
	PingURL string `json:"pingURL"`
}

// speedtestRunner owns the actual test and its result. It is shared by every widget
// with matching config.
type speedtestRunner struct {
	server     string
	duration   time.Duration
	concurrent int
	debounce   time.Duration
	client     *http.Client

	mu         sync.Mutex
	running    bool
	started    bool
	result     *speedtestResult
	resultTime time.Time
	lastErr    error
	selected   *speedtestServer
}

// trigger starts a test in a detached goroutine and returns immediately, so a long
// (~35s) run never stalls the page update batch. It is a no-op when a test is already
// running or one finished within the debounce window, ensuring one test for all widgets.
func (r *speedtestRunner) trigger() {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	if r.result != nil && time.Since(r.resultTime) < r.debounce {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.started = true
	r.mu.Unlock()

	go func() {
		timeout := 2*r.duration + 45*time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		res, err := r.runTest(ctx)

		r.mu.Lock()
		if err == nil {
			r.result = res
			r.resultTime = time.Now()
		}
		r.lastErr = err
		r.running = false
		r.mu.Unlock()
	}()
}

func (r *speedtestRunner) snapshot() (res *speedtestResult, started bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.started, r.lastErr
}

type speedtestWidget struct {
	widgetBase `yaml:",inline"`
	Frameless  bool          `yaml:"frameless"`
	Server     string        `yaml:"server"`
	Duration   durationField `yaml:"duration"`
	Concurrent int           `yaml:"concurrent"`

	runner *speedtestRunner
}

func (widget *speedtestWidget) initialize() error {
	widget.withTitle("Speed Test").withCacheDuration(6 * time.Hour)
	widget.widgetBase.WIP = true

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(6 * time.Hour)
		widget.UpdateInterval = &interval
	}

	if widget.Duration <= 0 {
		widget.Duration = durationField(15 * time.Second)
	}

	if widget.Concurrent <= 0 {
		widget.Concurrent = 3
	}

	widget.Server = strings.TrimRight(widget.Server, "/")

	widget.runner = speedtestSharedRunner(widget.Server, time.Duration(widget.Duration), widget.Concurrent)

	return nil
}

// update reflects the shared runner's state and asks it to (re)start a test. The runner
// itself deduplicates concurrent/recent requests, so many widgets cause a single test.
func (widget *speedtestWidget) update(context.Context) {
	widget.ContentAvailable = true
	widget.scheduleNextUpdate()

	widget.runner.trigger()

	_, _, err := widget.runner.snapshot()
	if err != nil {
		widget.withNotice(fmt.Errorf("speed test failed: %w", err))
	} else {
		widget.withNotice(nil)
	}
}

func (widget *speedtestWidget) Render() template.HTML {
	return widget.renderTemplate(widget, speedtestWidgetTemplate)
}

// UpdateIntervalMs polls quickly while a test runs so the freshly measured numbers
// replace the loader within seconds, then falls back to the configured interval.
func (widget *speedtestWidget) UpdateIntervalMs() int64 {
	if widget.IsTesting() {
		return (3 * time.Second).Milliseconds()
	}
	return widget.widgetBase.UpdateIntervalMs()
}

// Template accessors (runner is mutex-guarded: its background goroutine writes result concurrently).

func (widget *speedtestWidget) HasResult() bool {
	res, _, _ := widget.runner.snapshot()
	return res != nil
}

func (widget *speedtestWidget) IsTesting() bool {
	res, started, _ := widget.runner.snapshot()
	return started && res == nil
}

func (widget *speedtestWidget) Download() string {
	return widget.formatMetric(func(r *speedtestResult) float64 { return r.DownloadMbps }, 1)
}

func (widget *speedtestWidget) Upload() string {
	return widget.formatMetric(func(r *speedtestResult) float64 { return r.UploadMbps }, 1)
}

func (widget *speedtestWidget) Ping() string {
	return widget.formatMetric(func(r *speedtestResult) float64 { return r.PingMs }, 0)
}

func (widget *speedtestWidget) ServerName() string {
	res, _, _ := widget.runner.snapshot()
	if res == nil {
		return ""
	}
	return res.ServerName
}

func (widget *speedtestWidget) formatMetric(get func(*speedtestResult) float64, decimals int) string {
	res, _, _ := widget.runner.snapshot()
	if res == nil {
		return "-"
	}
	return strconv.FormatFloat(get(res), 'f', decimals, 64)
}

// runTest selects a server (if needed), then measures ping, download and upload.

func (r *speedtestRunner) runTest(ctx context.Context) (*speedtestResult, error) {
	srv, err := r.resolveServer(ctx)
	if err != nil {
		return nil, err
	}

	ping, err := r.measurePing(ctx, srv)
	if err != nil {
		r.forgetAutoSelectedServer()
		return nil, err
	}

	dl, err := r.measureStream(ctx, srv, false)
	if err != nil {
		r.forgetAutoSelectedServer()
		return nil, fmt.Errorf("download: %w", err)
	}

	ul, err := r.measureStream(ctx, srv, true)
	if err != nil {
		r.forgetAutoSelectedServer()
		return nil, fmt.Errorf("upload: %w", err)
	}

	return &speedtestResult{
		DownloadMbps: dl,
		UploadMbps:   ul,
		PingMs:       ping,
		ServerName:   srv.Name,
	}, nil
}

// forgetAutoSelectedServer clears the cached auto-selected server so the next run
// re-probes and can pick a different, working one. No-op for a manually set server.
func (r *speedtestRunner) forgetAutoSelectedServer() {
	if r.server != "" {
		return
	}
	r.mu.Lock()
	r.selected = nil
	r.mu.Unlock()
}

func (r *speedtestRunner) resolveServer(ctx context.Context) (*speedtestServer, error) {
	if r.server != "" {
		return &speedtestServer{
			Name:    r.server,
			Server:  r.server,
			DlURL:   speedtestDefaultDlPath,
			UlURL:   speedtestDefaultUlPath,
			PingURL: speedtestDefaultPingPath,
		}, nil
	}

	r.mu.Lock()
	cached := r.selected
	r.mu.Unlock()
	if cached != nil {
		return cached, nil
	}

	srv, err := r.autoSelectServer(ctx)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.selected = srv
	r.mu.Unlock()
	return srv, nil
}

func (r *speedtestRunner) autoSelectServer(ctx context.Context) (*speedtestServer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, speedtestDefaultListURL, nil)
	if err != nil {
		return nil, err
	}

	servers, err := decodeJsonFromRequest[[]speedtestServer](r.client, req)
	if err != nil {
		return nil, fmt.Errorf("fetching server list: %w", err)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("server list is empty")
	}

	type pingResult struct {
		idx     int
		latency time.Duration
	}

	results := make(chan pingResult, len(servers))
	var wg sync.WaitGroup

	for i := range servers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			latency, err := r.pingOnce(pingCtx, &servers[idx])
			if err != nil {
				results <- pingResult{idx: idx, latency: 0}
				return
			}
			results <- pingResult{idx: idx, latency: latency}
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	best := -1
	var bestLatency time.Duration
	for res := range results {
		if res.latency <= 0 {
			continue
		}
		if best == -1 || res.latency < bestLatency {
			best = res.idx
			bestLatency = res.latency
		}
	}

	if best == -1 {
		return nil, fmt.Errorf("no reachable speed test server found")
	}

	srv := servers[best]
	return &srv, nil
}

func speedtestJoinURL(base, path string) string {
	if strings.HasPrefix(base, "//") {
		base = "https:" + base
	}
	base = strings.TrimRight(base, "/")
	path = strings.TrimLeft(path, "/")
	return base + "/" + path
}

func (r *speedtestRunner) pingOnce(ctx context.Context, srv *speedtestServer) (time.Duration, error) {
	url := speedtestJoinURL(srv.Server, srv.PingURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "dynglance-speedtest")

	start := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024)) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return elapsed, nil
}

func (r *speedtestRunner) measurePing(ctx context.Context, srv *speedtestServer) (float64, error) {
	var total time.Duration
	var count int

	for i := 0; i < speedtestPingCount; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		latency, err := r.pingOnce(pingCtx, srv)
		cancel()
		if err != nil {
			if i == 0 {
				return 0, err
			}
			continue
		}
		if i == 0 {
			continue // discard first sample (handshake overhead)
		}
		total += latency
		count++
	}

	if count == 0 {
		return 0, fmt.Errorf("all pings failed")
	}

	return float64(total.Microseconds()) / float64(count) / 1000.0, nil
}

// measureStream runs concurrent staggered streams for duration and returns Mbps.
// upload=false drives GET garbage downloads, upload=true drives POST uploads.
func (r *speedtestRunner) measureStream(ctx context.Context, srv *speedtestServer, upload bool) (float64, error) {
	streamCtx, cancel := context.WithTimeout(ctx, r.duration)
	defer cancel()

	var counter atomic.Int64
	var wg sync.WaitGroup

	var payload []byte
	if upload {
		payload = make([]byte, speedtestUploadChunk)
		if _, err := rand.Read(payload); err != nil {
			return 0, err
		}
	}

	start := time.Now()

	for i := 0; i < r.concurrent; i++ {
		select {
		case <-streamCtx.Done():
		case <-time.After(time.Duration(i) * speedtestStagger):
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			for streamCtx.Err() == nil {
				if upload {
					r.uploadOnce(streamCtx, srv, payload, &counter)
				} else {
					r.downloadOnce(streamCtx, srv, &counter)
				}
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0, fmt.Errorf("invalid elapsed time")
	}

	// No bytes transferred means the chosen server's endpoint failed (bad path,
	// refused upload, etc). Report it as an error so the run retries instead of
	// storing a bogus 0.0 Mbps result.
	bytesMoved := counter.Load()
	if bytesMoved == 0 {
		return 0, fmt.Errorf("no data transferred")
	}

	mbps := float64(bytesMoved) / elapsed / 125000.0
	return mbps, nil
}

func (r *speedtestRunner) downloadOnce(ctx context.Context, srv *speedtestServer, counter *atomic.Int64) {
	url := speedtestJoinURL(srv.Server, srv.DlURL) + "?ckSize=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "dynglance-speedtest")

	resp, err := r.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	buf := make([]byte, 64*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			counter.Add(int64(n))
		}
		if err != nil {
			return
		}
	}
}

func (r *speedtestRunner) uploadOnce(ctx context.Context, srv *speedtestServer, payload []byte, counter *atomic.Int64) {
	url := speedtestJoinURL(srv.Server, srv.UlURL)
	body := &speedtestCountingReader{reader: bytes.NewReader(payload), counter: counter}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return
	}
	req.ContentLength = int64(len(payload))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "dynglance-speedtest")

	resp, err := r.client.Do(req)
	if err != nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024)) //nolint:errcheck
	resp.Body.Close()
}

// speedtestCountingReader counts bytes actually sent by the transport.
type speedtestCountingReader struct {
	reader  *bytes.Reader
	counter *atomic.Int64
}

func (r *speedtestCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.counter.Add(int64(n))
	}
	return n, err
}
