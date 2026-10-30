package dynacat

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

var (
	redditWidgetHorizontalCardsTemplate = mustParseTemplate("reddit-horizontal-cards.html", "widget-base.html")
	redditWidgetVerticalCardsTemplate   = mustParseTemplate("reddit-vertical-cards.html", "widget-base.html")
)

type redditWidget struct {
	widgetBase          `yaml:",inline"`
	Frameless           bool              `yaml:"frameless"`
	Posts               forumPostList     `yaml:"-"`
	Subreddit           string            `yaml:"subreddit"`
	Proxy               proxyOptionsField `yaml:"proxy"`
	Style               string            `yaml:"style"`
	ShowThumbnails      bool              `yaml:"show-thumbnails"`
	ShowFlairs          bool              `yaml:"show-flairs"`
	SortBy              string            `yaml:"sort-by"`
	TopPeriod           string            `yaml:"top-period"`
	Search              string            `yaml:"search"`
	ExtraSortBy         string            `yaml:"extra-sort-by"`
	CommentsURLTemplate string            `yaml:"comments-url-template"`
	Limit               int               `yaml:"limit"`
	CollapseAfter       int               `yaml:"collapse-after"`
	RequestURLTemplate  string            `yaml:"request-url-template"`

	AppAuth struct {
		Name   string `yaml:"name"`
		ID     string `yaml:"id"`
		Secret string `yaml:"secret"`

		enabled        bool
		accessToken    string
		tokenExpiresAt time.Time
	} `yaml:"app-auth"`
}

func (widget *redditWidget) initialize() error {
	if widget.Subreddit == "" {
		return errors.New("subreddit is required")
	}

	if widget.Limit <= 0 {
		widget.Limit = 15
	}

	if widget.CollapseAfter == 0 || widget.CollapseAfter < -1 {
		widget.CollapseAfter = 5
	}

	s := widget.SortBy
	if s != "hot" && s != "new" && s != "top" && s != "rising" {
		widget.SortBy = "hot"
	}

	p := widget.TopPeriod
	if p != "hour" && p != "day" && p != "week" && p != "month" && p != "year" && p != "all" {
		widget.TopPeriod = "day"
	}

	if widget.RequestURLTemplate != "" {
		if !strings.Contains(widget.RequestURLTemplate, "{REQUEST-URL}") {
			return errors.New("no `{REQUEST-URL}` placeholder specified")
		}
	}

	a := &widget.AppAuth
	if a.Name != "" || a.ID != "" || a.Secret != "" {
		if a.Name == "" || a.ID == "" || a.Secret == "" {
			return errors.New("application name, client ID and client secret are required")
		}
		a.enabled = true
	}

	if !a.enabled && widget.Search != "" {
		return errors.New("search requires app-auth, Reddit no longer allows signed-out searches")
	}

	widget.
		withTitle("r/" + widget.Subreddit).
		withTitleURL("https://www.reddit.com/r/" + widget.Subreddit + "/").
		withCacheDuration(30 * time.Minute)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(25 * time.Minute)
		widget.UpdateInterval = &interval
	}

	return nil
}

func (widget *redditWidget) update(ctx context.Context) {
	posts, err := widget.fetchSubredditPosts()
	fromRSS := false

	// Reddit's RSS feeds still serve signed-out clients when the JSON API blocks them.
	if err != nil && !widget.AppAuth.enabled {
		rssPosts, rssErr := widget.fetchSubredditPostsFromRSS()
		if rssErr != nil {
			err = fmt.Errorf("%w, rss fallback also failed: %v", err, rssErr)
		} else {
			posts, err, fromRSS = rssPosts, nil, true
		}
	}

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	if len(posts) > widget.Limit {
		posts = posts[:widget.Limit]
	}

	if widget.ExtraSortBy == "engagement" && !fromRSS {
		posts.calculateEngagement()
		posts.sortByEngagement()
	}

	if widget.Providers != nil {
		for i := range posts {
			if posts[i].ThumbnailUrl == "" {
				continue
			}
			posts[i].ThumbnailUrl = widget.Providers.SecureImageURL(ctx, posts[i].ThumbnailUrl, false, false)
		}
	}

	widget.Posts = posts
}

func (widget *redditWidget) Render() template.HTML {
	if widget.Style == "horizontal-cards" {
		return widget.renderTemplate(widget, redditWidgetHorizontalCardsTemplate)
	}

	if widget.Style == "vertical-cards" {
		return widget.renderTemplate(widget, redditWidgetVerticalCardsTemplate)
	}

	return widget.renderTemplate(widget, forumPostsTemplate)

}

