package dynacat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

var customAPIWidgetTemplate = mustParseTemplate("custom-api.html", "widget-base.html")

// Needs to be exported for the YAML unmarshaler to work
type CustomAPIRequest struct {
	URL                string               `yaml:"url"`
	AllowInsecure      bool                 `yaml:"allow-insecure"`
	Headers            map[string]string    `yaml:"headers"`
	Parameters         queryParametersField `yaml:"parameters"`
	Method             string               `yaml:"method"`
	BodyType           string               `yaml:"body-type"`
	Body               any                  `yaml:"body"`
	SkipJSONValidation bool                 `yaml:"skip-json-validation"`
	bodyReader         io.ReadSeeker        `yaml:"-"`
	httpRequest        *http.Request        `yaml:"-"`
}

type customAPIWidget struct {
	widgetBase        `yaml:",inline"`
	*CustomAPIRequest `yaml:",inline"`
	Subrequests       map[string]*CustomAPIRequest `yaml:"subrequests"`
	Options           customAPIOptions             `yaml:"options"`
	Template          string                       `yaml:"template"`
	Frameless         bool                         `yaml:"frameless"`
	// Serialized layout of the visual editor, never read while rendering.
	Builder          string             `yaml:"builder"`
	compiledTemplate *template.Template `yaml:"-"`
	CompiledHTML     template.HTML      `yaml:"-"`
	APIResponse      json.RawMessage    `yaml:"-"`
}

func (widget *customAPIWidget) initialize() error {
	widget.withTitle("Custom API").withCacheDuration(1 * time.Minute)

	if err := widget.CustomAPIRequest.initialize(); err != nil {
		return fmt.Errorf("initializing primary request: %v", err)
	}

	for key := range widget.Subrequests {
		if err := widget.Subrequests[key].initialize(); err != nil {
			return fmt.Errorf("initializing subrequest %q: %v", key, err)
		}
	}

	if widget.Template == "" {
		return errors.New("template is required")
	}

	compiledTemplate, err := template.New("").Funcs(customAPITemplateFuncs(nil)).Parse(widget.Template)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}

	widget.compiledTemplate = compiledTemplate

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(10 * time.Second)
		widget.UpdateInterval = &interval
	}

	if *widget.UpdateInterval <= 0 {
		return errors.New("update-interval must be greater than 0")
	}

	return nil
}

func (widget *customAPIWidget) update(ctx context.Context) {
	widget.Hidden = false
	compiledHTML, hidden, rawResponse, err := fetchAndRenderCustomAPIRequest(
		widget.CustomAPIRequest, widget.Subrequests, widget.Options, widget.compiledTemplate,
	)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	widget.APIResponse = rawResponse
	widget.Hidden = hidden
	widget.CompiledHTML = rewriteImgSrcs(ctx, compiledHTML, widget.Providers)
}

func (widget *customAPIWidget) setProviders(providers *widgetProviders) {
	widget.widgetBase.setProviders(providers)
	if widget.Template == "" {
		return
	}

	compiledTemplate, err := template.New("").Funcs(customAPITemplateFuncs(providers)).Parse(widget.Template)
	if err != nil {
		slog.Error("Failed to recompile custom API template", "error", err)
		return
	}

	widget.compiledTemplate = compiledTemplate
}

func (widget *customAPIWidget) Render() template.HTML {
	return widget.renderTemplate(widget, customAPIWidgetTemplate)
}

type customAPIOptions map[string]any

func (o *customAPIOptions) StringOr(key, defaultValue string) string {
	return customAPIGetOptionOrDefault(*o, key, defaultValue)
}

func (o *customAPIOptions) IntOr(key string, defaultValue int) int {
	return customAPIGetOptionOrDefault(*o, key, defaultValue)
}

func (o *customAPIOptions) FloatOr(key string, defaultValue float64) float64 {
	return customAPIGetOptionOrDefault(*o, key, defaultValue)
}

