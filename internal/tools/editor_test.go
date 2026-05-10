package tools

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestPsQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple path", `C:\Users\dexbox\file.txt`, `'C:\Users\dexbox\file.txt'`},
		{"path with spaces", `C:\Program Files\My App\file.txt`, `'C:\Program Files\My App\file.txt'`},
		{"path with single quote", `C:\Users\O'Brien\file.txt`, `'C:\Users\O''Brien\file.txt'`},
		{"path with dollar sign", `C:\$Recycle.Bin\file.txt`, `'C:\$Recycle.Bin\file.txt'`},
		{"path with backtick", "C:\\Users\\test`file.txt", "'C:\\Users\\test`file.txt'"},
		{"path with multiple quotes", `it's a 'test'`, `'it''s a ''test'''`},
		{"empty string", "", "''"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PsQuote(tt.input)
			if got != tt.want {
				t.Errorf("PsQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPsBase64Expr(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"simple text", "Hello, World!"},
		{"multiline", "line1\nline2\nline3"},
		{"special chars", "path with $var and `backtick` and 'quotes'"},
		{"empty", ""},
		{"unicode", "emoji: \u2603 snowman"},
		{"windows newlines", "line1\r\nline2\r\nline3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr := psBase64Expr(tt.content)

			// Verify the expression wraps a valid base64 payload.
			if !strings.HasPrefix(expr, "[System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('") {
				t.Errorf("unexpected prefix: %s", expr)
			}
			if !strings.HasSuffix(expr, "'))") {
				t.Errorf("unexpected suffix: %s", expr)
			}

			// Extract and decode the base64 payload.
			b64 := expr[len("[System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('") : len(expr)-len("'))")]
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				t.Fatalf("base64 decode failed: %v", err)
			}
			if string(decoded) != tt.content {
				t.Errorf("round-trip failed: got %q, want %q", string(decoded), tt.content)
			}
		})
	}
}
