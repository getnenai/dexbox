package server

import (
	"testing"
)

func TestSessionManager_CreateAndGet(t *testing.T) {
	sm := NewSessionManager()

	session := &Session{
		APIKey: "test-key",
		Model:  "test-model",
	}

	token := sm.CreateSession(session)
	if token == "" {
		t.Error("expected non-empty token")
	}
	if session.Token != token {
		t.Errorf("expected session.Token to be set to %s, got %s", token, session.Token)
	}
	if session.EventQueue == nil {
		t.Error("expected EventQueue to be initialized")
	}

	retrieved, err := sm.GetSession(token)
	if err != nil {
		t.Fatalf("expected no error getting session, got %v", err)
	}
	if retrieved.APIKey != "test-key" {
		t.Errorf("expected APIKey 'test-key', got '%s'", retrieved.APIKey)
	}
}

func TestSessionManager_Delete(t *testing.T) {
	sm := NewSessionManager()
	token := sm.CreateSession(&Session{APIKey: "del-key"})

	_, err := sm.GetSession(token)
	if err != nil {
		t.Fatalf("did not expect error, got %v", err)
	}

	sm.DeleteSession(token)

	_, err = sm.GetSession(token)
	if err == nil {
		t.Error("expected error getting session after deletion, got nil")
	}
}

func TestSessionManager_GetNonExistent(t *testing.T) {
	sm := NewSessionManager()

	_, err := sm.GetSession("invalid-token")
	if err == nil {
		t.Error("expected error for non-existent token")
	}
}
