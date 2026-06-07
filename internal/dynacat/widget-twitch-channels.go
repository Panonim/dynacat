package dynacat

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"sort"
	"strings"
	"time"
)

var twitchChannelsWidgetTemplate = mustParseTemplate("twitch-channels.html", "widget-base.html")

type twitchChannelsWidget struct {
	widgetBase      `yaml:",inline"`
	Frameless       bool            `yaml:"frameless"`
	ChannelsRequest []string        `yaml:"channels"`
	Channels        []twitchChannel `yaml:"-"`
	CollapseAfter   int             `yaml:"collapse-after"`
	SortBy          string          `yaml:"sort-by"`
}

func (widget *twitchChannelsWidget) initialize() error {
	widget.
		withTitle("Twitch Channels").
		withTitleURL("https://www.twitch.tv/directory/following").
		withCacheDuration(time.Minute * 10)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(15 * time.Minute)
		widget.UpdateInterval = &interval
	}

	if widget.CollapseAfter == 0 || widget.CollapseAfter < -1 {
		widget.CollapseAfter = 5
	}

	switch widget.SortBy {
	case "viewers", "live":
	default:
		widget.SortBy = "viewers"
	}

	return nil
}

func (widget *twitchChannelsWidget) update(ctx context.Context) {
	channels, err := fetchChannelsFromTwitch(ctx, widget.ChannelsRequest)

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	switch widget.SortBy {
	case "viewers":
		channels.sortByViewers()
	case "live":
		channels.sortByLive()
	}

	for i := range channels {
		if channels[i].IsLive {
			channels[i].PreviewUrl = buildTwitchPreviewURL(channels[i].Login)
		}
		if widget.Providers == nil {
			continue
		}
		if channels[i].AvatarUrl != "" {
			channels[i].AvatarUrl = widget.Providers.SecureImageURL(ctx, channels[i].AvatarUrl, false)
		}
		if channels[i].PreviewUrl != "" {
			channels[i].PreviewUrl = widget.Providers.SecureImageURL(ctx, channels[i].PreviewUrl, false)
		}
	}

	widget.Channels = channels
}

func (widget *twitchChannelsWidget) Render() template.HTML {
	return widget.renderTemplate(widget, twitchChannelsWidgetTemplate)
}

type twitchChannel struct {
	Login        string
	Exists       bool
	Name         string
	StreamTitle  string
	PreviewUrl   string
	AvatarUrl    string
	IsLive       bool
	LiveSince    time.Time
	Category     string
	CategorySlug string
	ViewersCount int
}

type twitchChannelList []twitchChannel

func (channels twitchChannelList) sortByViewers() {
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].ViewersCount > channels[j].ViewersCount
	})
}

func (channels twitchChannelList) sortByLive() {
	sort.SliceStable(channels, func(i, j int) bool {
		return channels[i].IsLive && !channels[j].IsLive
	})
}

func buildTwitchPreviewURL(login string) string {
	if login == "" {
		return ""
	}
	return fmt.Sprintf("https://static-cdn.jtvnw.net/previews-ttv/live_user_%s-440x248.jpg", login)
}

type twitchChannelStatusOperationResponse struct {
	User *struct {
		DisplayName     string `json:"displayName"`
		ProfileImageUrl string `json:"profileImageURL"`
		Stream          *struct {
			ViewersCount int    `json:"viewersCount"`
			StartedAt    string `json:"createdAt"`
			Game         *struct {
				Slug string `json:"slug"`
				Name string `json:"name"`
			} `json:"game"`
		} `json:"stream"`
		LastBroadcast *struct {
			Title string `json:"title"`
		}
	} `json:"user"`
}

const twitchChannelStatusOperationName = "ChannelStatus"
const twitchChannelStatusOperationQuery = `query ChannelStatus($login: String!) { user(login: $login) { displayName profileImageURL(width: 70) stream { viewersCount createdAt game { name slug } } lastBroadcast { title } } }`
const twitchChannelsBatchSize = twitchMaxOperationsPerRequest
const twitchMaxConcurrentBatches = 3

type twitchChannelStatusOperationVariables struct {
	Login string `json:"login"`
}

func newTwitchChannelStatusOperationRequest(channel string) twitchGraphQLOperationRequest {
	return newTwitchGraphQLQueryRequest(
		twitchChannelStatusOperationName,
		twitchChannelStatusOperationVariables{Login: channel},
		twitchChannelStatusOperationQuery,
	)
}

