package dynacat

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var envInlineCommentPattern = regexp.MustCompile(`\s+#.*$`)

func parseEnvValue(value string) string {
	if value != "" && (value[0] == '"' || value[0] == '\'') {
		for i := 1; i < len(value); i++ {
			if value[0] == '"' && value[i] == '\\' {
				i++
			} else if value[i] == value[0] {
				return strings.ReplaceAll(value[1:i], `\"`, `"`)
			}
		}
	}

	return envInlineCommentPattern.ReplaceAllString(value, "")
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening env file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimPrefix(line, "export ")

		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("env file line %d: missing '=' separator", lineNum)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if key == "" {
			return fmt.Errorf("env file line %d: empty variable name", lineNum)
		}

		value = parseEnvValue(value)

		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("env file line %d: setting %s failed: %w", lineNum, key, err)
		}
	}

	return scanner.Err()
}
