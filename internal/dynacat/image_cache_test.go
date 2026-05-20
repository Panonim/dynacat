package dynacat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Uses a Jellyfin-style URL: /Items/movie-123/Images/Primary?api_key=jellyfin-token.
// Verifies:
// - SecureImageURL immediately returns /api/image-proxy/{hash}.
// - Proxy URL does not expose the API key.
// - Proxy registry keeps the real upstream URL.
// - Background cache fetches the real upstream URL with the API key.
// - Cached URL is /.cache/*.png and does not expose the API key.
func TestSecureImageURLReturnsProxyThenBackgroundCachesMediaImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items/movie-123/Images/Primary" {
			t.Fatalf("Expected media image path, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("api_key") != "jellyfin-token" {
			t.Fatalf("Expected API key query to be preserved for upstream fetch, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("poster image"))
	}))
	defer server.Close()

	cache := newImageCache("", t.TempDir())
	app := &application{
		ctx:            context.Background(),
		imageCache:     cache,
		imageProxyURLs: make(map[string]imageProxyInfo),
	}
	providers := &widgetProviders{imageCache: cache, app: app}
	imageURL := server.URL + "/Items/movie-123/Images/Primary?api_key=jellyfin-token"

	proxiedURL := providers.SecureImageURL(context.Background(), imageURL, false)
	if !strings.HasPrefix(proxiedURL, "/api/image-proxy/") {
		t.Fatalf("Expected image proxy URL, got %q", proxiedURL)
	}
	if strings.Contains(proxiedURL, "jellyfin-token") {
		t.Fatalf("Expected proxy URL not to expose API key, got %q", proxiedURL)
	}
	proxyInfo, ok := app.getImageProxyInfo(strings.TrimPrefix(proxiedURL, "/api/image-proxy/"))
	if !ok {
		t.Fatal("Expected proxy info to be registered")
	}
	if proxyInfo.URL != imageURL {
		t.Fatalf("Expected proxy to retain upstream image URL, got %q", proxyInfo.URL)
	}

	cachedURL, err := cache.CachedURLOrWait(context.Background(), imageURL, false)
	if err != nil {
		t.Fatalf("Expected background cache to finish, got %v", err)
	}
	if !strings.HasPrefix(cachedURL, "/.cache/") || !strings.HasSuffix(cachedURL, ".png") {
		t.Fatalf("Expected cached PNG URL, got %q", cachedURL)
	}
	if strings.Contains(cachedURL, "jellyfin-token") {
		t.Fatalf("Expected cached URL not to expose API key, got %q", cachedURL)
	}
}

// Uses a blocked httptest media server and fills all background image download slots.
// Verifies:
// - The over-limit image does not create new in-flight background work.
// - The test then releases and waits for the queued downloads cleanly.
func TestCachedURLOrQueueDropsNewBackgroundWorkWhenMediaServerIsSaturated(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("poster image"))
	}))
	defer server.Close()

	cache := newImageCache("", t.TempDir())
	queuedURLs := make([]string, 0, maxBackgroundImageDownloads)
	for i := range maxBackgroundImageDownloads {
		imageURL := fmt.Sprintf("%s/Items/movie-%d/Images/Primary?api_key=token-%d", server.URL, i, i)
		_, err := cache.CachedURLOrQueue(context.Background(), imageURL, false)
		if err != nil {
			t.Fatalf("Expected no queue error for image %d, got %v", i, err)
		}
		queuedURLs = append(queuedURLs, imageURL)
	}

	overLimitURL := server.URL + "/Items/movie-over-limit/Images/Primary?api_key=token-over-limit"
	cachedURL, err := cache.CachedURLOrQueue(context.Background(), overLimitURL, false)
	if err != nil {
		t.Fatalf("Expected no queue error when background limit is full, got %v", err)
	}
	if cachedURL != "" {
		t.Fatalf("Expected no cached URL when background limit is full, got %q", cachedURL)
	}

	overLimitKey := imageCacheKey(overLimitURL, false)

	cache.mu.Lock()
	_, overLimitInFlight := cache.inFlight[overLimitKey]
	cache.mu.Unlock()
	if overLimitInFlight {
		t.Fatal("Expected over-limit image not to create in-flight background work")
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, queuedURL := range queuedURLs {
		if _, err := cache.CachedURLOrWait(ctx, queuedURL, false); err != nil {
			t.Fatalf("Expected queued image to finish after releasing media server, got %v", err)
		}
	}
}

func TestImageProxyStreamsInFlightBackgroundCacheWithoutSecondUpstreamFetch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			close(started)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("poster "))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
		_, _ = w.Write([]byte("image"))
	}))
	defer server.Close()

	cache := newImageCache("", t.TempDir())
	imageURL := server.URL + "/Items/movie-123/Images/Primary?api_key=jellyfin-token"
	if _, err := cache.CachedURLOrQueue(context.Background(), imageURL, false); err != nil {
		t.Fatalf("Expected background cache to queue, got %v", err)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Expected background cache request to start")
	}

	proxyDone := make(chan []byte)
	go func() {
		recorder := httptest.NewRecorder()
		cachedURL, streamed, err := cache.ServeCachedOrInFlight(context.Background(), imageURL, false, recorder)
		if err != nil {
			t.Errorf("Expected in-flight stream, got error %v", err)
		}
		if cachedURL != "" || !streamed {
			t.Errorf("Expected in-flight stream, got cachedURL=%q streamed=%t", cachedURL, streamed)
		}
		proxyDone <- recorder.Body.Bytes()
	}()

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("Expected one upstream request while proxy streams in-flight cache, got %d", got)
	}
	close(release)

	select {
	case body := <-proxyDone:
		if string(body) != "poster image" {
			t.Fatalf("Expected proxy to stream full image, got %q", string(body))
		}
	case <-time.After(time.Second):
		t.Fatal("Expected proxy stream to finish")
	}

	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("Expected cache and proxy to share one upstream request, got %d", got)
	}
	cachedURL, err := cache.CachedURLOrWait(context.Background(), imageURL, false)
	if err != nil {
		t.Fatalf("Expected cache to finish, got %v", err)
	}
	if !strings.HasPrefix(cachedURL, "/.cache/") {
		t.Fatalf("Expected final cached URL, got %q", cachedURL)
	}

	cacheFile := strings.TrimPrefix(cachedURL, "/.cache/")
	contents, err := os.ReadFile(filepath.Join(cache.dir, cacheFile))
	if err != nil {
		t.Fatalf("Expected cached file to exist, got %v", err)
	}
	if string(contents) != "poster image" {
		t.Fatalf("Expected cached file to contain full image, got %q", string(contents))
	}
}