func (o *customAPIOptions) BoolOr(key string, defaultValue bool) bool {
	return customAPIGetOptionOrDefault(*o, key, defaultValue)
}

func (o *customAPIOptions) JSON(key string) string {
	value, exists := (*o)[key]
	if !exists {
		panic(fmt.Sprintf("key %q does not exist in options", key))
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshaling %s: %v", key, err))
	}

	return string(encoded)
}

func customAPIGetOptionOrDefault[T any](o customAPIOptions, key string, defaultValue T) T {
	if value, exists := o[key]; exists {
		if typedValue, ok := value.(T); ok {
			return typedValue
		}
	}
	return defaultValue
}

func (req *CustomAPIRequest) initialize() error {
	if req == nil || req.URL == "" {
		return nil
	}

	if req.Body != nil {
		if req.Method == "" {
			req.Method = http.MethodPost
		}

		if req.BodyType == "" {
			req.BodyType = "json"
		}

		if req.BodyType != "json" && req.BodyType != "string" {
			return errors.New("invalid body type, must be either 'json' or 'string'")
		}

		switch req.BodyType {
		case "json":
			encoded, err := json.Marshal(req.Body)
			if err != nil {
				return fmt.Errorf("marshaling body: %v", err)
			}

			req.bodyReader = bytes.NewReader(encoded)
		case "string":
			bodyAsString, ok := req.Body.(string)
			if !ok {
				return errors.New("body must be a string when body-type is 'string'")
			}

			req.bodyReader = strings.NewReader(bodyAsString)
		}

	} else if req.Method == "" {
		req.Method = http.MethodGet
	}

	if !strings.Contains(req.URL, "://") {
		req.URL = "https://" + req.URL
	}

	httpReq, err := http.NewRequest(strings.ToUpper(req.Method), req.URL, req.bodyReader)
	if err != nil {
		return err
	}

	if len(req.Parameters) > 0 {
		httpReq.URL.RawQuery = req.Parameters.toQueryString()
	}

	if req.BodyType == "json" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	for key, value := range req.Headers {
		httpReq.Header.Add(key, value)
	}

	req.httpRequest = httpReq

	return nil
}

type customAPIResponseData struct {
	JSON     decoratedGJSONResult
	Response *http.Response
}

type customAPITemplateData struct {
	*customAPIResponseData
	subrequests map[string]*customAPIResponseData
	Options     customAPIOptions
}

func (data *customAPITemplateData) JSONLines() []decoratedGJSONResult {
	result := make([]decoratedGJSONResult, 0, 5)

	gjson.ForEachLine(data.JSON.Raw, func(line gjson.Result) bool {
		result = append(result, decoratedGJSONResult{line})
		return true
	})

	return result
}

func (data *customAPITemplateData) Subrequest(key string) *customAPIResponseData {
	req, exists := data.subrequests[key]
	if !exists {
		// Panic - Go's template engine recovers it and surfaces it as a template error.
		panic(fmt.Sprintf("subrequest with key %q has not been defined", key))
	}

	return req
}

func fetchCustomAPIResponse(ctx context.Context, req *CustomAPIRequest) (*customAPIResponseData, error) {
	if req == nil || req.URL == "" {
		return &customAPIResponseData{
			JSON:     decoratedGJSONResult{gjson.Result{}},
			Response: &http.Response{},
		}, nil
	}

	if req.bodyReader != nil {
		req.bodyReader.Seek(0, io.SeekStart)
	}

	client := ternary(req.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	resp, err := client.Do(req.httpRequest.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := readLimited(resp.Body)
	if err != nil {
		return nil, err
	}

	body := strings.TrimSpace(string(bodyBytes))

	if !req.SkipJSONValidation && body != "" && !gjson.Valid(body) {
		if 200 <= resp.StatusCode && resp.StatusCode < 300 {
			truncatedBody, isTruncated := limitStringLength(body, 100)
			if isTruncated {
				truncatedBody += "... <truncated>"
			}

			slog.Error("Invalid response JSON in custom API widget", "url", req.httpRequest.URL.String(), "body", truncatedBody)
			return nil, errors.New("invalid response JSON")
		}

		return nil, fmt.Errorf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))

	}

	return &customAPIResponseData{
		JSON:     decoratedGJSONResult{gjson.Parse(body)},
		Response: resp,
	}, nil
}

