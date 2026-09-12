package dynacat

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	firstRunSetupAddr          = ":8080"
	firstRunSetupMaxBodyBytes  = 4 << 10
	firstRunMaxPageNameLength  = 40
	firstRunPresetEmpty        = "empty"
	firstRunDefaultPageName    = "Home"
	firstRunAllowedNameSymbols = " -_.()"
)

//go:embed starters
var starterFS embed.FS

var firstRunSetupTemplate = mustParseTemplate("first-run-setup.html")

// starterPreset drives both the setup page and the config it writes.
type starterPreset struct {
	Key         string
	Label       string
	DefaultName string
	Description string
	Widgets     []string
	Icon        template.HTML
	file        string
}

var starterPresets = []starterPreset{
	{
		Key:         firstRunPresetEmpty,
		Label:       "Empty page",
		DefaultName: firstRunDefaultPageName,
		Description: "One empty column, every widget added by you",
		Icon:        template.HTML(`<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v6m3-3H9m12 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />`),
	},
	{
		Key:         "startpage",
		Label:       "Startpage",
		DefaultName: "Startpage",
		Description: "A slim, centered launcher for your services and links",
		Widgets:     []string{"search", "monitor", "bookmarks"},
		Icon:        template.HTML(`<path stroke-linecap="round" stroke-linejoin="round" d="m2.25 12 8.954-8.955c.44-.439 1.152-.439 1.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75" />`),
		file:        "startpage.yml",
	},
	{
		Key:         "markets",
		Label:       "Markets",
		DefaultName: "Markets",
		Description: "Indices, crypto and stocks next to financial news",
		Widgets:     []string{"markets", "rss", "reddit", "videos"},
		Icon:        template.HTML(`<path stroke-linecap="round" stroke-linejoin="round" d="M2.25 18 9 11.25l4.306 4.307a11.95 11.95 0 0 1 5.814-5.518l2.74-1.22m0 0-5.94-2.28m5.94 2.28-2.28 5.941" />`),
		file:        "markets.yml",
	},
	{
		Key:         "gaming",
		Label:       "Gaming",
		DefaultName: "Gaming",
		Description: "Twitch charts, gaming subreddits and video channels",
		Widgets:     []string{"twitch", "reddit", "videos"},
		Icon:        template.HTML(`<path stroke-linecap="round" stroke-linejoin="round" d="M6 20.25h12m-7.5-3v3m3-3v3m-10.125-3h17.25c.621 0 1.125-.504 1.125-1.125V4.875c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v11.25c0 .621.504 1.125 1.125 1.125Z" />`),
		file:        "gaming.yml",
	},
}

type firstRunSetupView struct {
	ConfigPath    string
	MaxNameLength int
	Presets       []starterPreset
}

func findStarterPreset(key string) (starterPreset, bool) {
	for _, preset := range starterPresets {
		if preset.Key == key {
			return preset, true
		}
	}

	return starterPreset{}, false
}

// configIsMissingOrEmpty reports whether there is nothing usable at path yet.
func configIsMissingOrEmpty(path string) bool {
	stat, err := os.Stat(path)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}

	return !stat.IsDir() && stat.Size() == 0
}

type firstRunSetup struct {
	configPath string
	mu         sync.Mutex
	done       bool
	onDone     func()
}

func (s *firstRunSetup) isDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.done
}

