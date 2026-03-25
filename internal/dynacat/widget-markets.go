package dynacat

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

var marketsWidgetTemplate = mustParseTemplate("markets.html", "widget-base.html")

type yahooFinanceCrumbCache struct {
	sync.Mutex
	crumb   string
	cookies []*http.Cookie
}

var yahooFinanceCrumb = &yahooFinanceCrumbCache{}

func (c *yahooFinanceCrumbCache) get() (string, []*http.Cookie, error) {
	c.Lock()
	defer c.Unlock()

	if c.crumb != "" {
		return c.crumb, c.cookies, nil
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", nil, err
	}

	client := &http.Client{
		Jar:       jar,
		Timeout:   10 * time.Second,
		Transport: defaultHTTPClient.Transport,
	}

	// Visit fc.yahoo.com to obtain initial session cookies
	req, _ := http.NewRequest("GET", "https://fc.yahoo.com", nil)
	setBrowserUserAgentHeader(req)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}

	// Fetch the crumb token
	req, _ = http.NewRequest("GET", "https://query1.finance.yahoo.com/v1/test/getcrumb", nil)
	setBrowserUserAgentHeader(req)
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("getting Yahoo Finance crumb: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("reading Yahoo Finance crumb: %w", err)
	}

	crumb := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK || crumb == "" {
		return "", nil, fmt.Errorf("invalid Yahoo Finance crumb response (status %d)", resp.StatusCode)
	}

	u, _ := url.Parse("https://query1.finance.yahoo.com")

	c.crumb = crumb
	c.cookies = jar.Cookies(u)

	return crumb, c.cookies, nil
}

func (c *yahooFinanceCrumbCache) invalidate() {
	c.Lock()
	defer c.Unlock()
	c.crumb = ""
	c.cookies = nil
}

type marketsWidget struct {
	widgetBase         `yaml:",inline"`
	StocksRequests     []marketRequest `yaml:"stocks"`
	MarketRequests     []marketRequest `yaml:"markets"`
	ChartLinkTemplate  string          `yaml:"chart-link-template"`
	SymbolLinkTemplate string          `yaml:"symbol-link-template"`
	Sort               string          `yaml:"sort-by"`
	Markets            marketList      `yaml:"-"`
}

func (widget *marketsWidget) initialize() error {
	widget.withTitle("Markets").withCacheDuration(time.Hour)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(10 * time.Minute)
		widget.UpdateInterval = &interval
	}

	// legacy support, remove in v0.10.0
	if len(widget.MarketRequests) == 0 {
		widget.MarketRequests = widget.StocksRequests
	}

	for i := range widget.MarketRequests {
		m := &widget.MarketRequests[i]

		if widget.ChartLinkTemplate != "" && m.ChartLink == "" {
			m.ChartLink = strings.ReplaceAll(widget.ChartLinkTemplate, "{SYMBOL}", m.Symbol)
		}

		if widget.SymbolLinkTemplate != "" && m.SymbolLink == "" {
			m.SymbolLink = strings.ReplaceAll(widget.SymbolLinkTemplate, "{SYMBOL}", m.Symbol)
		}
	}

	return nil
}

func (widget *marketsWidget) update(ctx context.Context) {
	markets, err := fetchMarketsDataFromYahoo(widget.MarketRequests)

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	if widget.Sort == "absolute-change" {
		markets.sortByAbsChange()
	} else if widget.Sort == "change" {
		markets.sortByChange()
	}

	widget.Markets = markets
}

func (widget *marketsWidget) Render() template.HTML {
	return widget.renderTemplate(widget, marketsWidgetTemplate)
}

type marketRequest struct {
	CustomName string `yaml:"name"`
	Symbol     string `yaml:"symbol"`
	ChartLink  string `yaml:"chart-link"`
	SymbolLink string `yaml:"symbol-link"`
}

type market struct {
	marketRequest
	Name           string
	Currency       string
	Price          float64
	PriceHint      int
	PercentChange  float64
	SvgChartPoints string
}

type marketList []market

func (t marketList) sortByAbsChange() {
	sort.Slice(t, func(i, j int) bool {
		return math.Abs(t[i].PercentChange) > math.Abs(t[j].PercentChange)
	})
}

