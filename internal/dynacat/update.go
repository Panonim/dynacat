package dynacat

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

const latestReleaseURL = "https://api.github.com/repos/Panonim/dynacat/releases/latest"

type latestReleaseJson struct {
	TagName string `json:"tag_name"`
	HtmlUrl string `json:"html_url"`
	Assets  []struct {
		Name        string `json:"name"`
		DownloadUrl string `json:"browser_download_url"`
	} `json:"assets"`
}

func cliUpdate() int {
	started, err := runUpdate()
	if err != nil {
		fmt.Printf("Update failed: %v\n", err)
	}

	if !started {
		pauseIfOwnConsole()
	}

	if err != nil {
		return 1
	}

	return 0
}

func runUpdate() (bool, error) {
	current, ok := parseReleaseVersion(buildVersion)
	if !ok {
		return false, fmt.Errorf("updates are only available for release builds, this is %s", buildVersion)
	}

	fmt.Println("Checking for updates...")

	request, err := http.NewRequest("GET", latestReleaseURL, nil)
	if err != nil {
		return false, err
	}

	release, err := decodeJsonFromRequest[latestReleaseJson](defaultHTTPClient, request)
	if err != nil {
		return false, err
	}

	latest, ok := parseReleaseVersion(release.TagName)
	if !ok || slices.Compare(latest[:], current[:]) <= 0 {
		fmt.Printf("Dynacat is up to date (%s)\n", buildVersion)
		return false, nil
	}

	fmt.Printf("Updating Dynacat %s to %s\n", buildVersion, release.TagName)
	return installUpdate(&release)
}

// Only plain tags like v0.8.4 count, so dev and beta builds never update.
func parseReleaseVersion(version string) ([3]int, bool) {
	var parts [3]int

	fields := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(fields) != len(parts) {
		return parts, false
	}

	for i, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil {
			return parts, false
		}
		parts[i] = number
	}

	return parts, true
}
