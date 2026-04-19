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
	t, err := parseDate(date)

	day := t.Day()

	weekday := t.Format("Monday")
	month := strings.ToLower(t.Format("Jan."))

	result := fmt.Sprintf("%s %d %s", weekday, day, month)

	return result, err
}

func parseDate(date string) (t time.Time, err error) {
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
	t, err = time.Parse(layout, date)
	return t, err
}

func getUpcomingEvents(icalURL string, limit int) (events []upcomingEventsIcalList, err error) {
	cal, err := ics.ParseCalendarFromUrl(icalURL)

	if err != nil {
		return nil, err
	}

	endDate := make(map[time.Time]upcomingEventsIcalList)

	for _, component := range cal.Components {
		var dateEnd time.Time
		isCurrent := false
		content := ""
		startHour := ""
		for _, property := range component.UnknownPropertiesIANAProperties() {
			switch property.IANAToken {
			case "DTSTART":
				t, err := parseDate(property.Value)

				if err != nil {
					return nil, err
				}
				now := time.Now()

				isCurrent = t.Before(now)
				startHour = t.Format("15:04")
				break
			case "DTEND":
				t, err := parseDate(property.Value)

				if err != nil {
					return nil, err
				}
				now := time.Now()

				if t.Before(now) {
					break
				}

				dateEnd = t
				break
			case "SUMMARY":
				content = property.Value
				break
			}
		}
		if !dateEnd.IsZero() && content != "" && startHour != "" {
			var val upcomingEventsIcalList
			var ok = false
			if val, ok = endDate[dateEnd]; !ok {
				val = upcomingEventsIcalList{}
			}
			val = append(val, upcomingEventIcal{
				EndDate:   dateEnd,
				Content:   content,
				IsCurrent: isCurrent,
				StartHour: startHour,
			})
			endDate[dateEnd] = val
		}

	}
	keys := make([]time.Time, 0, len(endDate))
	for k := range endDate {
		keys = append(keys, k)
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(keys, func(a, b time.Time) int {
		return a.Compare(b)
	})

	returnValue := make([]upcomingEventsIcalList, 0)
	added := 0
	keyID := 0
	for added != limit {

		if keyID >= len(keys) {
			break
		}

		evt := endDate[keys[keyID]]
		if len(evt) < limit-added {
			added += len(evt)

			valPP, err := prettyPrintDate(evt[0].EndDate.String())
			if err != nil {
				return nil, err
			}

			evt[0].EndDatePrettyPrint = valPP
			returnValue = append(returnValue, evt)
		} else {
			delta := limit - added
			valPP, err := prettyPrintDate(evt[0].EndDate.String())
			if err != nil {
				return nil, err
			}
			evt[0].EndDatePrettyPrint = valPP
			returnValue = append(returnValue, evt[:delta])
			added += delta
		}
		keyID++
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