func (t marketList) sortByChange() {
	sort.Slice(t, func(i, j int) bool {
		return t[i].PercentChange > t[j].PercentChange
	})
}

type marketResponseJson struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Currency           string  `json:"currency"`
				Symbol             string  `json:"symbol"`
				RegularMarketPrice float64 `json:"regularMarketPrice"`
				ChartPreviousClose float64 `json:"chartPreviousClose"`
				ShortName          string  `json:"shortName"`
				PriceHint          int     `json:"priceHint"`
			} `json:"meta"`
			Indicators struct {
				Quote []struct {
					Close []float64 `json:"close,omitempty"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

// TODO: allow changing chart time frame
const marketChartDays = 21

func fetchMarketsDataFromYahoo(marketRequests []marketRequest) (marketList, error) {
	crumb, cookies, err := yahooFinanceCrumb.get()
	if err != nil {
		slog.Warn("Failed to get Yahoo Finance crumb, requests may be rate limited", "error", err)
		crumb = ""
		cookies = nil
	}

	requests := make([]*http.Request, 0, len(marketRequests))

	for i := range marketRequests {
		urlStr := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s?range=1mo&interval=1d", marketRequests[i].Symbol)
		if crumb != "" {
			urlStr += "&crumb=" + url.QueryEscape(crumb)
		}
		request, _ := http.NewRequest("GET", urlStr, nil)
		setBrowserUserAgentHeader(request)
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		requests = append(requests, request)
	}

	job := newJob(decodeJsonFromRequestTask[marketResponseJson](defaultHTTPClient), requests)
	responses, errs, err := workerPoolDo(job)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoContent, err)
	}

	markets := make(marketList, 0, len(responses))
	var failed int
	var authFailed int

	for i := range responses {
		if errs[i] != nil {
			failed++
			if strings.Contains(errs[i].Error(), "status code 429") || strings.Contains(errs[i].Error(), "status code 401") {
				authFailed++
			}
			slog.Error("Failed to fetch market data", "symbol", marketRequests[i].Symbol, "error", errs[i])
			continue
		}

		response := responses[i]

		if len(response.Chart.Result) == 0 {
			failed++
			slog.Error("Market response contains no data", "symbol", marketRequests[i].Symbol)
			continue
		}

		result := &response.Chart.Result[0]
		prices := result.Indicators.Quote[0].Close

		if len(prices) > marketChartDays {
			prices = prices[len(prices)-marketChartDays:]
		}

		previous := result.Meta.ChartPreviousClose

		if len(prices) >= 2 && prices[len(prices)-2] != 0 {
			previous = prices[len(prices)-2]
		}

		points := svgPolylineCoordsFromYValues(100, 50, maybeCopySliceWithoutZeroValues(prices))

		currency, exists := currencyToSymbol[strings.ToUpper(result.Meta.Currency)]
		if !exists {
			currency = result.Meta.Currency
		}

		markets = append(markets, market{
			marketRequest: marketRequests[i],
			Price:         result.Meta.RegularMarketPrice,
			Currency:      currency,
			PriceHint:     result.Meta.PriceHint,
			Name: ternary(marketRequests[i].CustomName == "",
				result.Meta.ShortName,
				marketRequests[i].CustomName,
			),
			PercentChange: percentChange(
				result.Meta.RegularMarketPrice,
				previous,
			),
			SvgChartPoints: points,
		})
	}

	if authFailed > 0 {
		yahooFinanceCrumb.invalidate()
	}

	if len(markets) == 0 {
		return nil, errNoContent
	}

	if failed > 0 {
		return markets, fmt.Errorf("%w: could not fetch data for %d market(s)", errPartialContent, failed)
	}

	return markets, nil
}

var currencyToSymbol = map[string]string{
	"USD": "$",
	"EUR": "€",
	"JPY": "¥",
	"CAD": "C$",
	"AUD": "A$",
	"GBP": "£",
	"CHF": "Fr",
	"NZD": "N$",
	"INR": "₹",
	"BRL": "R$",
	"RUB": "₽",
	"TRY": "₺",
	"ZAR": "R",
	"CNY": "¥",
	"KRW": "₩",
	"HKD": "HK$",
	"SGD": "S$",
	"SEK": "kr",
	"NOK": "kr",
	"DKK": "kr",
	"PLN": "zł",
	"PHP": "₱",
}
