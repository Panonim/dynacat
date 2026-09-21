package dynacat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type tennisTestClient func(*http.Request) (*http.Response, error)

func (f tennisTestClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

func tennisTestResponse(status int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func tennisTestData(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/tennis.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTennisConfigAndSecret(t *testing.T) {
	var ws widgets
	if err := yaml.Unmarshal([]byte("- type: tennis\n  api-key: private-test-key\n  cache: 1s\n  update-interval: 1s\n"), &ws); err != nil {
		t.Fatal(err)
	}
	w := ws[0].(*tennisWidget)
	if err := w.initialize(); err != nil {
		t.Fatal(err)
	}
	if w.getCacheDuration() < tennisRequestInterval || w.UpdateIntervalMs() < tennisRequestInterval.Milliseconds() {
		t.Fatal("hand-edited intervals bypassed the request floor")
	}
	data, err := json.Marshal(newAPIWidgetView(w, true))
	if err != nil || strings.Contains(string(data), w.APIKey) || strings.Contains(string(w.Render()), w.APIKey) {
		t.Fatalf("API key exposed or invalid API view: %v", err)
	}
	w.APIKey = ""
	if w.initialize() == nil {
		t.Fatal("missing key accepted")
	}
}

func TestTennisSharedPersistentBudget(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	key := "private-test-key"
	fixture := tennisTestData(t)
	calls := 0
	client := tennisTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != tennisMatchesURL || r.Method != http.MethodGet || r.Header.Get("X-API-Key") != key {
			t.Errorf("wrong request endpoint, method or authentication")
		}
		return tennisTestResponse(200, fixture)
	})
	ctx := context.Background()
	for _, elapsed := range []time.Duration{0, time.Second, tennisRequestInterval - time.Nanosecond} {
		snapshot, err := fetchTennisSnapshot(ctx, key, dir, now.Add(elapsed), client)
		if err != nil || !snapshot.FetchedAt.IsZero() || calls != 0 {
			t.Fatalf("cold cache did not wait: %v, %d requests", err, calls)
		}
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := fetchTennisSnapshot(ctx, key, dir, now.Add(tennisRequestInterval), client)
			if err != nil || len(snapshot.Matches) != 4 || !snapshot.HasMore {
				t.Errorf("unexpected shared result: %+v, %v", snapshot, err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("20 concurrent widgets made %d requests", calls)
	}
	// A new widget instance reads the persisted snapshot; no in-memory cache is required.
	w := &tennisWidget{APIKey: key, Limit: 2}
	if err := w.initialize(); err != nil {
		t.Fatal(err)
	}
	w.Providers = &widgetProviders{app: &application{configPath: filepath.Join(dir, "dynacat.yml")}}
	cacheDir := filepath.Join(dir, ".tennis-cache")
	if err := os.Mkdir(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	filename := fmt.Sprintf("%x.json", sha256.Sum256([]byte(key)))
	if err := os.Rename(filepath.Join(dir, filename), filepath.Join(cacheDir, filename)); err != nil {
		t.Fatal(err)
	}
	w.update(ctx)
	if len(w.Matches) != 2 || !w.HasMore || w.FetchedAt == "" || w.Error != nil {
		t.Fatalf("widget did not restore snapshot: %+v", w)
	}
	html := string(w.Render())
	if !strings.Contains(html, "6–4 · 3–4 · points 15–30") || !strings.Contains(html, "&lt;Final&gt;") || strings.Contains(html, "<Final>") {
		t.Fatalf("incorrect or unescaped score rendering: %s", html)
	}
	if strings.Contains(html, key) {
		t.Fatal("key leaked into rendered output")
	}
	state, err := os.ReadFile(filepath.Join(cacheDir, filename))
	if err != nil || strings.Contains(string(state), key) {
		t.Fatal("key leaked into persisted state, or unreadable state")
	}
	info, err := os.Stat(filepath.Join(cacheDir, filename))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("snapshot must remain private")
	}
}

func TestTennisFailuresConsumeBudgetAndKeepSnapshot(t *testing.T) {
	fixture := tennisTestData(t)
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, "private-test-key"},
		{"forbidden", 403, "private-test-key"},
		{"limited", 429, "private-test-key"},
		{"server", 500, "private-test-key"},
		{"redirect", 302, "private-test-key"},
		{"malformed", 200, "{"},
		{"missing data", 200, `{}`},
		{"null data", 200, `{"data":null}`},
		{"oversized", 200, strings.Repeat(" ", (2<<20)+1)},
		{"network", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, now := t.TempDir(), time.Now().UTC()
			calls := 0
			client := tennisTestClient(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return tennisTestResponse(200, fixture)
				}
				if tc.status == 0 {
					return nil, errors.New("private-test-key")
				}
				return tennisTestResponse(tc.status, tc.body)
			})
			ctx := context.Background()
			if _, err := fetchTennisSnapshot(ctx, "key", dir, now, client); err != nil {
				t.Fatal(err)
			}
			good, err := fetchTennisSnapshot(ctx, "key", dir, now.Add(tennisRequestInterval), client)
			if err != nil {
				t.Fatal(err)
			}
			for _, elapsed := range []time.Duration{2 * tennisRequestInterval, 3*tennisRequestInterval - time.Nanosecond} {
				snapshot, err := fetchTennisSnapshot(ctx, "key", dir, now.Add(elapsed), client)
				if err == nil || strings.Contains(err.Error(), "private-test-key") || !snapshot.FetchedAt.Equal(good.FetchedAt) || len(snapshot.Matches) != 4 {
					t.Fatalf("failure lost snapshot or leaked response: %+v, %v", snapshot, err)
				}
			}
			if calls != 2 {
				t.Fatalf("failure retried early: %d calls", calls)
			}
		})
	}
}

