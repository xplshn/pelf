package utils

import (
	"bufio"
	"os"
	"strings"
)

// DesktopFile represents a parsed .desktop file
type DesktopFile struct {
	Sections map[string]map[string]string
}

// ParseDesktopFile parses a .desktop file from the given path.
// It is designed to be robust and ignore lines that do not follow the key=value format,
// such as placeholders or malformed entries, instead of failing.
func ParseDesktopFile(filePath string) (*DesktopFile, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	df := &DesktopFile{
		Sections: make(map[string]map[string]string),
	}

	scanner := bufio.NewScanner(file)
	currentSection := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for section header [Section Name]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = line[1 : len(line)-1]
			if df.Sections[currentSection] == nil {
				df.Sections[currentSection] = make(map[string]string)
			}
			continue
		}

		if currentSection != "" {
			// Find the first '=' character
			idx := strings.Index(line, "=")
			if idx != -1 {
				key := strings.TrimSpace(line[:idx])
				value := strings.TrimSpace(line[idx+1:])
				df.Sections[currentSection][key] = value
			}
			// Lines without '=' are ignored (e.g., placeholders like @@EXTRA_DESKTOP_ENTRIES@@)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return df, nil
}

// GetValue returns the value for a given key in a section.
// Returns an empty string if the section or key does not exist.
func (df *DesktopFile) GetValue(section, key string) string {
	if s, ok := df.Sections[section]; ok {
		return s[key]
	}
	return ""
}
