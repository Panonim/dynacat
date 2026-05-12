package dynacat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

var torrentingWidgetTemplate = mustParseTemplate("torrenting.html", "widget-base.html")

var errTorrentUnauthorized = errors.New("torrent client: unauthorized")

type TorrentingHostConfig struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Client   string `yaml:"client"`
	client   *http.Client

	trSessionID string
	trSessionMu sync.Mutex
}

type torrentingWidget struct {
	widgetBase    `yaml:",inline"`
	Frameless     bool                   `yaml:"frameless"`
	Hosts         []TorrentingHostConfig `yaml:"hosts"`
	HideCompleted bool                   `yaml:"hide-completed"`
	HideInactive  bool                   `yaml:"hide-inactive"`
	HideBar       bool                   `yaml:"hide-bar"`
	WrapText      bool                   `yaml:"wrap-text"`
	CollapseAfter int                    `yaml:"collapse-after"`

	mu        sync.RWMutex
	Torrents  []torrentInfo
	MultiHost bool
}

type torrentInfo struct {
	Name          string
	State         string
	Progress      float64
	Downloaded    int64
	Size          int64
	ETA           int64
	IsCompleted   bool
	IsActive      bool
	Icon          string
	FmtProgress   string
	FmtETA        string
	ShortName     string
	ProgressWidth string
	ClientIcon    string
}

type qbTorrentJSON struct {
	Name       string  `json:"name"`
	State      string  `json:"state"`
	Progress   float64 `json:"progress"`
	Downloaded int64   `json:"downloaded"`
	Size       int64   `json:"size"`
	ETA        int64   `json:"eta"`
}

func (widget *torrentingWidget) initialize() error {
	widget.withTitle("Torrents")

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(30 * time.Second)
		widget.UpdateInterval = &interval
	}

	widget.withCacheDuration(time.Duration(*widget.UpdateInterval))

	if widget.CollapseAfter == 0 {
		widget.CollapseAfter = 3
	}

	if len(widget.Hosts) == 0 {
		return fmt.Errorf("at least one host must be specified")
	}

	for i := range widget.Hosts {
		host := &widget.Hosts[i]
		if host.URL == "" {
			return fmt.Errorf("host URL is required")
		}
		if host.Password == "" {
			return fmt.Errorf("host password is required")
		}

		switch strings.ToLower(host.Client) {
		case "", "qbittorrent":
			host.Client = "qbittorrent"
			if host.Username == "" {
				return fmt.Errorf("host username is required for qBittorrent")
			}
		case "deluge":
			host.Client = "deluge"
		case "transmission":
			host.Client = "transmission"
		default:
			return fmt.Errorf("unsupported torrent client: %s (supported: qbittorrent, deluge, transmission)", host.Client)
		}

		jar, err := cookiejar.New(nil)
		if err != nil {
			return fmt.Errorf("failed to create cookie jar: %w", err)
		}
		host.client = &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
		}
	}

	return nil
}

func (widget *torrentingWidget) update(ctx context.Context) {
	widget.MultiHost = len(widget.Hosts) > 1

	type fetchResult struct {
		torrents []torrentInfo
		err      error
		url      string
		client   string
	}

	results := make(chan fetchResult, len(widget.Hosts))
	var wg sync.WaitGroup

	for i := range widget.Hosts {
		host := &widget.Hosts[i]
		wg.Add(1)
		go func(h *TorrentingHostConfig) {
			defer wg.Done()
			torrents, err := widget.fetchFromHost(ctx, h)
			results <- fetchResult{torrents: torrents, err: err, url: h.URL, client: h.Client}
		}(host)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allTorrents []torrentInfo
	successCount := 0
	errorCount := 0

	for result := range results {
		if result.err != nil {
			errorCount++
			slog.Error("failed to fetch torrents", "url", result.url, "error", result.err)
			continue
		}
		successCount++
		if widget.MultiHost {
			for i := range result.torrents {
				result.torrents[i].ClientIcon = result.client
			}
		}
		allTorrents = append(allTorrents, result.torrents...)
	}

	sort.SliceStable(allTorrents, func(i, j int) bool {
		if p, q := torrentDownloadPriority(allTorrents[i]), torrentDownloadPriority(allTorrents[j]); p != q {
			return p < q
		}
		if allTorrents[i].IsCompleted != allTorrents[j].IsCompleted {
			return allTorrents[j].IsCompleted
		}
		return strings.ToLower(allTorrents[i].Name) < strings.ToLower(allTorrents[j].Name)
	})

	widget.mu.Lock()
	widget.Torrents = allTorrents
	widget.mu.Unlock()

	var err error
	if successCount == 0 {
		err = errNoContent
	} else if errorCount > 0 {
		err = errPartialContent
	}

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}
}

