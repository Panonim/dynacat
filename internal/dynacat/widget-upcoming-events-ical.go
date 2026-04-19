package dynacat

import (
	"context"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

type upcomingEventIcal struct {
	EndDate            time.Time
	Content            string
	EndDatePrettyPrint string
	IsCurrent          bool
	StartHour          string
}

type upcomingEventsIcalList []upcomingEventIcal

type upcomingEventsIcalWidget struct {
	widgetBase     `yaml:",inline"`
	Limit          int    `yaml:"limit"`
	IcalURL        string `yaml:"icalURL"`
	UpcomingEvents []upcomingEventsIcalList
}

func (widget *upcomingEventsIcalWidget) initialize() error {
	widget.
		withTitle("Upcoming events").
		withCacheDuration(30 * time.Minute)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(5 * time.Minute)
		widget.UpdateInterval = &interval
	}

	if widget.Limit <= 0 {
		widget.Limit = 5
	}

	if widget.IcalURL == "" {
		widget.IcalURL = "https://calendar.google.com/calendar/ical/en.usa%23holiday%40group.v.calendar.google.com/public/basic.ics"
	}

	return nil
}

func prettyPrintDate(date string) (string, error) {
	t, err := parseDate(date)
	if err != nil {
		return "", err
	}

	weekday := t.Format("Monday")
	month := strings.ToLower(t.Format("Jan."))
	day := t.Day()

	return fmt.Sprintf("%s %d %s", weekday, day, month), nil
}

func parseDate(date string) (time.Time, error) {
	layout := ""
	switch len(date) {
	case 8:
		layout = "20060102"
	case 15:
		layout = "20060102T150405"
	case 29:
		layout = "2006-01-02 15:04:05 -0700 MST"
	default:
		return time.Time{}, fmt.Errorf("unknown date format: %s", date)
	}

	return time.Parse(layout, date)
}

func getUpcomingEvents(icalURL string, limit int) ([]upcomingEventsIcalList, error) {
	cal, err := ics.ParseCalendarFromUrl(icalURL)
	if err != nil {
		return nil, err
	}

	eventsByDate := make(map[int]upcomingEventsIcalList)
	now := time.Now()

	for _, component := range cal.Components {
		var dateEnd time.Time
		var isCurrent bool
		var content, startHour string

		for _, property := range component.UnknownPropertiesIANAProperties() {
			switch property.IANAToken {
			case "DTSTART":
				t, err := parseDate(property.Value)
				if err != nil {
					return nil, err
				}
				isCurrent = t.Before(now)
				startHour = t.Format("15:04")

			case "DTEND":
				t, err := parseDate(property.Value)
				if err != nil {
					return nil, err
				}
				if t.Before(now) {
					break
				}
				dateEnd = t

			case "SUMMARY":
				content = property.Value
			}
		}

		if !dateEnd.IsZero() && content != "" && startHour != "" {
			day := int(dateEnd.Unix() / 86400)
			eventsByDate[day] = append(eventsByDate[day], upcomingEventIcal{
				EndDate:   dateEnd,
				Content:   content,
				IsCurrent: isCurrent,
				StartHour: startHour,
			})
		}
	}

	keys := make([]int, 0, len(eventsByDate))
	for k := range eventsByDate {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var result []upcomingEventsIcalList
	totalAdded := 0

	for _, key := range keys {
		if totalAdded >= limit {
			break
		}

		events := eventsByDate[key]
		remaining := limit - totalAdded

		prettyDate, err := prettyPrintDate(events[0].EndDate.String())
		if err != nil {
			return nil, err
		}
		events[0].EndDatePrettyPrint = prettyDate

		if len(events) <= remaining {
			result = append(result, events)
			totalAdded += len(events)
		} else {
			result = append(result, events[:remaining])
			totalAdded = limit
		}
	}

	return result, nil
}

func (widget *upcomingEventsIcalWidget) update(ctx context.Context) {
	evt, err := getUpcomingEvents(widget.IcalURL, widget.Limit)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}
	widget.UpcomingEvents = evt
}

var upcomingEventsTemplate = mustParseTemplate("upcoming-events.html", "widget-base.html")

func (widget *upcomingEventsIcalWidget) Render() template.HTML {
	return widget.renderTemplate(widget, upcomingEventsTemplate)
}
