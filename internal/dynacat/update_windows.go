package dynacat

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func installUpdate(release *latestReleaseJson) (bool, error) {
	exePath, err := os.Executable()
	if err != nil {
		return false, err
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(exePath), "unins000.exe")); err != nil {
		fmt.Printf("This copy was not installed with the Windows installer, download the new version from %s\n", release.HtmlUrl)
		return false, nil
	}

	assetName := "dynacat-windows-" + runtime.GOARCH + "-setup.exe"
	var downloadURL, checksumURL string
	for _, asset := range release.Assets {
		switch asset.Name {
		case assetName:
			downloadURL = asset.DownloadUrl
		case "checksums.txt":
			checksumURL = asset.DownloadUrl
		}
	}

	if downloadURL == "" || checksumURL == "" {
		return false, fmt.Errorf("release %s has no %s or checksums.txt", release.TagName, assetName)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	checksum, err := downloadChecksum(checksumURL, assetName)
	if err != nil {
		return false, err
	}

	setupPath, err := downloadVerifiedFile(downloadURL, checksum)
	if err != nil {
		return false, err
	}

	// The installer keeps the existing folders and options, then starts Dynacat again because of /RELAUNCH.
	cmd := exec.Command(setupPath, "/SILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/RELAUNCH")
	if err := cmd.Start(); err != nil {
		return false, err
	}

	fmt.Println("Installing, Dynacat will start again once it is done")
	return true, nil
}

func httpGet(url string, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: timeout}

	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}

	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("unexpected status code %d from %s", response.StatusCode, url)
	}

	return response, nil
}

// Finds the digest for name in a sha256sum output covering every release asset.
func downloadChecksum(url string, name string) ([]byte, error) {
	response, err := httpGet(url, time.Minute)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return nil, err
	}

	for line := range strings.Lines(string(body)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}

		checksum, err := hex.DecodeString(fields[0])
		if err != nil || len(checksum) != sha256.Size {
			return nil, fmt.Errorf("invalid checksum for %s in %s", name, url)
		}
		return checksum, nil
	}

	return nil, fmt.Errorf("no checksum for %s in %s", name, url)
}

// Saves to a randomly named temp file and only returns its path when the hash matches.
func downloadVerifiedFile(url string, checksum []byte) (string, error) {
	response, err := httpGet(url, 10*time.Minute)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	file, err := os.CreateTemp("", "dynacat-setup-*.exe")
	if err != nil {
		return "", err
	}

	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(file, hash), response.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err == nil && !bytes.Equal(hash.Sum(nil), checksum) {
		err = fmt.Errorf("checksum mismatch for %s", url)
	}

	if err != nil {
		os.Remove(file.Name())
		return "", err
	}

	return file.Name(), nil
}

// The Start menu shortcut opens a console only for this process, which would close before the output can be read.
func pauseIfOwnConsole() {
	var pids [2]uint32
	count, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList").Call(
		uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)),
	)
	if count != 1 {
		return
	}

	fmt.Print("\nPress Enter to close...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