func (widget *torrentingWidget) qbLogin(ctx context.Context, host *TorrentingHostConfig) error {
	loginURL := strings.TrimRight(host.URL, "/") + "/api/v2/auth/login"

	form := url.Values{
		"username": {host.Username},
		"password": {host.Password},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := host.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if strings.TrimSpace(string(body)) != "Ok." {
		return fmt.Errorf("login failed: %s", strings.TrimSpace(string(body)))
	}

	return nil
}

func (widget *torrentingWidget) fetchFromHost(ctx context.Context, host *TorrentingHostConfig) ([]torrentInfo, error) {
	switch host.Client {
	case "deluge":
		return widget.fetchFromDeluge(ctx, host)
	case "transmission":
		return widget.fetchFromTransmission(ctx, host)
	default:
		return widget.fetchFromQBittorrent(ctx, host)
	}
}

func (widget *torrentingWidget) fetchFromQBittorrent(ctx context.Context, host *TorrentingHostConfig) ([]torrentInfo, error) {
	torrents, err := widget.qbFetchTorrentsOnce(ctx, host)
	if errors.Is(err, errTorrentUnauthorized) {
		slog.Info("qBittorrent session expired, re-logging in", "url", host.URL)
		if loginErr := widget.qbLogin(ctx, host); loginErr != nil {
			slog.Error("qBittorrent re-login failed", "url", host.URL, "error", loginErr)
			return nil, loginErr
		}
		torrents, err = widget.qbFetchTorrentsOnce(ctx, host)
	}
	return torrents, err
}

func (widget *torrentingWidget) qbFetchTorrentsOnce(ctx context.Context, host *TorrentingHostConfig) ([]torrentInfo, error) {
	apiURL := strings.TrimRight(host.URL, "/") + "/api/v2/torrents/info"

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := host.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errTorrentUnauthorized
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, host.URL)
	}

	var raw []qbTorrentJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode torrents JSON: %w", err)
	}

	torrents := make([]torrentInfo, 0, len(raw))
	for _, t := range raw {
		torrents = append(torrents, computeTorrentInfo(t))
	}
	return torrents, nil
}

