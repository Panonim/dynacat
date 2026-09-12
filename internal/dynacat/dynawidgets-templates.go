package dynacat

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	dynawidgetsUpdateCheckInterval     = 3 * 24 * time.Hour
	dynawidgetsSlowUpdateCheckInterval = 14 * 24 * time.Hour
	// Consecutive checks that found nothing before the slow interval takes over.
	dynawidgetsSlowUpdateAfterChecks = 2
	// How often a widget asks; the metadata interval decides whether that reaches the network.
	dynawidgetsCheckPollInterval    = time.Hour
	dynawidgetsUpdateNoticeDuration = 7 * 24 * time.Hour
)

type dynawidgetsTemplateMeta struct {
	URL             string    `json:"url"`
	Title           string    `json:"title,omitempty"`
	ETag            string    `json:"etag,omitempty"`
	CheckedAt       time.Time `json:"checked-at"`
	UpdatedAt       time.Time `json:"updated-at"`
	UnchangedChecks int       `json:"unchanged-checks,omitempty"`
}

// Templates that keep coming back unchanged are checked far less often.
func (meta *dynawidgetsTemplateMeta) checkInterval() time.Duration {
	if meta.UnchangedChecks >= dynawidgetsSlowUpdateAfterChecks {
		return dynawidgetsSlowUpdateCheckInterval
	}

	return dynawidgetsUpdateCheckInterval
}

// Includes the branch so two branches cannot share a cached template.
func dynawidgetsCacheKey(slug string, repo string) string {
	return slug + "@" + repo
}

// Keeps generated paths inside the assets directory, whatever the slug looks like.
func dynawidgetsAssetPath(slug string, repo string, suffix string) (string, error) {
	if !dynawidgetsSlugPattern.MatchString(slug) {
		return "", fmt.Errorf("invalid slug %q", slug)
	}

	if !dynawidgetsRepoPattern.MatchString(repo) {
		return "", fmt.Errorf("invalid repo %q", repo)
	}

	path := filepath.Join(dynawidgetsAssetsDir, dynawidgetsCacheKey(slug, repo)+suffix)
	absAssets, err := filepath.Abs(dynawidgetsAssetsDir)
	if err != nil {
		return path, nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(absPath, absAssets+string(filepath.Separator)) {
		return "", errors.New("refusing to resolve template outside assets dir")
	}

	return path, nil
}

func dynawidgetsTemplateModTime(slug string, repo string) time.Time {
	path, err := dynawidgetsAssetPath(slug, repo, ".txt")
	if err != nil {
		return time.Time{}
	}

	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}

	return info.ModTime()
}

// Metadata for every cached template, keyed by slug and branch.
var dynawidgetsMetaPath = filepath.Join(dynawidgetsAssetsDir, "templates.meta.json")

var dynawidgetsMetaMu sync.Mutex

func dynawidgetsReadAllMeta() map[string]dynawidgetsTemplateMeta {
	data, err := os.ReadFile(dynawidgetsMetaPath)
	if err != nil {
		return map[string]dynawidgetsTemplateMeta{}
	}

	entries := map[string]dynawidgetsTemplateMeta{}
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("Ignoring unreadable dynawidgets metadata", "error", err, "path", dynawidgetsMetaPath)
		return map[string]dynawidgetsTemplateMeta{}
	}

	return entries
}

func dynawidgetsReadMeta(slug string, repo string) *dynawidgetsTemplateMeta {
	dynawidgetsMetaMu.Lock()
	defer dynawidgetsMetaMu.Unlock()

	meta, ok := dynawidgetsReadAllMeta()[dynawidgetsCacheKey(slug, repo)]
	if !ok {
		return nil
	}

	return &meta
}

func dynawidgetsWriteMeta(slug string, repo string, meta *dynawidgetsTemplateMeta) {
	dynawidgetsMetaMu.Lock()
	defer dynawidgetsMetaMu.Unlock()

	entries := dynawidgetsReadAllMeta()
	entries[dynawidgetsCacheKey(slug, repo)] = *meta

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}

	if err := os.MkdirAll(dynawidgetsAssetsDir, 0755); err != nil {
		slog.Error("Failed to create dynawidgets assets directory", "error", err)
		return
	}

	if err := os.WriteFile(dynawidgetsMetaPath, data, 0600); err != nil {
		slog.Error("Failed to write dynawidgets metadata", "error", err, "path", dynawidgetsMetaPath)
	}
}

