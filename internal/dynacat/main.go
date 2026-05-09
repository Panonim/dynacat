package dynacat

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"golang.org/x/crypto/bcrypt"
)

var buildVersion = "dev"

func Main() int {
	options, err := parseCliOptions()
	if err != nil {
		fmt.Println(err)
		return 1
	}

	// Resolve config path with fallback to glance.yml for backward compatibility
	options.configPath = resolveConfigPath(options.configPath)

	switch options.intent {
	case cliIntentVersionPrint:
		fmt.Println(buildVersion)
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

		primaryConfig, err := newConfigFromYAML(contents)
		if err != nil {
			fmt.Printf("Config file is invalid: %v\n", err)
			return 1
		}

		if narrowRelPath := primaryConfig.Layout.NarrowViewportConfig; narrowRelPath != "" {
			narrowPath, err := resolveNarrowConfigPath(options.configPath, narrowRelPath)
			if err != nil {
				fmt.Printf("Could not resolve narrow-viewport-config path: %v\n", err)
				return 1
			}
			narrowContents, _, err := parseYAMLIncludes(narrowPath)
			if err != nil {
				fmt.Printf("Could not parse narrow config file: %v\n", err)
				return 1
			}
			narrowConfig, err := newConfigFromYAML(narrowContents)
			if err != nil {
				fmt.Printf("Narrow config file is invalid: %v\n", err)
				return 1
			}
			if err := validateLayoutPairConfigs(primaryConfig, narrowConfig); err != nil {
				fmt.Printf("Layout pair validation failed: %v\n", err)
				return 1
			}
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

// resolveConfigPath falls back to glance.yml if dynacat.yml (the default) doesn't exist,
// for backward compatibility with legacy Glance configurations
func resolveConfigPath(primaryPath string) string {
	// user explicitly sets config
	if primaryPath != "dynacat.yml" {
		return primaryPath
	}

	// checks if dynacat.yml or glance.yml exists
	if _, err := os.Stat("dynacat.yml"); err == nil {
		return primaryPath
	}

	if _, err := os.Stat("glance.yml"); err == nil {
		log.Println("Warning: Using legacy glance.yml config file. Please rename it to dynacat.yml to avoid deprecation issues.")
		return "glance.yml"
	}

	// If neither exists, just return the original path
	return primaryPath
}

func serveApp(configPath string) error {
	exitChannel := make(chan struct{})
	hadValidConfigOnStartup := false
	var stopServer func() error

	// onReload handles both single-layout (narrowPath == "") and dual-layout modes.
	// It is called by the file watcher whenever either config tree changes.
	onReload := func(primaryContents []byte, narrowPath string, narrowContents []byte) {
		if stopServer != nil {
			log.Println("Config file changed, reloading...")
		}

		primaryConfig, err := newConfigFromYAML(primaryContents)
		if err != nil {
			log.Printf("Config has errors: %v", err)
			if !hadValidConfigOnStartup {
				close(exitChannel)
			}
			return
		}

		primaryApp, err := newApplication(primaryConfig)
		if err != nil {
			log.Printf("Failed to create application: %v", err)
			if !hadValidConfigOnStartup {
				close(exitChannel)
			}
			return
		}

		var narrowApp *application
		if narrowPath != "" {
			narrowConfig, err := newConfigFromYAML(narrowContents)
			if err != nil {
				log.Printf("Narrow config has errors: %v", err)
				if !hadValidConfigOnStartup {
					close(exitChannel)
				}
				return
			}
			if err := validateLayoutPairConfigs(primaryConfig, narrowConfig); err != nil {
				log.Printf("Layout pair config error: %v", err)
				if !hadValidConfigOnStartup {
					close(exitChannel)
				}
				return
			}
			narrowApp, err = newApplication(narrowConfig)
			if err != nil {
				log.Printf("Failed to create narrow application: %v", err)
				if !hadValidConfigOnStartup {
					close(exitChannel)
				}
				return
			}
		}

		if !hadValidConfigOnStartup {
			hadValidConfigOnStartup = true
		}

		if stopServer != nil {
			if err := stopServer(); err != nil {
				log.Printf("Error while trying to stop server: %v", err)
			}
		}

		go func() {
			var startFn func() error
			if narrowApp != nil {
				primaryApp.NarrowLayoutEnabled = true
				narrowApp.NarrowLayoutEnabled = true
				startFn, stopServer = buildDualServer(primaryApp, narrowApp)
			} else {
				startFn, stopServer = primaryApp.server()
			}
			if err := startFn(); err != nil {
				log.Printf("Failed to start server: %v", err)
			}
		}()
	}

	onErr := func(err error) {
		log.Printf("Error watching config files: %v", err)
	}

	primaryContents, primaryIncludes, err := parseYAMLIncludes(configPath)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	// Extract the narrow config path from the raw primary YAML (before full parse).
	narrowRelPath, err := extractNarrowConfigPath(primaryContents)
	if err != nil {
		return fmt.Errorf("extracting narrow config path: %w", err)
	}

	var narrowPath string
	var narrowContents []byte
	var narrowIncludes map[string]struct{}
	if narrowRelPath != "" {
		narrowPath, err = resolveNarrowConfigPath(configPath, narrowRelPath)
		if err != nil {
			return fmt.Errorf("resolving narrow config path: %w", err)
		}
		narrowContents, narrowIncludes, err = parseYAMLIncludes(narrowPath)
		if err != nil {
			return fmt.Errorf("parsing narrow config: %w", err)
		}
	}

	// dualConfigFilesWatcher handles both single and dual mode; it will dynamically
	// switch mode if narrow-viewport-config is added or removed at runtime.
	stopWatching, err := dualConfigFilesWatcher(
		configPath, primaryContents, primaryIncludes,
		narrowPath, narrowContents, narrowIncludes,
		onReload, onErr,
	)
	if err == nil {
		defer stopWatching()
	} else {
		log.Printf("Error starting file watcher, config file changes will require a manual restart. (%v)", err)

		// No watcher — start directly (blocking call, no hot-reload).
		primaryConfig, err := newConfigFromYAML(primaryContents)
		if err != nil {
			return fmt.Errorf("validating config file: %w", err)
		}
		primaryApp, err := newApplication(primaryConfig)
		if err != nil {
			return fmt.Errorf("creating application: %w", err)
		}

		var startFn func() error
		if narrowPath != "" {
			narrowConfig, err := newConfigFromYAML(narrowContents)
			if err != nil {
				return fmt.Errorf("validating narrow config file: %w", err)
			}
			if err := validateLayoutPairConfigs(primaryConfig, narrowConfig); err != nil {
				return fmt.Errorf("layout pair config: %w", err)
			}
			narrowApp, err := newApplication(narrowConfig)
			if err != nil {
				return fmt.Errorf("creating narrow application: %w", err)
			}
			primaryApp.NarrowLayoutEnabled = true
			narrowApp.NarrowLayoutEnabled = true
			startFn, _ = buildDualServer(primaryApp, narrowApp)
		} else {
			startFn, _ = primaryApp.server()
		}

		if err := startFn(); err != nil {
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

	// dynacat.yml wasn't mounted to begin with or was incorrectly mounted as a directory
	if stat, err := os.Stat("dynacat.yml"); err != nil || stat.IsDir() {
		return false
	}

	templateFile, _ := templateFS.Open("v0.7-update-notice-page.html")
	bodyContents, _ := io.ReadAll(templateFile)

	fmt.Println("!!! WARNING !!!")
	fmt.Println("The default location of dynacat.yml in the Docker image has changed starting from v0.7.0.")
	fmt.Println("Please see https://github.com/Panonim/dynacat/blob/main/docs/docs/installation.md#coming-from-glance for more information.")

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(bodyContents))
	})

	server := http.Server{
		Addr:    ":8080",
		Handler: mux,
	}
	server.ListenAndServe()

	return true
}
