package dynacat

import (
	"bytes"
	"crypto/md5"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static
var _staticFS embed.FS

//go:embed templates
var _templateFS embed.FS

var staticFS, _ = fs.Sub(_staticFS, "static")
var templateFS, _ = fs.Sub(_templateFS, "templates")

func readAllFromStaticFS(path string) ([]byte, error) {
	path = strings.ReplaceAll(path, "\\", "/")

	file, err := staticFS.Open(path)
	if err != nil {
		return nil, err
	}

	return io.ReadAll(file)
}

var (
	staticFSHashOnce sync.Once
	staticFSHashVal  string
)

func getStaticFSHash() string {
	staticFSHashOnce.Do(func() {
		hash, err := computeFSHash(staticFS)
		if err != nil {
			slog.Warn("Could not compute static assets cache key", "error", err)
			staticFSHashVal = strconv.FormatInt(time.Now().Unix(), 10)
			return
		}
		staticFSHashVal = hash
	})
	return staticFSHashVal
}

func computeFSHash(files fs.FS) (string, error) {
	hash := md5.New()

	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		file, err := files.Open(path)
		if err != nil {
			return err
		}

		if _, err := io.Copy(hash, file); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil))[:10], nil
}

var cssImportPattern = regexp.MustCompile(`(?m)^@import "(.*?)";$`)
var cssSingleLineCommentPattern = regexp.MustCompile(`(?m)^\s*\/\*.*?\*\/$`)

func bundleCSS(mainFilePath string) []byte {
	var recursiveParseImports func(path string, depth int) ([]byte, error)
	recursiveParseImports = func(path string, depth int) ([]byte, error) {
		if depth > 20 {
			return nil, errors.New("maximum import depth reached, is one of your imports circular?")
		}

		mainFileContents, err := readAllFromStaticFS(path)
		if err != nil {
			return nil, err
		}

		mainFileContents = bytes.ReplaceAll(mainFileContents, []byte("\r\n"), []byte("\n"))

		mainFileDir := filepath.Dir(path)
		var importLastErr error

		parsed := cssImportPattern.ReplaceAllFunc(mainFileContents, func(match []byte) []byte {
			if importLastErr != nil {
				return nil
			}

			matches := cssImportPattern.FindSubmatch(match)
			if len(matches) != 2 {
				importLastErr = fmt.Errorf(
					"import didn't return expected number of capture groups: %s, expected 2, got %d",
					match, len(matches),
				)
				return nil
			}

			importFilePath := filepath.Join(mainFileDir, string(matches[1]))
			importContents, err := recursiveParseImports(importFilePath, depth+1)
			if err != nil {
				importLastErr = err
				return nil
			}

			return importContents
		})

		if importLastErr != nil {
			return nil, importLastErr
		}

		return parsed, nil
	}

	contents, err := recursiveParseImports(mainFilePath, 0)
	if err != nil {
		panic(fmt.Sprintf("building CSS bundle: %v", err))
	}

	contents = cssSingleLineCommentPattern.ReplaceAll(contents, nil)
	contents = whitespaceAtBeginningOfLinePattern.ReplaceAll(contents, nil)
	contents = bytes.ReplaceAll(contents, []byte("\n"), []byte(""))

	return contents
}

var bundledCSSContents = bundleCSS("css/main.css")

// Moved away from the main bundle so visitors who never open the editor won't download the file.
var bundledEditorCSSContents = bundleCSS("css/editor.css")
