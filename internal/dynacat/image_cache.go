package dynacat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type imageCache struct {
	baseURL             string
	dir                 string
	mu                  sync.Mutex
	inFlight            map[string]*cacheEntry
	backgroundDownloads chan struct{}
}

const maxBackgroundImageDownloads = 1

type cacheEntry struct {
	done        chan struct{}
	ready       chan struct{}
	readyOnce   sync.Once
	background  bool
	tmpPath     string
	contentType string
	url         string
	err         error
}

type imageCacheLookup struct {
	parsed    *url.URL
	hashHex   string
	cachedURL string
	cacheable bool
}

var allowedImageExtensions = []string{
	".svg",
	".png",
	".jpg",
	".jpeg",
	".gif",
	".webp",
	".avif",
	".ico",
	".bmp",
	".img",
}

var contentTypeToExtension = map[string]string{
	"image/svg+xml":            ".svg",
	"image/png":                ".png",
	"image/jpeg":               ".jpg",
	"image/jpg":                ".jpg",
	"image/gif":                ".gif",
	"image/webp":               ".webp",
	"image/avif":               ".avif",
	"image/x-icon":             ".ico",
	"image/vnd.microsoft.icon": ".ico",
	"image/bmp":                ".bmp",
}

func newImageCache(baseURL string, dir string) *imageCache {
	return &imageCache{
		baseURL:             strings.TrimRight(baseURL, "/"),
		dir:                 dir,
		inFlight:            make(map[string]*cacheEntry),
		backgroundDownloads: make(chan struct{}, maxBackgroundImageDownloads),
	}
}

func (c *imageCache) CacheURL(ctx context.Context, rawURL string) (string, error) {
	return c.CacheURLWithClient(ctx, rawURL, false)
}

func (c *imageCache) CacheURLWithClient(ctx context.Context, rawURL string, allowInsecure bool) (string, error) {
	lookup, err := c.lookup(rawURL)
	if err != nil || !lookup.cacheable || lookup.cachedURL != "" {
		return lookup.cachedURL, err
	}

	key := imageCacheKey(rawURL, allowInsecure)
	entry, started := c.getOrCreateCacheEntry(key, false)
	if !started {
		<-entry.done
		return entry.url, entry.err
	}

	entry.url, entry.err = c.downloadAndCacheWithClient(ctx, rawURL, lookup.hashHex, lookup.parsed.Path, allowInsecure, entry)
	c.finishCacheEntry(key, entry)

	return entry.url, entry.err
}

func (c *imageCache) CachedURLOrQueue(ctx context.Context, rawURL string, allowInsecure bool) (string, error) {
	lookup, err := c.lookup(rawURL)
	if err != nil || !lookup.cacheable || lookup.cachedURL != "" {
		return lookup.cachedURL, err
	}

	key := imageCacheKey(rawURL, allowInsecure)
	select {
	case c.backgroundDownloads <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	default:
		return "", nil
	}

	entry, started := c.getOrCreateCacheEntry(key, true)
	if !started {
		<-c.backgroundDownloads
		return "", nil
	}

	go func() {
		defer func() { <-c.backgroundDownloads }()

		entry.url, entry.err = c.downloadAndCacheWithClient(ctx, rawURL, lookup.hashHex, lookup.parsed.Path, allowInsecure, entry)
		c.finishCacheEntry(key, entry)
	}()

	return "", nil
}

func (c *imageCache) ServeCachedOrInFlight(ctx context.Context, rawURL string, allowInsecure bool, w http.ResponseWriter) (string, bool, error) {
	lookup, err := c.lookup(rawURL)
	if err != nil || !lookup.cacheable || lookup.cachedURL != "" {
		return lookup.cachedURL, false, err
	}

	key := imageCacheKey(rawURL, allowInsecure)
	c.mu.Lock()
	entry, ok := c.inFlight[key]
	c.mu.Unlock()
	if !ok {
		return "", false, nil
	}

	select {
	case <-entry.done:
		return entry.url, false, entry.err
	default:
	}

	select {
	case <-entry.ready:
	case <-ctx.Done():
		return "", false, ctx.Err()
	}

	if entry.tmpPath == "" {
		return "", false, entry.err
	}

	file, err := os.Open(entry.tmpPath)
	if err != nil {
		return "", false, err
	}
	defer file.Close()

	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
	w.WriteHeader(http.StatusOK)

	return "", true, streamGrowingFile(ctx, file, entry, w)
}