type subredditResponseJson struct {
	Data struct {
		Children []struct {
			Data struct {
				Id            string  `json:"id"`
				Title         string  `json:"title"`
				Subreddit     string  `json:"subreddit"`
				Upvotes       int     `json:"ups"`
				Url           string  `json:"url"`
				Time          float64 `json:"created"`
				CommentsCount int     `json:"num_comments"`
				Domain        string  `json:"domain"`
				Permalink     string  `json:"permalink"`
				Stickied      bool    `json:"stickied"`
				Pinned        bool    `json:"pinned"`
				IsSelf        bool    `json:"is_self"`
				Thumbnail     string  `json:"thumbnail"`
				Flair         string  `json:"link_flair_text"`
				ParentList    []struct {
					Id        string `json:"id"`
					Subreddit string `json:"subreddit"`
					Permalink string `json:"permalink"`
				} `json:"crosspost_parent_list"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

func (widget *redditWidget) parseCustomCommentsURL(subreddit, postId, postPath string) string {
	template := strings.ReplaceAll(widget.CommentsURLTemplate, "{SUBREDDIT}", subreddit)
	template = strings.ReplaceAll(template, "{POST-ID}", postId)
	template = strings.ReplaceAll(template, "{POST-PATH}", strings.TrimLeft(postPath, "/"))

	return template
}

func buildRedditRequestURL(baseURL, path string, query url.Values) string {
	encodedQuery := query.Encode()

	if encodedQuery == "" {
		return baseURL + path
	}

	return baseURL + path + "?" + encodedQuery
}

func (widget *redditWidget) fetchSubredditPosts() (forumPostList, error) {
	var client requestDoer = redditHTTPClient
	var requestPath string
	var headers http.Header
	query := url.Values{}
	app := &widget.AppAuth

	if !app.enabled {
		headers = http.Header{
			"User-Agent": []string{getBrowserUserAgentHeader()},
			"Accept":     []string{"application/json"},
		}
	} else {
		if app.accessToken == "" || time.Now().Add(time.Minute).After(app.tokenExpiresAt) {
			if err := widget.fetchNewAppAccessToken(); err != nil {
				return nil, fmt.Errorf("fetching new app access token: %v", err)
			}
		}

		headers = http.Header{
			"Authorization": []string{"Bearer " + app.accessToken},
			"User-Agent":    []string{app.Name + "/1.0"},
			"Accept":        []string{"application/json"},
		}
	}

	if widget.Limit > 25 {
		query.Set("limit", strconv.Itoa(widget.Limit))
	}

	if widget.Search != "" {
		query.Set("q", widget.Search)
		query.Set("sort", widget.SortBy)
		query.Set("restrict_sr", "true")
		requestPath = fmt.Sprintf("/r/%s/search", widget.Subreddit)
	} else {
		if widget.SortBy == "top" {
			query.Set("t", widget.TopPeriod)
		}
		requestPath = fmt.Sprintf("/r/%s/%s", widget.Subreddit, widget.SortBy)
	}

	if widget.RequestURLTemplate == "" && widget.Proxy.client != nil {
		client = widget.Proxy.client
	}

	// The public .json endpoints are blocked for signed-out clients, so both modes go through oauth.reddit.com.
	requestURL := buildRedditRequestURL("https://oauth.reddit.com", requestPath, query)

	if widget.RequestURLTemplate != "" {
		requestURL = strings.ReplaceAll(widget.RequestURLTemplate, "{REQUEST-URL}", requestURL)
	}

	request, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		return nil, err
	}

	request.Header = headers

	// The guest token is only sent to Reddit over HTTPS, never to a request-url-template host.
	if !app.enabled && request.URL.Scheme == "https" && isRedditHost(request.URL.Hostname()) {
		token, err := getRedditGuestToken()
		if err != nil {
			return nil, fmt.Errorf("could not solve reddit challenge: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}

	responseJson, err := decodeJsonFromRequest[subredditResponseJson](client, request)
	if err != nil {
		// The 403 body is Reddit's full HTML block page, so only the URL is reported.
		if !app.enabled && strings.Contains(err.Error(), "status code 403") {
			return nil, fmt.Errorf("reddit blocked the request to %s (403), configure `app-auth` to use the official API", requestURL)
		}
		return nil, err
	}

	if len(responseJson.Data.Children) == 0 {
		return nil, fmt.Errorf("no posts found")
	}

	posts := make(forumPostList, 0, len(responseJson.Data.Children))

	for i := range responseJson.Data.Children {
		post := &responseJson.Data.Children[i].Data

		if post.Stickied || post.Pinned {
			continue
		}

		var commentsUrl string

		if widget.CommentsURLTemplate == "" {
			commentsUrl = "https://www.reddit.com" + post.Permalink
		} else {
			commentsUrl = widget.parseCustomCommentsURL(widget.Subreddit, post.Id, post.Permalink)
		}

		forumPost := forumPost{
			Title:           html.UnescapeString(post.Title),
			Subreddit:       post.Subreddit,
			DiscussionUrl:   commentsUrl,
			TargetUrlDomain: post.Domain,
			CommentCount:    post.CommentsCount,
			Score:           post.Upvotes,
			TimePosted:      time.Unix(int64(post.Time), 0),
		}

		if post.Thumbnail != "" && post.Thumbnail != "self" && post.Thumbnail != "default" && post.Thumbnail != "nsfw" {
			forumPost.ThumbnailUrl = html.UnescapeString(post.Thumbnail)
		}

		if !post.IsSelf {
			forumPost.TargetUrl = post.Url
		}

		if widget.ShowFlairs && post.Flair != "" {
			forumPost.Tags = append(forumPost.Tags, post.Flair)
		}

		if len(post.ParentList) > 0 {
			forumPost.IsCrosspost = true
			forumPost.TargetUrlDomain = "r/" + post.ParentList[0].Subreddit

			if widget.CommentsURLTemplate == "" {
				forumPost.TargetUrl = "https://www.reddit.com" + post.ParentList[0].Permalink
			} else {
				forumPost.TargetUrl = widget.parseCustomCommentsURL(
					post.ParentList[0].Subreddit,
					post.ParentList[0].Id,
					post.ParentList[0].Permalink,
				)
			}
		}

		posts = append(posts, forumPost)
	}

	return posts, nil
}

var redditRSSTargetLinkPattern = regexp.MustCompile(`<a href="([^"]+)">\[link\]</a>`)

func (widget *redditWidget) fetchSubredditPostsFromRSS() (forumPostList, error) {
	query := url.Values{}

	if widget.SortBy == "top" {
		query.Set("t", widget.TopPeriod)
	}

	if widget.Limit > 25 {
		query.Set("limit", strconv.Itoa(widget.Limit))
	}

	requestURL := buildRedditRequestURL("https://www.reddit.com", fmt.Sprintf("/r/%s/%s/.rss", widget.Subreddit, widget.SortBy), query)

	if widget.RequestURLTemplate != "" {
		requestURL = strings.ReplaceAll(widget.RequestURLTemplate, "{REQUEST-URL}", requestURL)
	}

	request, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		return nil, err
	}

	setBrowserUserAgentHeader(request)

	var client requestDoer = redditHTTPClient
	if widget.RequestURLTemplate == "" && widget.Proxy.client != nil {
		client = widget.Proxy.client
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", response.StatusCode, requestURL)
	}

	feed, err := feedParser.Parse(response.Body)
	if err != nil {
		return nil, err
	}

	if len(feed.Items) == 0 {
		return nil, fmt.Errorf("no posts found")
	}

	posts := make(forumPostList, 0, len(feed.Items))

	for _, item := range feed.Items {
		post := forumPost{
			Title:         html.UnescapeString(item.Title),
			Subreddit:     widget.Subreddit,
			DiscussionUrl: httpURLOrEmpty(item.Link),
			StatsUnknown:  true,
			ThumbnailUrl:  findThumbnailInItemExtensions(item),
		}

		if len(item.Categories) > 0 {
			post.Subreddit = item.Categories[0]
		}

		if item.PublishedParsed != nil {
			post.TimePosted = *item.PublishedParsed
		}

		if widget.CommentsURLTemplate != "" {
			if parsed, err := url.Parse(item.Link); err == nil {
				post.DiscussionUrl = widget.parseCustomCommentsURL(post.Subreddit, strings.TrimPrefix(item.GUID, "t3_"), parsed.Path)
			}
		}

		if matches := redditRSSTargetLinkPattern.FindStringSubmatch(item.Content); matches != nil {
			target := html.UnescapeString(matches[1])
			if strings.HasPrefix(target, "/") {
				target = "https://www.reddit.com" + target
			}

			if parsed, err := url.Parse(target); err == nil && target != item.Link {
				post.TargetUrl = target
				post.TargetUrlDomain = strings.TrimPrefix(parsed.Hostname(), "www.")
			}
		}

		posts = append(posts, post)
	}

	return posts, nil
}

func (widget *redditWidget) fetchNewAppAccessToken() error {
	body := strings.NewReader("grant_type=client_credentials")
	req, err := http.NewRequest("POST", "https://www.reddit.com/api/v1/access_token", body)
	if err != nil {
		return fmt.Errorf("creating request for app access token: %v", err)
	}

	app := &widget.AppAuth
	req.SetBasicAuth(app.ID, app.Secret)
	req.Header.Add("User-Agent", app.Name+"/1.0")
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	type tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	client := ternary(widget.Proxy.client != nil, widget.Proxy.client, redditHTTPClient)
	response, err := decodeJsonFromRequest[tokenResponse](client, req)
	if err != nil {
		return err
	}

	app.accessToken = response.AccessToken
	app.tokenExpiresAt = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)

	return nil
}

