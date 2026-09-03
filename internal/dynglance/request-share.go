package dynglance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Shared HTTP request layer: collapses concurrent identical GET requests
// across widgets into a single fetch with a per-caller cache. See
// docs/design.md ("Shared HTTP request layer") for the full rationale.

const (
	// defaultSharedFetchMaxAge is used when a caller does not supply a maxAge
	// (no widget cacheDuration in the request context).
	defaultSharedFetchMaxAge = 1 * time.Minute

	// sharedFetchMaxPruneAge is the age past which a cached entry is dropped
	// during a sweep. Longer than any realistic widget reuse window.
	sharedFetchMaxPruneAge = 1 * time.Hour

	// sharedFetchPruneThreshold triggers a lazy sweep once the map grows past it.
	sharedFetchPruneThreshold = 256

	// sharedFetchInfiniteMaxAge is handed to infinite cache widgets so they
	// reuse any entry that has not been pruned yet.
	sharedFetchInfiniteMaxAge = sharedFetchMaxPruneAge
)

type sharedFetchEntry struct {
	done      chan struct{}
	status    int
	header    http.Header
	body      []byte
	fetchedAt time.Time
	err       error
}

type sharedFetcher struct {
	mu      sync.Mutex
	entries map[string]*sharedFetchEntry
}

var globalSharedFetcher = &sharedFetcher{entries: make(map[string]*sharedFetchEntry)}

// sharedFetchRelevantHeaders are folded into the cache key so requests that
// differ in auth, content negotiation or conditional state never share.
var sharedFetchRelevantHeaders = []string{
	"Authorization",
	"Accept",
	"Content-Type",
	"If-None-Match",
	"If-Modified-Since",
}

func sharedFetchKey(client requestDoer, req *http.Request) string {
	var b strings.Builder

	b.WriteString(req.Method)
	b.WriteByte(0)
	b.WriteString(req.URL.String())
	b.WriteByte(0)
	// Client discriminator: requests issued through different clients (secure,
	// insecure TLS, reddit uTLS, per widget proxy) must never share a response.
	b.WriteString(fmt.Sprintf("%p", client))

	for _, h := range sharedFetchRelevantHeaders {
		b.WriteByte(0)
		b.WriteString(req.Header.Get(h))
	}

	return hashString(b.String())
}

// do returns a shared response for a GET request. Concurrent identical requests
// share a single in flight fetch; a completed entry is reused while it is no
// older than maxAge. The returned body is shared read only across callers and
// must not be mutated (callers only unmarshal it).
func (f *sharedFetcher) do(client requestDoer, req *http.Request, maxAge time.Duration) (int, http.Header, []byte, error) {
	key := sharedFetchKey(client, req)

	for {
		f.mu.Lock()

		if entry, ok := f.entries[key]; ok {
			select {
			case <-entry.done:
				// Completed. Reuse if fresh and successful.
				if entry.err == nil && time.Since(entry.fetchedAt) <= maxAge {
					f.mu.Unlock()
					return entry.status, entry.header, entry.body, nil
				}
				// Stale or errored. If another caller already replaced it, loop
				// and re evaluate; otherwise claim the refetch ourselves.
				if f.entries[key] != entry {
					f.mu.Unlock()
					continue
				}
				delete(f.entries, key)
			default:
				// In flight. Wait for it, then re evaluate from the top.
				f.mu.Unlock()
				<-entry.done
				continue
			}
		}

		newEntry := &sharedFetchEntry{done: make(chan struct{})}
		f.entries[key] = newEntry
		f.pruneLocked()
		f.mu.Unlock()

		status, header, body, err := doRequestReadAll(client, req)

		f.mu.Lock()
		if err != nil {
			// Do not cache transport errors; let the next caller retry.
			if f.entries[key] == newEntry {
				delete(f.entries, key)
			}
			f.mu.Unlock()
			newEntry.err = err
			close(newEntry.done)
			return 0, nil, nil, err
		}

		newEntry.status = status
		newEntry.header = header
		newEntry.body = body
		newEntry.fetchedAt = time.Now()
		f.mu.Unlock()
		close(newEntry.done)

		return status, header, body, nil
	}
}

// pruneLocked drops entries older than sharedFetchMaxPruneAge once the map has
// grown past the threshold. Caller must hold f.mu.
func (f *sharedFetcher) pruneLocked() {
	if len(f.entries) <= sharedFetchPruneThreshold {
		return
	}

	now := time.Now()
	for key, entry := range f.entries {
		select {
		case <-entry.done:
			if now.Sub(entry.fetchedAt) > sharedFetchMaxPruneAge {
				delete(f.entries, key)
			}
		default:
			// Still in flight; keep it.
		}
	}
}

// doRequestReadAll issues a request and reads the full response body. The
// returned header is a clone so concurrent readers never race the http stack.
func doRequestReadAll(client requestDoer, req *http.Request) (int, http.Header, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}

	return resp.StatusCode, resp.Header.Clone(), body, nil
}

type sharedFetchMaxAgeKey struct{}

func withSharedFetchMaxAge(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, sharedFetchMaxAgeKey{}, d)
}

func sharedFetchMaxAgeFromContext(ctx context.Context) (time.Duration, bool) {
	d, ok := ctx.Value(sharedFetchMaxAgeKey{}).(time.Duration)
	return d, ok
}

// sharedFetchMaxAgeForRequest derives the reuse window for a request from its
// context, falling back to the default when no widget cacheDuration is present.
func sharedFetchMaxAgeForRequest(req *http.Request) time.Duration {
	d, ok := sharedFetchMaxAgeFromContext(req.Context())
	if !ok {
		return defaultSharedFetchMaxAge
	}

	if d < 0 {
		// Infinite cache widget: reuse any entry that has not been pruned.
		return sharedFetchInfiniteMaxAge
	}
	if d == 0 {
		return defaultSharedFetchMaxAge
	}

	return d
}
