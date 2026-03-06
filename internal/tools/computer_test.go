package tools

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

// To test ComputerTool without actually running xdotool/scrot, we can briefly modify
// the tool or use an override/mock. Since Go tests allow us to replace functions
// or we can test using a fake runner.
// Let's modify computer.go in a second to support a custom CommandRunner interface
// or just test the behavior via interface.
// For now, I'll write the test structure, then modify computer.go to support injecting a runner.

type mockRunner struct {
	commands [][]string
}

func (m *mockRunner) runCommand(ctx context.Context, cmdName string, args ...string) (string, error) {
	fullCmd := append([]string{cmdName}, args...)
	m.commands = append(m.commands, fullCmd)
	return "mock_output", nil
}

func TestComputerTool_Click(t *testing.T) {
	runner := &mockRunner{}
	tool := &ComputerTool{
		Runner: runner,
	}

	err := tool.Click(context.Background(), "left")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(runner.commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(runner.commands))
	}

	expected := []string{"xdotool", "click", "1"}
	if !reflect.DeepEqual(runner.commands[0], expected) {
		t.Errorf("expected %v, got %v", expected, runner.commands[0])
	}
}

func TestComputerTool_Move(t *testing.T) {
	runner := &mockRunner{}
	tool := &ComputerTool{
		Runner: runner,
	}

	err := tool.Move(context.Background(), 100, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"xdotool", "mousemove", "--sync", "100", "200"}
	if !reflect.DeepEqual(runner.commands[0], expected) {
		t.Errorf("expected %v, got %v", expected, runner.commands[0])
	}
}

func TestComputerTool_Type(t *testing.T) {
	runner := &mockRunner{}
	tool := &ComputerTool{
		Runner: runner,
	}

	err := tool.Type(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"xdotool", "type", "--delay", "12", "--", "hello"}
	if !reflect.DeepEqual(runner.commands[0], expected) {
		t.Errorf("expected %v, got %v", expected, runner.commands[0])
	}
}

func TestComputerTool_Scroll(t *testing.T) {
	runner := &mockRunner{}
	tool := &ComputerTool{
		Runner: runner,
	}

	x := 50
	y := 60
	err := tool.Scroll(context.Background(), "down", 3, &x, &y)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"xdotool", "mousemove", "--sync", "50", "60", "click", "--repeat", "3", "5"}
	if !reflect.DeepEqual(runner.commands[0], expected) {
		t.Errorf("expected %v, got %v", expected, runner.commands[0])
	}
}

func TestComputerTool_Screenshot(t *testing.T) {
	// For Screenshot, testing without gnome-screenshot or scrot present can fail
	// because it uses exec.LookPath before calling runCommand.
	// We can set up a dummy executable in PATH for testing, or skip testing lookpath.
	// Let's create a dummy script in a temporary dir and add to PATH.

	tempDir := t.TempDir()
	dummyScrot := tempDir + "/scrot"
	err := os.WriteFile(dummyScrot, []byte("#!/bin/sh\ntouch $2"), 0755)
	if err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)
	os.Setenv("PATH", tempDir+":"+oldPath)

	runner := &mockRunner{}
	tool := &ComputerTool{
		Runner: runner,
	}

	// We create a dummy file to be read by screenshot so it doesn't fail os.ReadFile
	// The runner won't actually call scrot.
	// But computer.go expects the file to be there afterwards.
	// We should mock runCommand to actually create the file if cmdName == "scrot".
	runnerWithFile := &fileCreatingRunner{
		mockRunner: runner,
	}
	tool.Runner = runnerWithFile

	output, err := tool.Screenshot(context.Background(), "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(output, "bW9jayBpbWFnZQ==") { // "mock image"
		t.Errorf("unexpected output: %s", output)
	}

	// Just check prefix of the commands since the path involves uuid
	if runner.commands[0][0] != "scrot" || runner.commands[0][1] != "-p" {
		t.Errorf("unexpected command: %v", runner.commands[0])
	}
}

type fileCreatingRunner struct {
	*mockRunner
}

func (m *fileCreatingRunner) runCommand(ctx context.Context, cmdName string, args ...string) (string, error) {
	_, err := m.mockRunner.runCommand(ctx, cmdName, args...)
	if cmdName == "scrot" {
		// Create the file that the screenshot function expects
		path := args[1]
		os.WriteFile(path, []byte("mock image"), 0644)
	}
	return "mock_output", err
}
