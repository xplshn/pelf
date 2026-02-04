package utils

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// --- FS-Related functions --

// FindFiles searches for files matching the given glob patterns in the filesystem up to the specified walk depth.
// If walkDepth is 0, it searches all subdirectories. Returns the path of the first matching file or an empty string if none found.
func FindFiles(fsys fs.FS, dir string, walkDepth uint, globs []string) (string, error) {
	var foundPath string

	err := fs.WalkDir(fsys, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Calculate depth by counting path separators relative to dir
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		var currentDepth int
		if relPath == "." {
			currentDepth = 0
		} else {
			currentDepth = len(strings.Split(relPath, string(filepath.Separator)))
		}

		// Skip if beyond walkDepth (unless walkDepth is 0)
		if walkDepth != 0 && currentDepth > int(walkDepth) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Check if the file matches any glob pattern
		if !d.IsDir() {
			for _, glob := range globs {
				if match, _ := filepath.Match(glob, d.Name()); match {
					foundPath = path
					return fs.SkipAll
				}
			}
		}
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to walk directory %s: %w", dir, err)
	}

	return foundPath, nil
}

// IsRepo returns true if the string contains both a dot (.) and a slash (/)
func IsRepo(s string) bool {
	return strings.Contains(s, ".") && strings.Contains(s, "/")
}

// --- AppBundleID-related code ---

// AppBundleID format types
const (
	TypeI   = iota + 1 // name-dd_mm_yyyy-maintainer OR name-versionString-maintainer OR name-dd_mm_yyyy-repo OR name-versionString-repo
	TypeII             // name#repo[:version]
	TypeIII            // name#repo[:version][@date]
)

const (
	ValidSubstr        = `^[A-Za-z0-9._]+$`
	ValidRepoSubstr    = `^[A-Za-z0-9._/\-]+$`
	ValidNameSubstr    = `^[A-Za-z0-9._/\-]+$`
	ValidVersionSubstr = `^[A-Za-z0-9._]+$` // Version cannot contain hyphens
	TypeIFormat        = `^(.+)-(\d{2}_\d{2}_\d{4}|\d{4}\d{2}\d{2}|\d{4}_\d{2}_\d{2})-([^-]+)$`
	TypeIIFormat       = `^([^#]+)#([^:@]+)(?::([^@]+))?$`
	TypeIIIFormat      = `^([^#]+)#([^:@]+)(?::([^@]+))?(@(?:\d{2}_\d{2}_\d{4}|\d{4}\d{2}\d{2}|\d{4}_\d{2}_\d{2}))$`
	DateFormat         = `^(\d{2})_(\d{2})_(\d{4})$`
	// Multiple date format support
	TimeLayoutYYYYMMDD   = "20060102"   // YYYYMMDD
	TimeLayoutDD_MM_YYYY = "02_01_2006" // DD_MM_YYYY
	TimeLayoutYYYY_MM_DD = "2006_01_02" // YYYY_MM_DD
)

var (
	typeIRe              = regexp.MustCompile(TypeIFormat)
	typeIIRe             = regexp.MustCompile(TypeIIFormat)
	typeIIIRe            = regexp.MustCompile(TypeIIIFormat)
	dateRe               = regexp.MustCompile(DateFormat)
	validSubstrRe        = regexp.MustCompile(ValidSubstr)
	validRepoSubstrRe    = regexp.MustCompile(ValidRepoSubstr)
	validNameSubstrRe    = regexp.MustCompile(ValidNameSubstr)
	validVersionSubstrRe = regexp.MustCompile(ValidVersionSubstr)
)

// parseDate attempts to parse a date string using multiple supported formats
func parseDate(dateStr string) (*time.Time, error) {
	layouts := []string{
		TimeLayoutYYYYMMDD,
		TimeLayoutYYYY_MM_DD,
		TimeLayoutDD_MM_YYYY,
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, dateStr); err == nil {
			return &t, nil
		}
	}

	return nil, fmt.Errorf("unable to parse date: %s", dateStr)
}