// serveFirstRunSetupIfNoConfig blocks on a temporary server that writes the initial config.
func serveFirstRunSetupIfNoConfig(configPath string) error {
	if !configIsMissingOrEmpty(configPath) {
		return nil
	}

	slog.Warn(
		"No config file found, serving the first run setup page - anyone who can reach this server can create the initial config",
		"path", configPath,
	)

	mux := http.NewServeMux()
	server := &http.Server{Addr: firstRunSetupAddr, Handler: mux}

	setup := &firstRunSetup{
		configPath: configPath,
		onDone:     func() { go server.Shutdown(context.Background()) },
	}

	mux.HandleFunc("POST /api/setup", setup.handleCreate)

	// The page polls this until the real app answers on the same port.
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	mux.Handle("GET /static/css/bundle.css", gzipTextAssets(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Content-Type", "text/css; charset=utf-8")
			w.Write(bundledCSSContents)
		},
	)))

	mux.Handle("GET /static/{path...}", gzipTextAssets(http.StripPrefix(
		"/static",
		fileServerWithCache(http.FS(staticFS), STATIC_ASSETS_CACHE_DURATION),
	)))

	view := firstRunSetupView{
		ConfigPath:    configPath,
		MaxNameLength: firstRunMaxPageNameLength,
		Presets:       starterPresets,
	}

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		firstRunSetupTemplate.Execute(w, view)
	})

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving first run setup page: %w", err)
	}

	if !setup.isDone() {
		return errors.New("first run setup stopped before a config was created")
	}

	slog.Info("Created the initial config file", "path", configPath)

	return nil
}

func (s *firstRunSetup) handleCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, firstRunSetupMaxBodyBytes)

	var body struct {
		Name   string `json:"name"`
		Preset string `json:"preset"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Could not read the request")
		return
	}

	preset, ok := findStarterPreset(body.Preset)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Unknown starting point")
		return
	}

	name, err := sanitizePageName(body.Name)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	contents, err := newStarterConfigYAML(name, preset)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, fmt.Sprintf("Could not create a valid config: %v", err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done {
		writeJSONError(w, http.StatusConflict, "A config file has already been created")
		return
	}

	if !configIsMissingOrEmpty(s.configPath) {
		writeJSONError(w, http.StatusConflict, "A config file already exists, restart the container to load it")
		return
	}

	if err := writeStarterConfig(s.configPath, contents); err != nil {
		slog.Error("Failed to write the initial config file", "path", s.configPath, "error", err)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Could not write %s: %v", s.configPath, err))
		return
	}

	s.done = true
	w.WriteHeader(http.StatusNoContent)
	s.onDone()
}

func newStarterConfigYAML(pageName string, preset starterPreset) ([]byte, error) {
	page, err := starterPageNode(preset)
	if err != nil {
		return nil, err
	}

	setMappingKey(page, "name", scalarNode(pageName))

	pages := sequenceNode()
	pages.Content = append(pages.Content, page)

	root := newMappingNode()
	addPair(root, "pages", pages)
	setBlockStyleDeep(root)

	contents := marshalDocument(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}})

	if _, err := newConfigFromYAML(contents); err != nil {
		return nil, err
	}

	return contents, nil
}

func starterPageNode(preset starterPreset) (*yaml.Node, error) {
	if preset.file == "" {
		return newPageMapping(editorMutation{Layout: []string{"full"}}), nil
	}

	contents, err := starterFS.ReadFile("starters/" + preset.file)
	if err != nil {
		return nil, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(contents, &doc); err != nil {
		return nil, err
	}

	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode || len(doc.Content[0].Content) == 0 {
		return nil, fmt.Errorf("starter %s does not contain a page", preset.file)
	}

	return doc.Content[0].Content[0], nil
}

// sanitizePageName keeps the name to characters that survive YAML quoting and config variable expansion.
func sanitizePageName(raw string) (string, error) {
	name := strings.Join(strings.Fields(raw), " ")
	if name == "" {
		name = firstRunDefaultPageName
	}

	if len([]rune(name)) > firstRunMaxPageNameLength {
		return "", fmt.Errorf("the page name cannot be longer than %d characters", firstRunMaxPageNameLength)
	}

	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(firstRunAllowedNameSymbols, r) {
			continue
		}

		return "", errors.New("the page name can only contain letters, digits, spaces and - _ . ( )")
	}

	if slices.Contains(reservedPageSlugs, titleToSlug(name)) {
		return "", fmt.Errorf("%s is a reserved page name", name)
	}

	return name, nil
}

func writeStarterConfig(configPath string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(configPath, contents, 0o644)
}
