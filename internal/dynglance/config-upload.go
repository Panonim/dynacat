package dynglance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

var configUploadPageTemplate = mustParseTemplate("config-upload.html", "document.html", "footer.html")

// configUploadMaxBodyBytes bounds the upload body so an unauthenticated caller can't exhaust memory before the passphrase check.
const configUploadMaxBodyBytes = 5 << 20 // 5 MiB

// configUploadBackupsToKeep bounds how many timestamped backups of the main
// config file accumulate under repeated uploads.
const configUploadBackupsToKeep = 5

var configUploadFilenamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.(?:ya?ml)$`)

func (a *application) handleConfigUploadPageRequest(w http.ResponseWriter, r *http.Request) {
	data := &templateData{App: a}
	a.populateTemplateRequestData(&data.Request, r)

	var responseBytes bytes.Buffer
	if err := configUploadPageTemplate.Execute(&responseBytes, data); err != nil {
		slog.Error("Rendering config-upload page failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
		return
	}

	w.Write(responseBytes.Bytes())
}

type configUploadRequest struct {
	Passphrase string `json:"passphrase"`
	Mode       string `json:"mode"` // "replace" or "include"
	Filename   string `json:"filename"`
	Content    string `json:"content"`
}

type configUploadResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Message     string `json:"message,omitempty"`
	IncludeLine string `json:"includeLine,omitempty"`
}

func (a *application) writeConfigUploadJSON(w http.ResponseWriter, status int, resp configUploadResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

func (a *application) handleConfigUploadSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	waitOnFailure := 1*time.Second - time.Duration(mathrand.IntN(500))*time.Millisecond
	ip := a.addressOfRequest(r)
	rateLimitKey := "cfgupload:" + ip

	a.authAttemptsMu.Lock()
	exceededRateLimit, retryAfter := func() (bool, int) {
		attempt, exists := a.failedAuthAttempts[rateLimitKey]
		if !exists {
			a.failedAuthAttempts[rateLimitKey] = &failedAuthAttempt{attempts: 1, first: time.Now()}
			return false, 0
		}

		elapsed := time.Since(attempt.first)
		if elapsed < AUTH_RATE_LIMIT_WINDOW && attempt.attempts >= AUTH_RATE_LIMIT_MAX_ATTEMPTS {
			return true, max(1, int(AUTH_RATE_LIMIT_WINDOW.Seconds()-elapsed.Seconds()))
		}

		attempt.attempts++
		return false, 0
	}()
	a.authAttemptsMu.Unlock()

	if exceededRateLimit {
		time.Sleep(waitOnFailure)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, configUploadMaxBodyBytes)
	var req configUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if req.Passphrase == "" || bcrypt.CompareHashAndPassword(a.configUploadPasswordHash, []byte(req.Passphrase)) != nil {
		slog.Warn("Failed config-upload passphrase attempt", "ip", ip)
		time.Sleep(waitOnFailure)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// Successful auth: forget prior failures from this IP for this endpoint.
	a.authAttemptsMu.Lock()
	delete(a.failedAuthAttempts, rateLimitKey)
	a.authAttemptsMu.Unlock()

	if strings.TrimSpace(req.Content) == "" {
		a.writeConfigUploadJSON(w, http.StatusBadRequest, configUploadResponse{Error: "File is empty"})
		return
	}

	switch req.Mode {
	case "include":
		a.handleConfigUploadInclude(w, req)
	case "replace":
		a.handleConfigUploadReplace(w, req)
	default:
		a.writeConfigUploadJSON(w, http.StatusBadRequest, configUploadResponse{Error: "mode must be \"replace\" or \"include\""})
	}
}

func (a *application) handleConfigUploadInclude(w http.ResponseWriter, req configUploadRequest) {
	filename := strings.TrimSpace(req.Filename)
	if !configUploadFilenamePattern.MatchString(filename) {
		a.writeConfigUploadJSON(w, http.StatusBadRequest, configUploadResponse{
			Error: "Filename must contain only letters, numbers, '.', '_', '-' and end in .yml or .yaml",
		})
		return
	}

	var probe any
	if err := yaml.Unmarshal([]byte(req.Content), &probe); err != nil {
		a.writeConfigUploadJSON(w, http.StatusBadRequest, configUploadResponse{Error: "Invalid YAML: " + err.Error()})
		return
	}

	uploadsDir := filepath.Join(filepath.Dir(a.ConfigPath), "uploads")
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		slog.Error("Creating config-upload uploads directory failed", "error", err)
		a.writeConfigUploadJSON(w, http.StatusInternalServerError, configUploadResponse{Error: "Could not create uploads directory"})
		return
	}

	destPath := filepath.Join(uploadsDir, filename)
	if err := os.WriteFile(destPath, []byte(req.Content), 0o644); err != nil {
		slog.Error("Writing uploaded config fragment failed", "path", destPath, "error", err)
		a.writeConfigUploadJSON(w, http.StatusInternalServerError, configUploadResponse{Error: "Could not write file"})
		return
	}

	slog.Info("Config fragment uploaded", "path", destPath)

	a.writeConfigUploadJSON(w, http.StatusOK, configUploadResponse{
		OK:          true,
		Message:     "Saved. Add the line below wherever you want it included, then save dynglance.yml.",
		IncludeLine: "$include: uploads/" + filename,
	})
}

func (a *application) handleConfigUploadReplace(w http.ResponseWriter, req configUploadRequest) {
	if a.ConfigPath == "" {
		a.writeConfigUploadJSON(w, http.StatusInternalServerError, configUploadResponse{Error: "Server does not know its own config path"})
		return
	}

	tempPath := a.ConfigPath + ".upload-tmp"
	if err := os.WriteFile(tempPath, []byte(req.Content), 0o644); err != nil {
		slog.Error("Writing temporary uploaded config failed", "error", err)
		a.writeConfigUploadJSON(w, http.StatusInternalServerError, configUploadResponse{Error: "Could not write temporary file"})
		return
	}
	defer os.Remove(tempPath)

	expanded, _, err := parseYAMLIncludes(tempPath)
	if err != nil {
		a.writeConfigUploadJSON(w, http.StatusBadRequest, configUploadResponse{Error: "Invalid config: " + err.Error()})
		return
	}

	if _, err := newConfigFromYAML(expanded); err != nil {
		a.writeConfigUploadJSON(w, http.StatusBadRequest, configUploadResponse{Error: "Invalid config: " + err.Error()})
		return
	}

	if err := backupConfigFile(a.ConfigPath); err != nil {
		slog.Error("Backing up config file before replace failed", "error", err)
		a.writeConfigUploadJSON(w, http.StatusInternalServerError, configUploadResponse{Error: "Could not back up existing config"})
		return
	}

	if err := os.Rename(tempPath, a.ConfigPath); err != nil {
		slog.Error("Replacing config file failed", "error", err)
		a.writeConfigUploadJSON(w, http.StatusInternalServerError, configUploadResponse{Error: "Could not replace config file"})
		return
	}

	slog.Info("Config file replaced via upload", "path", a.ConfigPath)

	a.writeConfigUploadJSON(w, http.StatusOK, configUploadResponse{
		OK:      true,
		Message: "Config replaced. It will reload automatically in a moment.",
	})
}

// backupConfigFile copies the config to a timestamped .bak-* file (no-op if missing) and prunes beyond configUploadBackupsToKeep.
func backupConfigFile(configPath string) error {
	contents, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	backupPath := fmt.Sprintf("%s.bak-%d", configPath, time.Now().Unix())
	if err := os.WriteFile(backupPath, contents, 0o644); err != nil {
		return err
	}

	pattern := configPath + ".bak-*"
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) <= configUploadBackupsToKeep {
		return nil
	}

	sort.Strings(matches) // timestamp suffix sorts chronologically
	for _, old := range matches[:len(matches)-configUploadBackupsToKeep] {
		os.Remove(old)
	}

	return nil
}