func isRedditHost(host string) bool {
	host = strings.ToLower(host)
	return host == "reddit.com" || strings.HasSuffix(host, ".reddit.com")
}

var redditHTTPClient = &http.Client{Transport: &http2.Transport{
	DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		tcpConn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		uconn := utls.UClient(tcpConn, &utls.Config{
			ServerName: host,
		}, utls.HelloFirefox_Auto)

		if err := uconn.HandshakeContext(ctx); err != nil {
			tcpConn.Close()
			return nil, err
		}

		return uconn, nil
	},
}}

var (
	redditChallengePattern = regexp.MustCompile(`await\(async \w+\s*=>\s*\w+\s*\+\s*\w+\)\("([^"]+)"\)`)
	redditTokenPattern     = regexp.MustCompile(`name="jsc_token"\s+value="([^"]+)"`)
	redditOrigRPattern     = regexp.MustCompile(`name="jsc_orig_r"\s+value="([^"]*)"`)
)

// Guest token is shared by all widgets and lives only in memory, refreshed well before its 24h expiry.
var getRedditGuestToken = func() func() (string, error) {
	var lastUpdate time.Time
	var cachedToken string

	return NewSingleflight(func() (string, error) {
		if time.Since(lastUpdate) < 6*time.Hour && cachedToken != "" {
			return cachedToken, nil
		}

		token, err := fetchRedditGuestToken()
		if err != nil {
			if cachedToken != "" {
				return cachedToken, nil
			}
			return "", err
		}

		lastUpdate = time.Now()
		cachedToken = token
		return token, nil
	})
}()

