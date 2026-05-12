package dynacat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var dockerControllerWidgetTemplate = mustParseTemplate("docker-controller.html", "widget-base.html")

var (
	validDockerIDPattern  = regexp.MustCompile(`^[0-9a-f]{12}([0-9a-f]{52})?$`)
	validImageNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\-/:@]*$`)
	dockerCgroupIDPattern = regexp.MustCompile(`[0-9a-f]{64}`)
)

type dockerActivePull struct {
	ID        string
	Image     string
	mu        sync.Mutex
	layers    map[string][2]int64 // layerID -> [current, total]
	done      bool
	err       string
	startedAt time.Time
}

func (p *dockerActivePull) setLayerProgress(id string, current, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.layers[id] = [2]int64{current, total}
}

func (p *dockerActivePull) percent() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var cur, tot int64
	for _, v := range p.layers {
		cur += v[0]
		tot += v[1]
	}
	if tot == 0 {
		return 0
	}
	pct := int(cur * 100 / tot)
	if pct > 99 {
		pct = 99 // reserve 100 for fully done
	}
	return pct
}

type dockerControllerWidget struct {
	widgetBase    `yaml:",inline"`
	Frameless     bool                  `yaml:"frameless"`
	SockPath      string                `yaml:"sock-path"`
	Show          string                `yaml:"show"`
	FormatNames   bool                  `yaml:"format-container-names"`
	CollapseAfter int                   `yaml:"collapse-after"`
	Containers    []dockerCtrlContainer `yaml:"-"`
	Images        []dockerCtrlImage     `yaml:"-"`
	selfID        string
	selfImageName string
	pullsMu       sync.Mutex
	pulls         map[string]*dockerActivePull
	pullsSeq      uint64
}

type dockerCtrlContainer struct {
	ID        string
	Name      string
	Image     string
	State     string
	StateText string
	StateIcon string
}

type dockerCtrlImage struct {
	ID            string
	Tags          []string
	SizeHuman     string
	ExtraTagCount int
}

type dockerCtrlContainerJSON struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

type dockerCtrlImageJSON struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Size     int64    `json:"Size"`
}

func (widget *dockerControllerWidget) initialize() error {
	widget.withTitle("Docker").withCacheDuration(15 * time.Second)

	if widget.SockPath == "" {
		widget.SockPath = "/var/run/docker.sock"
	}

	if widget.Show == "" {
		widget.Show = "both"
	}

	if widget.Show != "both" && widget.Show != "containers" && widget.Show != "images" {
		return fmt.Errorf("show must be one of: both, containers, images")
	}

	if widget.CollapseAfter <= 0 {
		widget.CollapseAfter = 4
	}

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(30 * time.Second)
		widget.UpdateInterval = &interval
	}

	if *widget.UpdateInterval <= 0 {
		return fmt.Errorf("update-interval must be greater than 0")
	}

	widget.pulls = make(map[string]*dockerActivePull)

	return nil
}

func getSelfContainerID() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	m := dockerCgroupIDPattern.Find(data)
	if len(m) < 12 {
		return ""
	}
	return string(m[:12])
}

func newDockerCtrlClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

func (widget *dockerControllerWidget) update(ctx context.Context) {
	client := newDockerCtrlClient(widget.SockPath)

	selfID := getSelfContainerID()

	var containers []dockerCtrlContainer
	var images []dockerCtrlImage
	var updateErr error
	var selfImageName string

	if widget.Show != "images" {
		c, imgName, err := fetchDockerCtrlContainers(client, widget.FormatNames, selfID)
		if err != nil {
			updateErr = fmt.Errorf("fetching containers: %w", err)
		} else {
			containers = c
			selfImageName = imgName
		}
	} else {
		// Still need self image name to filter images even if not showing containers.
		// Fetch it from the containers endpoint without showing results.
		_, imgName, _ := fetchDockerCtrlContainers(client, false, selfID)
		selfImageName = imgName
	}

	if updateErr == nil && widget.Show != "containers" {
		i, err := fetchDockerCtrlImages(client, selfImageName)
		if err != nil {
			updateErr = fmt.Errorf("fetching images: %w", err)
		} else {
			images = i
		}
	}

	if !widget.canContinueUpdateAfterHandlingErr(updateErr) {
		return
	}

	widget.selfID = selfID
	widget.selfImageName = selfImageName
	widget.Containers = containers
	widget.Images = images
}

func (widget *dockerControllerWidget) Render() template.HTML {
	return widget.renderTemplate(widget, dockerControllerWidgetTemplate)
}

func (widget *dockerControllerWidget) handleRequest(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	parts := strings.Split(strings.Trim(action, "/"), "/")

	if len(parts) < 2 {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}

	client := newDockerCtrlClient(widget.SockPath)

	switch {
	case parts[0] == "containers" && len(parts) == 3:
		id := parts[1]
		op := parts[2]
		if !validDockerIDPattern.MatchString(id) {
			http.Error(w, "invalid container ID", http.StatusBadRequest)
			return
		}
		if widget.selfID != "" && id == widget.selfID {
			http.Error(w, "cannot control own container", http.StatusForbidden)
			return
		}
		if op != "start" && op != "stop" && op != "restart" && op != "remove" {
			http.Error(w, "unknown container action", http.StatusBadRequest)
			return
		}
		if err := dockerCtrlContainerAction(client, id, op); err != nil {
			http.Error(w, "action failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case parts[0] == "images" && len(parts) == 2 && parts[1] == "pull":
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var body struct {
			Image string `json:"image"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if body.Image == "" || !validImageNamePattern.MatchString(body.Image) {
			http.Error(w, "invalid image name", http.StatusBadRequest)
			return
		}

		widget.pullsMu.Lock()
		widget.pullsSeq++
		pullID := fmt.Sprintf("%x", widget.pullsSeq)
		pull := &dockerActivePull{
			ID:        pullID,
			Image:     body.Image,
			layers:    make(map[string][2]int64),
			startedAt: time.Now(),
		}
		widget.pulls[pullID] = pull
		widget.pullsMu.Unlock()

		go func() {
			err := dockerCtrlPullImageWithProgress(newDockerCtrlClient(widget.SockPath), body.Image, pull)
			pull.mu.Lock()
			pull.done = true
			if err != nil {
				pull.err = err.Error()
			}
			pull.mu.Unlock()

			time.AfterFunc(60*time.Second, func() {
				widget.pullsMu.Lock()
				delete(widget.pulls, pullID)
				widget.pullsMu.Unlock()
			})
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"pullId": pullID}) //nolint:errcheck

	case parts[0] == "images" && len(parts) == 4 && parts[1] == "pull" && parts[3] == "status":
		pullID := parts[2]
		widget.pullsMu.Lock()
		pull := widget.pulls[pullID]
		widget.pullsMu.Unlock()
		if pull == nil {
			http.Error(w, "pull not found", http.StatusNotFound)
			return
		}
		pull.mu.Lock()
		done := pull.done
		errStr := pull.err
		pull.mu.Unlock()
		pct := pull.percent()
		if done && errStr == "" {
			pct = 100
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"percent": pct, "done": done, "error": errStr}) //nolint:errcheck

	case parts[0] == "images" && len(parts) == 3 && parts[2] == "remove":
		id := parts[1]
		if !validDockerIDPattern.MatchString(id) {
			http.Error(w, "invalid image ID", http.StatusBadRequest)
			return
		}
		if err := dockerCtrlRemoveImage(client, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func fetchDockerCtrlContainers(client *http.Client, formatNames bool, selfID string) ([]dockerCtrlContainer, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/containers/json?all=true", nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("docker API returned status %d", resp.StatusCode)
	}

	var raw []dockerCtrlContainerJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, "", fmt.Errorf("decoding response: %w", err)
	}

	var selfImageName string
	containers := make([]dockerCtrlContainer, 0, len(raw))
	for _, c := range raw {
		shortID := c.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		if selfID != "" && shortID == selfID {
			selfImageName = c.Image
			continue
		}

		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimLeft(c.Names[0], "/")
		}
		if formatNames && name != "" {
			name = formatDockerCtrlName(name)
		}

		containers = append(containers, dockerCtrlContainer{
			ID:        shortID,
			Name:      name,
			Image:     c.Image,
			State:     strings.ToLower(c.State),
			StateText: strings.ToLower(c.Status),
			StateIcon: dockerContainerStateToStateIcon(strings.ToLower(c.State)),
		})
	}

	return containers, selfImageName, nil
}

