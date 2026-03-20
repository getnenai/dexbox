package config

import (
	"os"
	"path/filepath"
	"strconv"
)

var (
	VMName           = getEnv("DEXBOX_VM_NAME", "dexbox-win11")
	VMUser           = getEnv("DEXBOX_VM_USER", "dexbox")
	VMPass           = getEnv("DEXBOX_VM_PASS", "dexbox123")
	SOAPAddr         = getEnv("DEXBOX_SOAP_ADDR", "http://localhost:18083")
	SharedDir        = getEnvExpand("DEXBOX_SHARED_DIR", filepath.Join(homeDir(), ".dexbox", "shared"))
	Listen           = getEnv("DEXBOX_LISTEN", ":8600")
	ScreenshotWidth  = getEnvInt("DEXBOX_SCREENSHOT_WIDTH", 1024)
	ScreenshotHeight = getEnvInt("DEXBOX_SCREENSHOT_HEIGHT", 768)
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvExpand(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
