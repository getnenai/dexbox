// Package main — dexbox CLI
//
// Commands:
//
//	dexbox start        — Start dexbox server + vboxwebsrv daemon
//	dexbox stop         — Stop dexbox server + vboxwebsrv
//	dexbox status       — Server health + list of VMs
//	dexbox create vm    — Install VirtualBox + create and provision a VM
//	dexbox vm ...       — VM lifecycle commands
//	dexbox run ...      — Execute tool actions directly
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/getnenai/dexbox/internal/config"
	"github.com/getnenai/dexbox/internal/server"
	"github.com/getnenai/dexbox/internal/vbox"
)

func main() {
	var envFile string

	root := &cobra.Command{
		Use:   "dexbox",
		Short: "dexbox — VirtualBox-based computer-use tool server",
		Long: `dexbox is a VirtualBox-based computer-use tool server.

It manages Windows VMs and exposes an HTTP API for computer-use actions
(screenshots, mouse, keyboard, file operations) used by AI workflows.

Environment variables (or .env file):
  DEXBOX_VM_USER           Guest OS username (default: dexbox)
  DEXBOX_VM_PASS           Guest OS password (default: dexbox123)
  DEXBOX_SOAP_ADDR         VirtualBox SOAP endpoint (default: http://localhost:18083)
  DEXBOX_SHARED_DIR        Host/guest shared folder (default: ~/.dexbox/shared)
  DEXBOX_LISTEN            Server listen address (default: :8600)
  DEXBOX_SCREENSHOT_WIDTH  Screenshot width in pixels (default: 1024)
  DEXBOX_SCREENSHOT_HEIGHT Screenshot height in pixels (default: 768)`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if envFile != "" {
				if err := godotenv.Load(envFile); err != nil {
					return fmt.Errorf("loading env file %s: %w", envFile, err)
				}
			} else {
				_ = godotenv.Load()
			}
			return nil
		},
	}

	root.PersistentFlags().StringVarP(&envFile, "env-file", "e", "", "Path to .env file")

	root.AddCommand(cmdStart(), cmdStop(), cmdStatus(), cmdCreate(), cmdVM(), cmdRunAction())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// dexbox start
// ---------------------------------------------------------------------------

func cmdStart() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start dexbox server and vboxwebsrv",
		RunE: func(cmd *cobra.Command, args []string) error {
			port := parseSoapPort()

			// Run vboxwebsrv as a foreground child so Go owns the real PID
			// and can kill it reliably on shutdown.
			fmt.Println("Starting vboxwebsrv...")
			vboxwebsrv := exec.Command("vboxwebsrv", "--host", "localhost", "--port", port)
			vboxwebsrv.Stdout = os.Stdout
			vboxwebsrv.Stderr = os.Stderr
			if err := vboxwebsrv.Start(); err != nil {
				return fmt.Errorf("failed to start vboxwebsrv: %w", err)
			}

			// Poll until vboxwebsrv is actually listening.
			if err := waitForPort(port, 10*time.Second); err != nil {
				_ = vboxwebsrv.Process.Kill()
				return fmt.Errorf("vboxwebsrv failed to start: %w", err)
			}

			fmt.Println("Starting dexbox server...")
			srv := server.New(server.Options{
				ListenAddr: config.Listen,
				SOAPAddr:   config.SOAPAddr,
				VMUser:     config.VMUser,
				VMPass:     config.VMPass,
				Width:      config.ScreenshotWidth,
				Height:     config.ScreenshotHeight,
				SharedDir:  config.SharedDir,
			})

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			errCh := make(chan error, 1)
			go func() {
				errCh <- srv.Run()
			}()

			// Detect if vboxwebsrv dies while we're running.
			vboxDied := make(chan error, 1)
			go func() {
				vboxDied <- vboxwebsrv.Wait()
			}()

			// Helper to shut down vboxwebsrv gracefully with a timeout.
			stopVBox := func() {
				_ = vboxwebsrv.Process.Signal(syscall.SIGTERM)
				select {
				case <-vboxDied:
				case <-time.After(5 * time.Second):
					_ = vboxwebsrv.Process.Kill()
				}
			}

			select {
			case err := <-errCh:
				stopVBox()
				return err
			case err := <-vboxDied:
				if err != nil {
					return fmt.Errorf("vboxwebsrv exited unexpectedly: %w", err)
				}
				return fmt.Errorf("vboxwebsrv exited unexpectedly")
			case <-ctx.Done():
				fmt.Println("\nShutting down...")
				stopVBox()
				return nil
			}
		},
	}
}