func fetchDockerCtrlImages(client *http.Client, selfImageName string) ([]dockerCtrlImage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/images/json", nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned status %d", resp.StatusCode)
	}

	var raw []dockerCtrlImageJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	images := make([]dockerCtrlImage, 0, len(raw))
	for _, img := range raw {
		shortID := img.ID
		if colonIdx := strings.LastIndex(shortID, ":"); colonIdx >= 0 {
			shortID = shortID[colonIdx+1:]
		}
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		filteredTags := make([]string, 0, len(img.RepoTags))
		for _, t := range img.RepoTags {
			if t != "<none>:<none>" {
				filteredTags = append(filteredTags, t)
			}
		}

		if len(filteredTags) == 0 {
			continue
		}

		if selfImageName != "" {
			isSelf := false
			for _, t := range filteredTags {
				if t == selfImageName {
					isSelf = true
					break
				}
			}
			if isSelf {
				continue
			}
		}

		extraCount := 0
		if len(filteredTags) > 1 {
			extraCount = len(filteredTags) - 1
		}

		images = append(images, dockerCtrlImage{
			ID:            shortID,
			Tags:          filteredTags,
			SizeHuman:     formatDockerImageSize(img.Size),
			ExtraTagCount: extraCount,
		})
	}

	return images, nil
}

