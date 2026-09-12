package dynacat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type sseClient struct {
	ch   chan string
	done <-chan struct{}
	user *authenticatedUser
}

// Dropped once full, widgets re-register their images on the next update.
const imageProxyMaxEntries = 10000

func (a *application) registerImageProxy(hash string, url string, allowInsecure bool) {
	a.imageProxyMu.Lock()
	defer a.imageProxyMu.Unlock()

	if len(a.imageProxyURLs) >= imageProxyMaxEntries {
		clear(a.imageProxyURLs)
	}

	a.imageProxyURLs[hash] = imageProxyInfo{URL: url, AllowInsecure: allowInsecure}
}

func (a *application) getImageProxyInfo(hash string) (imageProxyInfo, bool) {
	a.imageProxyMu.RLock()
	defer a.imageProxyMu.RUnlock()
	info, ok := a.imageProxyURLs[hash]
	return info, ok
}

func validatePublicFetchURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme %q not allowed", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolving host: %w", err)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("host resolves to disallowed address %s", ip)
		}
	}
	return nil
}

func isDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return true
	}
	return false
}

func (a *application) handleImageProxyRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	hash := r.PathValue("hash")
	if hash == "" {
		http.Error(w, "Missing hash parameter", http.StatusBadRequest)
		return
	}

	info, exists := a.getImageProxyInfo(hash)
	if !exists {
		http.NotFound(w, r)
		return
	}

	if err := validatePublicFetchURL(info.URL); err != nil {
		http.Error(w, "Forbidden URL", http.StatusForbidden)
		return
	}

	client := ternary(info.AllowInsecure, publicOnlyInsecureHTTPClient, publicOnlyHTTPClient)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, info.URL, nil)
	if err != nil {
		http.Error(w, "Failed to fetch image", http.StatusInternalServerError)
		return
	}

	req.Header.Set("Accept", "image/*")
	setBrowserUserAgentHeader(req)

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to fetch image", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Image not found", http.StatusNotFound)
		return
	}

	// The proxied URL comes from remote widget content, so reflecting its content type would
	// let an upstream serve HTML from this origin.
	contentType := resp.Header.Get("Content-Type")
	if extensionFromContentType(contentType) == "" {
		http.Error(w, "Not an image", http.StatusUnsupportedMediaType)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
	w.WriteHeader(http.StatusOK)

	io.Copy(w, io.LimitReader(resp.Body, maxResponseBytes))
}

func (a *application) respondWithOpenSearchSuggestions(w http.ResponseWriter, r *http.Request, requestURL string, client *http.Client) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, requestURL, nil)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	setBrowserUserAgentHeader(req)

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to fetch suggestions", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var raw []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil || len(raw) < 2 {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
		return
	}

	var suggestions []string
	if err := json.Unmarshal(raw[1], &suggestions); err != nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
		return
	}

	type phrase struct {
		Phrase string `json:"phrase"`
	}
	result := make([]phrase, len(suggestions))
	for i, s := range suggestions {
		result[i] = phrase{Phrase: s}
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}

func (a *application) handleSearchAutocompleteRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	provider := r.URL.Query().Get("provider")

	w.Header().Set("Content-Type", "application/json")

	if provider == "brave" {
		braveURL := "https://search.brave.com/api/suggest?" + url.Values{"q": {query}, "rich": {"false"}}.Encode()
		a.respondWithOpenSearchSuggestions(w, r, braveURL, publicOnlyHTTPClient)
		return
	}

	if provider == "custom" {
		widgetID, err := strconv.ParseUint(r.URL.Query().Get("widgetId"), 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
			return
		}

		source, ok := a.searchAutocompleteURLs[widgetID]
		if !ok {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
			return
		}

		customURL := strings.ReplaceAll(source.URL, "{QUERY}", url.QueryEscape(query))
		// Self-hosted instances named in the config are allowed to sit on a private
		// address, unlike URLs that could otherwise be probed through this endpoint.
		client := defaultHTTPClient
		if !source.AllowPrivate {
			if err := validatePublicFetchURL(customURL); err != nil {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("[]"))
				return
			}
			client = publicOnlyHTTPClient
		}

		a.respondWithOpenSearchSuggestions(w, r, customURL, client)
		return
	}

	ddgURL := "https://duckduckgo.com/ac/?" + url.Values{"q": {query}}.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, ddgURL, nil)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	setBrowserUserAgentHeader(req)

	resp, err := publicOnlyHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "Failed to fetch suggestions", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(http.StatusOK)
	io.Copy(w, io.LimitReader(resp.Body, maxResponseBytes))
}

func (a *application) handleSSEUpdates(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	user := a.getAuthenticatedUser(w, r)

	if !a.DynamicUpdateEnabled {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	client := &sseClient{
		ch:   make(chan string, 16),
		done: r.Context().Done(),
		user: user,
	}
	a.sseRegisterClient(client)
	defer a.sseUnregisterClient(client)

	authRecheck := time.NewTicker(15 * time.Second)
	defer authRecheck.Stop()

	for {
		select {
		case msg := <-client.ch:
			fmt.Fprintf(w, "event: widget-update\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-authRecheck.C:
			if a.RequiresAuth && a.getAuthenticatedUser(w, r) == nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (a *application) sseUpdateLoop(ctx context.Context) {
	if !a.DynamicUpdateEnabled {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sseCheckAndPushUpdates(ctx)
		}
	}
}

func (a *application) sseBroadcastWidgetUpdate(pg *page, msg string) {
	a.sseMu.RLock()
	defer a.sseMu.RUnlock()

	for c := range a.sseClients {
		if !a.canUserAccessPage(c.user, pg) {
			continue
		}

		select {
		case c.ch <- msg:
		default:
		}
	}
}

func (a *application) sseCheckAndPushUpdates(ctx context.Context) {
	a.sseMu.RLock()
	clientCount := len(a.sseClients)
	a.sseMu.RUnlock()
	if clientCount == 0 {
		return
	}

	now := time.Now()

	var wg sync.WaitGroup
	for widgetID, w := range a.widgetByID {
		if !w.requiresUpdate(&now) {
			continue
		}

		pg, exists := a.widgetToPage[widgetID]
		if !exists {
			continue
		}

		if !pg.DynamicUpdatesEnabled() {
			continue
		}

		wg.Add(1)
		go func(w widget, pg *page) {
			defer wg.Done()

			pg.mu.Lock()
			defer pg.mu.Unlock()

			recheckNow := time.Now()
			if !w.requiresUpdate(&recheckNow) {
				return
			}

			w.update(withSharedFetchMaxAge(ctx, w.getCacheDuration()))
			html := string(w.Render())

			type payload struct {
				WidgetID uint64 `json:"widgetId"`
				HTML     string `json:"html"`
			}
			msg, err := json.Marshal(payload{WidgetID: w.GetID(), HTML: html})
			if err != nil {
				return
			}

			a.sseBroadcastWidgetUpdate(pg, string(msg))
		}(w, pg)
	}
	wg.Wait()
}
