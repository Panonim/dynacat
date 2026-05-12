package dynacat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

var contributionGraphWidgetTemplate = mustParseTemplate("contribution-graph.html", "widget-base.html")

type contributionMonthLabel struct {
	Name        string
	StartCol    int
	PixelOffset int
}

type contributionDay struct {
	Date  string
	Count int
	Level int
}

type contributionWeek struct {
	Days []contributionDay
}

type contributionGraphWidget struct {
	widgetBase  `yaml:",inline"`
	Frameless   bool   `yaml:"frameless"`
	User        string `yaml:"user"`
	Token       string `yaml:"token"`
	GitLabToken string `yaml:"gitlab-token"`
	Source      string `yaml:"source"`
	GitLabURL   string `yaml:"gitlab-url"`

	Weeks              []contributionWeek     `yaml:"-"`
	MonthLabels        []contributionMonthLabel `yaml:"-"`
	TotalContributions int                      `yaml:"-"`
}

func (widget *contributionGraphWidget) initialize() error {
	widget.withTitle("Contributions").withCacheDuration(12 * time.Hour)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(12 * time.Hour)
		widget.UpdateInterval = &interval
	}

	if widget.User == "" {
		return fmt.Errorf("contribution-graph widget requires a 'user' property")
	}

	if widget.Source == "" {
		widget.Source = "github"
	}

	if widget.GitLabURL == "" {
		widget.GitLabURL = "https://gitlab.com"
	}

	return nil
}

func (widget *contributionGraphWidget) update(ctx context.Context) {
	var weeks []contributionWeek
	var monthLabels []contributionMonthLabel
	var total int
	var err error

	switch widget.Source {
	case "github":
		if widget.Token == "" {
			weeks, monthLabels, total = buildContributionGrid(make(map[string]int))
			err = nil
		} else {
			weeks, monthLabels, total, err = fetchGithubContributions(widget.User, widget.Token)
		}
	case "gitlab":
		weeks, monthLabels, total, err = fetchGitlabContributions(widget.GitLabURL, widget.User, widget.GitLabToken)
	default:
		if widget.Token == "" {
			weeks, monthLabels, total = buildContributionGrid(make(map[string]int))
			err = nil
		} else {
			weeks, monthLabels, total, err = fetchGithubContributions(widget.User, widget.Token)
		}
	}

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	widget.Weeks = weeks
	widget.MonthLabels = monthLabels
	widget.TotalContributions = total
}

func (widget *contributionGraphWidget) Render() template.HTML {
	return widget.renderTemplate(widget, contributionGraphWidgetTemplate)
}

type githubContributionResponse struct {
	Data struct {
		User struct {
			ContributionsCollection struct {
				ContributionCalendar struct {
					TotalContributions int `json:"totalContributions"`
					Weeks              []struct {
						ContributionDays []struct {
							Date              string `json:"date"`
							ContributionCount int    `json:"contributionCount"`
						} `json:"contributionDays"`
					} `json:"weeks"`
				} `json:"contributionCalendar"`
			} `json:"contributionsCollection"`
		} `json:"user"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func fetchGithubContributions(user, token string) ([]contributionWeek, []contributionMonthLabel, int, error) {
	type graphqlQuery struct {
		Query string `json:"query"`
	}

	q := graphqlQuery{
		Query: fmt.Sprintf(`{ user(login: "%s") { contributionsCollection { contributionCalendar { totalContributions weeks { contributionDays { date contributionCount } } } } } }`, user),
	}

	body, err := json.Marshal(q)
	if err != nil {
		return nil, nil, 0, err
	}

	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	response, err := decodeJsonFromRequest[githubContributionResponse](defaultHTTPClient, req)
	if err != nil {
		return nil, nil, 0, err
	}

	if len(response.Errors) > 0 {
		return nil, nil, 0, fmt.Errorf("github graphql error: %s", response.Errors[0].Message)
	}

	dayMap := make(map[string]int)
	for _, week := range response.Data.User.ContributionsCollection.ContributionCalendar.Weeks {
		for _, day := range week.ContributionDays {
			dayMap[day.Date] = day.ContributionCount
		}
	}

	weeks, monthLabels, total := buildContributionGrid(dayMap)
	return weeks, monthLabels, total, nil
}

func fetchGitlabContributions(baseURL, user, token string) ([]contributionWeek, []contributionMonthLabel, int, error) {
	url := fmt.Sprintf("%s/users/%s/calendar.json", baseURL, user)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, nil, 0, err
	}

	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}

	dayMap, err := decodeJsonFromRequest[map[string]int](defaultHTTPClient, req)
	if err != nil {
		return nil, nil, 0, err
	}

	weeks, monthLabels, total := buildContributionGrid(dayMap)
	return weeks, monthLabels, total, nil
}

func countToContributionLevel(count int) int {
	switch {
	case count <= 0:
		return 0
	case count <= 3:
		return 1
	case count <= 6:
		return 2
	case count <= 9:
		return 3
	default:
		return 4
	}
}

func buildContributionGrid(days map[string]int) ([]contributionWeek, []contributionMonthLabel, int) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Find the Sunday of the current week
	currentWeekday := int(today.Weekday())
	endWeekSunday := today.AddDate(0, 0, -currentWeekday)

	// Start 52 weeks before the current week's Sunday (53 weeks total)
	startDate := endWeekSunday.AddDate(0, 0, -52*7)

	const colWidth = 12 // 10px cell + 2px gap
	const minLabelGap = 30 // minimum px between labels to avoid overlap

	weeks := make([]contributionWeek, 53)
	var total int
	var monthLabels []contributionMonthLabel
	lastMonth := -1
	lastLabelOffset := -minLabelGap

	for w := 0; w < 53; w++ {
		week := contributionWeek{Days: make([]contributionDay, 7)}
		for d := 0; d < 7; d++ {
			currentDate := startDate.AddDate(0, 0, w*7+d)

			monthInt := int(currentDate.Month())
			if monthInt != lastMonth {
				lastMonth = monthInt
				offset := w * colWidth
				if offset-lastLabelOffset >= minLabelGap {
					monthLabels = append(monthLabels, contributionMonthLabel{
						Name:        currentDate.Format("Jan"),
						StartCol:    w + 1,
						PixelOffset: offset,
					})
					lastLabelOffset = offset
				}
			}

			count := 0
			if !currentDate.After(today) {
				dateStr := currentDate.Format("2006-01-02")
				count = days[dateStr]
			}
			total += count

			week.Days[d] = contributionDay{
				Date:  currentDate.Format("Jan 2, 2006"),
				Count: count,
				Level: countToContributionLevel(count),
			}
		}
		weeks[w] = week
	}

	return weeks, monthLabels, total
}
