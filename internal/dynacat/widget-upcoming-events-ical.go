package dynacat

import (
	"context"
	"fmt"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

type upcomingEventIcal struct {
	EndDate string
	Content string
}

type upcomingEventsIcalList []upcomingEventIcal

type upcomingEventsIcalWidget struct {
	widgetBase     `yaml:",inline"`
	Limit          int    `yaml:"limit"`
	IcalURL        string `yaml:"icalURL"`
	UpcomingEvents upcomingEventsIcalList
}

type Node struct {
	Data any
	Next *Node
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

func prettyPrintDate(date string) (pretty string, err error) {
	t, err := time.Parse("20060102", date) // TODO group events on the same date
	if err != nil {
		return "", err
	}

	day := t.Day()

	weekday := t.Format("Monday")
	month := strings.ToLower(t.Format("Jan."))

	result := fmt.Sprintf("%s %d %s", weekday, day, month)

	return result, err
}

func getUpcomingEvents(icalURL string, limit int) (events upcomingEventsIcalList, err error) {
	cal, err := ics.ParseCalendarFromUrl(icalURL)

	// TODO manage events currently in progress
	if err != nil {
		return nil, err
	}

	endDate := make(map[string]upcomingEventIcal)

	for _, component := range cal.Components {
		dateEnd := ""
		content := ""
		for _, property := range component.UnknownPropertiesIANAProperties() {
			if property.IANAToken == "DTEND" {
				const layout = "20060102"
				t, err := time.Parse(layout, property.Value)
				if err != nil {
					return nil, err
				}
				now := time.Now()

				if t.Before(now) {
					break
				}

				dateEnd = property.Value
			} // TODO use switch
			if property.IANAToken == "SUMMARY" {
				content = property.Value
			}
		}
		if dateEnd != "" && content != "" {
			endDate[dateEnd] = upcomingEventIcal{
				EndDate: dateEnd,
				Content: content,
			}
		}

	} // TODO time-based not day-based
	keys := make([]int, 0, len(endDate))
	for k := range endDate {
		val, err := strconv.Atoi(k)
		keys = append(keys, val)
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(keys)
	returnValue := make(upcomingEventsIcalList, limit)
	for i := range limit {
		keyString := strconv.Itoa(keys[i])
		evt := endDate[keyString]
		valPP, err := prettyPrintDate(evt.EndDate)
		if err != nil {
			return nil, err
		}
		evt.EndDate = valPP
		returnValue[i] = evt
	}

	return returnValue, nil
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
