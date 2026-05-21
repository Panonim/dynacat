package dynacat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type marketTestClient struct {
	requests     atomic.Int64
	response     string
	contextValue string
}

func (c *marketTestClient) Do(request *http.Request) (*http.Response, error) {
	c.requests.Add(1)
	if c.contextValue != "" && request.Context().Value(marketTestContextKey{}) != c.contextValue {
		panic("expected request context value")
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(c.response)),
	}, nil
}

func TestFetchMarketsDataFromYahooDedupesSymbols(t *testing.T) {
	client := &marketTestClient{response: marketResponseJSON("SPY", "SPDR S&P 500 ETF", []float64{100, 101, 102}), contextValue: "request-context"}
	ctx := context.WithValue(context.Background(), marketTestContextKey{}, "request-context")

	markets, err := fetchMarketsDataFromYahoo(ctx, []marketRequest{
		{Symbol: "SPY", CustomName: "First"},
		{Symbol: "SPY", CustomName: "Second"},
	}, 21, client)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if client.requests.Load() != 1 {
		t.Fatalf("Expected 1 request, got %d", client.requests.Load())
	}
	if len(markets) != 2 {
		t.Fatalf("Expected 2 markets, got %d", len(markets))
	}
	if markets[0].Name != "First" || markets[1].Name != "Second" {
		t.Fatalf("Expected duplicate rows to preserve request data, got %#v", markets)
	}
}

func TestMarketFromYahooResponseRejectsMissingQuote(t *testing.T) {
	response := marketResponseFromJSON(t, `{"chart":{"result":[{"meta":{},"indicators":{}}]}}`)

	_, err := marketFromYahooResponse(marketRequest{Symbol: "SPY"}, response, 21)
	if err == nil {
		t.Fatal("Expected missing quote error")
	}
}

func TestMarketFromYahooResponseTrimsChartDays(t *testing.T) {
	response := marketResponseFromJSON(t, marketResponseJSON("SPY", "SPDR S&P 500 ETF", []float64{100, 101, 102, 103}))

	market, err := marketFromYahooResponse(marketRequest{Symbol: "SPY"}, response, 2)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if market.SvgChartPoints != "0.00,49.00 100.00,1.00" {
		t.Fatalf("Expected trimmed chart points, got %q", market.SvgChartPoints)
	}
}

func TestMarketFromYahooResponseIgnoresNullPrices(t *testing.T) {
	response := marketResponseFromJSON(t, `{"chart":{"result":[{"meta":{"currency":"USD","regularMarketPrice":102,"chartPreviousClose":101,"shortName":"SPDR S&P 500 ETF","priceHint":2},"indicators":{"quote":[{"close":[100,null,102]}]}}]}}`)

	market, err := marketFromYahooResponse(marketRequest{Symbol: "SPY"}, response, 21)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if market.SvgChartPoints != "0.00,49.00 100.00,1.00" {
		t.Fatalf("Expected null price to be ignored, got %q", market.SvgChartPoints)
	}
}

func TestMarketFromYahooResponseIgnoresLeadingNullPrices(t *testing.T) {
	response := marketResponseFromJSON(t, `{"chart":{"result":[{"meta":{"currency":"USD","regularMarketPrice":102,"chartPreviousClose":101,"shortName":"SPDR S&P 500 ETF","priceHint":2},"indicators":{"quote":[{"close":[null,null,100,101,102]}]}}]}}`)

	market, err := marketFromYahooResponse(marketRequest{Symbol: "SPY"}, response, 21)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if market.SvgChartPoints != "0.00,49.00 50.00,25.00 100.00,1.00" {
		t.Fatalf("Expected leading null prices to be ignored, got %q", market.SvgChartPoints)
	}
}

func TestMarketFromYahooResponseKeepsTrailingNullFromShiftingPreviousClose(t *testing.T) {
	response := marketResponseFromJSON(t, `{"chart":{"result":[{"meta":{"currency":"USD","regularMarketPrice":102,"chartPreviousClose":101,"shortName":"SPDR S&P 500 ETF","priceHint":2},"indicators":{"quote":[{"close":[100,101,null]}]}}]}}`)

	market, err := marketFromYahooResponse(marketRequest{Symbol: "SPY"}, response, 21)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if market.PercentChange != percentChange(102, 101) {
		t.Fatalf("Expected trailing null not to shift previous close, got %f", market.PercentChange)
	}
}

func TestMarketFromYahooResponseSkipsNullGapBeforeCurrentPrice(t *testing.T) {
	response := marketResponseFromJSON(t, `{"chart":{"result":[{"meta":{"currency":"USD","regularMarketPrice":102,"chartPreviousClose":80,"shortName":"Bitcoin USD","priceHint":2},"indicators":{"quote":[{"close":[80,100,null,102]}]}}]}}`)

	market, err := marketFromYahooResponse(marketRequest{Symbol: "BTC-USD"}, response, 21)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if market.PercentChange != percentChange(102, 100) {
		t.Fatalf("Expected null gap to be skipped for previous close, got %f", market.PercentChange)
	}
}

type marketTestContextKey struct{}

func TestYahooChartRangeForDays(t *testing.T) {
	tests := []struct {
		days       int
		rangeValue string
	}{
		{21, "1mo"},
		{22, "3mo"},
		{64, "6mo"},
		{127, "1y"},
		{253, "2y"},
		{505, "5y"},
		{1261, "max"},
	}

	for _, test := range tests {
		if value := yahooChartRangeForDays(test.days); value != test.rangeValue {
			t.Fatalf("Expected %d days to use range %q, got %q", test.days, test.rangeValue, value)
		}
	}
}

func TestSvgPolylineCoordsFromYValuesHandlesFlatValues(t *testing.T) {
	points := svgPolylineCoordsFromYValues(100, 50, []float64{5, 5, 5})

	if points != "0.00,25.00 50.00,25.00 100.00,25.00" {
		t.Fatalf("Expected flat line points, got %q", points)
	}
}

func marketResponseFromJSON(t *testing.T, data string) marketResponseJson {
	t.Helper()

	var response marketResponseJson
	if err := json.NewDecoder(strings.NewReader(data)).Decode(&response); err != nil {
		t.Fatalf("Failed to decode market response: %v", err)
	}
	return response
}

func marketResponseJSON(symbol string, name string, closes []float64) string {
	prices := make([]string, 0, len(closes))
	for i := range closes {
		prices = append(prices, fmt.Sprintf("%.2f", closes[i]))
	}

	return `{"chart":{"result":[{"meta":{"currency":"USD","symbol":"` + symbol + `","regularMarketPrice":102,"chartPreviousClose":101,"shortName":"` + name + `","priceHint":2},"indicators":{"quote":[{"close":[` + strings.Join(prices, ",") + `]}]}}]}}`
}
