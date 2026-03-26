package tools

import (
	"strings"
	"testing"
)

func TestSanitizeOutput_NormalizesLineEndings(t *testing.T) {
	input := "line1\r\nline2\r\nline3\r\n"
	got := sanitizeOutput(input)
	if strings.Contains(got, "\r") {
		t.Errorf("output still contains \\r: %q", got)
	}
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeOutput_TrimsTrailingWhitespace(t *testing.T) {
	input := "Name   \t\nLength   \n"
	got := sanitizeOutput(input)
	for i, line := range strings.Split(got, "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d has trailing whitespace: %q", i, line)
		}
	}
}

func TestSanitizeOutput_CollapsesBlankLines(t *testing.T) {
	input := "header\n\n\n\n\ncontent\n\n\n\nfooter"
	got := sanitizeOutput(input)
	// Should never have 3+ consecutive newlines (i.e. 2+ blank lines).
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("output contains 3+ consecutive newlines: %q", got)
	}
	if !strings.Contains(got, "header") || !strings.Contains(got, "footer") {
		t.Errorf("output lost content: %q", got)
	}
}

func TestSanitizeOutput_TrimsLeadingTrailingBlanks(t *testing.T) {
	input := "\r\n\r\n  actual content  \r\n\r\n"
	got := sanitizeOutput(input)
	if got != "actual content" {
		t.Errorf("got %q, want %q", got, "actual content")
	}
}

func TestSanitizeOutput_PassthroughCleanOutput(t *testing.T) {
	input := "File copied successfully."
	got := sanitizeOutput(input)
	if got != input {
		t.Errorf("clean output was modified: got %q, want %q", got, input)
	}
}

func TestSanitizeOutput_TruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("x", 20_000)
	got := sanitizeOutput(long)
	if len(got) > maxOutputLen+200 { // allow for truncation marker
		t.Errorf("output too long: %d chars (max ~%d)", len(got), maxOutputLen+200)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncated output missing truncation marker")
	}
}

func TestSanitizeOutput_RealWorldPowerShell(t *testing.T) {
	// Simulate typical messy output from Get-ChildItem.
	input := "\r\nName                           Length LastWriteTime           \r\n" +
		"----                           ------ -------------           \r\n" +
		"invoice_BRL001.pdf               2042 3/26/2026 12:59 AM     \r\n" +
		"                                                              \r\n" +
		"                                                              \r\n" +
		"\r\n\r\n"
	got := sanitizeOutput(input)

	if strings.Contains(got, "\r") {
		t.Errorf("output still contains \\r")
	}
	if strings.Contains(got, "           \n") {
		t.Errorf("output has trailing spaces")
	}
	if !strings.Contains(got, "invoice_BRL001.pdf") {
		t.Error("lost content")
	}
}
