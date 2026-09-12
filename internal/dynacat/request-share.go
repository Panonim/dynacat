package dynacat

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	defaultSharedFetchMaxAge = 1 * time.Minute

	sharedFetchMaxPruneAge = 1 * time.Hour

	sharedFetchPruneThreshold = 256

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

var sharedFetchIgnoredHeaders = []string{"User-Agent"}

func sharedFetchKey(client requestDoer, req *http.Request) string {
	var b strings.Builder

	b.WriteString(req.Method)
	b.WriteByte(0)
	b.WriteString(req.URL.String())
	b.WriteByte(0)
	b.WriteString(fmt.Sprintf("%p", client))

	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		if !slices.Contains(sharedFetchIgnoredHeaders, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	for _, name := range names {
		b.WriteByte(0)
		b.WriteString(name)
		b.WriteByte(0)
		b.WriteString(strings.Join(req.Header.Values(name), ","))
	}

	return hashString(b.String())
}

func (f *sharedFetcher) do(client requestDoer, req *http.Request, maxAge time.Duration) (int, http.Header, []byte, error) {
	key := sharedFetchKey(client, req)

	for {
		f.mu.Lock()

		if entry, ok := f.entries[key]; ok {
			select {
			case <-entry.done:
				if entry.err == nil && time.Since(entry.fetchedAt) <= maxAge {
					f.mu.Unlock()
					return entry.status, entry.header, entry.body, nil
				}
				if f.entries[key] != entry {
					f.mu.Unlock()
					continue
				}
				delete(f.entries, key)
			default:
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
		}
	}
}

func doRequestReadAll(client requestDoer, req *http.Request) (int, http.Header, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	body, err := readLimited(resp.Body)
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

func sharedFetchMaxAgeForRequest(req *http.Request) time.Duration {
	d, ok := sharedFetchMaxAgeFromContext(req.Context())
	if !ok {
		return defaultSharedFetchMaxAge
	}

	if d < 0 {
		return sharedFetchInfiniteMaxAge
	}
	if d == 0 {
		return defaultSharedFetchMaxAge
	}

	return d
}