// ---------------------------------------------------------------------------
// dexbox stop
// ---------------------------------------------------------------------------

func cmdStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop dexbox server and vboxwebsrv",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Stopping vboxwebsrv...")
			_ = exec.Command("pkill", "-f", "vboxwebsrv").Run()

			// Verify the process actually died.
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if exec.Command("pgrep", "-f", "vboxwebsrv").Run() != nil {
					fmt.Println("Stopped.")
					return nil
				}
				time.Sleep(500 * time.Millisecond)
			}

			// Force kill if still alive.
			_ = exec.Command("pkill", "-9", "-f", "vboxwebsrv").Run()
			fmt.Println("Force stopped.")
			return nil
		},
	}
}

// parseSoapPort extracts the port number from config.SOAPAddr (e.g. "http://localhost:18083").
func parseSoapPort() string {
	u, err := url.Parse(config.SOAPAddr)
	if err != nil || u.Port() == "" {
		return "18083"
	}
	return u.Port()
}

// waitForPort polls until a TCP connection to localhost:port succeeds or the timeout expires.
func waitForPort(port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "localhost:"+port, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("port %s not ready after %s", port, timeout)
}

// ---------------------------------------------------------------------------
// dexbox status
// ---------------------------------------------------------------------------

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server health and VM states",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// List VMs
			vms, err := vbox.ListVMs(ctx)
			if err != nil {
				fmt.Printf("VirtualBox: not available (%v)\n", err)
				return nil
			}

			if len(vms) == 0 {
				fmt.Println("No VMs found. Run 'dexbox create vm <name> --iso <path>' to create one.")
				return nil
			}

			fmt.Printf("%-20s %-12s %s\n", "NAME", "STATE", "GUEST ADDITIONS")
			for _, name := range vms {
				state, _ := vbox.VMState(ctx, name)
				ga := vbox.GuestAdditionsReady(ctx, name)
				gaStr := "no"
				if ga {
					gaStr = "yes"
				}
				fmt.Printf("%-20s %-12s %s\n", name, state, gaStr)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// dexbox create
// ---------------------------------------------------------------------------

func cmdCreate() *cobra.Command {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create dexbox-managed resources",
	}
	create.AddCommand(cmdCreateVM())
	return create
}

func cmdCreateVM() *cobra.Command {
	var isoPath string
	c := &cobra.Command{
		Use:   "vm <name>",
		Short: "Install VirtualBox and provision a new Windows VM",
		Long: `Install VirtualBox (if needed), then create and provision a Windows 11 VM.

The VM name is used as the VirtualBox machine name and must be unique.
On ARM hosts the --iso flag is required; on x86 the ISO is auto-downloaded.

Example:
  dexbox create vm desktop-1 --iso ~/Downloads/Win11_25H2_English_Arm64.iso`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			expandedISO := isoPath
			if isoPath != "" && len(isoPath) > 1 && isoPath[0] == '~' {
				home, _ := os.UserHomeDir()
				expandedISO = home + isoPath[1:]
			}
			return vbox.Install(context.Background(), name, expandedISO)
		},
	}
	c.Flags().StringVar(&isoPath, "iso", "", "Path to Windows ISO (required on ARM; auto-downloaded on x86)")
	return c
}

// ---------------------------------------------------------------------------
// dexbox vm
// ---------------------------------------------------------------------------

