package dynacat

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/crypto/bcrypt"
)

var buildVersion = "dev"

func Main() int {
	options, err := parseCliOptions()
	if err != nil {
		fmt.Println(err)
		return 1
	}

	configureLogging(options.background && options.intent == cliIntentServe)

	if options.envFile != "" {
		if err := loadEnvFile(options.envFile); err != nil {
			slog.Error("Failed to load env file", "error", err)
			return 1
		}
	}

	options.configPath = resolveConfigPath(options.configPath)

	switch options.intent {
	case cliIntentVersionPrint:
		fmt.Println(buildVersion)
	case cliIntentServe:
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		err := serveFirstRunSetupIfNoConfig(ctx, options.configPath)
		if ctx.Err() != nil {
			return 0
		}
		if err != nil {
			slog.Error("First run setup failed", "error", err)
			return 1
		}

		if err := serveApp(ctx, options.configPath); err != nil {
			slog.Error("Failed to serve app", "error", err)
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
	case cliIntentUpdate:
		return cliUpdate()
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

func resolveConfigPath(primaryPath string) string {
	if stat, err := os.Stat(primaryPath); err == nil && !stat.IsDir() && stat.Size() > 0 {
		return primaryPath
	}

	if filepath.Base(primaryPath) != "dynacat.yml" {
		return primaryPath
	}

	glancePath := filepath.Join(filepath.Dir(primaryPath), "glance.yml")
	if stat, err := os.Stat(glancePath); err == nil && !stat.IsDir() && stat.Size() > 0 {
		slog.Warn("Using legacy glance.yml config file. Please rename it to dynacat.yml to avoid deprecation issues")
		return glancePath
	}

	return primaryPath
}

func serveApp(ctx context.Context, configPath string) error {
	// TODO: refactor, hard to reason about with all the callbacks and simultaneous operations
	exitChannel := make(chan struct{})
	var exitErr error
	hadValidConfigOnStartup := false
	var stopServer func() error

	warnIfDirNotWritable(filepath.Dir(configPath))

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
		app.configPath = configPath

		if !hadValidConfigOnStartup {
			hadValidConfigOnStartup = true
			warnIfDirNotWritable(config.assetsPath())
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
		app.configPath = configPath

		var startServer func() error
		startServer, stopServer = app.server()
		go func() {
			if err := startServer(); err != nil {
				exitErr = fmt.Errorf("starting server: %w", err)
				close(exitChannel)
			}
		}()
	}

	select {
	case <-exitChannel:
		return exitErr
	case <-ctx.Done():
		slog.Info("Shutting down")
		if stopServer != nil {
			stopServer()
		}
		return nil
	}
}
