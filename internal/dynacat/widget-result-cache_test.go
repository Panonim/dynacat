package dynacat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWidgetResultCacheSharesSameWeatherRequestAcrossWidgets(t *testing.T) {
	cache := newWidgetResultCache[*weather]()
	firstWidget := &weatherWidget{Location: "London, United Kingdom", Units: "metric"}
	secondWidget := &weatherWidget{Location: "London, United Kingdom", Units: "metric"}
	if err := firstWidget.initialize(); err != nil {
		t.Fatal(err)
	}
	if err := secondWidget.initialize(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetchLondonWeather := func(context.Context) (*weather, error) {
		calls++
		return &weather{
			Temperature:         21,
			ApparentTemperature: 19,
			WeatherCode:         2,
			Columns: []weatherColumn{
				{Temperature: 18, Scale: 0.4},
				{Temperature: 21, Scale: 0.7},
				{Temperature: 16, Scale: 0.2, HasPrecipitation: true},
			},
		}, nil
	}

	key := "weather|" + firstWidget.Location + "|" + firstWidget.Units
	weather, err := cache.GetForWidget(context.Background(), &firstWidget.widgetBase, key, fetchLondonWeather)
	if err != nil {
		t.Fatal(err)
	}
	firstWidget.Weather = weather

	weather, err = cache.GetForWidget(context.Background(), &secondWidget.widgetBase, key, fetchLondonWeather)
	if err != nil {
		t.Fatal(err)
	}
	secondWidget.Weather = weather

	if firstWidget.Weather != secondWidget.Weather || secondWidget.Weather.Temperature != 21 || calls != 1 {
		t.Fatalf("got same_result=%t temp=%d calls=%d", firstWidget.Weather == secondWidget.Weather, secondWidget.Weather.Temperature, calls)
	}
}

func TestWidgetResultCacheCoalescesConcurrentSameRepositoryReleaseWidgets(t *testing.T) {
	cache := newWidgetResultCache[appReleaseList]()
	widgets := []*releasesWidget{
		{Repositories: []*releaseRequest{{Repository: "Panonim/dynacat", source: releaseSourceGithub}}},
		{Repositories: []*releaseRequest{{Repository: "Panonim/dynacat", source: releaseSourceGithub}}},
	}
	for _, widget := range widgets {
		if err := widget.initialize(); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fetchDynacatReleases := func(context.Context) (appReleaseList, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return appReleaseList{
			{
				Source:       releaseSourceGithub,
				Name:         "Panonim/dynacat",
				Version:      "v1.2.3",
				NotesUrl:     "https://github.com/Panonim/dynacat/releases/tag/v1.2.3",
				TimeReleased: time.Unix(1710000000, 0),
			},
		}, nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(widgets))
	key := "releases|github|Panonim/dynacat|include-prereleases=false"
	for i, widget := range widgets {
		wg.Add(1)
		go func(widget *releasesWidget) {
			defer wg.Done()
			result, err := cache.GetForWidget(context.Background(), &widget.widgetBase, key, fetchDynacatReleases)
			if err != nil {
				errs <- err
				return
			}
			widget.Releases = result
			errs <- nil
		}(widget)

		if i == 0 {
			<-started
		}
	}

	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if calls.Load() != 1 {
		t.Fatalf("fetch called %d times", calls.Load())
	}

	for _, widget := range widgets {
		if len(widget.Releases) != 1 || widget.Releases[0].Version != "v1.2.3" {
			t.Fatalf("got %#v", widget.Releases)
		}
	}
}

func TestWidgetResultCacheClearRefetchesTodoListAfterSave(t *testing.T) {
	cache := newWidgetResultCache[[]todoTask]()
	widget := &todoWidget{TodoID: "deploy", Storage: "server"}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fetchTasks := func(context.Context) ([]todoTask, error) {
		call := calls.Add(1)
		if call == 1 {
			close(started)
			<-release
		}

		if call == 1 {
			return []todoTask{{Text: "deploy", Checked: false}}, nil
		}

		return []todoTask{{Text: "deploy", Checked: true}}, nil
	}

	firstDone := make(chan struct{})
	firstResult := make(chan []todoTask, 1)
	go func() {
		defer close(firstDone)
		result, err := cache.GetForWidget(context.Background(), &widget.widgetBase, "to-do|"+widget.Storage+"|"+widget.TodoID, fetchTasks)
		if err != nil {
			t.Errorf("Get returned error: %v", err)
			return
		}
		firstResult <- result
	}()

	<-started
	cache.Clear()
	close(release)
	<-firstDone
	close(firstResult)

	for result := range firstResult {
		if len(result) != 1 || !result[0].Checked {
			t.Fatalf("got stale in-flight tasks %#v", result)
		}
	}

	if calls.Load() != 2 {
		t.Fatalf("fetch called %d times", calls.Load())
	}
}

func TestWidgetResultCacheCachesWeatherErrorsForErrorTTL(t *testing.T) {
	cache := newWidgetResultCache[*weather]()
	widget := &weatherWidget{Location: "Paris, France", Units: "metric"}
	if err := widget.initialize(); err != nil {
		t.Fatal(err)
	}
	expectedErr := errors.New("open-meteo rate limited")
	calls := 0
	fetchWeather := func(context.Context) (*weather, error) {
		calls++
		return nil, expectedErr
	}

	for i := 0; i < 2; i++ {
		_, err := cache.GetForWidget(context.Background(), &widget.widgetBase, "weather|"+widget.Location+"|"+widget.Units, fetchWeather)
		if !errors.Is(err, expectedErr) {
			t.Fatalf("got error %v", err)
		}
	}

	if calls != 1 {
		t.Fatalf("fetch called %d times", calls)
	}
}
