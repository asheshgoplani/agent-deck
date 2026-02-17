// Package update provides version checking and self-update functionality.
package update

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
	"strings"
	"time"
)

const (
	// GitHubRepo is the repository to check for updates
	GitHubRepo = "asheshgoplani/agent-deck"

	// CacheFileName stores the last update check result
	CacheFileName = "update-cache.json"

	// DefaultCheckInterval is the default check interval (1 hour)
	// Can be overridden via config.toml [updates] check_interval_hours
	DefaultCheckInterval = 1 * time.Hour
)

// checkInterval stores the configurable interval (set via SetCheckInterval)
var checkInterval = DefaultCheckInterval

// SetCheckInterval sets the update check interval from config
func SetCheckInterval(hours int) {
	if hours > 0 {
		checkInterval = time.Duration(hours) * time.Hour
	}
}

// Release represents a GitHub release
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset represents a release asset (binary download)
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// UpdateCache stores the last check result
type UpdateCache struct {
	CheckedAt      time.Time `json:"checked_at"`
	LatestVersion  string    `json:"latest_version"`
	CurrentVersion string    `json:"current_version"`
	DownloadURL    string    `json:"download_url"`
	ReleaseURL     string    `json:"release_url"`
}

// UpdateInfo contains information about an available update
type UpdateInfo struct {
	Available      bool
	CurrentVersion string
	LatestVersion  string
	DownloadURL    string
	ReleaseURL     string
}

// getCacheDir returns the cache directory path
func getCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-deck"), nil
}

// loadCache loads the update cache from disk
func loadCache() (*UpdateCache, error) {
	cacheDir, err := getCacheDir()
	if err != nil {
		return nil, err
	}

	cachePath := filepath.Join(cacheDir, CacheFileName)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	var cache UpdateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}

	return &cache, nil
}

// saveCache saves the update cache to disk
func saveCache(cache *UpdateCache) error {
	cacheDir, err := getCacheDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}

	cachePath := filepath.Join(cacheDir, CacheFileName)
	return os.WriteFile(cachePath, data, 0644)
}

// fetchLatestRelease fetches the latest release from GitHub
func fetchLatestRelease() (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GitHubRepo)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release: %w", err)
	}

	return &release, nil
}

// getAssetURL returns the download URL for the current platform
func getAssetURL(release *Release) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Construct expected asset name: agent-deck_X.Y.Z_os_arch.tar.gz
	version := strings.TrimPrefix(release.TagName, "v")
	expectedName := fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", version, goos, goarch)

	for _, asset := range release.Assets {
		if asset.Name == expectedName {
			return asset.BrowserDownloadURL
		}
	}

	return ""
}

// CompareVersions compares two semantic versions
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
func CompareVersions(v1, v2 string) int {
	// Remove 'v' prefix if present
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// Pad with zeros
	for len(parts1) < 3 {
		parts1 = append(parts1, "0")
	}
	for len(parts2) < 3 {
		parts2 = append(parts2, "0")
	}

	for i := 0; i < 3; i++ {
		var n1, n2 int
		_, _ = fmt.Sscanf(parts1[i], "%d", &n1)
		_, _ = fmt.Sscanf(parts2[i], "%d", &n2)

		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}

	return 0
}

// CheckForUpdate checks if a new version is available
// Uses cache to avoid hitting GitHub API too frequently
func CheckForUpdate(currentVersion string, forceCheck bool) (*UpdateInfo, error) {
	info := &UpdateInfo{
		Available:      false,
		CurrentVersion: currentVersion,
	}

	// Try to use cache first (unless force check)
	if !forceCheck {
		cache, err := loadCache()
		if err == nil && time.Since(cache.CheckedAt) < checkInterval {
			// Cache is fresh, use it
			info.LatestVersion = cache.LatestVersion
			info.DownloadURL = cache.DownloadURL
			info.ReleaseURL = cache.ReleaseURL
			info.Available = CompareVersions(currentVersion, cache.LatestVersion) < 0
			return info, nil
		}
	}

	// Fetch from GitHub
	release, err := fetchLatestRelease()
	if err != nil {
		return info, err
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	downloadURL := getAssetURL(release)

	// Update cache
	cache := &UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  latestVersion,
		CurrentVersion: currentVersion,
		DownloadURL:    downloadURL,
		ReleaseURL:     release.HTMLURL,
	}
	_ = saveCache(cache) // Ignore cache save errors

	info.LatestVersion = latestVersion
	info.DownloadURL = downloadURL
	info.ReleaseURL = release.HTMLURL
	info.Available = CompareVersions(currentVersion, latestVersion) < 0

	return info, nil
}