var dynawidgetsCheckLocks = struct {
	mu    sync.Mutex
	slugs map[string]*sync.Mutex
}{slugs: map[string]*sync.Mutex{}}

func dynawidgetsCheckLock(slug string, repo string) *sync.Mutex {
	dynawidgetsCheckLocks.mu.Lock()
	defer dynawidgetsCheckLocks.mu.Unlock()

	key := repo + "/" + slug
	lock, ok := dynawidgetsCheckLocks.slugs[key]
	if !ok {
		lock = &sync.Mutex{}
		dynawidgetsCheckLocks.slugs[key] = lock
	}

	return lock
}

// Refetches the cached template once its check interval has passed and rewrites the cache file if it changed.
func dynawidgetsCheckTemplate(slug string, repo string) error {
	if repo == "" {
		repo = dynawidgetsDefaultRepo
	}

	lock := dynawidgetsCheckLock(slug, repo)
	lock.Lock()
	defer lock.Unlock()

	templatePath, err := dynawidgetsAssetPath(slug, repo, ".txt")
	if err != nil {
		return err
	}

	cached, err := os.ReadFile(templatePath)
	if err != nil {
		// Nothing cached yet, so the next initialize downloads it anyway.
		return nil
	}

	meta := dynawidgetsReadMeta(slug, repo)
	if meta == nil {
		meta = &dynawidgetsTemplateMeta{}
	}

	if time.Since(meta.CheckedAt) < meta.checkInterval() {
		return nil
	}

	templateURL := meta.URL
	if templateURL == "" {
		templateURL, meta.Title, err = dynawidgetsTemplateURL(slug, repo)
		if err != nil {
			return err
		}
		meta.URL = templateURL
	}

	body, notModified, etag, err := dynawidgetsDownloadTemplate(templateURL, meta.ETag)
	if err != nil {
		return err
	}

	now := time.Now()
	meta.CheckedAt = now
	meta.ETag = etag

	if notModified || string(body) == string(cached) {
		meta.UnchangedChecks++
		dynawidgetsWriteMeta(slug, repo, meta)
		return nil
	}

	if err := os.WriteFile(templatePath, body, 0600); err != nil {
		return fmt.Errorf("writing template: %w", err)
	}

	meta.UpdatedAt = now
	meta.UnchangedChecks = 0
	dynawidgetsWriteMeta(slug, repo, meta)
	slog.Info("Dynawidget template updated", "slug", slug, "repo", repo, "url", templateURL)

	return nil
}

func dynawidgetsDownloadTemplate(templateURL string, etag string) (body []byte, notModified bool, newETag string, err error) {
	parsedURL, err := url.Parse(templateURL)
	if err != nil {
		return nil, false, "", fmt.Errorf("invalid template URL %q: %w", templateURL, err)
	}
	if parsedURL.Scheme != "https" || parsedURL.Host != dynawidgetsTemplateHost {
		return nil, false, "", fmt.Errorf(
			"refusing to fetch template from unexpected host %q (must be https://%s)",
			parsedURL.Host, dynawidgetsTemplateHost,
		)
	}

	request, err := http.NewRequest(http.MethodGet, templateURL, nil)
	if err != nil {
		return nil, false, "", err
	}
	request.Header.Set("User-Agent", dynacatUserAgentString)
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}

	resp, err := defaultHTTPClient.Do(request)
	if err != nil {
		return nil, false, "", fmt.Errorf("fetching template: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, true, etag, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, "", fmt.Errorf("fetching template: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	body, err = readLimited(resp.Body)
	if err != nil {
		return nil, false, "", fmt.Errorf("reading template body: %w", err)
	}

	return body, false, resp.Header.Get("ETag"), nil
}
