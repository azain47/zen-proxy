package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckForUpdateAtMatchesReleaseTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repository/releases/latest" {
			t.Fatalf("path = %q, want latest release endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Fatalf("Accept = %q, want GitHub JSON media type", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3"}`))
	}))
	defer server.Close()

	result, err := checkForUpdateAt(context.Background(), "1.2.3", "owner/repository", server.URL, server.Client())
	if err != nil {
		t.Fatalf("checkForUpdateAt() error = %v", err)
	}
	if result.Comparison != comparisonCurrent {
		t.Fatalf("result = %#v, want current", result)
	}
	if result.Latest != "v1.2.3" {
		t.Fatalf("latest = %q, want v1.2.3", result.Latest)
	}
}

func TestCheckForUpdateAtDetectsDevelopmentAndMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	}))
	defer server.Close()

	development, err := checkForUpdateAt(context.Background(), "dev", "owner/repository", server.URL, server.Client())
	if err != nil {
		t.Fatalf("development check error = %v", err)
	}
	if development.Comparison != comparisonDevelopment {
		t.Fatalf("development result = %#v", development)
	}

	mismatch, err := checkForUpdateAt(context.Background(), "v1.9.0", "owner/repository", server.URL, server.Client())
	if err != nil {
		t.Fatalf("mismatch check error = %v", err)
	}
	if mismatch.Comparison != comparisonBehind {
		t.Fatalf("mismatch result = %#v, want behind", mismatch)
	}
}

func TestLatestReleaseEndpointRejectsInvalidRepository(t *testing.T) {
	if _, err := latestReleaseEndpoint("https://api.example", "invalid"); err == nil {
		t.Fatal("latestReleaseEndpoint() accepted repository without owner")
	}
}

func TestCompareReleaseVersions(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    updateComparison
	}{
		{name: "behind", current: "v1.2.0", latest: "v1.2.1", want: comparisonBehind},
		{name: "current", current: "1.2.1", latest: "v1.2.1", want: comparisonCurrent},
		{name: "ahead", current: "v1.3.0", latest: "v1.2.1", want: comparisonAhead},
		{name: "prerelease", current: "v1.2.1-rc.1", latest: "v1.2.1", want: comparisonBehind},
		{name: "build metadata", current: "v1.2.1+local", latest: "v1.2.1", want: comparisonCurrent},
		{name: "development", current: "dev", latest: "v1.2.1", want: comparisonDevelopment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareReleaseVersions(tc.current, tc.latest); got != tc.want {
				t.Fatalf("compareReleaseVersions(%q, %q) = %d, want %d", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

func TestParseRunOptionsSupportsUpdateCheck(t *testing.T) {
	_, command, err := parseRunOptions([]string{"--check-update"})
	if err != nil {
		t.Fatalf("parseRunOptions() error = %v", err)
	}
	if command != "check-update" {
		t.Fatalf("command = %q, want check-update", command)
	}
}