// CheckForUpdateAsync checks for updates in the background
// Returns a channel that will receive the result
func CheckForUpdateAsync(currentVersion string) <-chan *UpdateInfo {
	ch := make(chan *UpdateInfo, 1)

	go func() {
		info, err := CheckForUpdate(currentVersion, false)
		if err != nil {
			// On error, return no update available
			ch <- &UpdateInfo{Available: false, CurrentVersion: currentVersion}
		} else {
			ch <- info
		}
		close(ch)
	}()

	return ch
}

// PerformUpdate downloads and installs the latest version
func PerformUpdate(downloadURL string) error {
	if downloadURL == "" {
		return fmt.Errorf("no download URL available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Resolve symlinks to get actual binary location
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	// Download the release
	fmt.Printf("Downloading from %s...\n", downloadURL)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Create temp file for download
	tmpFile, err := os.CreateTemp("", "agent-deck-update-*.tar.gz")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Copy download to temp file
	fmt.Println("Downloading...")
	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("failed to save download: %w", err)
	}

	// Extract the binary from tarball
	fmt.Println("Extracting...")
	binaryData, err := extractBinaryFromTarGz(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to extract: %w", err)
	}

	// Create temp file for new binary
	newBinaryPath := execPath + ".new"
	if err := os.WriteFile(newBinaryPath, binaryData, 0755); err != nil {
		return fmt.Errorf("failed to write new binary: %w", err)
	}

	// Backup old binary
	oldBinaryPath := execPath + ".old"
	if err := os.Rename(execPath, oldBinaryPath); err != nil {
		os.Remove(newBinaryPath)
		return fmt.Errorf("failed to backup old binary: %w", err)
	}

	// Move new binary into place
	if err := os.Rename(newBinaryPath, execPath); err != nil {
		// Try to restore old binary
		_ = os.Rename(oldBinaryPath, execPath)
		return fmt.Errorf("failed to install new binary: %w", err)
	}

	// Remove old binary
	os.Remove(oldBinaryPath)

	fmt.Println("✓ Update complete!")
	return nil
}

// ChangelogEntry represents a single version's changelog
type ChangelogEntry struct {
	Version string
	Date    string
	Content string
}

// FetchChangelog fetches the CHANGELOG.md from GitHub
func FetchChangelog() (string, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/main/CHANGELOG.md", GitHubRepo)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch changelog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch changelog: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB max
	if err != nil {
		return "", fmt.Errorf("failed to read changelog: %w", err)
	}

	return string(data), nil
}

// ParseChangelog parses CHANGELOG.md and returns entries for versions
func ParseChangelog(content string) []ChangelogEntry {
	var entries []ChangelogEntry
	lines := strings.Split(content, "\n")

	var currentEntry *ChangelogEntry
	var contentBuilder strings.Builder

	for _, line := range lines {
		// Match version headers: ## [0.6.1] - 2025-12-24
		if strings.HasPrefix(line, "## [") {
			// Save previous entry
			if currentEntry != nil {
				currentEntry.Content = strings.TrimSpace(contentBuilder.String())
				entries = append(entries, *currentEntry)
			}

			// Parse new entry
			// Extract version and date from "## [0.6.1] - 2025-12-24"
			rest := strings.TrimPrefix(line, "## [")
			parts := strings.SplitN(rest, "]", 2)
			if len(parts) >= 1 {
				version := parts[0]
				date := ""
				if len(parts) >= 2 && strings.Contains(parts[1], " - ") {
					dateParts := strings.SplitN(parts[1], " - ", 2)
					if len(dateParts) >= 2 {
						date = strings.TrimSpace(dateParts[1])
					}
				}
				currentEntry = &ChangelogEntry{
					Version: version,
					Date:    date,
				}
				contentBuilder.Reset()
			}
		} else if currentEntry != nil {
			contentBuilder.WriteString(line)
			contentBuilder.WriteString("\n")
		}
	}

	// Save last entry
	if currentEntry != nil {
		currentEntry.Content = strings.TrimSpace(contentBuilder.String())
		entries = append(entries, *currentEntry)
	}

	return entries
}

// GetChangesBetweenVersions returns changelog entries between two versions (exclusive of current, inclusive of latest)
func GetChangesBetweenVersions(entries []ChangelogEntry, currentVersion, latestVersion string) []ChangelogEntry {
	var result []ChangelogEntry

	currentVersion = strings.TrimPrefix(currentVersion, "v")
	latestVersion = strings.TrimPrefix(latestVersion, "v")

	for _, entry := range entries {
		entryVersion := strings.TrimPrefix(entry.Version, "v")

		// Include if version is greater than current and less than or equal to latest
		if CompareVersions(entryVersion, currentVersion) > 0 &&
			CompareVersions(entryVersion, latestVersion) <= 0 {
			result = append(result, entry)
		}
	}

	return result
}

// FormatChangelogForDisplay formats changelog entries for terminal display
func FormatChangelogForDisplay(entries []ChangelogEntry) string {
	if len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n━━━ What's New ━━━\n")

	for _, entry := range entries {
		header := fmt.Sprintf("\n📦 v%s", entry.Version)
		if entry.Date != "" {
			header += fmt.Sprintf(" (%s)", entry.Date)
		}
		sb.WriteString(header)
		sb.WriteString("\n")

		// Process content - indent and clean up
		lines := strings.Split(entry.Content, "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			// Skip section headers that are just category names, but keep content
			if strings.HasPrefix(line, "### ") {
				section := strings.TrimPrefix(line, "### ")
				sb.WriteString(fmt.Sprintf("\n  [%s]\n", section))
			} else if strings.HasPrefix(line, "- ") {
				sb.WriteString(fmt.Sprintf("  %s\n", line))
			} else if strings.HasPrefix(line, "  ") {
				// Nested content
				sb.WriteString(fmt.Sprintf("  %s\n", line))
			} else {
				// Preserve unrecognized non-empty lines (e.g. plain text paragraphs)
				sb.WriteString(fmt.Sprintf("  %s\n", line))
			}
		}
	}

	sb.WriteString("\n━━━━━━━━━━━━━━━━━━\n")
	return sb.String()
}

// extractBinaryFromTarGz extracts the agent-deck binary from a .tar.gz file
func extractBinaryFromTarGz(tarPath string) ([]byte, error) {
	file, err := os.Open(tarPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// Look for the agent-deck binary
		if header.Typeflag == tar.TypeReg && header.Name == "agent-deck" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}

	return nil, fmt.Errorf("agent-deck binary not found in archive")
}

// UpdateBridgePy downloads and updates the bridge.py file from GitHub
func UpdateBridgePy() error {
	// Get the conductor directory
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	conductorDir := filepath.Join(home, ".agent-deck", "conductor")
	bridgePath := filepath.Join(conductorDir, "bridge.py")

	// Check if conductor directory exists
	if _, err := os.Stat(conductorDir); os.IsNotExist(err) {
		// Conductor not installed, skip update
		return nil
	}

	// Download bridge.py from GitHub
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/main/conductor/bridge.py", GitHubRepo)

	fmt.Println("Updating bridge.py...")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download bridge.py: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download bridge.py: status %d", resp.StatusCode)
	}

	// Read the content
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read bridge.py content: %w", err)
	}

	// Backup existing bridge.py
	if _, err := os.Stat(bridgePath); err == nil {
		backupPath := bridgePath + ".backup"
		if err := os.Rename(bridgePath, backupPath); err != nil {
			return fmt.Errorf("failed to backup bridge.py: %w", err)
		}
	}

	// Write new bridge.py
	if err := os.WriteFile(bridgePath, content, 0755); err != nil {
		return fmt.Errorf("failed to write bridge.py: %w", err)
	}

	fmt.Println("✓ bridge.py updated!")
	return nil
}