func computeTorrentInfo(t qbTorrentJSON) torrentInfo {
	info := torrentInfo{
		Name:       t.Name,
		State:      t.State,
		Progress:   t.Progress,
		Downloaded: t.Downloaded,
		Size:       t.Size,
		ETA:        t.ETA,
	}

	info.IsCompleted = t.Progress >= 1.0

	switch t.State {
	// qBittorrent active states
	case "downloading", "forcedDL", "uploading", "forcedUP":
		info.IsActive = true
	// Deluge active states
	case "Downloading", "Seeding":
		info.IsActive = true
	// Transmission active states
	case "tr-downloading", "tr-seeding":
		info.IsActive = true
	}

	switch {
	case info.IsCompleted:
		info.Icon = "✔"
	// qBittorrent states
	case t.State == "downloading" || t.State == "forcedDL":
		info.Icon = "↓"
	case t.State == "uploading" || t.State == "forcedUP":
		info.Icon = "↑"
	case t.State == "error" || t.State == "missingFiles":
		info.Icon = "!"
	case t.State == "checkingDL" || t.State == "checkingUP" || t.State == "allocating":
		info.Icon = "…"
	case t.State == "checkingResumeData":
		info.Icon = "⟳"
	// Deluge states
	case t.State == "Downloading":
		info.Icon = "↓"
	case t.State == "Seeding":
		info.Icon = "↑"
	case t.State == "Error":
		info.Icon = "!"
	case t.State == "Checking":
		info.Icon = "…"
	case t.State == "Moving":
		info.Icon = "⟳"
	case t.State == "Queued":
		info.Icon = "…"
	case t.State == "Paused":
		info.Icon = "❚❚"
	// Transmission states
	case t.State == "tr-downloading":
		info.Icon = "↓"
	case t.State == "tr-seeding":
		info.Icon = "↑"
	case t.State == "tr-error":
		info.Icon = "!"
	case t.State == "tr-checking" || t.State == "tr-check-wait":
		info.Icon = "…"
	case t.State == "tr-download-wait" || t.State == "tr-seed-wait":
		info.Icon = "…"
	case t.State == "tr-stopped":
		info.Icon = "❚❚"
	default:
		info.Icon = "❚❚"
	}

	if t.Size >= 1_073_741_824 {
		info.FmtProgress = fmt.Sprintf("%.2f GB / %.2f GB",
			float64(t.Downloaded)/1_073_741_824,
			float64(t.Size)/1_073_741_824,
		)
	} else {
		info.FmtProgress = fmt.Sprintf("%.2f MB / %.2f MB",
			float64(t.Downloaded)/1_048_576,
			float64(t.Size)/1_048_576,
		)
	}

	if t.ETA < 0 || t.ETA >= 8_640_000 {
		info.FmtETA = "∞"
	} else if t.ETA == 0 {
		info.FmtETA = "0m"
	} else {
		h := t.ETA / 3600
		m := (t.ETA % 3600) / 60
		s := t.ETA % 60
		if h > 0 {
			info.FmtETA = fmt.Sprintf("%dh %dm", h, m)
		} else if m > 0 {
			info.FmtETA = fmt.Sprintf("%dm", m)
		} else {
			info.FmtETA = fmt.Sprintf("%ds", s)
		}
	}

	info.ShortName = t.Name

	info.ProgressWidth = fmt.Sprintf("%.1f%%", t.Progress*100)

	return info
}

// Deluge JSON-RPC types and methods

type delugeJSONRPCRequest struct {
	ID     int           `json:"id"`
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
}

type delugeJSONRPCResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

type delugeTorrentJSON struct {
	Name      string  `json:"name"`
	State     string  `json:"state"`
	Progress  float64 `json:"progress"`
	TotalDone int64   `json:"total_done"`
	TotalSize int64   `json:"total_size"`
	ETA       float64 `json:"eta"`
}

func (widget *torrentingWidget) delugeRPC(ctx context.Context, host *TorrentingHostConfig, method string, params ...interface{}) (*delugeJSONRPCResponse, error) {
	rpcURL := strings.TrimRight(host.URL, "/") + "/json"

	if params == nil {
		params = []interface{}{}
	}

	reqBody := delugeJSONRPCRequest{
		ID:     1,
		Method: method,
		Params: params,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rpcURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := host.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errTorrentUnauthorized
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, host.URL)
	}

	var rpcResp delugeJSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode Deluge JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		if rpcResp.Error.Code == 1 {
			return nil, errTorrentUnauthorized
		}
		return nil, fmt.Errorf("deluge RPC error: %s", rpcResp.Error.Message)
	}

	return &rpcResp, nil
}

