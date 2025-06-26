package utils

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Constants for regex patterns and time format
const (
	ValidSubstr        = `^[A-Za-z0-9.\-/]+$`
	ValidRepoSubstr    = `^[A-Za-z0-9.\-_/]+$`
	ValidNameSubstr    = `^[A-Za-z0-9.\-/]+$`
	LegacyFormat       = `^(.+)-(\d{2}_\d{2}_\d{4})-([^-]+)$` // legacy: name-dd_mm_yyyy-maintainer
	NewBaseFormat      = `^([^#]+)#([^:@]+)(?::([^@]+))?(@\d{2}_\d{2}_\d{4})?$` // name#repo[:version][@date]
	DateFormat         = `^(\d{2})_(\d{2})_(\d{4})$`
	TimeLayout         = "02_01_2006"
)

var (
	legacyRe          = regexp.MustCompile(LegacyFormat)
	baseRe            = regexp.MustCompile(NewBaseFormat)
	dateRe            = regexp.MustCompile(DateFormat)
	validSubstrRe     = regexp.MustCompile(ValidSubstr)
	validRepoSubstrRe = regexp.MustCompile(ValidRepoSubstr)
	validNameSubstrRe = regexp.MustCompile(ValidNameSubstr)
)

// validateField checks if the field matches the appropriate regex and replaces '/' with '.' for repo and maintainer.
func validateField(field, fieldName string) (string, error) {
	if field == "" {
		return "", nil
	}
	var re *regexp.Regexp

	if fieldName == "repo" {
		re = validRepoSubstrRe
	} else if fieldName == "name" {
		re = validNameSubstrRe
	} else {
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
	Raw        string
	Name       string
	Repo       string
	Version    string
	Date       *time.Time
	Maintainer string
}

// ParseAppBundleID parses the input into a structured AppBundleID.
// It supports both legacy and encouraged formats.
func ParseAppBundleID(raw string) (*AppBundleID, error) {
	if raw == "" {
		return nil, fmt.Errorf("AppBundleID is empty")
	}

	// Handle legacy format
	if legacyRe.MatchString(raw) {
		m := legacyRe.FindStringSubmatch(raw)
		t, err := time.Parse(TimeLayout, m[2])
		if err != nil {
			return nil, fmt.Errorf("invalid legacy date: %v", err)
		}
		maintainer, err := validateField(m[3], "maintainer")
		if err != nil {
			return nil, err
		}
		name, err := validateField(m[1], "name")
		if err != nil {
			return nil, err
		}
		return &AppBundleID{
			Raw:        raw,
			Name:       name,
			Date:       &t,
			Maintainer: maintainer,
		}, nil
	}

	// Handle new formats
	match := baseRe.FindStringSubmatch(raw)
	if match == nil {
		return nil, fmt.Errorf("invalid AppBundleID base format: %s", raw)
	}

	name, err := validateField(match[1], "name")
	if err != nil {
		return nil, err
	}
	repo, err := validateField(match[2], "repo")
	if err != nil {
		return nil, err
	}
	var version string
	if match[3] != "" {
		version, err = validateField(match[3], "version")
		if err != nil {
			return nil, err
		}
	}
	var tPtr *time.Time
	if match[4] != "" {
		dateStr := strings.TrimPrefix(match[4], "@")
		t, err := time.Parse(TimeLayout, dateStr)
		if err != nil {
			return nil, fmt.Errorf("invalid date in AppBundleID: %s. %v", dateStr, err)
		}
		tPtr = &t
	}

	return &AppBundleID{
		Raw:     raw,
		Name:    name,
		Repo:    repo,
		Version: version,
		Date:    tPtr,
	}, nil
}

// String returns the properly formatted canonical form (NewBaseFormat, possibly with @date).
func (a *AppBundleID) String() string {
	if a == nil {
		return ""
	}
	if a.Repo != "" {
		if a.Version != "" {
			base := fmt.Sprintf("%s#%s:%s", a.Name, a.Repo, a.Version)
			if a.Date != nil {
				return base + "@" + a.Date.Format(TimeLayout)
			}
			return base
		}
		// Handle name#repo@date format
		base := fmt.Sprintf("%s#%s", a.Name, a.Repo)
		if a.Date != nil {
			return base + "@" + a.Date.Format(TimeLayout)
		}
		return base
	}
	if a.Maintainer != "" && a.Date != nil {
		return fmt.Sprintf("%s-%s-%s", a.Name, a.Date.Format(TimeLayout), a.Maintainer)
	}
	return a.Raw
}

// IsDated returns true if a date is present.
func (a *AppBundleID) IsDated() bool {
	return a != nil && a.Date != nil
}

// ShortName returns only the base part.
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
	if a.Maintainer != "" && a.Date != nil {
		return fmt.Sprintf("%s-%s-%s", a.Name, a.Date.Format(TimeLayout), a.Maintainer)
	}
	return a.Raw
}

// MarshalText serializes the AppBundleID as text based on available fields.
func (a *AppBundleID) MarshalText() ([]byte, error) {
	if a == nil {
		return nil, nil
	}
	if a.Name != "" && a.Maintainer != "" && a.Date != nil {
		// Legacy format: name-dd_mm_yyyy-maintainer
		return []byte(fmt.Sprintf("%s-%s-%s", a.Name, a.Date.Format(TimeLayout), a.Maintainer)), nil
	}
	if a.Name != "" && a.Repo != "" {
		// Encouraged format: name#repo[:version][@dd_mm_yyyy]
		return []byte(a.String()), nil
	}
	return nil, fmt.Errorf("insufficient fields to marshal AppBundleID")
}

// UnmarshalText sets the AppBundleID by parsing text.
func (a *AppBundleID) UnmarshalText(text []byte) error {
	parsed, err := ParseAppBundleID(string(text))
	if err != nil {
		return err
	}
	*a = *parsed
	return nil
}

// Verify returns an error if the ID doesn't follow legacy or new base format.
func (a *AppBundleID) Verify() error {
	if a == nil {
		return fmt.Errorf("nil AppBundleID")
	}
	_, err := ParseAppBundleID(a.Raw)
	return err
}

// Compliant returns an error if the ID is *not* in the encouraged new format.
func (a *AppBundleID) Compliant() error {
	if a == nil {
		return fmt.Errorf("nil AppBundleID")
	}
	if a.Maintainer != "" {
		return fmt.Errorf("non-compliant AppBundleID format: %s. Expected 'name#repo:version', 'name#repo@date', or 'name#repo:version@date'", a.Raw)
	}
	return nil
}
