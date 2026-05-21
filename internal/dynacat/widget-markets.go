package dynacat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

var marketsWidgetTemplate = mustParseTemplate("markets.html", "widget-base.html")

const defaultMarketChartDays = 21

type marketsWidget struct {
	widgetBase         `yaml:",inline"`
	Frameless          bool            `yaml:"frameless"`
	StocksRequests     []marketRequest `yaml:"stocks"`
	MarketRequests     []marketRequest `yaml:"markets"`
	ChartLinkTemplate  string          `yaml:"chart-link-template"`
	SymbolLinkTemplate string          `yaml:"symbol-link-template"`
	ChartDays          int             `yaml:"chart-days"`
	Minimal            bool            `yaml:"minimal"`
	Sort               string          `yaml:"sort-by"`
	Proxy              string          `yaml:"proxy"`
	Markets            marketList      `yaml:"-"`
	httpClient         *http.Client    `yaml:"-"`
}

func (widget *marketsWidget) MinimalNameWidth() string {
	width := 0

	for i := range widget.Markets {
		name := widget.Markets[i].CustomName
		if name == "" {
			name = widget.Markets[i].Symbol
		}

		width = max(width, len([]rune(name)))
	}

	if width > 12 {
		width = 12
	}

	if width < 3 {
		width = 3
	}

	return fmt.Sprintf("%dch", width)
}