func cmdVM() *cobra.Command {
	vm := &cobra.Command{
		Use:   "vm",
		Short: "Manage VMs",
		Long: `Manage VirtualBox VMs used by dexbox.

If a command accepts an optional [name] argument and it is omitted,
dexbox auto-detects the VM. Auto-detection fails when multiple VMs exist.`,
	}

	vm.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List all VMs",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				mgr := vbox.NewManager(config.SOAPAddr, config.VMUser, config.VMPass)
				vms, err := mgr.List(ctx)
				if err != nil {
					return err
				}
				if len(vms) == 0 {
					fmt.Println("No VMs found.")
					return nil
				}
				fmt.Printf("%-20s %-12s %s\n", "NAME", "STATE", "GUEST ADDITIONS")
				for _, vm := range vms {
					gaStr := "no"
					if vm.GuestAdditions {
						gaStr = "yes"
					}
					fmt.Printf("%-20s %-12s %s\n", vm.Name, vm.State, gaStr)
				}
				return nil
			},
		},
		vmAction("start", "Start a VM", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			return mgr.Start(ctx, name)
		}),
		vmAction("stop", "Graceful ACPI shutdown", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			return mgr.Stop(ctx, name)
		}),
		vmAction("poweroff", "Immediately cut power (force stop)", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			return mgr.Poweroff(ctx, name)
		}),
		vmAction("pause", "Freeze VM in memory", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			return mgr.Pause(ctx, name)
		}),
		vmAction("suspend", "Save state to disk and power off", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			return mgr.Suspend(ctx, name)
		}),
		vmAction("resume", "Resume from pause or suspend", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			return mgr.Resume(ctx, name)
		}),
		vmAction("status", "Show VM state", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			status, err := mgr.Status(ctx, name)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(status, "", "  ")
			fmt.Println(string(b))
			return nil
		}),
		vmAction("destroy", "Delete VM and disk", func(ctx context.Context, mgr *vbox.Manager, name string) error {
			return mgr.Delete(ctx, name)
		}),
	)

	return vm
}

func vmAction(use, short string, fn func(context.Context, *vbox.Manager, string) error) *cobra.Command {
	return &cobra.Command{
		Use:   use + " [name]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr := vbox.NewManager(config.SOAPAddr, config.VMUser, config.VMPass)

			name, err := resolveVMName(ctx, args)
			if err != nil {
				return err
			}

			return fn(ctx, mgr, name)
		},
	}
}

// resolveVMName resolves the VM name from args or auto-detects it.
func resolveVMName(ctx context.Context, args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}

	vms, err := vbox.ListVMs(ctx)
	if err != nil {
		return "", err
	}
	if len(vms) == 0 {
		return "", fmt.Errorf("no VMs found. Run 'dexbox create vm <name> --iso <path>' to create one")
	}
	if len(vms) > 1 {
		return "", fmt.Errorf("multiple VMs found (%s), specify a name", strings.Join(vms, ", "))
	}
	return vms[0], nil
}

// ---------------------------------------------------------------------------
// dexbox run
// ---------------------------------------------------------------------------

