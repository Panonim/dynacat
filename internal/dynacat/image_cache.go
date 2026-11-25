package dynacat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	limits   imageCacheLimits
	// Images rendered only once, such as icons, are pinned per owner since evicting them would break them until a reload.
	pinned map[any]map[string]bool
}

type cacheEntry struct {
	done     chan struct{}
	filename string
	err      error
}

type imageCacheLimits struct {
	maxIdle         time.Duration
	maxBytes        int64
	cleanupInterval time.Duration
}

const (
	imageCacheJanitorTick = time.Minute
	imageCacheTempMaxAge  = time.Hour
)

// Shared across config reloads so a reload neither loses usage data nor restarts the sweep schedule.
var (
	imageCacheSharedMu  sync.Mutex
	imageCacheLastUsed  = make(map[string]time.Time)
	imageCacheLastSweep = time.Now()
)

var imageCacheLimitsFromEnv = sync.OnceValue(func() imageCacheLimits {
	return imageCacheLimits{
		maxIdle:         envOrDefault("IMAGE_CACHE_MAX_IDLE", 30*24*time.Hour, parsePositiveDuration),
		maxBytes:        envOrDefault("IMAGE_CACHE_MAX_SIZE", 1<<30, parseByteSize),
		cleanupInterval: envOrDefault("IMAGE_CACHE_CLEANUP_INTERVAL", time.Hour, parsePositiveDuration),
	}
})

func envOrDefault[T any](name string, fallback T, parse func(string) (T, error)) T {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}

	parsed, err := parse(value)
	if err != nil {
		slog.Warn("Invalid "+name+", using the default", "value", value, "error", err)
		return fallback
	}

	return parsed
}

func parsePositiveDuration(value string) (time.Duration, error) {
	duration, err := parseDurationString(value)
	if err == nil && duration <= 0 {
		err = fmt.Errorf("duration must be above zero: %s", value)
	}

	return duration, err
}

// Accepts 0 or a whole number of megabytes or gigabytes, such as 500MB or 2GB.
func parseByteSize(value string) (int64, error) {
	if value == "0" {
		return 0, nil
	}

	units := map[string]int64{"MB": 1 << 20, "GB": 1 << 30}
	if unit, ok := units[strings.ToUpper(value[max(len(value)-2, 0):])]; ok {
		if size, err := strconv.ParseUint(value[:len(value)-2], 10, 32); err == nil {
			return int64(size) * unit, nil
		}
	}

	return 0, fmt.Errorf("invalid size, expected a number ending in MB or GB: %s", value)
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
		limits:   imageCacheLimitsFromEnv(),
		pinned:   make(map[any]map[string]bool),
	}
}

func (c *imageCache) CacheURL(ctx context.Context, rawURL string) (string, error) {
	return c.CacheURLWithClient(ctx, rawURL, false, true, 0, c)
}

// A maxAge of 0 never refetches a cached file. Only URLs the admin configured, or got from their own services, should allowPrivate.
func (c *imageCache) CacheURLWithClient(ctx context.Context, rawURL string, allowInsecure bool, allowPrivate bool, maxAge time.Duration, pinOwner any) (string, error) {
	filename, err := c.cacheFile(ctx, rawURL, allowInsecure, allowPrivate, maxAge)
	if filename == "" {
		return "", err
	}

	if pinOwner != nil {
		c.mu.Lock()
		if c.pinned[pinOwner] == nil {
			c.pinned[pinOwner] = make(map[string]bool)
		}
		c.pinned[pinOwner][filename] = true
		c.mu.Unlock()
	}

	return c.publicURL(filename), err
}

// Drops the owner's pins so images it no longer renders can be evicted, unless another owner still pins them.
func (c *imageCache) unpin(owner any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.pinned, owner)
	c.mu.Unlock()
}

func (c *imageCache) cacheFile(ctx context.Context, rawURL string, allowInsecure bool, allowPrivate bool, maxAge time.Duration) (string, error) {
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
	if found {
		// Also keeps the janitor from evicting a stale file while its refresh runs.
		markImageUsed(existing)
		if maxAge <= 0 || !c.isStale(existing, maxAge) {
			return existing, nil
		}
	}

	c.mu.Lock()
	if entry, ok := c.inFlight[cacheKey]; ok {
		c.mu.Unlock()
		<-entry.done
		return entry.filename, entry.err
	}

	entry := &cacheEntry{done: make(chan struct{})}
	c.inFlight[cacheKey] = entry
	c.mu.Unlock()

	entry.filename, entry.err = c.downloadAndCacheWithClient(ctx, rawURL, hashHex, parsed.Path, allowInsecure, allowPrivate)
	if entry.err != nil && found && fileExists(filepath.Join(c.dir, existing)) {
		entry.filename, entry.err = existing, nil
	}

	c.mu.Lock()
	delete(c.inFlight, cacheKey)
	c.mu.Unlock()
	if entry.filename != "" {
		markImageUsed(entry.filename)
	}
	close(entry.done)

	return entry.filename, entry.err
}

