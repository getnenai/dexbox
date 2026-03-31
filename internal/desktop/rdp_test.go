package desktop

import "testing"

// TestRDP_GuacdConnectionID_BeforeConnect verifies that GuacdConnectionID
// returns an empty string before the client has reached SessionActive.
func TestRDP_GuacdConnectionID_BeforeConnect(t *testing.T) {
	r := NewRDP("test", RDPConfig{}, "localhost:4822")
	if id := r.GuacdConnectionID(); id != "" {
		t.Errorf("expected empty string before connect, got %q", id)
	}
}

// TestRDP_GuacdConnectionID_AfterActive verifies that GuacdConnectionID
// returns the connection ID stored once the session reaches SessionActive.
// We set connID directly (same package) to isolate the getter from the
// real guacd connection logic.
func TestRDP_GuacdConnectionID_AfterActive(t *testing.T) {
	r := NewRDP("test", RDPConfig{}, "localhost:4822")
	r.connID = "$abc-def-123"

	if id := r.GuacdConnectionID(); id != "$abc-def-123" {
		t.Errorf("expected $abc-def-123, got %q", id)
	}
}