func (widget *torrentingWidget) delugeLogin(ctx context.Context, host *TorrentingHostConfig) error {
	resp, err := widget.delugeRPC(ctx, host, "auth.login", host.Password)
	if err != nil {
		return err
	}

	var result bool
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("failed to parse Deluge login response: %w", err)
	}

	if !result {
		return fmt.Errorf("deluge login failed: invalid password")
	}

	return nil
}

func (widget *torrentingWidget) delugeEnsureConnected(ctx context.Context, host *TorrentingHostConfig) error {
	resp, err := widget.delugeRPC(ctx, host, "web.connected")
	if err != nil {
		return err
	}

	var connected bool
	if err := json.Unmarshal(resp.Result, &connected); err != nil {
		return fmt.Errorf("failed to parse Deluge connected response: %w", err)
	}

	if connected {
		return nil
	}

	hostsResp, err := widget.delugeRPC(ctx, host, "web.get_hosts")
	if err != nil {
		return fmt.Errorf("failed to get Deluge hosts: %w", err)
	}

	var hosts [][]interface{}
	if err := json.Unmarshal(hostsResp.Result, &hosts); err != nil {
		return fmt.Errorf("failed to parse Deluge hosts: %w", err)
	}

	if len(hosts) == 0 {
		return fmt.Errorf("no Deluge daemon hosts available")
	}

	hostID, ok := hosts[0][0].(string)
	if !ok {
		return fmt.Errorf("unexpected Deluge host ID format")
	}

	_, err = widget.delugeRPC(ctx, host, "web.connect", hostID)
	return err
}

func (widget *torrentingWidget) fetchFromDeluge(ctx context.Context, host *TorrentingHostConfig) ([]torrentInfo, error) {
	torrents, err := widget.delugeFetchTorrentsOnce(ctx, host)
	if errors.Is(err, errTorrentUnauthorized) {
		slog.Info("Deluge session expired, re-logging in", "url", host.URL)
		if loginErr := widget.delugeLogin(ctx, host); loginErr != nil {
			slog.Error("Deluge re-login failed", "url", host.URL, "error", loginErr)
			return nil, loginErr
		}
		if connErr := widget.delugeEnsureConnected(ctx, host); connErr != nil {
			slog.Error("Deluge daemon connection failed", "url", host.URL, "error", connErr)
			return nil, connErr
		}
		torrents, err = widget.delugeFetchTorrentsOnce(ctx, host)
	}
	return torrents, err
}

func (widget *torrentingWidget) delugeFetchTorrentsOnce(ctx context.Context, host *TorrentingHostConfig) ([]torrentInfo, error) {
	fields := []string{"name", "state", "progress", "total_done", "total_size", "eta"}

	resp, err := widget.delugeRPC(ctx, host, "core.get_torrents_status", map[string]interface{}{}, fields)
	if err != nil {
		return nil, err
	}

	var raw map[string]delugeTorrentJSON
	if err := json.Unmarshal(resp.Result, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode Deluge torrents: %w", err)
	}

	torrents := make([]torrentInfo, 0, len(raw))
	for _, t := range raw {
		torrents = append(torrents, computeTorrentInfo(qbTorrentJSON{
			Name:       t.Name,
			State:      t.State,
			Progress:   t.Progress / 100.0,
			Downloaded: t.TotalDone,
			Size:       t.TotalSize,
			ETA:        int64(t.ETA),
		}))
	}

	return torrents, nil
}

// Transmission RPC types and methods

