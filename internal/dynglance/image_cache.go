package dynglance

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
)

type imageCache struct {
	baseURL  string
	dir      string
	mu       sync.Mutex
	inFlight map[string]*cacheEntry
}

type cacheEntry struct {
	done chan struct{}
	url  string
	err  error
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
		baseURL:  strings.TrimRight(baseURL, "/"),
		dir:      dir,
		inFlight: make(map[string]*cacheEntry),
	}
}

func (c *imageCache) CacheURL(ctx context.Context, rawURL string) (string, error) {
	return c.CacheURLWithClient(ctx, rawURL, false)
}

func (c *imageCache) CacheURLWithClient(ctx context.Context, rawURL string, allowInsecure bool) (string, error) {
	if c == nil || rawURL == "" {
		return "", nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil
	}

	hashHex := hashString(rawURL)
	if existing, ok := c.findExistingFile(hashHex, parsed.Path); ok {
		return c.publicURL(existing), nil
	}

	c.mu.Lock()
	if entry, ok := c.inFlight[rawURL]; ok {
		c.mu.Unlock()
		<-entry.done
		return entry.url, entry.err
	}

	entry := &cacheEntry{done: make(chan struct{})}
	c.inFlight[rawURL] = entry
	c.mu.Unlock()

	entry.url, entry.err = c.downloadAndCacheWithClient(ctx, rawURL, hashHex, parsed.Path, allowInsecure)

	c.mu.Lock()
	delete(c.inFlight, rawURL)
	c.mu.Unlock()
	close(entry.done)

	return entry.url, entry.err
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
	return c.downloadAndCacheWithClient(ctx, rawURL, hashHex, urlPath, false)
}

func (c *imageCache) downloadAndCacheWithClient(ctx context.Context, rawURL string, hashHex string, urlPath string, allowInsecure bool) (string, error) {
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

	ext := extensionFromPath(urlPath)
	if ext == "" {
		ext = extensionFromContentType(resp.Header.Get("Content-Type"))
	}
	if ext == "" {
		ext = ".img"
	}

	tmpPath := filepath.Join(c.dir, hashHex+".tmp")
	file, err := os.Create(tmpPath)
	if err != nil {
		return "", err
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
	return len(c.inFlight) > 0
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