const customAPIHideWidgetSentinel = "\x00__dynacat_hide_widget__\x00"

func fetchAndRenderCustomAPIRequest(
	primaryReq *CustomAPIRequest,
	subReqs map[string]*CustomAPIRequest,
	options customAPIOptions,
	tmpl *template.Template,
) (template.HTML, bool, json.RawMessage, error) {
	var primaryData *customAPIResponseData
	subData := make(map[string]*customAPIResponseData, len(subReqs))
	var err error

	if len(subReqs) == 0 {
		primaryData, err = fetchCustomAPIResponse(context.Background(), primaryReq)
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var wg sync.WaitGroup
		var mu sync.Mutex // protects subData and err

		wg.Add(1)
		go func() {
			defer wg.Done()
			var localErr error
			primaryData, localErr = fetchCustomAPIResponse(ctx, primaryReq)
			mu.Lock()
			if localErr != nil && err == nil {
				err = localErr
				cancel()
			}
			mu.Unlock()
		}()

		for key, req := range subReqs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var localErr error
				var data *customAPIResponseData
				data, localErr = fetchCustomAPIResponse(ctx, req)
				mu.Lock()
				if localErr == nil {
					subData[key] = data
				} else if err == nil {
					err = localErr
					cancel()
				}
				mu.Unlock()
			}()
		}

		wg.Wait()
	}

	emptyBody := template.HTML("")

	if err != nil {
		return emptyBody, false, nil, err
	}

	// Kept so the widget can expose the upstream payload through the API.
	var rawResponse json.RawMessage
	if primaryData != nil && json.Valid([]byte(primaryData.JSON.Raw)) {
		rawResponse = json.RawMessage(primaryData.JSON.Raw)
	}

	body, hidden, err := renderCustomAPIData(primaryData, subData, options, tmpl)
	return body, hidden, rawResponse, err
}

func renderCustomAPIData(
	primaryData *customAPIResponseData,
	subData map[string]*customAPIResponseData,
	options customAPIOptions,
	tmpl *template.Template,
) (template.HTML, bool, error) {
	data := customAPITemplateData{
		customAPIResponseData: primaryData,
		subrequests:           subData,
		Options:               options,
	}

	var templateBuffer bytes.Buffer
	if err := tmpl.Execute(&templateBuffer, &data); err != nil {
		return "", false, err
	}

	output := templateBuffer.String()
	hidden := strings.Contains(output, customAPIHideWidgetSentinel)
	if hidden {
		output = strings.ReplaceAll(output, customAPIHideWidgetSentinel, "")
	}

	return template.HTML(output), hidden, nil
}

type decoratedGJSONResult struct {
	gjson.Result
}

func gJsonResultArrayToDecoratedResultArray(results []gjson.Result) []decoratedGJSONResult {
	decoratedResults := make([]decoratedGJSONResult, len(results))

	for i, result := range results {
		decoratedResults[i] = decoratedGJSONResult{result}
	}

	return decoratedResults
}

func (r *decoratedGJSONResult) Exists(key string) bool {
	return r.Result.Get(key).Exists()
}

func (r *decoratedGJSONResult) Array(key string) []decoratedGJSONResult {
	if key == "" {
		return gJsonResultArrayToDecoratedResultArray(r.Result.Array())
	}

	return gJsonResultArrayToDecoratedResultArray(r.Result.Get(key).Array())
}

func (r *decoratedGJSONResult) String(key string) string {
	if key == "" {
		return r.Result.String()
	}

	return r.Result.Get(key).String()
}

