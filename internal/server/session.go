package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Session struct {
	Token            string              `json:"token"`
	APIKey           string              `json:"api_key"`
	AnthropicAPIKey  string              `json:"anthropic_api_key,omitempty"`
	OpenAIAPIKey     string              `json:"openai_api_key,omitempty"`
	LuxAPIKey        string              `json:"lux_api_key,omitempty"`
	GeminiAPIKey     string              `json:"gemini_api_key,omitempty"`
	Model            string              `json:"model"`
	Provider         string              `json:"provider"`
	AnthropicBaseURL string              `json:"anthropic_base_url,omitempty"`
	OpenAIBaseURL    string              `json:"openai_base_url,omitempty"`
	WorkflowID       string              `json:"workflow_id"`
	Variables        map[string]any      `json:"variables,omitempty"`
	SecureParams     map[string]string   `json:"secure_params,omitempty"`
	ArtifactsDir     string              `json:"artifacts_dir,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
	Code             string              `json:"code,omitempty"`
	EventQueue       chan map[string]any `json:"-"`
	OutputData       map[string]any      `json:"output,omitempty"`
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

func (m *SessionManager) CreateSession(s *Session) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s.Token == "" {
		s.Token = uuid.New().String()
	}
	s.CreatedAt = time.Now()
	s.EventQueue = make(chan map[string]any, 100)
	m.sessions[s.Token] = s
	return s.Token
}

func (m *SessionManager) GetSession(token string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[token]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	return s, nil
}

func (m *SessionManager) DeleteSession(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}
