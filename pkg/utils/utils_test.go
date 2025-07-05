package utils

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"
)

func parseTime(t *testing.T, s string) *time.Time {
	t.Helper()
	layouts := []string{
		TimeLayoutDD_MM_YYYY, // DD_MM_YYYY
		TimeLayoutYYYYMMDD,   // YYYYMMDD
		TimeLayoutYYYY_MM_DD, // YYYY_MM_DD
	}
	for _, layout := range layouts {
		if tm, err := time.Parse(layout, s); err == nil {
			return &tm
		}
	}
	t.Fatalf("Failed to parse time %q", s)
	return nil
}

func TestParseAppBundleID(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		shouldErr bool
		expected  *AppBundleID
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
			raw:       "myapp#core_repo:v1.2.3@20230101",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:     "myapp#core_repo:v1.2.3@20230101",
				Name:    "myapp",
				Repo:    "core_repo",
				Version: "v1.2.3",
				Date:    parseTime(t, "2023_01_01"),
			},
		},
		{
			name:      "Valid legacy format",
			raw:       "some-tool-13_04_2022-xplshn",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:  "some-tool-13_04_2022-xplshn",
				Name: "some-tool",
				Repo: "xplshn",
				Date: parseTime(t, "13_04_2022"),
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
			name:      "Invalid characters in name",
			raw:       "app name#core:v1",
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
			name:      "Empty input",
			raw:       "",
			shouldErr: true,
		},
		{
			name:      "Complex repo format",
			raw:       "app#github.com/xplshn/pelf:v1.0",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:     "app#github.com/xplshn/pelf:v1.0",
				Name:    "app",
				Repo:    "github.com.xplshn.pelf",
				Version: "v1.0",
			},
		},
		{
			name:      "Version with hyphens",
			raw:       "app#repo:v1.2.3-beta",
			shouldErr: false,
			expected: &AppBundleID{
				Raw:     "app#repo:v1.2.3-beta",
				Name:    "app",
				Repo:    "repo",
				Version: "v1.2.3_beta",
			},
		},
		{
			name:      "Version with invalid characters",
			raw:       "app#repo:v1.2.3!",
			shouldErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, _, err := ParseAppBundleID(tt.raw)
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
			if tt.expected.Date == nil && id.Date != nil {
				t.Errorf("Date mismatch: got %v, want nil", id.Date)
			} else if tt.expected.Date != nil && (id.Date == nil || !id.Date.Equal(*tt.expected.Date)) {
				t.Errorf("Date mismatch: got %v, want %v", id.Date, tt.expected.Date)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name       string
		id         *AppBundleID
		formatType int
		expected   string
		shouldErr  bool
	}{
		{
			name: "TypeI with date",
			id: &AppBundleID{
				Name: "app",
				Repo: "repo",
				Date: parseTime(t, "2023_01_01"),
			},
			formatType: TypeI,
			expected:   "app-20230101-repo",
			shouldErr:  false,
		},
		{
			name: "TypeI with version",
			id: &AppBundleID{
				Name:    "app",
				Repo:    "repo",
				Version: "v1.0",
			},
			formatType: TypeI,
			expected:   "app-v1.0-repo",
			shouldErr:  false,
		},
		{
			name: "TypeI without date or version",
			id: &AppBundleID{
				Name: "app",
				Repo: "repo",
			},
			formatType: TypeI,
			expected:   "",
			shouldErr:  true,
		},
		{
			name: "TypeII with version",
			id: &AppBundleID{
				Name:    "app",
				Repo:    "repo",
				Version: "v1.0",
			},
			formatType: TypeII,
			expected:   "app#repo:v1.0",
			shouldErr:  false,
		},
		{
			name: "TypeII without version",
			id: &AppBundleID{
				Name: "app",
				Repo: "repo",
			},
			formatType: TypeII,
			expected:   "app#repo",
			shouldErr:  false,
		},
		{
			name: "TypeIII with version and date",
			id: &AppBundleID{
				Name:    "app",
				Repo:    "repo",
				Version: "v1.0",
				Date:    parseTime(t, "2023_01_01"),
			},
			formatType: TypeIII,
			expected:   "app#repo:v1.0@20230101",
			shouldErr:  false,
		},
		{
			name: "TypeIII without version with date",
			id: &AppBundleID{
				Name: "app",
				Repo: "repo",
				Date: parseTime(t, "2023_01_01"),
			},
			formatType: TypeIII,
			expected:   "app#repo@20230101",
			shouldErr:  false,
		},
		{
			name: "TypeIII without date",
			id: &AppBundleID{
				Name: "app",
				Repo: "repo",
			},
			formatType: TypeIII,
			expected:   "app#repo",
			shouldErr:  false,
		},
		{
			name: "Invalid format type",
			id: &AppBundleID{
				Name: "app",
				Repo: "repo",
			},
			formatType: 999,
			expected:   "",
			shouldErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.id.Format(tt.formatType)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("Expected error, got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if got != tt.expected {
					t.Errorf("Expected %q, got %q", tt.expected, got)
				}
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
			name: "Type I format",
			id: &AppBundleID{
				Name: "tool",
				Repo: "user",
				Date: parseTime(t, "2023_01_01"),
			},
			expected:  "tool#user@20230101",
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
				Date:    parseTime(t, "2023_01_01"),
			},
			expected:  "app#core:v1.0@20230101",
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
			name: "Valid legacy format",
			id: &AppBundleID{
				Raw:  "tool-01_01_2023-user",
				Name: "tool",
				Repo: "user",
				Date: parseTime(t, "01_01_2023"),
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
		{
			name:      "Nil AppBundleID",
			id:        nil,
			shouldErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.id.Compliant()
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
				Date:    parseTime(t, "2024_12_12"),
			},
			expected: "foo#bar:v3@20241212",
		},
		{
			name: "Legacy format",
			id: &AppBundleID{
				Raw:  "tool-01_01_2023-user",
				Name: "tool",
				Repo: "user",
				Date: parseTime(t, "2023_01_01"),
			},
			expected: "tool#user@20230101",
		},
		{
			name: "New format without date",
			id: &AppBundleID{
				Name:    "app",
				Repo:    "core",
				Version: "v1.0",
			},
			expected: "app#core:v1.0",
		},
		{
			name:     "Nil AppBundleID",
			id:       nil,
			expected: "",
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
				Raw:  "tool-01_01_2023-user",
				Name: "tool",
				Repo: "user",
				Date: parseTime(t, "01_01_2023"),
			},
			expected: "tool#user",
		},
		{
			name:     "Nil AppBundleID",
			id:       nil,
			expected: "",
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

func TestIsDated(t *testing.T) {
	tests := []struct {
		name     string
		id       *AppBundleID
		expected bool
	}{
		{
			name: "With date",
			id: &AppBundleID{
				Raw:  "app#core:v1.0@01_01_2023",
				Date: parseTime(t, "01_01_2023"),
			},
			expected: true,
		},
		{
			name: "Without date",
			id: &AppBundleID{
				Raw: "app#core:v1.0",
			},
			expected: false,
		},
		{
			name:     "Nil AppBundleID",
			id:       nil,
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.IsDated(); got != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestAppStreamIDToName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Valid AppStream ID",
			input:    "org.example.App",
			expected: "app",
		},
		{
			name:     "Single part",
			input:    "app",
			expected: "app",
		},
		{
			name:     "Empty input",
			input:    "",
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AppStreamIDToName(tt.input); got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Normal string",
			input:    "MyApp",
			expected: "myapp",
		},
		{
			name:     "With slashes and colons",
			input:    "My/App:Test",
			expected: "my_app_test",
		},
		{
			name:     "With spaces",
			input:    " My App ",
			expected: "my_app",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.input); got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestFindFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"root/file1.txt":           {Data: []byte("file1 content")},
		"root/subdir/file2.conf":   {Data: []byte("file2 content")},
		"root/subdir/sub/file3.yml": {Data: []byte("file3 content")},
	}
	tests := []struct {
		name      string
		fsys      fs.FS
		dir       string
		walkDepth uint
		globs     []string
		expected  string
		shouldErr bool
	}{
		{
			name:      "Find txt file at root",
			fsys:      fsys,
			dir:       "root",
			walkDepth: 1,
			globs:     []string{"*.txt"},
			expected:  "root/file1.txt",
			shouldErr: false,
		},
		{
			name:      "Find conf file in subdir",
			fsys:      fsys,
			dir:       "root",
			walkDepth: 2,
			globs:     []string{"*2.conf"},
			expected:  "root/subdir/file2.conf",
			shouldErr: false,
		},
		{
			name:      "Find yml file in deep subdir",
			fsys:      fsys,
			dir:       "root",
			walkDepth: 0,
			globs:     []string{"*.yml"},
			expected:  "root/subdir/sub/file3.yml",
			shouldErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FindFiles(tt.fsys, tt.dir, tt.walkDepth, tt.globs)
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
			if got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestIsRepo(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Valid repo with domain",
			input:    "github.com/xplshn/pelf",
			expected: true,
		},
		{
			name:     "Valid repo with custom domain",
			input:    "git.lol.org/game",
			expected: true,
		},
		{
			name:     "Invalid repo no dot",
			input:    "user/repo",
			expected: false,
		},
		{
			name:     "Invalid repo no slash",
			input:    "user.name",
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRepo(tt.input); got != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, got)
			}
		})
	}
}
