package dynacat

import (
	"bufio"
	"bytes"
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
	return c.CacheURLWithClient(ctx, rawURL, false, true, 0)
}

// A maxAge of 0 keeps cached files forever. Only URLs the admin configured, or got from their own services, should allowPrivate.
func (c *imageCache) CacheURLWithClient(ctx context.Context, rawURL string, allowInsecure bool, allowPrivate bool, maxAge time.Duration) (string, error) {
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

	// Public-only fetches get their own entries, so they never reuse a file fetched without the address checks.
	cacheKey := ternary(allowPrivate, rawURL, "public:"+rawURL)
	hashHex := hashString(cacheKey)
	existing, found := c.findExistingFile(hashHex, parsed.Path)
	if found && (maxAge <= 0 || !c.isStale(existing, maxAge)) {
		return c.publicURL(existing), nil
	}

	c.mu.Lock()
	if entry, ok := c.inFlight[cacheKey]; ok {
		c.mu.Unlock()
		<-entry.done
		return entry.url, entry.err
	}

	entry := &cacheEntry{done: make(chan struct{})}
	c.inFlight[cacheKey] = entry
	c.mu.Unlock()

	entry.url, entry.err = c.downloadAndCacheWithClient(ctx, rawURL, hashHex, parsed.Path, allowInsecure, allowPrivate)
	if entry.err != nil && found {
		entry.url, entry.err = c.publicURL(existing), nil
	}

	c.mu.Lock()
	delete(c.inFlight, cacheKey)
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

func (c *imageCache) downloadAndCacheWithClient(ctx context.Context, rawURL string, hashHex string, urlPath string, allowInsecure bool, allowPrivate bool) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "image/*")
	setBrowserUserAgentHeader(req)

	client := ternary(allowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	if !allowPrivate {
		if err := validatePublicFetchURL(rawURL); err != nil {
			return "", err
		}
		client = ternary(allowInsecure, publicOnlyInsecureHTTPClient, publicOnlyHTTPClient)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d for %s", resp.StatusCode, rawURL)
	}

	contentType := resp.Header.Get("Content-Type")
	ext := extensionFromContentType(contentType)
	if ext == "" && pathMayNameImageType(contentType) {
		ext = extensionFromPath(urlPath)
	}
	if ext == "" {
		return "", fmt.Errorf("unsupported content type %q for %s", resp.Header.Get("Content-Type"), rawURL)
	}

	body := bufio.NewReaderSize(io.LimitReader(resp.Body, maxResponseBytes+1), imageSniffBytes)
	head, err := body.Peek(imageSniffBytes)
	if err != nil && err != io.EOF {
		return "", err
	}
	if !looksLikeImage(head) {
		return "", fmt.Errorf("response body for %s is not an image", rawURL)
	}

	tmpPath := filepath.Join(c.dir, hashHex+".tmp")
	file, err := os.Create(tmpPath)
	if err != nil {
		return "", err
	}

	written, copyErr := io.Copy(file, body)
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
	for _, other := range allowedImageExtensions {
		if other != ext {
			_ = os.Remove(filepath.Join(c.dir, hashHex+other))
		}
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
		path += "?v=" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
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

// Enough to reach the root element of SVGs that open with a long comment or doctype.
const imageSniffBytes = 8 << 10

var (
	utf8BOM   = []byte("\xef\xbb\xbf")
	isoFtyp   = []byte("ftyp")
	avifBrand = []byte("avif")
	avisBrand = []byte("avis")
)

// Checks the bytes themselves, since a server can label any response as an image.
func looksLikeImage(head []byte) bool {
	if strings.HasPrefix(http.DetectContentType(head), "image/") {
		return true
	}
	if len(head) >= 12 && bytes.Equal(head[4:8], isoFtyp) {
		brands := head[8:min(len(head), 64)]
		return bytes.Contains(brands, avifBrand) || bytes.Contains(brands, avisBrand)
	}
	return startsWithSVGElement(head)
}

// Skips the XML prolog, comments and doctype so only documents whose root element is <svg> pass.
func startsWithSVGElement(b []byte) bool {
	b = bytes.TrimPrefix(b, utf8BOM)
	for {
		b = bytes.TrimLeft(b, " \t\r\n")
		end := "?>"
		switch {
		case bytes.HasPrefix(b, []byte("<?")):
		case bytes.HasPrefix(b, []byte("<!--")):
			end = "-->"
		case bytes.HasPrefix(b, []byte("<!")):
			end = ">"
			if i := bytes.IndexAny(b, "[>"); i >= 0 && b[i] == '[' {
				end = "]>"
			}
		default:
			return len(b) > 4 && bytes.EqualFold(b[:4], []byte("<svg")) && strings.IndexByte(" \t\r\n>/", b[4]) >= 0
		}
		i := bytes.Index(b, []byte(end))
		if i < 0 {
			return false
		}
		b = b[i+len(end):]
	}
}

// The URL path may only name the type of a body sent as some image or as untyped bytes, so text or HTML is never stored as an image.
func pathMayNameImageType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return contentType == "" || strings.HasPrefix(contentType, "image/") || strings.HasSuffix(contentType, "octet-stream")
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