func TestTennisCacheFailuresDoNotFetch(t *testing.T) {
	client := tennisTestClient(func(*http.Request) (*http.Response, error) {
		t.Error("request made without a valid persisted budget")
		return tennisTestResponse(200, `{"data":[]}`)
	})
	dir := t.TempDir()
	filename := filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte("key"))))
	for _, data := range []string{"{", "{}"} {
		if err := os.WriteFile(filename, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fetchTennisSnapshot(context.Background(), "key", dir, time.Now(), client); err == nil {
			t.Fatal("invalid cache accepted")
		}
	}
	// A file in place of the directory makes writes impossible, even as root.
	if _, err := fetchTennisSnapshot(context.Background(), "key", filename, time.Now(), client); err == nil {
		t.Fatal("unwritable cache accepted")
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchTennisSnapshot(context.Background(), "key", dir, time.Now(), client); err != nil {
		t.Fatal(err)
	}
}

func TestTennisScores(t *testing.T) {
	client := tennisTestClient(func(*http.Request) (*http.Response, error) {
		return tennisTestResponse(200, tennisTestData(t))
	})
	matches, _, err := requestTennisMatches(context.Background(), "key", client)
	if err != nil || len(matches) != 4 {
		t.Fatalf("unexpected response: %v", err)
	}
	for i, expected := range []string{"6–4 · 3–4 · points 15–30", "6–4 · 4–6 · [10–5]", "Score unavailable", "Score unavailable"} {
		if matches[i].ScoreText() != expected {
			t.Errorf("match %d: got %q, want %q", i, matches[i].ScoreText(), expected)
		}
	}
	if matches[0].Serving() != "B. Two" || matches[2].Serving() != "" {
		t.Fatal("incorrect serving player")
	}
	for _, body := range []string{`{"data":[]}`, `{"data":[{"status":"completed"}]}`} {
		client = tennisTestClient(func(*http.Request) (*http.Response, error) { return tennisTestResponse(200, body) })
		matches, _, err = requestTennisMatches(context.Background(), "key", client)
		if err != nil || len(matches) != 0 {
			t.Fatal("empty live list was not preserved")
		}
	}
	for _, tc := range []struct{ score, expected string }{
		{`{"games":[[6],[6]],"points":["4","2"],"is_tiebreak":true}`, "6–6 · tiebreak 4–2"},
		{`{"games":[[0],[0]],"points":[null,null]}`, "0–0"},
		{`{"games":[[6],[4,2]],"points":["0","0"]}`, "Score unavailable"},
	} {
		var match tennisMatch
		if err := json.Unmarshal([]byte(`{"score":`+tc.score+`}`), &match); err != nil {
			t.Fatal(err)
		}
		if match.ScoreText() != tc.expected {
			t.Errorf("got %q, want %q", match.ScoreText(), tc.expected)
		}
	}
}

func TestTennisDailyBudget(t *testing.T) {
	dir, now := t.TempDir(), time.Now().UTC()
	calls := 0
	client := tennisTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		return tennisTestResponse(200, `{"data":[]}`)
	})
	// Even direct refresh calls every minute cannot bypass the persisted floor.
	for minute := 0; minute <= 24*60; minute++ {
		if _, err := fetchTennisSnapshot(context.Background(), "key", dir, now.Add(time.Duration(minute)*time.Minute), client); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 96 {
		t.Fatalf("made %d requests in 24 hours, want 96", calls)
	}
}

func TestTennisClientDoesNotFollowRedirect(t *testing.T) {
	followed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/destination", http.StatusFound)
		} else {
			followed = true
		}
	}))
	defer server.Close()
	response, err := tennisHTTPClient.Get(server.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if followed || response.StatusCode != http.StatusFound {
		t.Fatal("redirect followed")
	}
}
