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
	"strconv"
	"strings"
	"sync"
	"time"
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
	return c.CacheURLWithClient(ctx, rawURL, false, 0)
}

// A maxAge of 0 keeps cached files forever.
func (c *imageCache) CacheURLWithClient(ctx context.Context, rawURL string, allowInsecure bool, maxAge time.Duration) (string, error) {
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
	existing, found := c.findExistingFile(hashHex, parsed.Path)
	if found && (maxAge <= 0 || !c.isStale(existing, maxAge)) {
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
	if entry.err != nil && found {
		entry.url, entry.err = c.publicURL(existing), nil
	}

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
		return "", fmt.Errorf("unsupported content type %q for %s", resp.Header.Get("Content-Type"), rawURL)
	}

	tmpPath := filepath.Join(c.dir, hashHex+".tmp")
	file, err := os.Create(tmpPath)
	if err != nil {
		return "", err
	}

	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, maxResponseBytes+1))
	closeErr := file.Close()
	if copyErr == nil && written > maxResponseBytes {
		copyErr = errResponseTooLarge
	}
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
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	return c.publicURL(filename), nil
}

func (c *imageCache) isStale(filename string, maxAge time.Duration) bool {
	info, err := os.Stat(filepath.Join(c.dir, filename))
	return err != nil || time.Since(info.ModTime()) > maxAge
}

// The mtime query param makes browsers refetch when the file is replaced.
func (c *imageCache) publicURL(filename string) string {
	path := "/.cache/" + filename
	if info, err := os.Stat(filepath.Join(c.dir, filename)); err == nil {
		path += "?v=" + strconv.FormatInt(info.ModTime().Unix(), 10)
	}

	return c.baseURL + path
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