func formatDockerCtrlName(name string) string {
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	words := strings.Split(name, " ")
	for i := range words {
		if len(words[i]) > 0 {
			words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
		}
	}
	return strings.Join(words, " ")
}

func formatDockerImageSize(bytes int64) string {
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.0f KB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.0f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
}

func dockerCtrlContainerAction(client *http.Client, id, action string) error {
	var method, path string

	switch action {
	case "start":
		method, path = "POST", "/containers/"+id+"/start"
	case "stop":
		method, path = "POST", "/containers/"+id+"/stop"
	case "restart":
		method, path = "POST", "/containers/"+id+"/restart"
	case "remove":
		method, path = "DELETE", "/containers/"+id
	default:
		return fmt.Errorf("unknown action: %s", action)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker API returned status %d", resp.StatusCode)
	}

	return nil
}

func dockerCtrlPullImageWithProgress(client *http.Client, image string, pull *dockerActivePull) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pullURL := "http://docker/images/create?fromImage=" + url.QueryEscape(image)
	req, err := http.NewRequestWithContext(ctx, "POST", pullURL, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return fmt.Errorf("docker API status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var event struct {
			ID          string `json:"id"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Error != "" {
			return fmt.Errorf("docker pull error: %s", event.Error)
		}
		if event.ErrorDetail.Message != "" {
			return fmt.Errorf("docker pull error: %s", event.ErrorDetail.Message)
		}
		if event.ID != "" && event.ProgressDetail.Total > 0 {
			pull.setLayerProgress(event.ID, event.ProgressDetail.Current, event.ProgressDetail.Total)
		}
	}
	return scanner.Err()
}

func dockerCtrlRemoveImage(client *http.Client, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "DELETE", "http://docker/images/"+id, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var dockerErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &dockerErr) == nil && dockerErr.Message != "" {
			return fmt.Errorf("%s", dockerErr.Message)
		}
		return fmt.Errorf("docker API returned status %d", resp.StatusCode)
	}

	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	return nil
}
