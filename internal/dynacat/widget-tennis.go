package dynacat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 24h / 15m = 96 attempts, within the free tier's 100 requests/day. This
// budget is shared by all tennis widgets using the same key in this installation.
const tennisRequestInterval = 15 * time.Minute
const tennisMatchesURL = "https://api.livetennisapi.com/api/public/v1/matches?status=live&limit=200"

var tennisWidgetTemplate = mustParseTemplate("tennis.html", "widget-base.html")
var tennisCacheMutex sync.Mutex
var tennisHTTPClient = &http.Client{
	Transport: defaultHTTPClient.Transport,
	Timeout:   defaultClientTimeout,
	// A redirect must neither spend another request nor forward the API key.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type tennisWidget struct {
	widgetBase `yaml:",inline"`
	APIKey     string        `yaml:"api-key" json:"-"`
	Limit      int           `yaml:"limit"`
	Matches    []tennisMatch `yaml:"-"`
	FetchedAt  string        `yaml:"-"`
	HasMore    bool          `yaml:"-"`
	Message    string        `yaml:"-"`
}

type tennisMatch struct {
	Tournament string `json:"tournament"`
	Status     string `json:"status"`
	Players    struct {
		P1 struct{ Name string } `json:"p1"`
		P2 struct{ Name string } `json:"p2"`
	} `json:"players"`
	Score *struct {
		Games      [][]int   `json:"games"`
		Points     []*string `json:"points"`
		Server     *int      `json:"server"`
		IsTiebreak bool      `json:"is_tiebreak"`
		Stale      bool      `json:"stale"`
	} `json:"score"`
}

type tennisSnapshot struct {
	AttemptedAt time.Time     `json:"attempted_at"`
	FetchedAt   time.Time     `json:"fetched_at"`
	Matches     []tennisMatch `json:"matches"`
	HasMore     bool          `json:"has_more"`
	Error       string        `json:"error,omitempty"`
}

func (w *tennisWidget) initialize() error {
	w.APIKey = strings.TrimSpace(w.APIKey)
	if w.APIKey == "" || strings.ContainsAny(w.APIKey, "\r\n") {
		return errors.New("tennis requires a valid api-key")
	}
	if time.Duration(w.CustomCacheDuration) < tennisRequestInterval {
		w.CustomCacheDuration = durationField(tennisRequestInterval)
	}
	if w.UpdateInterval == nil || time.Duration(*w.UpdateInterval) < tennisRequestInterval {
		interval := updateIntervalField(tennisRequestInterval)
		w.UpdateInterval = &interval
	}
	if w.Limit <= 0 || w.Limit > 200 {
		w.Limit = 5
	}
	w.withTitle("Tennis snapshots").withCacheDuration(tennisRequestInterval)
	return nil
}

func (w *tennisWidget) update(ctx context.Context) {
	if w.Providers == nil || w.Providers.app == nil || w.Providers.app.configPath == "" {
		w.withError(errors.New("tennis cache requires a config path"))
		return
	}
	// Keep the state beside the config, outside the publicly served image cache.
	dir := filepath.Join(filepath.Dir(w.Providers.app.configPath), ".tennis-cache")
	snapshot, err := fetchTennisSnapshot(ctx, w.APIKey, dir, time.Now(), tennisHTTPClient)
	w.scheduleNextUpdate()
	w.Message = ""
	if !snapshot.FetchedAt.IsZero() {
		w.FetchedAt = snapshot.FetchedAt.UTC().Format("2006-01-02 15:04 UTC")
		w.Matches = snapshot.Matches[:min(w.Limit, len(snapshot.Matches))]
		w.HasMore = snapshot.HasMore || len(snapshot.Matches) > w.Limit
		w.ContentAvailable = true
	}
	if err != nil {
		w.withError(err)
		w.Message = err.Error()
		return
	}
	w.withError(nil)
	if snapshot.FetchedAt.IsZero() {
		w.Message = "Waiting for the first snapshot. A new cache waits at least 15 minutes before fetching."
	}
}

func (w *tennisWidget) Render() template.HTML {
	return w.renderTemplate(w, tennisWidgetTemplate)
}

