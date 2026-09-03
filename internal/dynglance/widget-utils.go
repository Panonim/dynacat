package dynglance

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var imgSrcPattern = regexp.MustCompile(`(?i)<img([^>]*)\ssrc="([^"]*)"([^>]*)>`)

func rewriteImgSrcs(ctx context.Context, html template.HTML, providers *widgetProviders) template.HTML {
	if providers == nil {
		return html
	}
	result := imgSrcPattern.ReplaceAllStringFunc(string(html), func(match string) string {
		parts := imgSrcPattern.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		originalSrc := parts[2]
		newSrc := providers.SecureImageURL(ctx, originalSrc, false)
		if newSrc == originalSrc {
			return match
		}
		fallback := ` data-fallback-src="` + strings.ReplaceAll(originalSrc, `"`, `&quot;`) + `"`
		return "<img" + parts[1] + ` src="` + newSrc + `"` + parts[3] + fallback + ">"
	})
	return template.HTML(result)
}

var (
	errNoContent      = errors.New("failed to retrieve any content")
	errPartialContent = errors.New("failed to retrieve some of the content")
)

const defaultClientTimeout = 5 * time.Second

var defaultHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 10,
		Proxy:               http.ProxyFromEnvironment,
	},
	Timeout: defaultClientTimeout,
}

var defaultInsecureHTTPClient = &http.Client{
	Timeout: defaultClientTimeout,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Proxy:           http.ProxyFromEnvironment,
	},
}

type requestDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var dynglanceUserAgentString = "DynGlance/" + resolveVersion() + " +https://github.com/trooperthorn/ha_app_dynglance"
var userAgentPersistentVersion atomic.Int32

func getBrowserUserAgentHeader() string {
	if rand.IntN(2000) == 0 {
		userAgentPersistentVersion.Store(rand.Int32N(5))
	}

	version := strconv.Itoa(130 + int(userAgentPersistentVersion.Load()))
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:" + version + ".0) Gecko/20100101 Firefox/" + version + ".0"
}

func setBrowserUserAgentHeader(request *http.Request) {
	request.Header.Set("User-Agent", getBrowserUserAgentHeader())
}

// fetchRequestBody fetches a request body, sharing the result with other
// widgets when the request is a cacheable GET, otherwise issuing it directly.
func fetchRequestBody(client requestDoer, request *http.Request) (int, []byte, error) {
	if request.Method == "" || request.Method == http.MethodGet {
		status, _, body, err := globalSharedFetcher.do(client, request, sharedFetchMaxAgeForRequest(request))
		return status, body, err
	}

	status, _, body, err := doRequestReadAll(client, request)
	return status, body, err
}

func decodeJsonFromRequest[T any](client requestDoer, request *http.Request) (T, error) {
	var result T

	status, body, err := fetchRequestBody(client, request)
	if err != nil {
		return result, err
	}

	if status != http.StatusOK {
		truncatedBody, _ := limitStringLength(string(body), 256)

		return result, fmt.Errorf(
			"unexpected status code %d from %s, response: %s",
			status,
			request.URL,
			truncatedBody,
		)
	}

	err = json.Unmarshal(body, &result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func decodeJsonFromRequestTask[T any](client requestDoer) func(*http.Request) (T, error) {
	return func(request *http.Request) (T, error) {
		return decodeJsonFromRequest[T](client, request)
	}
}

func decodeXmlFromRequest[T any](client requestDoer, request *http.Request) (T, error) {
	var result T

	status, body, err := fetchRequestBody(client, request)
	if err != nil {
		return result, err
	}

	if status != http.StatusOK {
		truncatedBody, _ := limitStringLength(string(body), 256)

		return result, fmt.Errorf(
			"unexpected status code %d for %s, response: %s",
			status,
			request.URL,
			truncatedBody,
		)
	}

	err = xml.Unmarshal(body, &result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func decodeXmlFromRequestTask[T any](client requestDoer) func(*http.Request) (T, error) {
	return func(request *http.Request) (T, error) {
		return decodeXmlFromRequest[T](client, request)
	}
}

type workerPoolTask[I any, O any] struct {
	index  int
	input  I
	output O
	err    error
}

type workerPoolJob[I any, O any] struct {
	data    []I
	workers int
	task    func(I) (O, error)
	ctx     context.Context
}

const defaultNumWorkers = 10

func (job *workerPoolJob[I, O]) withWorkers(workers int) *workerPoolJob[I, O] {
	if workers == 0 {
		job.workers = defaultNumWorkers
	} else {
		job.workers = min(workers, len(job.data))
	}

	return job
}

func newJob[I any, O any](task func(I) (O, error), data []I) *workerPoolJob[I, O] {
	return &workerPoolJob[I, O]{
		workers: defaultNumWorkers,
		task:    task,
		data:    data,
		ctx:     context.Background(),
	}
}

func workerPoolDo[I any, O any](job *workerPoolJob[I, O]) ([]O, []error, error) {
	results := make([]O, len(job.data))
	errs := make([]error, len(job.data))

	if len(job.data) == 0 {
		return results, errs, nil
	}

	if len(job.data) == 1 {
		results[0], errs[0] = job.task(job.data[0])
		return results, errs, nil
	}

	tasksQueue := make(chan *workerPoolTask[I, O])
	resultsQueue := make(chan *workerPoolTask[I, O])

	var wg sync.WaitGroup

	for range job.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for t := range tasksQueue {
				t.output, t.err = job.task(t.input)
				resultsQueue <- t
			}
		}()
	}

	var err error

	go func() {
	loop:
		for i := range job.data {
			select {
			default:
				tasksQueue <- &workerPoolTask[I, O]{
					index: i,
					input: job.data[i],
				}
			case <-job.ctx.Done():
				err = job.ctx.Err()
				break loop
			}
		}

		close(tasksQueue)
		wg.Wait()
		close(resultsQueue)
	}()

	for task := range resultsQueue {
		errs[task.index] = task.err
		results[task.index] = task.output
	}

	return results, errs, err
}
