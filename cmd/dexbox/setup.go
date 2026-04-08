package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// dexbox setup — configuration commands
// ---------------------------------------------------------------------------

func cmdSetup() *cobra.Command {
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Configure integrations",
	}

	setup.AddCommand(cmdSetupClaudeCode())
	return setup
}

// ---------------------------------------------------------------------------
// dexbox setup claude-code
// ---------------------------------------------------------------------------

func cmdSetupClaudeCode() *cobra.Command {
	var desktop string

	c := &cobra.Command{
		Use:   "claude-code",
		Short: "Configure Claude Code to use dexbox as an MCP server",
		Long: `Automatically configure Claude Code's MCP settings to connect to dexbox.

This writes (or merges into) ~/.claude/claude_code_config.json so that
Claude Code discovers the "dexbox" MCP server on next startup.

Examples:
  dexbox setup claude-code
  dexbox setup claude-code --desktop my-vm`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setupClaudeCode(desktop)
		},
	}

	c.Flags().StringVar(&desktop, "desktop", "", "Default desktop name (sets DEXBOX_DESKTOP env var in config)")
	return c
}

func setupClaudeCode(desktop string) error {
	// Find the dexbox binary path
	binPath, err := findDexboxBinary()
	if err != nil {
		return fmt.Errorf("could not locate dexbox binary: %w", err)
	}
	fmt.Printf("Found dexbox at: %s\n", binPath)

	// Build the MCP server config
	env := map[string]string{}
	if desktop != "" {
		env["DEXBOX_DESKTOP"] = desktop
	}

	serverConfig := map[string]any{
		"command": binPath,
		"args":    []string{"mcp"},
	}
	if len(env) > 0 {
		serverConfig["env"] = env
	}

	// Read existing config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home directory: %w", err)
	}
	configPath := filepath.Join(homeDir, ".claude", "claude_code_config.json")

	var cfg map[string]any
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cfg = map[string]any{}
		}
	} else {
		cfg = map[string]any{}
	}

	// Merge MCP servers
	mcpServers, _ := cfg["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = map[string]any{}
	}

	// Check if dexbox already configured
	if existing, ok := mcpServers["dexbox"].(map[string]any); ok {
		existingCmd, _ := existing["command"].(string)
		existingArgs, _ := existing["args"].([]any)
		argsStr := ""
		for _, a := range existingArgs {
			if s, ok := a.(string); ok {
				argsStr += " " + s
			}
		}
		fmt.Printf("Existing dexbox config found:\n  command: %s%s\nUpdating...\n", existingCmd, argsStr)
	}

	mcpServers["dexbox"] = serverConfig
	cfg["mcpServers"] = mcpServers

	// Write config
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("could not write config: %w", err)
	}

	fmt.Printf("\n✅ Claude Code configured!\n\n")
	fmt.Printf("  Config: %s\n", configPath)
	fmt.Printf("  Server: %s mcp\n", binPath)
	if desktop != "" {
		fmt.Printf("  Desktop: %s\n", desktop)
	}
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. Make sure dexbox is running:  dexbox start\n")
	fmt.Printf("  2. Open Claude Code and start prompting!\n")

	return nil
}

func findDexboxBinary() (string, error) {
	// 1. Try the current executable
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
		if err == nil && fileExists(exe) {
			if isBinaryNamed(exe, "dexbox") {
				return exe, nil
			}
		}
	}

	// 2. Try PATH lookup
	path, err := exec.LookPath("dexbox")
	if err == nil {
		abs, err := filepath.Abs(path)
		if err == nil {
			return abs, nil
		}
		return path, nil
	}

	// 3. Try current directory
	cwd, _ := os.Getwd()
	local := filepath.Join(cwd, "dexbox")
	if fileExists(local) {
		return local, nil
	}

	return "", fmt.Errorf("dexbox binary not found in PATH or current directory")
}

func isBinaryNamed(path, name string) bool {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base)) == name
}