package utils

import (
	"testing"
	"time"
)

func TestParseAppBundleID(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		shouldErr  bool
		expected   *AppBundleID
	}{
		{
			name:      "Valid new format without date",
			raw:       "myapp#core_repo:v1.2.3",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:     "myapp#core_repo:v1.2.3",
				Name:    "myapp",
				Repo:    "core_repo",
				Version: "v1.2.3",
			},
		},
		{
			name:      "Valid new format with date",
			raw:       "myapp#core_repo:v1.2.3@01_01_2023",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:     "myapp#core_repo:v1.2.3@01_01_2023",
				Name:    "myapp",
				Repo:    "core_repo",
				Version: "v1.2.3",
				Date:    parseTime(t, "01_01_2023"),
			},
		},
		{
			name:      "Valid legacy format",
			raw:       "some-tool-13_04_2022-xplshn",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:        "some-tool-13_04_2022-xplshn",
				Name:       "some-tool",
				Maintainer: "xplshn",
				Date:       parseTime(t, "13_04_2022"),
			},
		},
		{
			name:      "Invalid format",
			raw:       "invalid_format_string",
			shouldErr: true,
		},
		{
			name:      "Invalid characters in repo",
			raw:       "app#core$repo:v1",
			shouldErr: true,
		},
		{
			name:      "Invalid characters in maintainer",
			raw:       "app-01_01_2023-main_tainer",
			shouldErr: true,
		},
		{
			name:      "Invalid characters in name",
			raw:       "app_name#core:v1",
			shouldErr: true,
		},
		{
			name:      "Invalid characters in version",
			raw:       "app#core:v1_2",
			shouldErr: true,
		},
		{
			name:      "Repo with slashes",
			raw:       "app#user/repo/sub:v1.0",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:     "app#user/repo/sub:v1.0",
				Name:    "app",
				Repo:    "user.repo.sub",
				Version: "v1.0",
			},
		},
		{
			name:      "Maintainer with slashes",
			raw:       "tool-01_01_2023-user/repo",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:        "tool-01_01_2023-user/repo",
				Name:       "tool",
				Maintainer: "user.repo",
				Date:       parseTime(t, "01_01_2023"),
			},
		},
		{
			name:      "Empty input",
			raw:       "",
			shouldErr: true,
		},
		{
			name:      "Underscore in repo (valid)",
			raw:       "app#core_repo_sub:v1.0",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:     "app#core_repo_sub:v1.0",
				Name:    "app",
				Repo:    "core_repo_sub",
				Version: "v1.0",
			},
		},
		{
			name:      "Underscore in name (invalid)",
			raw:       "app_name#core:v1",
			shouldErr: true,
		},
		{
			name:      "Underscore in version (invalid)",
			raw:       "app#core:v1_0",
			shouldErr: true,
		},
		{
			name:      "Underscore in maintainer (invalid)",
			raw:       "tool-01_01_2023-user_repo",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ParseAppBundleID(tt.raw)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("Expected error for %q, got none", tt.raw)
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error for %q: %v", tt.raw, err)
				return
			}
			if id.Raw != tt.expected.Raw {
				t.Errorf("Raw mismatch: got %q, want %q", id.Raw, tt.expected.Raw)
			}
			if id.Name != tt.expected.Name {
				t.Errorf("Name mismatch: got %q, want %q", id.Name, tt.expected.Name)
			}
			if id.Repo != tt.expected.Repo {
				t.Errorf("Repo mismatch: got %q, want %q", id.Repo, tt.expected.Repo)
			}
			if id.Version != tt.expected.Version {
				t.Errorf("Version mismatch: got %q, want %q", id.Version, tt.expected.Version)
			}
			if id.Maintainer != tt.expected.Maintainer {
				t.Errorf("Maintainer mismatch: got %q, want %q", id.Maintainer, tt.expected.Maintainer)
			}
			if tt.expected.Date == nil && id.Date != nil {
				t.Errorf("Date mismatch: got %v, want nil", id.Date)
			} else if tt.expected.Date != nil && (id.Date == nil || !id.Date.Equal(*tt.expected.Date)) {
				t.Errorf("Date mismatch: got %v, want %v", id.Date, tt.expected.Date)
			}
		})
	}
}

