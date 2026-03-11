package agent

import (
	"context"
	"testing"

	"github.com/getnenai/dexbox/pkg/cua"
)

type mockProvider struct {
	action        *cua.Action
	assistantText string
	err           error
}

func (m *mockProvider) Setup(config cua.DisplayConfig) error {
	return nil
}

func (m *mockProvider) PredictAction(ctx context.Context, history []cua.Message, state cua.VisualState) (*cua.Action, string, error) {
	return m.action, m.assistantText, m.err
}

func (m *mockProvider) NeedsVisuals() bool {
	return false
}

func (m *mockProvider) FormatHistory(history []cua.Message) (any, error) {
	return nil, nil
}

func TestSamplingLoop_NilActionWithAssistantText(t *testing.T) {
	provider := &mockProvider{
		action:        nil,
		assistantText: "I am thinking",
		err:           nil,
	}

	agent := NewAgent(provider, nil)

	instruction := "Do something"
	messages, err := agent.SamplingLoop(context.Background(), instruction, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(messages))
	}

	lastMsg := messages[len(messages)-1]
	if lastMsg.Role != "assistant" {
		t.Errorf("expected last message role to be assistant, got %s", lastMsg.Role)
	}
	if lastMsg.Content != "I am thinking" {
		t.Errorf("expected last message content to be 'I am thinking', got %s", lastMsg.Content)
	}
}
