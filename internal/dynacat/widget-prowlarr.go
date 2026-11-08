package dynacat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"
)

var prowlarrWidgetTemplate = mustParseTemplate("prowlarr.html", "widget-base.html")

const (
	prowlarrStatusFailing = iota
	prowlarrStatusHealthy
	prowlarrStatusDisabled
)

type prowlarrWidget struct {
	widgetBase    `yaml:",inline"`
	Frameless     bool              `yaml:"frameless"`
	URL           string            `yaml:"url"`
	Token         string            `yaml:"token"`
	AllowInsecure bool              `yaml:"allow-insecure"`
	Show          string            `yaml:"show"`
	CollapseAfter int               `yaml:"collapse-after"`
	Indexers      []prowlarrIndexer `yaml:"-"`
}

type prowlarrIndexer struct {
	Name         string
	Status       int
	FailingSince time.Time
}

func (i prowlarrIndexer) IsFailing() bool  { return i.Status == prowlarrStatusFailing }
func (i prowlarrIndexer) IsDisabled() bool { return i.Status == prowlarrStatusDisabled }

func (widget *prowlarrWidget) initialize() error {
	widget.withTitle("Prowlarr")

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(5 * time.Minute)
		widget.UpdateInterval = &interval
	}

	widget.withCacheDuration(time.Duration(*widget.UpdateInterval))

	if widget.URL == "" {
		return errors.New("url is required")
	}

	if widget.Token == "" {
		return errors.New("token is required")
	}

	widget.URL = strings.TrimRight(widget.URL, "/")

	widget.Show = cmp.Or(widget.Show, "all")

	if widget.Show != "all" && widget.Show != "errors" {
		return fmt.Errorf("invalid show value %q, must be all or errors", widget.Show)
	}

	if widget.CollapseAfter == 0 || widget.CollapseAfter < -1 {
		widget.CollapseAfter = 5
	}

	return nil
}

func (widget *prowlarrWidget) update(ctx context.Context) {
	client := ternary(widget.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)

	indexers, err := fetchProwlarrIndexers(client, widget.URL, widget.Token)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	if widget.Show == "errors" {
		indexers = slices.DeleteFunc(indexers, func(i prowlarrIndexer) bool { return !i.IsFailing() })
	}

	widget.Indexers = indexers
}

func (widget *prowlarrWidget) Render() template.HTML {
	return widget.renderTemplate(widget, prowlarrWidgetTemplate)
}

type prowlarrIndexerJson struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Enable bool   `json:"enable"`
}

type prowlarrIndexerStatusJson struct {
	IndexerID         int       `json:"indexerId"`
	DisabledTill      time.Time `json:"disabledTill"`
	InitialFailure    time.Time `json:"initialFailure"`
	MostRecentFailure time.Time `json:"mostRecentFailure"`
}

func fetchFromProwlarr[T any](client *http.Client, instanceURL, token, path string) (T, error) {
	request, err := http.NewRequest("GET", instanceURL+"/api/v1/"+path, nil)
	if err != nil {
		var zero T
		return zero, err
	}

	request.Header.Set("X-Api-Key", token)

	return decodeJsonFromRequest[T](client, request)
}

func fetchProwlarrIndexers(client *http.Client, instanceURL, token string) ([]prowlarrIndexer, error) {
	indexersJson, err := fetchFromProwlarr[[]prowlarrIndexerJson](client, instanceURL, token, "indexer")
	if err != nil {
		return nil, fmt.Errorf("could not fetch indexers: %v", err)
	}

	statusesJson, err := fetchFromProwlarr[[]prowlarrIndexerStatusJson](client, instanceURL, token, "indexerstatus")
	if err != nil {
		return nil, fmt.Errorf("could not fetch indexer statuses: %v", err)
	}

	now := time.Now()
	failures := make(map[int]prowlarrIndexerStatusJson, len(statusesJson))

	// Prowlarr keeps status rows after recovery, only a future disabledTill means the indexer is still failing.
	for _, status := range statusesJson {
		if status.DisabledTill.After(now) {
			failures[status.IndexerID] = status
		}
	}

	indexers := make([]prowlarrIndexer, 0, len(indexersJson))

	for _, indexerJson := range indexersJson {
		indexer := prowlarrIndexer{Name: indexerJson.Name, Status: prowlarrStatusHealthy}

		if !indexerJson.Enable {
			indexer.Status = prowlarrStatusDisabled
		} else if failure, failing := failures[indexerJson.ID]; failing {
			indexer.Status = prowlarrStatusFailing
			indexer.FailingSince = ternary(failure.InitialFailure.IsZero(), failure.MostRecentFailure, failure.InitialFailure)
		}

		indexers = append(indexers, indexer)
	}

	slices.SortFunc(indexers, func(a, b prowlarrIndexer) int {
		return cmp.Or(cmp.Compare(a.Status, b.Status), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})

	return indexers, nil
}
