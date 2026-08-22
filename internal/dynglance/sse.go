package dynglance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type sseClient struct {
	ch   chan string
	done <-chan struct{}
	user *authenticatedUser
}

func (a *application) registerImageProxy(hash string, url string, allowInsecure bool) {
	a.imageProxyMu.Lock()
	defer a.imageProxyMu.Unlock()
	a.imageProxyURLs[hash] = imageProxyInfo{URL: url, AllowInsecure: allowInsecure}
}

func (a *application) getImageProxyInfo(hash string) (imageProxyInfo, bool) {
	a.imageProxyMu.RLock()
	defer a.imageProxyMu.RUnlock()
	info, ok := a.imageProxyURLs[hash]
	return info, ok
}

func validateImageProxyURL(rawURL string) error {
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
	// Cloud metadata endpoints (169.254.x covered by link-local; include IMDSv2 fd00:ec2::254)
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

	if err := validateImageProxyURL(info.URL); err != nil {
		http.Error(w, "Forbidden URL", http.StatusForbidden)
		return
	}

	// Fetch the image using the stored URL with the appropriate client
	client := ternary(info.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
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

	// Set appropriate headers for the response
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "public, max-age=2592000, immutable") // 30 days
	w.WriteHeader(http.StatusOK)

	// Stream the image to the client
	if _, err := io.Copy(w, resp.Body); err != nil {
		// Error writing response, client may have disconnected
		return
	}
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
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, braveURL, nil)
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}
		setBrowserUserAgentHeader(req)

		resp, err := defaultHTTPClient.Do(req)
		if err != nil {
			http.Error(w, "Failed to fetch suggestions", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Brave returns OpenSearch format: ["query", ["s1", "s2", ...]]
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
		return
	}

	ddgURL := "https://duckduckgo.com/ac/?" + url.Values{"q": {query}}.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, ddgURL, nil)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	setBrowserUserAgentHeader(req)

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "Failed to fetch suggestions", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
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

	authRecheck := time.NewTicker(60 * time.Second)
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

			// Bound the update so a stalled widget can never hold pg.mu (and thus
			// every page request and SSE tick for it) indefinitely.
			updateCtx, cancel := context.WithTimeout(ctx, widgetUpdateBatchTimeout)
			defer cancel()

			w.update(withSharedFetchMaxAge(updateCtx, w.getCacheDuration()))
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