func cmdRunAction() *cobra.Command {
	var (
		toolType   string
		action     string
		coordinate string
		text       string
		command    string
		path       string
		fileText   string
		oldStr     string
		newStr     string
		vmName     string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute a tool action directly",
		Long: `Execute a low-level tool action against a VM.

Tool types and their actions:

  computer
    screenshot               Capture the VM screen (outputs raw PNG to stdout)
    left_click   --coordinate x,y
    right_click  --coordinate x,y
    middle_click --coordinate x,y
    double_click --coordinate x,y
    mouse_move   --coordinate x,y  Move cursor without clicking
    scroll       --coordinate x,y  Scroll down at position
    type         --text <string>    Type a string of text
    key          --text <key>       Press a key (e.g. Return, ctrl+c)

  bash
    --command <ps1>   Run a PowerShell command on the guest and print output

  text_editor
    --command view    --path <guest-path>
    --command create  --path <guest-path> --file-text <content>

Examples:
  dexbox run --type computer --action screenshot > screen.png
  dexbox run --type computer --action left_click --coordinate 100,200
  dexbox run --type computer --action type --text "hello world"
  dexbox run --type bash --command "Get-Process"
  dexbox run --type text_editor --command view --path 'C:\Users\dexbox\file.txt'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr := vbox.NewManager(config.SOAPAddr, config.VMUser, config.VMPass)

			name := vmName
			if name == "" {
				var err error
				name, err = resolveVMName(ctx, nil)
				if err != nil {
					return err
				}
			}

			// Ensure SOAP is connected
			if err := mgr.Start(ctx, name); err != nil {
				return err
			}

			soap := mgr.SOAPClient(name)

			switch toolType {
			case "computer":
				ct := newComputerToolForRun(name, soap)
				return runComputerAction(ctx, ct, action, coordinate, text)
			case "bash":
				out, err := vbox.GuestRun(ctx, name, config.VMUser, config.VMPass,
					"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
					"-NoProfile", "-NonInteractive", "-Command", command)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			case "text_editor":
				return runEditorAction(ctx, name, command, path, fileText, oldStr, newStr)
			default:
				return fmt.Errorf("unknown tool type %q (use: computer, bash, text_editor)", toolType)
			}
		},
	}

	cmd.Flags().StringVar(&toolType, "type", "", "Tool type: computer, bash, or text_editor (required)")
	cmd.Flags().StringVar(&action, "action", "", "Computer action: screenshot, left_click, right_click, middle_click, double_click, mouse_move, scroll, type, key")
	cmd.Flags().StringVar(&coordinate, "coordinate", "", "Mouse position as x,y (required for click/move/scroll actions)")
	cmd.Flags().StringVar(&text, "text", "", "Text to type or key to press (required for type/key actions)")
	cmd.Flags().StringVar(&command, "command", "", "PowerShell command (bash) or editor operation: view, create")
	cmd.Flags().StringVar(&path, "path", "", "Guest file path (required for text_editor actions)")
	cmd.Flags().StringVar(&fileText, "file-text", "", "File content to write (required for text_editor create)")
	cmd.Flags().StringVar(&oldStr, "old-str", "", "String to replace (text_editor str_replace)")
	cmd.Flags().StringVar(&newStr, "new-str", "", "Replacement string (text_editor str_replace)")
	cmd.Flags().StringVar(&vmName, "vm", "", "VM name (auto-detected when only one VM exists)")
	_ = cmd.MarkFlagRequired("type")

	return cmd
}

func newComputerToolForRun(vmName string, soap *vbox.SOAPClient) *computerToolRunner {
	return &computerToolRunner{vmName: vmName, soap: soap}
}

type computerToolRunner struct {
	vmName string
	soap   *vbox.SOAPClient
}

func runComputerAction(ctx context.Context, ct *computerToolRunner, action, coordinate, text string) error {
	switch action {
	case "screenshot":
		data, err := vbox.Screenshot(ctx, ct.vmName)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	case "left_click", "right_click", "middle_click", "double_click":
		x, y, err := parseCoordinate(coordinate)
		if err != nil {
			return err
		}
		buttonMask := 1
		switch action {
		case "right_click":
			buttonMask = 2
		case "middle_click":
			buttonMask = 4
		}
		if action == "double_click" {
			return ct.soap.MouseDoubleClick(x, y, 1)
		}
		return ct.soap.MouseClick(x, y, buttonMask)
	case "type":
		codes := vbox.TextToScancodes(text)
		return vbox.SendScancodes(ctx, ct.vmName, codes)
	case "key":
		codes := vbox.KeyToScancodes(text)
		return vbox.SendScancodes(ctx, ct.vmName, codes)
	case "mouse_move":
		x, y, err := parseCoordinate(coordinate)
		if err != nil {
			return err
		}
		return ct.soap.MouseMoveAbsolute(x, y)
	case "scroll":
		x, y, err := parseCoordinate(coordinate)
		if err != nil {
			return err
		}
		return ct.soap.MouseScroll(x, y, -3)
	default:
		return fmt.Errorf("unknown computer action %q", action)
	}
}

func runEditorAction(ctx context.Context, vmName, command, path, fileText, oldStr, newStr string) error {
	switch command {
	case "view":
		out, err := vbox.GuestRun(ctx, vmName, config.VMUser, config.VMPass,
			"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
			"-NoProfile", "-NonInteractive", "-Command",
			fmt.Sprintf("Get-Content -Raw '%s'", path))
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	case "create":
		tmpPath := fmt.Sprintf("%s/dexbox-tmp-%d.txt", config.SharedDir, os.Getpid())
		if err := os.MkdirAll(config.SharedDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(tmpPath, []byte(fileText), 0o644); err != nil {
			return err
		}
		defer os.Remove(tmpPath)

		guestShared := fmt.Sprintf("\\\\vboxsvr\\shared\\dexbox-tmp-%d.txt", os.Getpid())
		_, err := vbox.GuestRun(ctx, vmName, config.VMUser, config.VMPass,
			"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
			"-NoProfile", "-NonInteractive", "-Command",
			fmt.Sprintf("Copy-Item '%s' '%s' -Force", guestShared, path))
		if err != nil {
			return err
		}
		fmt.Printf("Created %s\n", path)
		return nil
	default:
		return fmt.Errorf("unknown editor command %q", command)
	}
}

func parseCoordinate(s string) (int, int, error) {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("coordinate must be in format x,y (got %q)", s)
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid x coordinate: %w", err)
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid y coordinate: %w", err)
	}
	return x, y, nil
}