func (r *decoratedGJSONResult) Int(key string) int {
	if key == "" {
		return int(r.Result.Int())
	}

	return int(r.Result.Get(key).Int())
}

func (r *decoratedGJSONResult) Float(key string) float64 {
	if key == "" {
		return r.Result.Float()
	}

	return r.Result.Get(key).Float()
}

func (r *decoratedGJSONResult) Bool(key string) bool {
	if key == "" {
		return r.Result.Bool()
	}

	return r.Result.Get(key).Bool()
}

func (r *decoratedGJSONResult) Get(key string) *decoratedGJSONResult {
	return &decoratedGJSONResult{r.Result.Get(key)}
}

func customAPIDoMathOp[T int | float64](a, b T, op string) T {
	switch op {
	case "add":
		return a + b
	case "sub":
		return a - b
	case "mul":
		return a * b
	case "div":
		if b == 0 {
			return 0
		}
		return a / b
	}
	return 0
}

func customAPITemplateFuncs(providers *widgetProviders) template.FuncMap {
	var regexpCacheMu sync.Mutex
	var regexpCache = make(map[string]*regexp.Regexp)

	getCachedRegexp := func(pattern string) (*regexp.Regexp, error) {
		regexpCacheMu.Lock()
		defer regexpCacheMu.Unlock()

		regex, exists := regexpCache[pattern]
		if !exists {
			var err error
			regex, err = regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			regexpCache[pattern] = regex
		}

		return regex, nil
	}

	doMathOpWithAny := func(a, b any, op string) any {
		switch at := a.(type) {
		case int:
			switch bt := b.(type) {
			case int:
				return customAPIDoMathOp(at, bt, op)
			case float64:
				return customAPIDoMathOp(float64(at), bt, op)
			default:
				return math.NaN()
			}
		case float64:
			switch bt := b.(type) {
			case int:
				return customAPIDoMathOp(at, float64(bt), op)
			case float64:
				return customAPIDoMathOp(at, bt, op)
			default:
				return math.NaN()
			}
		default:
			return math.NaN()
		}
	}

	secureImageURL := func(rawURL string) string {
		if providers == nil {
			return rawURL
		}
		return providers.SecureImageURL(context.Background(), rawURL, false)
	}

	secureImageURLAllowInsecure := func(rawURL string) string {
		if providers == nil {
			return rawURL
		}
		return providers.SecureImageURL(context.Background(), rawURL, true)
	}

	funcs := template.FuncMap{
		"toFloat": func(a int) float64 {
			return float64(a)
		},
		"toInt": func(a float64) int {
			return int(a)
		},
		"formatBytes": func(v any) string {
			var b float64
			switch t := v.(type) {
			case int:
				b = float64(t)
			case int64:
				b = float64(t)
			case float64:
				b = t
			default:
				return "?"
			}

			units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
			i := 0
			for b >= 1024 && i < len(units)-1 {
				b /= 1024
				i++
			}

			return strconv.FormatFloat(b, 'f', ternary(i == 0, 0, 1), 64) + " " + units[i]
		},
		"add": func(a, b any) any {
			return doMathOpWithAny(a, b, "add")
		},
		"sub": func(a, b any) any {
			return doMathOpWithAny(a, b, "sub")
		},
		"mul": func(a, b any) any {
			return doMathOpWithAny(a, b, "mul")
		},
		"div": func(a, b any) any {
			return doMathOpWithAny(a, b, "div")
		},
		"mod": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a % b
		},
		"now": func() time.Time {
			return time.Now()
		},
		"offsetNow": func(offset string) time.Time {
			d, err := time.ParseDuration(offset)
			if err != nil {
				return time.Now()
			}
			return time.Now().Add(d)
		},
		"duration": func(str string) time.Duration {
			d, err := time.ParseDuration(str)
			if err != nil {
				return 0
			}

			return d
		},
		"parseTime": func(layout, value string) time.Time {
			return customAPIFuncParseTimeInLocation(layout, value, time.UTC)
		},
		"formatTime": customAPIFuncFormatTime,
		"parseLocalTime": func(layout, value string) time.Time {
			return customAPIFuncParseTimeInLocation(layout, value, time.Local)
		},
		"toRelativeTime": dynamicRelativeTimeAttrs,
		"parseRelativeTime": func(layout, value string) template.HTMLAttr {
			// Shorthand to do both of the above with a single function call
			return dynamicRelativeTimeAttrs(customAPIFuncParseTimeInLocation(layout, value, time.UTC))
		},
		"secureImageURL":              secureImageURL,
		"secureImageURLAllowInsecure": secureImageURLAllowInsecure,
		"startOfDay": func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		},
		"endOfDay": func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
		},
		// Args flipped so the piped value lands last, enabling chaining.
		"trimPrefix": func(prefix, s string) string {
			return strings.TrimPrefix(s, prefix)
		},
		"trimSuffix": func(suffix, s string) string {
			return strings.TrimSuffix(s, suffix)
		},
		"trimSpace": strings.TrimSpace,
		"replaceAll": func(old, new, s string) string {
			return strings.ReplaceAll(s, old, new)
		},
		"replaceMatches": func(pattern, replacement, s string) string {
			if s == "" {
				return ""
			}
			regex, err := getCachedRegexp(pattern)
			if err != nil {
				return ""
			}
			return regex.ReplaceAllString(s, replacement)
		},
		"findMatch": func(pattern, s string) string {
			if s == "" {
				return ""
			}
			regex, err := getCachedRegexp(pattern)
			if err != nil {
				return ""
			}
			return regex.FindString(s)
		},
		"findSubmatch": func(pattern, s string) string {
			if s == "" {
				return ""
			}
			regex, err := getCachedRegexp(pattern)
			if err != nil {
				return ""
			}
			return itemAtIndexOrDefault(regex.FindStringSubmatch(s), 1, "")
		},
		"percentChange": percentChange,
		"sortByString": func(key, order string, results []decoratedGJSONResult) []decoratedGJSONResult {
			sort.Slice(results, func(a, b int) bool {
				if order == "asc" {
					return results[a].String(key) < results[b].String(key)
				}

				return results[a].String(key) > results[b].String(key)
			})

			return results
		},
		"sortByInt": func(key, order string, results []decoratedGJSONResult) []decoratedGJSONResult {
			sort.Slice(results, func(a, b int) bool {
				if order == "asc" {
					return results[a].Int(key) < results[b].Int(key)
				}

				return results[a].Int(key) > results[b].Int(key)
			})

			return results
		},
		"sortByFloat": func(key, order string, results []decoratedGJSONResult) []decoratedGJSONResult {
			sort.Slice(results, func(a, b int) bool {
				if order == "asc" {
					return results[a].Float(key) < results[b].Float(key)
				}

				return results[a].Float(key) > results[b].Float(key)
			})

			return results
		},
		"sortByTime": func(key, layout, order string, results []decoratedGJSONResult) []decoratedGJSONResult {
			sort.Slice(results, func(a, b int) bool {
				timeA := customAPIFuncParseTimeInLocation(layout, results[a].String(key), time.UTC)
				timeB := customAPIFuncParseTimeInLocation(layout, results[b].String(key), time.UTC)

				if order == "asc" {
					return timeA.Before(timeB)
				}

				return timeA.After(timeB)
			})

			return results
		},
		"concat": func(items ...string) string {
			return strings.Join(items, "")
		},
		"unique": func(key string, results []decoratedGJSONResult) []decoratedGJSONResult {
			seen := make(map[string]struct{})
			out := make([]decoratedGJSONResult, 0, len(results))
			for _, result := range results {
				val := result.String(key)
				if _, ok := seen[val]; !ok {
					seen[val] = struct{}{}
					out = append(out, result)
				}
			}
			return out
		},
		"newRequest": func(url string) *CustomAPIRequest {
			return &CustomAPIRequest{
				URL: url,
			}
		},
		"withHeader": func(key, value string, req *CustomAPIRequest) *CustomAPIRequest {
			if req.Headers == nil {
				req.Headers = make(map[string]string)
			}
			req.Headers[key] = value
			return req
		},
		"withParameter": func(key, value string, req *CustomAPIRequest) *CustomAPIRequest {
			if req.Parameters == nil {
				req.Parameters = make(queryParametersField)
			}
			req.Parameters[key] = append(req.Parameters[key], value)
			return req
		},
		"withStringBody": func(body string, req *CustomAPIRequest) *CustomAPIRequest {
			req.Body = body
			req.BodyType = "string"
			return req
		},
		"getResponse": func(req *CustomAPIRequest) *customAPIResponseData {
			err := req.initialize()
			if err != nil {
				panic(fmt.Sprintf("initializing request: %v", err))
			}

			data, err := fetchCustomAPIResponse(context.Background(), req)
			if err != nil {
				slog.Error("Could not fetch response within custom API template", "error", err)
				return &customAPIResponseData{
					JSON: decoratedGJSONResult{gjson.Result{}},
					Response: &http.Response{
						Status: err.Error(),
					},
				}
			}

			return data
		},
		"hide": func() template.HTML {
			return template.HTML(customAPIHideWidgetSentinel)
		},
		"list": func(items ...any) []any {
			return items
		},
		"append": func(slice []any, items ...any) []any {
			return append(slice, items...)
		},
		"uniq": func(slice []any) []any {
			out := make([]any, 0, len(slice))
			for _, candidate := range slice {
				isDuplicate := false

				for _, existing := range out {
					if reflect.DeepEqual(existing, candidate) {
						isDuplicate = true
						break
					}
				}

				if !isDuplicate {
					out = append(out, candidate)
				}
			}

			return out
		},
		"sortAlpha": func(slice []any) []any {
			type sortableItem struct {
				value any
				key   string
			}

			sortKey := func(value any) string {
				if asString, ok := value.(string); ok {
					return asString
				}

				if encoded, err := json.Marshal(value); err == nil {
					return string(encoded)
				}

				return fmt.Sprint(value)
			}

			items := make([]sortableItem, len(slice))
			for i, value := range slice {
				items[i] = sortableItem{value: value, key: sortKey(value)}
			}

			sort.SliceStable(items, func(i, j int) bool {
				return items[i].key < items[j].key
			})

			out := make([]any, len(items))
			for i := range items {
				out[i] = items[i].value
			}

			return out
		},
	}

	for key, value := range globalTemplateFunctions {
		if _, exists := funcs[key]; !exists {
			funcs[key] = value
		}
	}

	return funcs
}

func customAPIFuncFormatTime(layout string, t time.Time) string {
	switch strings.ToLower(layout) {
	case "unix":
		return strconv.FormatInt(t.Unix(), 10)
	case "rfc3339":
		layout = time.RFC3339
	case "rfc3339nano":
		layout = time.RFC3339Nano
	case "datetime":
		layout = time.DateTime
	case "dateonly":
		layout = time.DateOnly
	}

	return t.Format(layout)
}

func customAPIFuncParseTimeInLocation(layout, value string, loc *time.Location) time.Time {
	switch strings.ToLower(layout) {
	case "unix":
		asInt, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Unix(0, 0)
		}

		return time.Unix(asInt, 0)
	case "rfc3339":
		layout = time.RFC3339
	case "rfc3339nano":
		layout = time.RFC3339Nano
	case "datetime":
		layout = time.DateTime
	case "dateonly":
		layout = time.DateOnly
	}

	parsed, err := time.ParseInLocation(layout, value, loc)
	if err != nil {
		return time.Unix(0, 0)
	}

	return parsed
}