type transmissionRPCRequest struct {
	Method    string                 `json:"method"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type transmissionRPCResponse struct {
	Result    string `json:"result"`
	Arguments struct {
		Torrents []transmissionTorrentJSON `json:"torrents"`
	} `json:"arguments"`
}

type transmissionTorrentJSON struct {
	Name           string  `json:"name"`
	Status         int     `json:"status"`
	PercentDone    float64 `json:"percentDone"`
	DownloadedEver int64   `json:"downloadedEver"`
	TotalSize      int64   `json:"totalSize"`
	ETA            int64   `json:"eta"`
	Error          int     `json:"error"`
}

func transmissionStatusToState(status int, hasError bool) string {
	if hasError {
		return "tr-error"
	}
	switch status {
	case 0:
		return "tr-stopped"
	case 1:
		return "tr-check-wait"
	case 2:
		return "tr-checking"
	case 3:
		return "tr-download-wait"
	case 4:
		return "tr-downloading"
	case 5:
		return "tr-seed-wait"
	case 6:
		return "tr-seeding"
	default:
		return "tr-stopped"
	}
}

func (widget *torrentingWidget) trDoRPC(ctx context.Context, host *TorrentingHostConfig, rpcReq transmissionRPCRequest) (*transmissionRPCResponse, error) {
	rpcURL := strings.TrimRight(host.URL, "/") + "/transmission/rpc"

	bodyBytes, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rpcURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if host.Username != "" {
		req.SetBasicAuth(host.Username, host.Password)
	}

	host.trSessionMu.Lock()
	sessionID := host.trSessionID
	host.trSessionMu.Unlock()

	if sessionID != "" {
		req.Header.Set("X-Transmission-Session-Id", sessionID)
	}

	resp, err := host.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		newSessionID := resp.Header.Get("X-Transmission-Session-Id")
		if newSessionID == "" {
			return nil, fmt.Errorf("transmission returned 409 without session ID header")
		}

		host.trSessionMu.Lock()
		host.trSessionID = newSessionID
		host.trSessionMu.Unlock()

		retryReq, err := http.NewRequestWithContext(ctx, "POST", rpcURL, strings.NewReader(string(bodyBytes)))
		if err != nil {
			return nil, err
		}
		retryReq.Header.Set("Content-Type", "application/json")
		retryReq.Header.Set("X-Transmission-Session-Id", newSessionID)
		if host.Username != "" {
			retryReq.SetBasicAuth(host.Username, host.Password)
		}

		resp, err = host.client.Do(retryReq)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errTorrentUnauthorized
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, host.URL)
	}

	var rpcResp transmissionRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode Transmission RPC response: %w", err)
	}

	if rpcResp.Result != "success" {
		return nil, fmt.Errorf("transmission RPC error: %s", rpcResp.Result)
	}

	return &rpcResp, nil
}

func (widget *torrentingWidget) fetchFromTransmission(ctx context.Context, host *TorrentingHostConfig) ([]torrentInfo, error) {
	rpcReq := transmissionRPCRequest{
		Method: "torrent-get",
		Arguments: map[string]interface{}{
			"fields": []string{"name", "status", "percentDone", "downloadedEver", "totalSize", "eta", "error"},
		},
	}

	resp, err := widget.trDoRPC(ctx, host, rpcReq)
	if err != nil {
		return nil, err
	}

	torrents := make([]torrentInfo, 0, len(resp.Arguments.Torrents))
	for _, t := range resp.Arguments.Torrents {
		state := transmissionStatusToState(t.Status, t.Error > 0)
		eta := t.ETA
		if eta < -1 {
			eta = -1
		}
		torrents = append(torrents, computeTorrentInfo(qbTorrentJSON{
			Name:       t.Name,
			State:      state,
			Progress:   t.PercentDone,
			Downloaded: t.DownloadedEver,
			Size:       t.TotalSize,
			ETA:        eta,
		}))
	}

	return torrents, nil
}

func torrentDownloadPriority(info torrentInfo) int {
	switch info.State {
	case "downloading", "forcedDL", "Downloading", "tr-downloading":
		return 0
	case "uploading", "forcedUP", "Seeding", "tr-seeding":
		return 1
	default:
		if info.IsCompleted {
			return 3
		}
		return 2
	}
}

func (widget *torrentingWidget) Render() template.HTML {
	widget.mu.RLock()
	defer widget.mu.RUnlock()
	return widget.renderTemplate(widget, torrentingWidgetTemplate)
}