// isDateString checks if a string matches any of the supported date formats
func isDateString(s string) bool {
	layouts := []string{
		TimeLayoutYYYYMMDD,
		TimeLayoutYYYY_MM_DD,
		TimeLayoutDD_MM_YYYY,
	}

	for _, layout := range layouts {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}

	return false
}

// formatDate formats a time using the preferred DD_MM_YYYY format
func formatDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(TimeLayoutYYYYMMDD)
}

// validateField checks if the field matches the appropriate regex and replaces '/' with '.' for repo and maintainer.
func validateField(field, fieldName string) (string, error) {
	if field == "" {
		return "", nil
	}

	var re *regexp.Regexp
	switch fieldName {
	case "repo":
		re = validRepoSubstrRe
	case "name":
		re = validNameSubstrRe
	case "version":
		field = strings.ReplaceAll(field, "-", "_")
		re = validVersionSubstrRe
	default:
		re = validSubstrRe
	}

	if !re.MatchString(field) {
		return "", fmt.Errorf("invalid characters in %s: %s", fieldName, field)
	}

	if fieldName == "repo" || fieldName == "maintainer" {
		return strings.ReplaceAll(field, "/", "."), nil
	}
	return field, nil
}

type AppBundleID struct {
	Raw     string
	Name    string
	Repo    string
	Version string
	Date    *time.Time
}

// ParseAppBundleID parses the input into a structured AppBundleID.
// It supports type I, type II, and type III formats.
func ParseAppBundleID(raw string) (*AppBundleID, int, error) {
	if raw == "" {
		return nil, -1, fmt.Errorf("AppBundleID is empty")
	}

	// Try type III format first (most specific)
	if m := typeIIIRe.FindStringSubmatch(raw); m != nil {
		name, err := validateField(m[1], "name")
		if err != nil {
			return nil, -1, err
		}
		repo, err := validateField(m[2], "repo")
		if err != nil {
			return nil, -1, err
		}

		var version string
		if m[3] != "" {
			if version, err = validateField(m[3], "version"); err != nil {
				return nil, -1, err
			}
		}

		dateStr := strings.TrimPrefix(m[4], "@")
		t, err := parseDate(dateStr)
		if err != nil {
			return nil, -1, fmt.Errorf("invalid date in AppBundleID: %s: %w", dateStr, err)
		}

		return &AppBundleID{
			Raw:     raw,
			Name:    name,
			Repo:    repo,
			Version: version,
			Date:    t,
		}, TypeIII, nil
	}

	// Try type II format
	if m := typeIIRe.FindStringSubmatch(raw); m != nil {
		name, err := validateField(m[1], "name")
		if err != nil {
			return nil, -1, err
		}
		repo, err := validateField(m[2], "repo")
		if err != nil {
			return nil, -1, err
		}

		var version string
		if m[3] != "" {
			if version, err = validateField(m[3], "version"); err != nil {
				return nil, -1, err
			}
		}

		return &AppBundleID{
			Raw:     raw,
			Name:    name,
			Repo:    repo,
			Version: version,
		}, TypeII, nil
	}

	// Handle type I format
	if m := typeIRe.FindStringSubmatch(raw); m != nil {
		name, err := validateField(m[1], "name")
		if err != nil {
			return nil, -1, err
		}

		var version string
		var t *time.Time

		// Check if second part is a date or version using the new multi-format date checker
		if isDateString(m[2]) {
			parsedTime, err := parseDate(m[2])
			if err != nil {
				return nil, -1, fmt.Errorf("invalid date: %w", err)
			}
			t = parsedTime
		} else {
			// It's a version string
			version, err = validateField(m[2], "version")
			if err != nil {
				return nil, -1, err
			}
		}

		repo, err := validateField(m[3], "maintainer")
		if err != nil {
			return nil, -1, err
		}

		return &AppBundleID{
			Raw:     raw,
			Name:    name,
			Repo:    repo,
			Version: version,
			Date:    t,
		}, TypeI, nil
	}

	return nil, -1, fmt.Errorf("invalid AppBundleID format: %s", raw)
}

