package dynacat

import (
	"context"
	"errors"
	"html/template"
	"slices"
	"strings"
	"time"
)

var twitchGamesWidgetTemplate = mustParseTemplate("twitch-games-list.html", "widget-base.html")

type twitchGamesWidget struct {
	widgetBase    `yaml:",inline"`
	Frameless     bool             `yaml:"frameless"`
	Categories    []twitchCategory `yaml:"-"`
	Exclude       []string         `yaml:"exclude"`
	Limit         int              `yaml:"limit"`
	CollapseAfter int              `yaml:"collapse-after"`
}

func (widget *twitchGamesWidget) initialize() error {
	widget.
		withTitle("Top games on Twitch").
		withTitleURL("https://www.twitch.tv/directory?sort=VIEWER_COUNT").
		withCacheDuration(time.Minute * 10)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(35 * time.Minute)
		widget.UpdateInterval = &interval
	}

	if widget.Limit <= 0 {
		widget.Limit = 10
	}

	if widget.CollapseAfter == 0 || widget.CollapseAfter < -1 {
		widget.CollapseAfter = 5
	}

	return nil
}

func (widget *twitchGamesWidget) update(ctx context.Context) {
	categories, err := fetchTopGamesFromTwitch(ctx, widget.Exclude, widget.Limit)

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	if widget.Providers != nil {
		for i := range categories {
			if categories[i].AvatarUrl == "" {
				continue
			}
			categories[i].AvatarUrl = widget.Providers.SecureImageURL(ctx, categories[i].AvatarUrl, false)
		}
	}

	widget.Categories = categories
}

func (widget *twitchGamesWidget) Render() template.HTML {
	return widget.renderTemplate(widget, twitchGamesWidgetTemplate)
}

type twitchCategory struct {
	Slug            string              `json:"slug"`
	Name            string              `json:"name"`
	AvatarUrl       string              `json:"avatarURL"`
	ViewersCount    int                 `json:"viewersCount"`
	Tags            []twitchCategoryTag `json:"tags"`
	GameReleaseDate string              `json:"originalReleaseDate"`
	IsNew           bool                `json:"-"`
}

type twitchCategoryTag struct {
	Name string `json:"tagName"`
}

type twitchCategoryEdge struct {
	Node twitchCategory `json:"node"`
}

type twitchDirectoriesOperationResponse struct {
	DirectoriesWithTags struct {
		Edges []twitchCategoryEdge `json:"edges"`
	} `json:"directoriesWithTags"`
}

type twitchDirectoriesOperationVariables struct {
	Limit   int `json:"limit"`
	Options struct {
		Sort string   `json:"sort"`
		Tags []string `json:"tags"`
	} `json:"options"`
}

const twitchDirectoriesOperationName = "BrowsePage_AllDirectories"
const twitchDirectoriesOperationHash = "2f67f71ba89f3c0ed26a141ec00da1defecb2303595f5cda4298169549783d9e"

func newTwitchDirectoriesOperationRequest(limit int) twitchGraphQLOperationRequest {
	variables := twitchDirectoriesOperationVariables{Limit: limit}
	variables.Options.Sort = "VIEWER_COUNT"
	variables.Options.Tags = []string{}

	return newTwitchGraphQLPersistedQueryRequest(twitchDirectoriesOperationName, variables, twitchDirectoriesOperationHash)
}

func fetchTopGamesFromTwitch(ctx context.Context, exclude []string, limit int) ([]twitchCategory, error) {
	response, err := decodeJsonFromTwitchGraphQLRequest[twitchDirectoriesOperationResponse](ctx, []twitchGraphQLOperationRequest{
		newTwitchDirectoriesOperationRequest(len(exclude) + limit),
	})
	if err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, errors.New("no categories could be retrieved")
	}

	if len(response[0].Errors) > 0 {
		return nil, errors.New(response[0].Errors[0].Message)
	}
	if response[0].Extensions.OperationName != twitchDirectoriesOperationName {
		return nil, errors.New("unknown operation name: " + response[0].Extensions.OperationName)
	}

	return buildTwitchCategories(response[0].Data.DirectoriesWithTags.Edges, exclude, limit), nil
}

func buildTwitchCategories(edges []twitchCategoryEdge, exclude []string, limit int) []twitchCategory {
	categories := make([]twitchCategory, 0, len(edges))

	for i := range edges {
		if slices.Contains(exclude, edges[i].Node.Slug) {
			continue
		}

		category := &edges[i].Node
		category.AvatarUrl = strings.Replace(category.AvatarUrl, "285x380", "144x192", 1)

		if len(category.Tags) > 2 {
			category.Tags = category.Tags[:2]
		}

		gameReleasedDate, err := time.Parse("2006-01-02T15:04:05Z", category.GameReleaseDate)

		if err == nil {
			if time.Since(gameReleasedDate) < 14*24*time.Hour {
				category.IsNew = true
			}
		}

		categories = append(categories, *category)
	}

	if len(categories) > limit {
		categories = categories[:limit]
	}

	return categories
}