func (widget *marketsWidget) initialize() error {
	widget.withTitle("Markets").withCacheDuration(time.Hour)
	widget.LazyLoad = true

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(10 * time.Minute)
		widget.UpdateInterval = &interval
	}

	switch {
	case widget.ChartDays == 0:
		widget.ChartDays = defaultMarketChartDays
	case widget.ChartDays < 2:
		return errors.New("chart-days must be greater than 1")
	}

	transport := &http.Transport{
		MaxIdleConnsPerHost: 10,
		Proxy:               http.ProxyFromEnvironment,
	}

	if widget.Proxy != "" {
		proxyURL, err := url.Parse(widget.Proxy)
		if err != nil {
			return fmt.Errorf("invalid proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	widget.httpClient = &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	// legacy support, remove in v0.10.0
	if len(widget.MarketRequests) == 0 {
		widget.MarketRequests = widget.StocksRequests
	}

	if len(widget.MarketRequests) == 0 {
		return errors.New("markets must contain at least one market")
	}

	if widget.Sort != "" && widget.Sort != "change" && widget.Sort != "absolute-change" {
		return errors.New("sort-by must be one of: change, absolute-change")
	}

	for i := range widget.MarketRequests {
		m := &widget.MarketRequests[i]

		if strings.TrimSpace(m.Symbol) == "" {
			return errors.New("market symbol is required")
		}
		m.Symbol = strings.TrimSpace(m.Symbol)

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
	markets, err := fetchMarketsDataFromYahoo(ctx, widget.MarketRequests, widget.ChartDays, widget.httpClient)

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
	CustomName   string `yaml:"name"`
	Symbol       string `yaml:"symbol"`
	ChartLink    string `yaml:"chart-link"`
	SymbolLink   string `yaml:"symbol-link"`
	InvertColors bool   `yaml:"invert-colors"`
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
					Close marketClosePrices `json:"close,omitempty"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

type marketClosePrices []float64

func (prices *marketClosePrices) UnmarshalJSON(data []byte) error {
	var values []*float64
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}

	closePrices := make([]float64, 0, len(values))
	seenPrice := false
	for i := range values {
		if values[i] == nil && !seenPrice {
			continue
		}

		if values[i] != nil {
			seenPrice = true
			closePrices = append(closePrices, *values[i])
		} else {
			closePrices = append(closePrices, 0)
		}
	}

	*prices = closePrices
	return nil
}

func fetchMarketsDataFromYahoo(ctx context.Context, marketRequests []marketRequest, chartDays int, client requestDoer) (marketList, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if chartDays == 0 {
		chartDays = defaultMarketChartDays
	}

	symbols := make([]string, 0, len(marketRequests))
	seenSymbols := make(map[string]struct{}, len(marketRequests))

	for i := range marketRequests {
		symbol := strings.TrimSpace(marketRequests[i].Symbol)
		if symbol == "" {
			continue
		}

		if _, ok := seenSymbols[symbol]; ok {
			continue
		}

		seenSymbols[symbol] = struct{}{}
		symbols = append(symbols, symbol)
	}

	requests := make([]*http.Request, 0, len(symbols))
	rangeParam := yahooChartRangeForDays(chartDays)

	for i := range symbols {
		request, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s?range=%s&interval=1d", url.PathEscape(symbols[i]), rangeParam), nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errNoContent, err)
		}

		setBrowserUserAgentHeader(request)
		requests = append(requests, request)
	}

	job := newJob(decodeJsonFromRequestTask[marketResponseJson](client), requests)
	responses, errs, err := workerPoolDo(job)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoContent, err)
	}

	responsesBySymbol := make(map[string]marketResponseJson, len(responses))
	errsBySymbol := make(map[string]error, len(responses))
	var failed int

	for i := range responses {
		if errs[i] != nil {
			errsBySymbol[symbols[i]] = errs[i]
			slog.Error("Failed to fetch market data", "symbol", symbols[i], "error", errs[i])
			continue
		}

		responsesBySymbol[symbols[i]] = responses[i]
	}

	markets := make(marketList, 0, len(marketRequests))

	for i := range marketRequests {
		request := marketRequests[i]
		symbol := strings.TrimSpace(request.Symbol)

		if err := errsBySymbol[symbol]; err != nil {
			failed++
			continue
		}

		response, ok := responsesBySymbol[symbol]
		if !ok {
			failed++
			slog.Error("Market response contains no data", "symbol", symbol)
			continue
		}

		market, err := marketFromYahooResponse(request, response, chartDays)
		if err != nil {
			failed++
			slog.Error("Failed to parse market data", "symbol", symbol, "error", err)
			continue
		}

		markets = append(markets, market)
	}

	if len(markets) == 0 {
		return nil, errNoContent
	}

	if failed > 0 {
		return markets, fmt.Errorf("%w: could not fetch data for %d market(s)", errPartialContent, failed)
	}

	return markets, nil
}

func yahooChartRangeForDays(days int) string {
	switch {
	case days <= 21:
		return "1mo"
	case days <= 63:
		return "3mo"
	case days <= 126:
		return "6mo"
	case days <= 252:
		return "1y"
	case days <= 504:
		return "2y"
	case days <= 1260:
		return "5y"
	default:
		return "max"
	}
}

func marketFromYahooResponse(request marketRequest, response marketResponseJson, chartDays int) (market, error) {
	if chartDays < 2 {
		chartDays = 2
	}

	if len(response.Chart.Result) == 0 {
		return market{}, errors.New("response contains no result")
	}

	result := &response.Chart.Result[0]
	if len(result.Indicators.Quote) == 0 {
		return market{}, errors.New("response contains no quote")
	}

	prices := result.Indicators.Quote[0].Close
	if len(prices) == 0 {
		return market{}, errors.New("response contains no prices")
	}

	if len(prices) > chartDays {
		prices = prices[len(prices)-chartDays:]
	}

	previous := result.Meta.ChartPreviousClose
	if previous == 0 {
		previous = result.Meta.RegularMarketPrice
	}

	// Yahoo can insert null daily bars before the current price, so use the nearest real close.
	for i := len(prices) - 2; i >= 0; i-- {
		if prices[i] != 0 {
			previous = prices[i]
			break
		}
	}

	points := svgPolylineCoordsFromYValues(100, 50, maybeCopySliceWithoutZeroValues(prices))

	currency, exists := currencyToSymbol[strings.ToUpper(result.Meta.Currency)]
	if !exists {
		currency = result.Meta.Currency
	}

	return market{
		marketRequest: request,
		Price:         result.Meta.RegularMarketPrice,
		Currency:      currency,
		PriceHint:     result.Meta.PriceHint,
		Name: ternary(request.CustomName == "",
			result.Meta.ShortName,
			request.CustomName,
		),
		PercentChange: percentChange(
			result.Meta.RegularMarketPrice,
			previous,
		),
		SvgChartPoints: points,
	}, nil
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
