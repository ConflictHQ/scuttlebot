package ergo

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const ergoGitHubAPI = "https://api.github.com/repos/ergochat/ergo/releases/latest"

// EnsureBinary checks that the ergo binary exists at binaryPath. If it does
// not, it downloads the latest release from GitHub into destDir and returns
// the path to the installed binary.
//
// binaryPath is the configured path (may be just "ergo" meaning look in PATH).
// destDir is where to install if not found.
func EnsureBinary(binaryPath, destDir string) (string, error) {
	// If it's an absolute path or the caller set a specific path, check it first.
	if filepath.IsAbs(binaryPath) {
		if _, err := os.Stat(binaryPath); err == nil {
			return binaryPath, nil
		}
	}

	// Check if ergo is already in our data dir.
	localPath := filepath.Join(destDir, "ergo")
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	// Download from GitHub releases.
	version, downloadURL, err := latestReleaseURL()
	if err != nil {
		return "", fmt.Errorf("ergo: fetch latest release info: %w", err)
	}

	fmt.Fprintf(os.Stderr, "ergo binary not found — downloading %s...\n", version)

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", fmt.Errorf("ergo: create data dir: %w", err)
	}

	if err := downloadAndExtract(downloadURL, destDir); err != nil {
		return "", fmt.Errorf("ergo: download: %w", err)
	}

	fmt.Fprintf(os.Stderr, "ergo %s installed to %s\n", version, localPath)
	return localPath, nil
}

// latestReleaseURL queries GitHub for the latest ergo release and returns
// the version string and the download URL for the current OS/arch.
func latestReleaseURL() (string, string, error) {
	resp, err := http.Get(ergoGitHubAPI) //nolint:gosec // known GitHub API URL
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", err
	}

	suffix := platformSuffix()
	for _, asset := range release.Assets {
		if matchesPlatform(asset.Name, suffix) {
			return release.TagName, asset.BrowserDownloadURL, nil
		}
	}

	return "", "", fmt.Errorf("no release asset found for %s/%s (tag %s)", runtime.GOOS, runtime.GOARCH, release.TagName)
}

// platformSuffix returns the OS-arch suffix used in ergo release filenames.
func platformSuffix() string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	return os + "-" + arch
}

func matchesPlatform(name, suffix string) bool {
	// Ergo assets look like: ergo-v2.14.0-linux-x86_64.tar.gz
	return len(name) > 0 &&
		filepath.Ext(name) == ".gz" &&
		contains(name, suffix) &&
		contains(name, ".tar.")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// downloadAndExtract downloads a .tar.gz and extracts the "ergo" binary into destDir.
func downloadAndExtract(url, destDir string) error {
	resp, err := http.Get(url) //nolint:gosec // URL from GitHub API
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		// Extract only the "ergo" binary (may be at root or in a subdirectory).
		if filepath.Base(hdr.Name) != "ergo" || hdr.Typeflag != tar.TypeReg {
			continue
		}

		dest := filepath.Join(destDir, "ergo")
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // size bounded by release binary
			f.Close()
			return err
		}
		return f.Close()
	}

	return fmt.Errorf("ergo binary not found in archive")
}