func TestMarshalText(t *testing.T) {
	tests := []struct {
		name      string
		id        *AppBundleID
		expected  string
		shouldErr bool
	}{
		{
			name: "Legacy format",
			id: &AppBundleID{
				Name:       "tool",
				Maintainer: "user",
				Date:       parseTime(t, "01_01_2023"),
			},
			expected:  "tool-01_01_2023-user",
			shouldErr: false,
		},
		{
			name: "New format without date",
			id: &AppBundleID{
				Name:    "app",
				Repo:    "core",
				Version: "v1.0",
			},
			expected:  "app#core:v1.0",
			shouldErr: false,
		},
		{
			name: "New format with date",
			id: &AppBundleID{
				Name:    "app",
				Repo:    "core",
				Version: "v1.0",
				Date:    parseTime(t, "01_01_2023"),
			},
			expected:  "app#core:v1.0@01_01_2023",
			shouldErr: false,
		},
		{
			name:      "Insufficient fields",
			id:        &AppBundleID{Name: "app"},
			shouldErr: true,
		},
		{
			name:      "Nil AppBundleID",
			id:        nil,
			expected:  "",
			shouldErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := tt.id.MarshalText()
			if tt.shouldErr {
				if err == nil {
					t.Errorf("Expected error, got none")
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if string(b) != tt.expected {
				t.Errorf("Got %q, want %q", string(b), tt.expected)
			}
		})
	}
}

func TestCompliant(t *testing.T) {
	tests := []struct {
		name      string
		id        *AppBundleID
		shouldErr bool
	}{
		{
			name: "Legacy format",
			id: &AppBundleID{
				Raw:        "foo-01_01_2023-bar",
				Name:       "foo",
				Maintainer: "bar",
				Date:       parseTime(t, "01_01_2023"),
			},
			shouldErr: true,
		},
		{
			name: "New format",
			id: &AppBundleID{
				Raw:     "foo#repo:v1",
				Name:    "foo",
				Repo:    "repo",
				Version: "v1",
			},
			shouldErr: false,
		},
		{
			name: "New format with date",
			id: &AppBundleID{
				Raw:     "foo#repo:v1@01_01_2023",
				Name:    "foo",
				Repo:    "repo",
				Version: "v1",
				Date:    parseTime(t, "01_01_2023"),
			},
			shouldErr: false,
		},
		{
			name:      "Nil AppBundleID",
			id:        nil,
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.id.Compliant()
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error, got none")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	tests := []struct {
		name      string
		id        *AppBundleID
		shouldErr bool
	}{
		{
			name: "Valid new format",
			id: &AppBundleID{
				Raw:     "pkg#repo:v0.9",
				Name:    "pkg",
				Repo:    "repo",
				Version: "v0.9",
			},
			shouldErr: false,
		},
		{
			name: "Invalid format",
			id: &AppBundleID{
				Raw: "??invalid!",
			},
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.id.Verify()
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error, got none")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestStringOutput(t *testing.T) {
	tests := []struct {
		name     string
		id       *AppBundleID
		expected string
	}{
		{
			name: "New format with date",
			id: &AppBundleID{
				Raw:     "foo#bar:v3@12_12_2024",
				Name:    "foo",
				Repo:    "bar",
				Version: "v3",
				Date:    parseTime(t, "12_12_2024"),
			},
			expected: "foo#bar:v3@12_12_2024",
		},
		{
			name: "Legacy format",
			id: &AppBundleID{
				Raw:        "tool-01_01_2023-user",
				Name:       "tool",
				Maintainer: "user",
				Date:       parseTime(t, "01_01_2023"),
			},
			expected: "tool-01_01_2023-user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.String(); got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestShortName(t *testing.T) {
	tests := []struct {
		name     string
		id       *AppBundleID
		expected string
	}{
		{
			name: "New format",
			id: &AppBundleID{
				Raw:     "tool#main:v5@01_06_2025",
				Name:    "tool",
				Repo:    "main",
				Version: "v5",
				Date:    parseTime(t, "01_06_2025"),
			},
			expected: "tool#main:v5",
		},
		{
			name: "Legacy format",
			id: &AppBundleID{
				Raw:        "tool-01_01_2023-user",
				Name:       "tool",
				Maintainer: "user",
				Date:       parseTime(t, "01_01_2023"),
			},
			expected: "tool-01_01_2023-user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.ShortName(); got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

// parseTime is a helper to parse time strings for tests.
func parseTime(t *testing.T, s string) *time.Time {
	tm, err := time.Parse(TimeLayout, s)
	if err != nil {
		t.Fatalf("Failed to parse time %q: %v", s, err)
	}
	return &tm
}