func (c *imageCache) CachedURLOrWait(ctx context.Context, rawURL string, allowInsecure bool) (string, error) {
	lookup, err := c.lookup(rawURL)
	if err != nil || !lookup.cacheable || lookup.cachedURL != "" {
		return lookup.cachedURL, err
	}

	key := imageCacheKey(rawURL, allowInsecure)
	c.mu.Lock()
	entry, ok := c.inFlight[key]
	c.mu.Unlock()
	if !ok {
		return "", nil
	}

	select {
	case <-entry.done:
		return entry.url, entry.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *imageCache) lookup(rawURL string) (imageCacheLookup, error) {
	if c == nil || rawURL == "" {
		return imageCacheLookup{}, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return imageCacheLookup{}, err
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return imageCacheLookup{}, nil
	}

	hashHex := hashString(rawURL)
	lookup := imageCacheLookup{
		parsed:    parsed,
		hashHex:   hashHex,
		cacheable: true,
	}

	if existing, ok := c.findExistingFile(hashHex, parsed.Path); ok {
		lookup.cachedURL = c.publicURL(existing)
	}

	return lookup, nil
}

func (c *imageCache) getOrCreateCacheEntry(key string, background bool) (*cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.inFlight[key]; ok {
		return entry, false
	}

	entry := &cacheEntry{done: make(chan struct{}), ready: make(chan struct{}), background: background}
	c.inFlight[key] = entry
	return entry, true
}

func (c *imageCache) finishCacheEntry(key string, entry *cacheEntry) {
	entry.readyOnce.Do(func() { close(entry.ready) })
	c.mu.Lock()
	delete(c.inFlight, key)
	c.mu.Unlock()
	close(entry.done)
}

func imageCacheKey(rawURL string, allowInsecure bool) string {
	if allowInsecure {
		return rawURL + "\x00insecure"
	}

	return rawURL
}

func (c *imageCache) findExistingFile(hashHex string, urlPath string) (string, bool) {
	if ext := extensionFromPath(urlPath); ext != "" {
		filename := hashHex + ext
		if fileExists(filepath.Join(c.dir, filename)) {
			return filename, true
		}
	}

	for _, ext := range allowedImageExtensions {
		filename := hashHex + ext
		if fileExists(filepath.Join(c.dir, filename)) {
			return filename, true
		}
	}

	return "", false
}

func (c *imageCache) downloadAndCache(ctx context.Context, rawURL string, hashHex string, urlPath string) (string, error) {
	return c.downloadAndCacheWithClient(ctx, rawURL, hashHex, urlPath, false, nil)
}

func (c *imageCache) downloadAndCacheWithClient(ctx context.Context, rawURL string, hashHex string, urlPath string, allowInsecure bool, entry *cacheEntry) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "image/*")
	setBrowserUserAgentHeader(req)

	client := ternary(allowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d for %s", resp.StatusCode, rawURL)
	}
	contentType := resp.Header.Get("Content-Type")

	ext := extensionFromPath(urlPath)
	if ext == "" {
		ext = extensionFromContentType(contentType)
	}
	if ext == "" {
		ext = ".img"
	}

	file, err := os.CreateTemp(c.dir, hashHex+"-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := file.Name()
	if entry != nil {
		entry.tmpPath = tmpPath
		entry.contentType = contentType
		entry.readyOnce.Do(func() { close(entry.ready) })
	}

	_, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", closeErr
	}

	filename := hashHex + ext
	finalPath := filepath.Join(c.dir, filename)
	if fileExists(finalPath) {
		_ = os.Remove(tmpPath)
		return c.publicURL(filename), nil
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	return c.publicURL(filename), nil
}

func streamGrowingFile(ctx context.Context, file *os.File, entry *cacheEntry, dst io.Writer) error {
	buf := make([]byte, 32*1024)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	flusher, canFlush := dst.(http.Flusher)
	var offset int64
	for {
		n, err := file.Read(buf)
		if n > 0 {
			offset += int64(n)
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return err
		}

		select {
		case <-entry.done:
			if info, statErr := file.Stat(); statErr == nil && info.Size() > offset {
				continue
			}
			return entry.err
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *imageCache) publicURL(filename string) string {
	if c.baseURL == "" {
		return "/.cache/" + filename
	}

	return c.baseURL + "/.cache/" + filename
}

func (c *imageCache) IsBuildingCache() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.inFlight {
		if !entry.background {
			return true
		}
	}

	return false
}

func extensionFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return ""
	}

	for _, allowed := range allowedImageExtensions {
		if ext == allowed {
			return ext
		}
	}

	return ""
}

func extensionFromContentType(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return contentTypeToExtension[contentType]
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
