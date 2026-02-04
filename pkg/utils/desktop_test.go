package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDesktopFile(t *testing.T) {
	content := `[Desktop Entry]
Name=Chromium
Exec=chromium-browser %U
Icon=chromium
Terminal=false
Type=Application
@@EXTRA_DESKTOP_ENTRIES@@

[Desktop Action new-window]
Name=New Window
Exec=chromium-browser`

	tmpDir, err := os.MkdirTemp("", "desktop-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	desktopPath := filepath.Join(tmpDir, "test.desktop")
	if err := os.WriteFile(desktopPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	df, err := ParseDesktopFile(desktopPath)
	if err != nil {
		t.Fatalf("ParseDesktopFile failed: %v", err)
	}

	if df.GetValue("Desktop Entry", "Name") != "Chromium" {
		t.Errorf("Expected Name=Chromium, got %s", df.GetValue("Desktop Entry", "Name"))
	}

	if df.GetValue("Desktop Entry", "Exec") != "chromium-browser %U" {
		t.Errorf("Expected Exec=chromium-browser %%U, got %s", df.GetValue("Desktop Entry", "Exec"))
	}

	// This should be ignored
	if val, ok := df.Sections["Desktop Entry"]["@@EXTRA_DESKTOP_ENTRIES@@"]; ok {
		t.Errorf("@@EXTRA_DESKTOP_ENTRIES@@ should have been ignored, but got %s", val)
	}

	if df.GetValue("Desktop Action new-window", "Name") != "New Window" {
		t.Errorf("Expected Name=New Window in section Desktop Action new-window, got %s", df.GetValue("Desktop Action new-window", "Name"))
	}
}
