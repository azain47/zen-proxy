package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUpdateRepository = "azain47/zen-proxy"
	githubAPIBaseURL        = "https://api.github.com"
	updateCheckTimeout      = 5 * time.Second
)

var updateHTTPClient = &http.Client{}

type githubRelease struct {
	TagName string `json:"tag_name"`
}

type updateComparison uint8

const (
	comparisonUnknown updateComparison = iota
	comparisonDevelopment
	comparisonCurrent
	comparisonBehind
	comparisonAhead
)

type updateResult struct {
	Latest     string
	Comparison updateComparison
}

func checkForUpdate(ctx context.Context, currentVersion string) (updateResult, error) {
	repository := strings.TrimSpace(os.Getenv("ZEN_PROXY_REPO"))
	if repository == "" {
		repository = defaultUpdateRepository
	}
	return checkForUpdateAt(ctx, currentVersion, repository, githubAPIBaseURL, updateHTTPClient)
}

func checkForUpdateAt(ctx context.Context, currentVersion, repository, apiBaseURL string, client *http.Client) (updateResult, error) {
	endpoint, err := latestReleaseEndpoint(apiBaseURL, repository)
	if err != nil {
		return updateResult{}, err
	}
	if client == nil {
		client = updateHTTPClient
	}

	requestContext, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return updateResult{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "zen-proxy/"+strings.TrimSpace(currentVersion))

	resp, err := client.Do(req)
	if err != nil {
		return updateResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return updateResult{}, fmt.Errorf("latest release request returned %s", resp.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return updateResult{}, fmt.Errorf("decode latest release: %w", err)
	}
	release.TagName = strings.TrimSpace(release.TagName)
	if release.TagName == "" {
		return updateResult{}, fmt.Errorf("latest release did not include a tag")
	}

	comparison := compareReleaseVersions(currentVersion, release.TagName)
	return updateResult{Latest: release.TagName, Comparison: comparison}, nil
}

func latestReleaseEndpoint(apiBaseURL, repository string) (string, error) {
	repository = strings.Trim(repository, "/")
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", fmt.Errorf("invalid repository %q; expected owner/repository", repository)
	}
	return strings.TrimRight(apiBaseURL, "/") + "/repos/" + repository + "/releases/latest", nil
}

func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(version, "v") || strings.HasPrefix(version, "V") {
		version = version[1:]
	}
	return version
}

func versionsMatch(currentVersion, latestVersion string) bool {
	return compareReleaseVersions(currentVersion, latestVersion) == comparisonCurrent
}

func compareReleaseVersions(currentVersion, latestVersion string) updateComparison {
	if isDevelopmentVersion(currentVersion) {
		return comparisonDevelopment
	}

	current, currentOK := parseSemanticVersion(currentVersion)
	latest, latestOK := parseSemanticVersion(latestVersion)
	if !currentOK || !latestOK {
		if normalizeVersion(currentVersion) == normalizeVersion(latestVersion) {
			return comparisonCurrent
		}
		return comparisonUnknown
	}

	switch compareSemanticVersions(current, latest) {
	case 0:
		return comparisonCurrent
	case -1:
		return comparisonBehind
	default:
		return comparisonAhead
	}
}

type semanticVersion struct {
	major      int
	minor      int
	patch      int
	prerelease []string
}

func parseSemanticVersion(version string) (semanticVersion, bool) {
	version = normalizeVersion(version)
	if version == "" {
		return semanticVersion{}, false
	}
	if buildIndex := strings.IndexByte(version, '+'); buildIndex >= 0 {
		version = version[:buildIndex]
	}
	parts := strings.SplitN(version, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return semanticVersion{}, false
	}

	major, ok := parseSemanticNumber(core[0])
	if !ok {
		return semanticVersion{}, false
	}
	minor, ok := parseSemanticNumber(core[1])
	if !ok {
		return semanticVersion{}, false
	}
	patch, ok := parseSemanticNumber(core[2])
	if !ok {
		return semanticVersion{}, false
	}

	parsed := semanticVersion{major: major, minor: minor, patch: patch}
	if len(parts) == 2 {
		parsed.prerelease = strings.Split(parts[1], ".")
		for _, identifier := range parsed.prerelease {
			if !validPrereleaseIdentifier(identifier) {
				return semanticVersion{}, false
			}
		}
	}
	return parsed, true
}

func parseSemanticNumber(value string) (int, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') || !allDigits(value) {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil
}

func validPrereleaseIdentifier(identifier string) bool {
	if identifier == "" || (allDigits(identifier) && len(identifier) > 1 && identifier[0] == '0') {
		return false
	}
	for i := range identifier {
		ch := identifier[i]
		if (ch < '0' || ch > '9') && (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && ch != '-' {
			return false
		}
	}
	return true
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func compareSemanticVersions(a, b semanticVersion) int {
	if a.major != b.major {
		return compareInts(a.major, b.major)
	}
	if a.minor != b.minor {
		return compareInts(a.minor, b.minor)
	}
	if a.patch != b.patch {
		return compareInts(a.patch, b.patch)
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

func compareInts(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for i := range min(len(a), len(b)) {
		aNumeric := allDigits(a[i])
		bNumeric := allDigits(b[i])
		if aNumeric && bNumeric {
			if comparison := compareNumericIdentifiers(a[i], b[i]); comparison != 0 {
				return comparison
			}
			continue
		}
		if aNumeric != bNumeric {
			if aNumeric {
				return -1
			}
			return 1
		}
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return compareInts(len(a), len(b))
}

func compareNumericIdentifiers(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if a == "" {
		a = "0"
	}
	if b == "" {
		b = "0"
	}
	if len(a) != len(b) {
		return compareInts(len(a), len(b))
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func isDevelopmentVersion(version string) bool {
	switch strings.ToLower(normalizeVersion(version)) {
	case "", "dev", "latest", "(devel)":
		return true
	default:
		return false
	}
}