func (m tennisMatch) ScoreText() string {
	s := m.Score
	if s == nil || len(s.Games) != 2 || len(s.Games[0]) == 0 || len(s.Games[0]) != len(s.Games[1]) {
		return "Score unavailable"
	}
	parts := make([]string, 0, len(s.Games[0])+1)
	pointsKnown := len(s.Points) == 2 && s.Points[0] != nil && s.Points[1] != nil
	for i, p1 := range s.Games[0] {
		p2 := s.Games[1][i]
		pair := fmt.Sprintf("%d–%d", p1, p2)
		// During a match tiebreak, the last games slot can repeat the points.
		// Render that score once, as a tiebreak, rather than as a ten-game set.
		if i == len(s.Games[0])-1 && s.IsTiebreak && pointsKnown &&
			strconv.Itoa(p1) == *s.Points[0] && strconv.Itoa(p2) == *s.Points[1] {
			pair = "[" + pair + "]"
			pointsKnown = false
		}
		parts = append(parts, pair)
	}
	if pointsKnown {
		label := "points "
		if s.IsTiebreak {
			label = "tiebreak "
		}
		parts = append(parts, label+*s.Points[0]+"–"+*s.Points[1])
	}
	return strings.Join(parts, " · ")
}

func (m tennisMatch) Serving() string {
	if m.Score != nil && m.Score.Server != nil {
		switch *m.Score.Server {
		case 1:
			return m.Players.P1.Name
		case 2:
			return m.Players.P2.Name
		}
	}
	return ""
}

func fetchTennisSnapshot(ctx context.Context, key, dir string, now time.Time, client requestDoer) (tennisSnapshot, error) {
	// Also covers configuration reloads and simultaneous widgets. State on disk
	// preserves both successful snapshots and failed attempts across restarts.
	tennisCacheMutex.Lock()
	defer tennisCacheMutex.Unlock()
	var snapshot tennisSnapshot
	path := filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(key))))
	contents, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return snapshot, errors.New("could not read tennis cache; no request sent")
	}
	if os.IsNotExist(err) {
		// Losing/recreating a cache must not allow repeated immediate requests.
		snapshot.AttemptedAt = now
		return snapshot, saveTennisSnapshot(path, snapshot)
	}
	if json.Unmarshal(contents, &snapshot) != nil || snapshot.AttemptedAt.IsZero() {
		return tennisSnapshot{}, errors.New("invalid tennis cache; no request sent")
	}
	if now.Before(snapshot.AttemptedAt.Add(tennisRequestInterval)) {
		if snapshot.Error != "" {
			return snapshot, errors.New(snapshot.Error)
		}
		return snapshot, nil
	}
	snapshot.AttemptedAt = now
	snapshot.Error = "Snapshot refresh did not complete; retrying after 15 minutes."
	if err := saveTennisSnapshot(path, snapshot); err != nil {
		return snapshot, err
	}

	data, more, err := requestTennisMatches(ctx, key, client)
	if err != nil {
		snapshot.Error = err.Error()
	} else {
		snapshot.Matches, snapshot.HasMore = data, more
		snapshot.FetchedAt, snapshot.Error = now, ""
	}
	if saveErr := saveTennisSnapshot(path, snapshot); saveErr != nil {
		return snapshot, saveErr
	}
	return snapshot, err
}

func saveTennisSnapshot(path string, snapshot tennisSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("could not create tennis cache; no further requests will be sent")
	}
	contents, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return errors.New("could not write tennis cache; no further requests will be sent")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(contents)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || os.Rename(f.Name(), path) != nil {
		return errors.New("could not save tennis cache; no further requests will be sent")
	}
	return nil
}

func requestTennisMatches(ctx context.Context, key string, client requestDoer) ([]tennisMatch, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tennisMatchesURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, false, errors.New("tennis request failed; retrying after 15 minutes")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("tennis API returned HTTP %d; retrying after 15 minutes", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(contents) > 2<<20 {
		return nil, false, errors.New("could not read tennis response; retrying after 15 minutes")
	}
	var payload struct {
		Data *[]tennisMatch `json:"data"`
		Meta struct {
			HasMore bool `json:"has_more"`
		} `json:"meta"`
	}
	if json.Unmarshal(contents, &payload) != nil || payload.Data == nil {
		return nil, false, errors.New("invalid tennis response; retrying after 15 minutes")
	}
	matches := make([]tennisMatch, 0, len(*payload.Data))
	for _, match := range *payload.Data {
		if match.Status == "live" {
			matches = append(matches, match)
		}
	}
	return matches, payload.Meta.HasMore, nil
}
