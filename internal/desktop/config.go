package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RDPConfig holds the connection parameters for a single RDP target.
type RDPConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	IgnoreCert bool   `json:"ignore_cert"`
	// Security sets the RDP security mode passed to guacd: "any" (default),
	// "rdp" (classic), "nla", or "tls". Use "rdp" for VirtualBox VRDE.
	Security string `json:"security,omitempty"`
	// DriveEnabled enables RDP drive redirection via guacd. When true, the
	// host directory mounted at /guacd-shared inside the guacd container is
	// exposed to Windows as a named drive. DriveName controls the label
	// Windows shows in File Explorer (e.g. "Shared" appears as
	// "Shared on Dexbox"). Defaults to "Shared" when blank.
	DriveEnabled bool   `json:"drive_enabled,omitempty"`
	DriveName    string `json:"drive_name,omitempty"`
	// KeyDelayMs controls the inter-keystroke pause used by TypeText.
	//   unset / 0  use the default delay (50 ms) — recommended for reliability
	//   > 0        use exactly that many milliseconds
	//   < 0        invalid; TypeText returns an error
	KeyDelayMs int `json:"key_delay_ms,omitempty"`
}

// ConnectionStore persists RDP connection configs to ~/.dexbox/connections.json.
type ConnectionStore struct {
	path        string
	Connections map[string]RDPConfig `json:"connections"`
	mu          sync.Mutex
}

// NewConnectionStore creates a store backed by the given file path.
func NewConnectionStore(path string) *ConnectionStore {
	return &ConnectionStore{
		path:        path,
		Connections: make(map[string]RDPConfig),
	}
}

// DefaultStorePath returns ~/.dexbox/connections.json.
func DefaultStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".dexbox", "connections.json")
}

// Load reads the store from disk. Missing file is not an error (empty store).
func (s *ConnectionStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read connection store: %w", err)
	}

	var store struct {
		Connections map[string]RDPConfig `json:"connections"`
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("parse connection store: %w", err)
	}
	if store.Connections != nil {
		s.Connections = store.Connections
	}
	return nil
}

// Save writes the store to disk, creating parent directories as needed.
func (s *ConnectionStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(struct {
		Connections map[string]RDPConfig `json:"connections"`
	}{Connections: s.Connections}, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, append(data, '\n'), 0o600)
}

// Add registers a new RDP connection. Returns an error if the name already exists.
func (s *ConnectionStore) Add(name string, cfg RDPConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.Connections[name]; exists {
		return fmt.Errorf("connection %q already exists", name)
	}

	// Apply defaults
	if cfg.Port == 0 {
		cfg.Port = 3389
	}
	if cfg.Width == 0 {
		cfg.Width = 1024
	}
	if cfg.Height == 0 {
		cfg.Height = 768
	}

	s.Connections[name] = cfg
	return nil
}

// Remove deletes an RDP connection by name.
func (s *ConnectionStore) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.Connections[name]; !exists {
		return fmt.Errorf("connection %q not found", name)
	}
	delete(s.Connections, name)
	return nil
}

// Get returns a connection config by name.
func (s *ConnectionStore) Get(name string) (RDPConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.Connections[name]
	return cfg, ok
}

// List returns all connection configs.
func (s *ConnectionStore) List() map[string]RDPConfig {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make(map[string]RDPConfig, len(s.Connections))
	for k, v := range s.Connections {
		result[k] = v
	}
	return result
}
