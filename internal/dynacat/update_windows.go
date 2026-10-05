package dynacat

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.DownloadUrl
			break
		}
	}

	if downloadURL == "" {
		return false, fmt.Errorf("release %s has no %s", release.TagName, assetName)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	setupPath := filepath.Join(os.TempDir(), assetName)
	if err := downloadFile(downloadURL, setupPath); err != nil {
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

func downloadFile(url, path string) error {
	client := &http.Client{Timeout: 10 * time.Minute}

	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %d from %s", response.StatusCode, url)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}

	if _, err := io.Copy(file, response.Body); err != nil {
		file.Close()
		return err
	}

	return file.Close()
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