// Format returns the string representation of the AppBundleID in the specified format type.
func (a *AppBundleID) Format(formatType int) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil AppBundleID")
	}

	switch formatType {
	case TypeI:
		if a.Name == "" || a.Repo == "" {
			return "", fmt.Errorf("insufficient fields for type I format")
		}
		if a.Date != nil {
			return fmt.Sprintf("%s-%s-%s", a.Name, formatDate(a.Date), a.Repo), nil
		}
		if a.Version != "" {
			return fmt.Sprintf("%s-%s-%s", a.Name, a.Version, a.Repo), nil
		}
		return "", fmt.Errorf("type I format requires date or version")

	case TypeII:
		if a.Name == "" || a.Repo == "" {
			return "", fmt.Errorf("insufficient fields for type II format")
		}
		if a.Version != "" {
			return fmt.Sprintf("%s#%s:%s", a.Name, a.Repo, a.Version), nil
		}
		return fmt.Sprintf("%s#%s", a.Name, a.Repo), nil

	case TypeIII:
		if a.Name == "" || a.Repo == "" {
			return "", fmt.Errorf("insufficient fields for type III format")
		}
		base := fmt.Sprintf("%s#%s", a.Name, a.Repo)
		if a.Version != "" {
			base = fmt.Sprintf("%s#%s:%s", a.Name, a.Repo, a.Version)
		}
		if a.Date != nil {
			return base + "@" + formatDate(a.Date), nil
		}
		// Fall back to type II if no date
		return base, nil

	default:
		return "", fmt.Errorf("invalid format type: %d", formatType)
	}
}

// String returns the canonical string representation.
// Prefers type III if possible, falls back to type II, then type I.
func (a *AppBundleID) String() string {
	if a == nil {
		return ""
	}

	// Try type III first
	if s, err := a.Format(TypeIII); err == nil {
		return s
	}

	// Try type II
	if s, err := a.Format(TypeII); err == nil {
		return s
	}

	// Try type I
	if s, err := a.Format(TypeI); err == nil {
		return s
	}

	// Fallback to raw if all else fails
	return a.Raw
}

// MarshalText implements encoding.TextMarshaler.
func (a *AppBundleID) MarshalText() ([]byte, error) {
	if a == nil {
		return nil, nil
	}

	// Try to marshal in preferred order: III, II, I
	for _, formatType := range []int{TypeIII, TypeII, TypeI} {
		if s, err := a.Format(formatType); err == nil {
			return []byte(s), nil
		}
	}

	return nil, fmt.Errorf("insufficient fields to marshal AppBundleID")
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *AppBundleID) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		return fmt.Errorf("empty text for AppBundleID")
	}

	parsed, _, err := ParseAppBundleID(string(text))
	if err != nil {
		return fmt.Errorf("failed to parse AppBundleID: %w", err)
	}

	*a = *parsed
	return nil
}

// IsDated returns true if a date is present.
func (a *AppBundleID) IsDated() bool {
	return a != nil && a.Date != nil
}

// ShortName returns only the base part without date.
func (a *AppBundleID) ShortName() string {
	if a == nil {
		return ""
	}

	if a.Repo != "" {
		if a.Version != "" {
			return fmt.Sprintf("%s#%s:%s", a.Name, a.Repo, a.Version)
		}
		return fmt.Sprintf("%s#%s", a.Name, a.Repo)
	}

	return a.Raw
}

// Compliant returns an error if the ID doesn't follow any valid format.
func (a *AppBundleID) Compliant() (int, error) {
	if a == nil {
		return -1, fmt.Errorf("nil AppBundleID")
	}

	_, t, err := ParseAppBundleID(a.Raw)
	return t, err
}

// --- Appstream-related stuff ---

// AppStreamIDToName extracts the application name from an AppStream ID.
func AppStreamIDToName(appStreamID string) string {
	if appStreamID == "" {
		return ""
	}
	parts := strings.Split(appStreamID, ".")
	if len(parts) > 0 {
		return Sanitize(parts[len(parts)-1])
	}
	return appStreamID
}

func IsAppStreamID(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) == 3 {
		return true
	}
	return false
}

// --- Misc stuff ---

// Sanitize cleans up a name string by normalizing case and replacing problematic characters.
func Sanitize(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "(", "")
	name = strings.ReplaceAll(name, ")", "")
	return name
}