func markImageUsed(filename string) {
	imageCacheSharedMu.Lock()
	imageCacheLastUsed[filename] = time.Now()
	imageCacheSharedMu.Unlock()
}

// Serves only files the cache wrote, so directories are never listed, and records each hit for eviction.
func (c *imageCache) fileHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		path := filepath.Join(c.dir, name)
		if ext := cacheFileExt(name); ext == "" || ext == ".tmp" || !strings.HasPrefix(path, filepath.Clean(c.dir)+string(os.PathSeparator)) || !fileExists(path) {
			http.NotFound(w, r)
			return
		}

		markImageUsed(name)
		next.ServeHTTP(w, r)
	})
}

// Returns the extension of a file the cache itself wrote, or "" for anything else that may share the directory.
func cacheFileExt(name string) string {
	if len(name) <= 64 || strings.Trim(name[:64], "0123456789abcdef") != "" {
		return ""
	}

	if ext := name[64:]; ext == ".tmp" || slices.Contains(allowedImageExtensions, ext) {
		return ext
	}

	return ""
}

// Ticks often because every config reload restarts the janitor, while the sweep itself follows the shared schedule.
func (c *imageCache) runJanitor(ctx context.Context) {
	ticker := time.NewTicker(imageCacheJanitorTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if imageCacheSweepDue(c.limits.cleanupInterval) {
				c.evict()
			}
		}
	}
}

func imageCacheSweepDue(interval time.Duration) bool {
	imageCacheSharedMu.Lock()
	defer imageCacheSharedMu.Unlock()

	if time.Since(imageCacheLastSweep) < interval {
		return false
	}

	imageCacheLastSweep = time.Now()
	return true
}

// Drops abandoned downloads and idle unpinned files, then the least recently used ones browsers no longer hold while the cache is over its size cap.
func (c *imageCache) evict() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		slog.Warn("Could not read image cache directory", "path", c.dir, "error", err)
		return
	}

	imageCacheSharedMu.Lock()
	lastUsed := maps.Clone(imageCacheLastUsed)
	imageCacheSharedMu.Unlock()

	pinned := make(map[string]bool)
	c.mu.Lock()
	for _, files := range c.pinned {
		maps.Copy(pinned, files)
	}
	c.mu.Unlock()

	type cachedFile struct {
		name    string
		size    int64
		lastUse time.Time
	}

	var candidates []cachedFile
	var removed int
	var total, freed int64
	remove := func(name string, size int64) bool {
		imageCacheSharedMu.Lock()
		defer imageCacheSharedMu.Unlock()

		// A use recorded since the snapshot means the file is wanted again, and holding the lock keeps a new one from slipping in before the removal.
		if !imageCacheLastUsed[name].Equal(lastUsed[name]) || os.Remove(filepath.Join(c.dir, name)) != nil {
			return false
		}
		delete(imageCacheLastUsed, name)
		removed++
		freed += size
		return true
	}

	for _, entry := range entries {
		ext := cacheFileExt(entry.Name())
		info, err := entry.Info()
		if ext == "" || err != nil || !info.Mode().IsRegular() {
			continue
		}

		file := cachedFile{name: entry.Name(), size: info.Size(), lastUse: info.ModTime()}
		if used := lastUsed[file.name]; used.After(file.lastUse) {
			file.lastUse = used
		}

		switch {
		case ext == ".tmp":
			if time.Since(info.ModTime()) > imageCacheTempMaxAge {
				remove(file.name, file.size)
			}
		case pinned[file.name]:
			total += file.size
		case time.Since(file.lastUse) > c.limits.maxIdle:
			remove(file.name, file.size)
		default:
			total += file.size
			candidates = append(candidates, file)
		}
	}

	if maxBytes := c.limits.maxBytes; maxBytes > 0 && total > maxBytes {
		slices.SortFunc(candidates, func(a, b cachedFile) int { return a.lastUse.Compare(b.lastUse) })
		for _, file := range candidates {
			if total <= maxBytes*9/10 || time.Since(file.lastUse) < REMOTE_IMAGE_CACHE_DURATION {
				break
			}
			if remove(file.name, file.size) {
				total -= file.size
			}
		}
	}

	if removed == 0 {
		return
	}

	slog.Info("Evicted files from image cache", "files", removed, "freed_bytes", freed, "remaining_bytes", total)
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

	client := ternary(allowInsecure, privateInsecureHTTPClient, privateHTTPClient)
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

	return filename, nil
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
