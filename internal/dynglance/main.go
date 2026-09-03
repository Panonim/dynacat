package dynglance

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// buildVersion is injected at build/release time via -X; see docs/operations.md.
var buildVersion = ""

// resolveVersion returns the effective version string to display: the
// build-time injected value, or a same-day ".0" fallback when unset.
func resolveVersion() string {
	if buildVersion != "" {
		return buildVersion
	}
	return time.Now().UTC().Format("2006.01.02") + ".0"
}

func Main() int {
	configureLogging()

	options, err := parseCliOptions()
	if err != nil {
		fmt.Println(err)
		return 1
	}

	if options.envFile != "" {
		if err := loadEnvFile(options.envFile); err != nil {
			fmt.Printf("Failed to load env file: %v\n", err)
			return 1
		}
	}

	// Resolve config path with fallback to glance.yml for backward compatibility
	options.configPath = resolveConfigPath(options.configPath)

	switch options.intent {
	case cliIntentVersionPrint:
		fmt.Println(resolveVersion())
	case cliIntentServe:
		// remove in v0.10.0
		if serveUpdateNoticeIfConfigLocationNotMigrated(options.configPath) {
			return 1
		}

		if err := serveApp(options.configPath); err != nil {
			fmt.Println(err)
			return 1
		}
	case cliIntentConfigValidate:
		contents, _, err := parseYAMLIncludes(options.configPath)
		if err != nil {
			fmt.Printf("Could not parse config file: %v\n", err)
			return 1
		}

		if _, err := newConfigFromYAML(contents); err != nil {
			fmt.Printf("Config file is invalid: %v\n", err)
			return 1
		}
	case cliIntentConfigPrint:
		contents, _, err := parseYAMLIncludes(options.configPath)
		if err != nil {
			fmt.Printf("Could not parse config file: %v\n", err)
			return 1
		}

		fmt.Println(string(contents))
	case cliIntentSensorsPrint:
		return cliSensorsPrint()
	case cliIntentMountpointInfo:
		return cliMountpointInfo(options.args[1])
	case cliIntentDiagnose:
		runDiagnostic()
	case cliIntentSecretMake:
		key, err := makeAuthSecretKey(AUTH_SECRET_KEY_LENGTH)
		if err != nil {
			fmt.Printf("Failed to make secret key: %v\n", err)
			return 1
		}

		fmt.Println(key)
	case cliIntentPasswordHash:
		password := options.args[1]

		if password == "" {
			fmt.Println("Password cannot be empty")
			return 1
		}

		if len(password) < 12 {
			fmt.Println("Password must be at least 12 characters long")
			return 1
		}

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			fmt.Printf("Failed to hash password: %v\n", err)
			return 1
		}

		fmt.Println(string(hashedPassword))
	}

	return 0
}

// resolveConfigPath falls back through the project's previous names -
// dynacat.yml, then the original upstream glance.yml - if dynglance.yml
// doesn't exist, for backward compatibility with legacy configurations.
func resolveConfigPath(primaryPath string) string {
	if stat, err := os.Stat(primaryPath); err == nil && !stat.IsDir() && stat.Size() > 0 {
		return primaryPath
	}

	// Only fall back when the primary path ends with dynglance.yml
	if filepath.Base(primaryPath) != "dynglance.yml" {
		return primaryPath
	}

	dir := filepath.Dir(primaryPath)

	legacyNames := []string{"dynacat.yml", "glance.yml"}
	for _, name := range legacyNames {
		legacyPath := filepath.Join(dir, name)
		if stat, err := os.Stat(legacyPath); err == nil && !stat.IsDir() && stat.Size() > 0 {
			slog.Warn(fmt.Sprintf("Using legacy %s config file. Please rename it to dynglance.yml to avoid deprecation issues", name))
			return legacyPath
		}
	}

	return primaryPath
}

func serveApp(configPath string) error {
	exitChannel := make(chan struct{})
	hadValidConfigOnStartup := false
	var stopServer func() error

	onChange := func(newContents []byte) {
		if stopServer != nil {
			slog.Info("Config file changed, reloading")
		}

		config, err := newConfigFromYAML(newContents)
		if err != nil {
			slog.Error("Config has errors", "error", err)

			if !hadValidConfigOnStartup {
				close(exitChannel)
			}

			return
		}

		app, err := newApplication(config)
		if err != nil {
			slog.Error("Failed to create application", "error", err)

			if !hadValidConfigOnStartup {
				close(exitChannel)
			}

			return
		}
		app.ConfigPath = configPath

		if !hadValidConfigOnStartup {
			hadValidConfigOnStartup = true
		}

		if stopServer != nil {
			if err := stopServer(); err != nil {
				slog.Error("Error while trying to stop server", "error", err)
			}
		}

		go func() {
			var startServer func() error
			startServer, stopServer = app.server()

			if err := startServer(); err != nil {
				slog.Error("Failed to start server", "error", err)
			}
		}()
	}

	onErr := func(err error) {
		slog.Error("Error watching config files", "error", err)
	}

	configContents, configIncludes, err := parseYAMLIncludes(configPath)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	stopWatching, err := configFilesWatcher(configPath, configContents, configIncludes, onChange, onErr)
	if err == nil {
		defer stopWatching()
	} else {
		slog.Warn("Error starting file watcher, config file changes will require a manual restart", "error", err)

		config, err := newConfigFromYAML(configContents)
		if err != nil {
			return fmt.Errorf("validating config file: %w", err)
		}

		app, err := newApplication(config)
		if err != nil {
			return fmt.Errorf("creating application: %w", err)
		}
		app.ConfigPath = configPath

		startServer, _ := app.server()
		if err := startServer(); err != nil {
			return fmt.Errorf("starting server: %w", err)
		}
	}

	<-exitChannel
	return nil
}

func serveUpdateNoticeIfConfigLocationNotMigrated(configPath string) bool {
	if !isRunningInsideDockerContainer() {
		return false
	}

	if _, err := os.Stat(configPath); err == nil {
		return false
	}

	// dynglance.yml wasn't mounted to begin with or was incorrectly mounted as a directory
	if stat, err := os.Stat("dynglance.yml"); err != nil || stat.IsDir() {
		return false
	}

	templateFile, _ := templateFS.Open("v0.7-update-notice-page.html")
	bodyContents, _ := io.ReadAll(templateFile)

	fmt.Println("!!! WARNING !!!")
	fmt.Println("The default location of dynglance.yml in the Docker image has changed starting from v0.7.0.")
	fmt.Println("Please see https://github.com/trooperthorn/ha_app_dynglance/blob/main/docs/docs/installation.md#coming-from-glance for more information.")

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(bodyContents))
	})

	server := http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	server.ListenAndServe()

	return true
}
