//go:build !windows

package dynacat

import "fmt"

func installUpdate(release *latestReleaseJson) (bool, error) {
	fmt.Printf("Download it from %s or pull the latest Docker image\n", release.HtmlUrl)
	return false, nil
}

func pauseIfOwnConsole() {}