func fetchChannelFromTwitchOperation(channel string, response twitchGraphQLOperationResponse[twitchChannelStatusOperationResponse]) (twitchChannel, error) {
	result := twitchChannel{
		Login: strings.ToLower(channel),
	}

	switch response.Extensions.OperationName {
	case twitchChannelStatusOperationName:
	default:
		return result, fmt.Errorf("unknown operation name: %s", response.Extensions.OperationName)
	}

	if len(response.Errors) > 0 {
		return result, fmt.Errorf("twitch operation failed: %s", response.Errors[0].Message)
	}

	if response.Data.User == nil {
		result.Name = result.Login
		result.ViewersCount = -1
		return result, nil
	}

	result.Exists = true
	result.Name = response.Data.User.DisplayName
	result.AvatarUrl = response.Data.User.ProfileImageUrl

	if response.Data.User.Stream != nil {
		result.IsLive = true
		result.ViewersCount = response.Data.User.Stream.ViewersCount

		if response.Data.User.LastBroadcast != nil {
			result.StreamTitle = response.Data.User.LastBroadcast.Title
		}

		if response.Data.User.Stream.Game != nil {
			result.Category = response.Data.User.Stream.Game.Name
			result.CategorySlug = response.Data.User.Stream.Game.Slug
		}
		startedAt, err := time.Parse(time.RFC3339, response.Data.User.Stream.StartedAt)

		if err == nil {
			result.LiveSince = startedAt
		} else {
			slog.Warn("Failed to parse Twitch stream started at", "error", err, "started_at", response.Data.User.Stream.StartedAt)
		}
	} else {
		// This prevents live channels with 0 viewers from being
		// incorrectly sorted lower than offline channels.
		result.ViewersCount = -1
	}

	return result, nil
}

func fetchChannelsFromTwitchBatch(ctx context.Context, channelLogins []string) (twitchChannelList, error) {
	operations := make([]twitchGraphQLOperationRequest, 0, len(channelLogins))

	for i := range channelLogins {
		operations = append(operations, newTwitchChannelStatusOperationRequest(channelLogins[i]))
	}

	response, err := decodeJsonFromTwitchGraphQLRequest[twitchChannelStatusOperationResponse](ctx, operations)
	if err != nil {
		return nil, err
	}

	return fetchChannelsFromTwitchOperations(channelLogins, response)
}

func fetchChannelsFromTwitchOperations(channelLogins []string, response []twitchGraphQLOperationResponse[twitchChannelStatusOperationResponse]) (twitchChannelList, error) {
	if len(response) != len(channelLogins) {
		return nil, fmt.Errorf("expected %d operation responses, got %d", len(channelLogins), len(response))
	}

	channels := make(twitchChannelList, 0, len(channelLogins))
	var failed int

	for i := range channelLogins {
		channel, err := fetchChannelFromTwitchOperation(channelLogins[i], response[i])
		if err != nil {
			failed++
			continue
		}
		if !channel.Exists {
			slog.Warn("Twitch channel not found", "channel", channelLogins[i])
		}
		channels = append(channels, channel)
	}

	if failed > 0 {
		return channels, fmt.Errorf("%w: failed to fetch %d channels", errPartialContent, failed)
	}

	return channels, nil
}

func dedupeTwitchChannelLogins(channelLogins []string) []string {
	result := make([]string, 0, len(channelLogins))
	seen := make(map[string]struct{}, len(channelLogins))

	for _, channelLogin := range channelLogins {
		login := strings.ToLower(strings.TrimSpace(channelLogin))
		if login == "" {
			continue
		}

		if _, ok := seen[login]; ok {
			continue
		}

		seen[login] = struct{}{}
		result = append(result, login)
	}

	return result
}

func collectTwitchChannelBatchResults(batches [][]string, batchResults []twitchChannelList, errs []error) (twitchChannelList, int) {
	result := make(twitchChannelList, 0)
	var failed int

	for i := range batchResults {
		result = append(result, batchResults[i]...)

		if errs[i] != nil {
			failed += len(batches[i]) - len(batchResults[i])
		}
	}

	return result, failed
}

func fetchChannelsFromTwitch(ctx context.Context, channelLogins []string) (twitchChannelList, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Twitch channel logins are case-insensitive, so normalize before deduping requests.
	channelLogins = dedupeTwitchChannelLogins(channelLogins)
	result := make(twitchChannelList, 0, len(channelLogins))
	total := len(channelLogins)

	if len(channelLogins) == 0 {
		return result, errNoContent
	}

	batches := make([][]string, 0, (len(channelLogins)+twitchChannelsBatchSize-1)/twitchChannelsBatchSize)
	for len(channelLogins) > 0 {
		batchSize := min(len(channelLogins), twitchChannelsBatchSize)
		batches = append(batches, channelLogins[:batchSize])
		channelLogins = channelLogins[batchSize:]
	}

	job := newJob(func(logins []string) (twitchChannelList, error) {
		return fetchChannelsFromTwitchBatch(ctx, logins)
	}, batches).withWorkers(min(len(batches), twitchMaxConcurrentBatches))
	batchResults, errs, err := workerPoolDo(job)
	if err != nil {
		return result, err
	}

	var failed int
	result, failed = collectTwitchChannelBatchResults(batches, batchResults, errs)
	for i := range errs {
		if errs[i] != nil {
			slog.Error("Failed to fetch Twitch channels", "channels", strings.Join(batches[i], ","), "error", errs[i])
		}
	}

	if failed == total {
		return result, errNoContent
	}

	if failed > 0 {
		return result, fmt.Errorf("%w: failed to fetch %d channels", errPartialContent, failed)
	}

	return result, nil
}