// Solves Reddit's JS challenge, which hands out a token_v2 guest token usable on oauth.reddit.com.
func fetchRedditGuestToken() (string, error) {
	request, err := http.NewRequest("GET", "https://www.reddit.com/", nil)
	if err != nil {
		return "", err
	}

	setBrowserUserAgentHeader(request)

	response, err := redditHTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d when requesting challenge page", response.StatusCode)
	}

	challengeBody, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}

	challengeMatches := redditChallengePattern.FindSubmatch(challengeBody)
	tokenMatches := redditTokenPattern.FindSubmatch(challengeBody)

	if challengeMatches == nil {
		return "", fmt.Errorf("no JS challenge found")
	}

	if tokenMatches == nil {
		return "", fmt.Errorf("no token found in challenge page")
	}

	challengeStr := string(challengeMatches[1])
	token := string(tokenMatches[1])
	solution := challengeStr + challengeStr

	params := url.Values{
		"solution":     {solution},
		"js_challenge": {"1"},
		"jsc_token":    {token},
	}

	if origRMatches := redditOrigRPattern.FindSubmatch(challengeBody); origRMatches != nil {
		params.Set("jsc_orig_r", string(origRMatches[1]))
	}

	request, err = http.NewRequest("GET", "https://www.reddit.com/?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}

	setBrowserUserAgentHeader(request)

	response, err = redditHTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d when submitting challenge solution", response.StatusCode)
	}

	for _, cookie := range response.Cookies() {
		if cookie.Name == "token_v2" && cookie.Value != "" {
			return cookie.Value, nil
		}
	}

	return "", fmt.Errorf("no token_v2 cookie found")
}
