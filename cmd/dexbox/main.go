// Package main — dexbox CLI
//
// Commands:
//
//	dexbox start        — Start dexbox server + vboxwebsrv + guacd
//	dexbox stop         — Stop dexbox server + vboxwebsrv + guacd
//	dexbox status       — Server health + list of VMs
//	dexbox create vm    — Install VirtualBox + create and provision a VM
//	dexbox vm ...       — VM lifecycle commands
//	dexbox rdp ...      — Manage RDP connections
//	dexbox run ...      — Execute tool actions directly
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/getnenai/dexbox/internal/config"
	"github.com/getnenai/dexbox/internal/desktop"
	"github.com/getnenai/dexbox/internal/guacd"
	"github.com/getnenai/dexbox/internal/mcpserver"
	"github.com/getnenai/dexbox/internal/pidfile"
	"github.com/getnenai/dexbox/internal/server"
	"github.com/getnenai/dexbox/internal/vbox"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set by goreleaser via ldflags at build time.
var version = "dev"

func main() {
	var envFile string

	root := &cobra.Command{
		Use:     "dexbox",
		Short:   "dexbox — VirtualBox-based computer-use tool server",
		Version: version,
		Long: `dexbox is a VirtualBox-based computer-use tool server.

It manages Windows VMs and exposes an HTTP API for supported computer-use actions
(screenshots, mouse, keyboard, and guest shell commands) used by AI workflows.

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

	root.AddCommand(
		cmdStart(), cmdStop(), cmdStatus(),
		cmdUp(), cmdDown(), cmdList(), cmdView(),
		cmdCreate(), cmdVM(), cmdRDP(), cmdRunAction(),
		cmdMCP(),
	)

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
		Short: "Start dexbox server, vboxwebsrv, and guacd",
		RunE: func(cmd *cobra.Command, args []string) error {
			port := parseSoapPort()
			ctx := context.Background()

			// Record our PID so `dexbox stop` can target this process directly.
			// PID-file creation is a hard prerequisite: if it fails, another
			// instance may already be running or the runtime environment is
			// misconfigured, so we refuse to start.
			pidPath, err := dexboxPIDPath()
			if err != nil {
				return fmt.Errorf("could not resolve PID file path: %w", err)
			}
			pf := pidfile.New(pidPath, "dexbox")
			if _, err := pf.Write(); err != nil {
				return fmt.Errorf("could not write PID file: %w", err)
			}
			defer func() {
				if err := pf.Remove(); err != nil {
					fmt.Printf("Warning: could not remove PID file: %v\n", err)
				}
			}()

			// vboxwebsrv is optional — skip gracefully when VirtualBox is not
			// installed (e.g. QEMU-only environments).
			var vboxwebsrv *exec.Cmd
			vboxDied := make(chan error, 1)
			stopVBox := func() {}

			if _, err := exec.LookPath("vboxwebsrv"); err == nil {
				fmt.Println("Starting vboxwebsrv...")
				vboxwebsrv = exec.Command("vboxwebsrv", "--host", "localhost", "--port", port)
				vboxwebsrv.Stdout = os.Stdout
				vboxwebsrv.Stderr = os.Stderr
				if err := vboxwebsrv.Start(); err != nil {
					return fmt.Errorf("failed to start vboxwebsrv: %w", err)
				}
				if err := waitForPort(port, 10*time.Second); err != nil {
					_ = vboxwebsrv.Process.Kill()
					return fmt.Errorf("vboxwebsrv failed to start: %w", err)
				}
				go func() { vboxDied <- vboxwebsrv.Wait() }()
				stopVBox = func() {
					_ = vboxwebsrv.Process.Signal(syscall.SIGTERM)
					select {
					case <-vboxDied:
					case <-time.After(5 * time.Second):
						_ = vboxwebsrv.Process.Kill()
					}
				}
			} else {
				fmt.Println("vboxwebsrv not found — skipping (VirtualBox VMs disabled, RDP still available)")
				// Keep vboxDied permanently open so the select below never fires on it.
				// We never send on it, so it blocks forever.
			}

			// Start guacd for RDP support (native binary > Docker > skip)
			fmt.Println("Starting guacd...")
			if err := guacd.EnsureRunning(ctx, config.SharedDir); err != nil {
				fmt.Printf("Warning: failed to start guacd: %v\n", err)
				fmt.Println("RDP connections will not be available.")
			} else {
				fmt.Println("guacd running on port 4822")
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

			sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			errCh := make(chan error, 1)
			go func() { errCh <- srv.Run() }()

			stopGuacd := func() {
				guacd.StopNative()
				_ = guacd.Stop(context.Background())
			}

			select {
			case err := <-errCh:
				stopVBox()
				stopGuacd()
				return err
			case err := <-vboxDied:
				stopGuacd()
				if err != nil {
					return fmt.Errorf("vboxwebsrv exited unexpectedly: %w", err)
				}
				return fmt.Errorf("vboxwebsrv exited unexpectedly")
			case <-sigCtx.Done():
				fmt.Println("\nShutting down...")
				stopVBox()
				stopGuacd()
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
		Short: "Stop dexbox server, vboxwebsrv, and guacd",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			fmt.Println("Stopping dexbox server...")
			stopped := false
			if pidPath, err := dexboxPIDPath(); err == nil {
				stopped = pidfile.New(pidPath, "dexbox").Stop(5 * time.Second)
			}
			if !stopped {
				// Fall back when PID file is missing or stale.
				_ = exec.Command("pkill", "-f", "dexbox start").Run()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if exec.Command("pgrep", "-f", "dexbox start").Run() != nil {
						break
					}
					time.Sleep(500 * time.Millisecond)
				}
				_ = exec.Command("pkill", "-9", "-f", "dexbox start").Run()
			}

			fmt.Println("Stopping vboxwebsrv...")
			_ = exec.Command("pkill", "-f", "vboxwebsrv").Run()
			vboxDeadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(vboxDeadline) {
				if exec.Command("pgrep", "-f", "vboxwebsrv").Run() != nil {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			_ = exec.Command("pkill", "-9", "-f", "vboxwebsrv").Run()

			fmt.Println("Stopping guacd...")
			guacd.StopNative()
			_ = guacd.Stop(ctx)

			fmt.Println("Stopped.")
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
// PID file helpers
// ---------------------------------------------------------------------------

// dexboxPIDPath returns the path of the PID file, inside a user-scoped
// directory (~/.dexbox/dexbox.pid) that is not world-writable, eliminating
// the symlink-attack surface of a shared /tmp location.
func dexboxPIDPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, ".dexbox", "dexbox.pid"), nil
}

// ---------------------------------------------------------------------------
// dexbox status
// ---------------------------------------------------------------------------

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server health, VM states, and RDP connections",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// guacd status
			guacdRunning, _ := guacd.IsRunning(ctx)
			if guacdRunning {
				fmt.Println("guacd: running (port 4822)")
			} else if guacd.DockerAvailable(ctx) {
				fmt.Println("guacd: not running")
			} else {
				fmt.Println("guacd: Docker not available")
			}
			fmt.Println()

			// List VMs
			vms, err := vbox.ListVMs(ctx)
			if err != nil {
				fmt.Printf("VirtualBox: not available (%v)\n", err)
			} else if len(vms) == 0 {
				fmt.Println("VMs: none")
			} else {
				fmt.Printf("%-20s %-12s %s\n", "VM", "STATE", "GUEST ADDITIONS")
				for _, name := range vms {
					state, _ := vbox.VMState(ctx, name)
					ga := vbox.GuestAdditionsReady(ctx, name)
					gaStr := "no"
					if ga {
						gaStr = "yes"
					}
					fmt.Printf("%-20s %-12s %s\n", name, state, gaStr)
				}
			}
			fmt.Println()

			// RDP connections
			store := desktop.NewConnectionStore(desktop.DefaultStorePath())
			if err := store.Load(); err != nil {
				return fmt.Errorf("load connection store: %w", err)
			}
			conns := store.List()
			if len(conns) == 0 {
				fmt.Println("RDP: none")
			} else {
				fmt.Printf("%-20s %-30s %s\n", "RDP CONNECTION", "HOST", "USER")
				for name, cfg := range conns {
					fmt.Printf("%-20s %-30s %s\n", name, fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), cfg.Username)
				}
			}

			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// dexbox up / down / list
// ---------------------------------------------------------------------------

func newDesktopManager() (*desktop.Manager, error) {
	store := desktop.NewConnectionStore(desktop.DefaultStorePath())
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("load connection store: %w", err)
	}
	return desktop.NewManager(
		vbox.NewManager(config.SOAPAddr, config.VMUser, config.VMPass),
		store,
		guacd.DefaultAddr,
		config.SharedDir,
	), nil
}

func cmdUp() *cobra.Command {
	return &cobra.Command{
		Use:   "up <name>",
		Short: "Connect to a desktop (boot VM or verify RDP target)",
		Long: `Bring a desktop online.

For VMs: boots the VM if not running (durable — VM keeps running after exit).
For RDP: verifies guacd is running and the RDP target is reachable. RDP
sessions are per-command; each 'dexbox run' reconnects automatically.

Examples:
  dexbox up my-vm
  dexbox up my-rdp-server`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newDesktopManager()
			if err != nil {
				return err
			}
			if err := mgr.Up(context.Background(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Desktop %q is up\n", args[0])
			return nil
		},
	}
}

func cmdDown() *cobra.Command {
	var (
		force bool
		all   bool
	)

	c := &cobra.Command{
		Use:   "down [name]",
		Short: "Shut down a desktop or all desktops",
		Long: `Shut down a desktop (disconnect + graceful OS shutdown).

For a single desktop, disconnects the session and shuts down the guest.
With --force, performs a hard power off instead.
With --all, shuts down all VMs, disconnects all sessions, and stops guacd.

Examples:
  dexbox down my-vm              # graceful shutdown
  dexbox down my-vm --force      # hard power off
  dexbox down --all              # shut everything down
  dexbox down --all --force      # hard poweroff all VMs`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify both a name and --all")
			}
			if !all && len(args) == 0 {
				return fmt.Errorf("specify a desktop name or use --all")
			}

			mgr, err := newDesktopManager()
			if err != nil {
				return err
			}
			ctx := context.Background()

			if all {
				if err := mgr.DownAll(ctx, force); err != nil {
					return err
				}
				fmt.Println("All desktops shut down")
				return nil
			}

			name := args[0]
			if err := mgr.Down(ctx, name, true, force); err != nil {
				return err
			}
			if force {
				fmt.Printf("Desktop %q powered off\n", name)
			} else {
				fmt.Printf("Desktop %q shut down\n", name)
			}
			return nil
		},
	}

	c.Flags().BoolVar(&force, "force", false, "Hard poweroff instead of graceful ACPI shutdown")
	c.Flags().BoolVar(&all, "all", false, "Shut down all desktops and stop guacd")

	return c
}

func cmdList() *cobra.Command {
	var typeFilter string

	c := &cobra.Command{
		Use:   "list",
		Short: "List all desktops (VMs and RDP connections)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Try the running server first for accurate connected state.
			if desktops, err := listFromServer(typeFilter); err == nil {
				printDesktopList(desktops)
				return nil
			}

			// Server unreachable — fall back to local VBoxManage + connection store.
			mgr, err := newDesktopManager()
			if err != nil {
				return err
			}
			desktops, err := mgr.List(context.Background(), typeFilter)
			if err != nil {
				return err
			}

			printDesktopList(desktops)
			return nil
		},
	}

	c.Flags().StringVar(&typeFilter, "type", "", "Filter by type: vm or rdp")
	return c
}

// listFromServer queries the running dexbox server's /desktops endpoint.
func listFromServer(typeFilter string) ([]desktop.DesktopStatus, error) {
	addr := config.Listen
	if addr == "" || addr[0] == ':' {
		addr = "localhost" + addr
	}
	url := fmt.Sprintf("http://%s/desktops", addr)
	if typeFilter != "" {
		url += "?type=" + typeFilter
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var body struct {
		Desktops []desktop.DesktopStatus `json:"desktops"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Desktops, nil
}

func printDesktopList(desktops []desktop.DesktopStatus) {
	if len(desktops) == 0 {
		fmt.Println("No desktops found.")
		return
	}
	fmt.Printf("%-20s %-6s %-14s %s\n", "NAME", "TYPE", "STATE", "CONNECTED")
	for _, d := range desktops {
		connStr := "no"
		if d.Connected {
			connStr = "yes"
		}
		fmt.Printf("%-20s %-6s %-14s %s\n", d.Name, d.Type, d.State, connStr)
	}
}

// ---------------------------------------------------------------------------
// dexbox view
// ---------------------------------------------------------------------------

func cmdView() *cobra.Command {
	return &cobra.Command{
		Use:   "view <name>",
		Short: "Open a desktop in the browser",
		Long: `Open the browser-based remote desktop viewer for the named desktop.

This requires the dexbox server to be running (dexbox start) and guacd
to be available for RDP connections.

Example:
  dexbox view my-rdp-server`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			url := fmt.Sprintf("http://localhost%s/desktops/%s/view", config.Listen, name)
			fmt.Printf("Opening %s\n", url)

			// Open browser
			var openCmd *exec.Cmd
			switch {
			case fileExists("/usr/bin/open"): // macOS
				openCmd = exec.Command("open", url)
			case fileExists("/usr/bin/xdg-open"): // Linux
				openCmd = exec.Command("xdg-open", url)
			default:
				fmt.Printf("Open this URL in your browser: %s\n", url)
				return nil
			}
			return openCmd.Start()
		},
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
A Windows ISO must be provided via --iso.

Example:
  dexbox create vm desktop-1 --iso ~/Downloads/Win11_25H2_English_x64.iso`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if isoPath == "" {
				return fmt.Errorf("Windows ISO required: use --iso <path>")
			}
			expandedISO := isoPath
			if len(isoPath) > 1 && isoPath[0] == '~' {
				home, _ := os.UserHomeDir()
				expandedISO = home + isoPath[1:]
			}
			return vbox.Install(context.Background(), name, expandedISO)
		},
	}
	c.Flags().StringVar(&isoPath, "iso", "", "Path to Windows ISO (required)")
	_ = c.MarkFlagRequired("iso")
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
		vmAction("stop", "Graceful shutdown", func(ctx context.Context, mgr *vbox.Manager, name string) error {
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
// dexbox rdp
// ---------------------------------------------------------------------------

func cmdRDP() *cobra.Command {
	rdp := &cobra.Command{
		Use:   "rdp",
		Short: "Manage RDP connections",
		Long: `Manage RDP desktop connections.

RDP connections are stored in ~/.dexbox/connections.json and use
Apache Guacamole (guacd) as the connection proxy.`,
	}

	rdp.AddCommand(cmdRDPAdd(), cmdRDPRemove(), cmdRDPList(), cmdRDPConnect(), cmdRDPDisconnect())
	return rdp
}

func cmdRDPAdd() *cobra.Command {
	var (
		host       string
		port       int
		user       string
		pass       string
		width      int
		height     int
		ignoreCert bool
		security   string
		driveName  string
	)

	c := &cobra.Command{
		Use:   "add <name>",
		Short: "Register an RDP connection",
		Long: `Register a new RDP connection target.

Example:
  dexbox rdp add my-server --host 192.168.1.100 --user Administrator --pass secret`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Check for name collision with VMs
			ctx := context.Background()
			if vbox.VMExists(ctx, name) {
				return fmt.Errorf("name %q is already used by a VM; choose a different name", name)
			}

			store := desktop.NewConnectionStore(desktop.DefaultStorePath())
			if err := store.Load(); err != nil {
				return err
			}

			cfg := desktop.RDPConfig{
				Host:       host,
				Port:       port,
				Username:   user,
				Password:   pass,
				Width:      width,
				Height:     height,
				IgnoreCert: ignoreCert,
				Security:   security,
			}
			if driveName != "" {
				cfg.DriveEnabled = true
				cfg.DriveName = driveName
			}

			if err := store.Add(name, cfg); err != nil {
				return err
			}
			if err := store.Save(); err != nil {
				return err
			}

			fmt.Printf("Added RDP connection %q → %s:%d\n", name, host, cfg.Port)
			return nil
		},
	}

	c.Flags().StringVar(&host, "host", "", "RDP host address (required)")
	c.Flags().IntVar(&port, "port", 3389, "RDP port")
	c.Flags().StringVar(&user, "user", "", "Username (required)")
	c.Flags().StringVar(&pass, "pass", "", "Password (required)")
	c.Flags().IntVar(&width, "width", 1024, "Display width in pixels")
	c.Flags().IntVar(&height, "height", 768, "Display height in pixels")
	c.Flags().BoolVar(&ignoreCert, "ignore-cert", true, "Ignore certificate validation")
	c.Flags().StringVar(&security, "security", "", "RDP security mode: any (default), rdp, nla, tls (use rdp for VirtualBox VRDE)")
	c.Flags().StringVar(&driveName, "drive-name", "", "Enable drive redirection with this Windows share name (e.g. MyDrive)")
	_ = c.MarkFlagRequired("host")
	_ = c.MarkFlagRequired("user")
	_ = c.MarkFlagRequired("pass")

	return c
}

func cmdRDPRemove() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister an RDP connection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			store := desktop.NewConnectionStore(desktop.DefaultStorePath())
			if err := store.Load(); err != nil {
				return err
			}
			if err := store.Remove(name); err != nil {
				return err
			}
			if err := store.Save(); err != nil {
				return err
			}

			fmt.Printf("Removed RDP connection %q\n", name)
			return nil
		},
	}
}

func cmdRDPList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List RDP connections",
		RunE: func(cmd *cobra.Command, args []string) error {
			store := desktop.NewConnectionStore(desktop.DefaultStorePath())
			if err := store.Load(); err != nil {
				return err
			}

			conns := store.List()
			if len(conns) == 0 {
				fmt.Println("No RDP connections. Run 'dexbox rdp add <name> --host <host> --user <user> --pass <pass>' to add one.")
				return nil
			}

			fmt.Printf("%-20s %-30s %-15s %s\n", "NAME", "HOST", "USER", "RESOLUTION")
			for name, cfg := range conns {
				fmt.Printf("%-20s %-30s %-15s %dx%d\n",
					name,
					fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
					cfg.Username,
					cfg.Width, cfg.Height,
				)
			}
			return nil
		},
	}
}

func cmdRDPConnect() *cobra.Command {
	return &cobra.Command{
		Use:   "connect <name>",
		Short: "Verify an RDP target is reachable (alias for 'dexbox up')",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newDesktopManager()
			if err != nil {
				return err
			}
			if err := mgr.Up(context.Background(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Connected to %q\n", args[0])
			return nil
		},
	}
}

func cmdRDPDisconnect() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <name>",
		Short: "Disconnect from an RDP desktop (alias for 'dexbox down')",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newDesktopManager()
			if err != nil {
				return err
			}
			if err := mgr.Down(context.Background(), args[0], false, false); err != nil {
				return err
			}
			fmt.Printf("Disconnected from %q\n", args[0])
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// dexbox run
// ---------------------------------------------------------------------------

func cmdRunAction() *cobra.Command {
	var (
		toolType    string
		action      string
		coordinate  string
		text        string
		command     string
		vmName      string
		desktopName string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute a tool action directly",
		Long: `Execute a low-level tool action against a desktop (VM or RDP connection).

Tool types and their actions:

  computer
    screenshot               Capture the screen (outputs raw PNG to stdout)
    left_click   --coordinate x,y
    right_click  --coordinate x,y
    middle_click --coordinate x,y
    double_click --coordinate x,y
    mouse_move   --coordinate x,y  Move cursor without clicking
    scroll       --coordinate x,y  Scroll down at position
    type         --text <string>    Type a string of text
    key          --text <key>       Press a key (e.g. Return, ctrl+c)

  bash (VM only)
    --command <ps1>   Run a PowerShell command on the guest and print output

Examples:
  dexbox run --type computer --action screenshot > screen.png
  dexbox run --type computer --action left_click --coordinate 100,200
  dexbox run --type computer --action type --text "hello world"
  dexbox run --type bash --command "Get-Process"
  dexbox run --desktop my-rdp --type computer --action screenshot > screen.png`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// Determine desktop name from --desktop or --vm (backward compat)
			name := desktopName
			if name == "" {
				name = vmName
			}

			mgr, err := newDesktopManager()
			if err != nil {
				return err
			}

			// If we have a name, try to bring it up
			if name != "" {
				if err := mgr.Up(ctx, name); err != nil {
					return err
				}
			} else {
				// Auto-resolve: try to find a single VM
				var err error
				name, err = resolveVMName(ctx, nil)
				if err != nil {
					return err
				}
				if err := mgr.Up(ctx, name); err != nil {
					return err
				}
			}

			d, err := mgr.Resolve(name)
			if err != nil {
				return err
			}

			switch toolType {
			case "computer":
				return runComputerAction(ctx, d, action, coordinate, text)
			case "bash":
				if d.Type() == "rdp" {
					return fmt.Errorf("bash tool is not supported for RDP connections (only VMs with Guest Additions)")
				}
				out, err := vbox.GuestRun(ctx, name, config.VMUser, config.VMPass,
					"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
					"-NoProfile", "-NonInteractive", "-Command", command)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			default:
				return fmt.Errorf("unknown tool type %q (use: computer, bash)", toolType)
			}
		},
	}

	cmd.Flags().StringVar(&toolType, "type", "", "Tool type: computer or bash (required)")
	cmd.Flags().StringVar(&action, "action", "", "Computer action: screenshot, left_click, right_click, middle_click, double_click, mouse_move, scroll, type, key")
	cmd.Flags().StringVar(&coordinate, "coordinate", "", "Mouse position as x,y (required for click/move/scroll actions)")
	cmd.Flags().StringVar(&text, "text", "", "Text to type or key to press (required for type/key actions)")
	cmd.Flags().StringVar(&command, "command", "", "PowerShell command (bash)")
	cmd.Flags().StringVar(&desktopName, "desktop", "", "Desktop name (VM or RDP connection)")
	cmd.Flags().StringVar(&vmName, "vm", "", "VM name (deprecated, use --desktop)")
	_ = cmd.MarkFlagRequired("type")

	return cmd
}

func runComputerAction(ctx context.Context, d desktop.Desktop, action, coordinate, text string) error {
	switch action {
	case "screenshot":
		data, err := d.Screenshot(ctx)
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
			return d.MouseDoubleClick(x, y, 1)
		}
		return d.MouseClick(x, y, buttonMask)
	case "type":
		return d.TypeText(ctx, text)
	case "key":
		return d.KeyPress(ctx, text)
	case "mouse_move":
		x, y, err := parseCoordinate(coordinate)
		if err != nil {
			return err
		}
		return d.MouseMoveAbsolute(x, y)
	case "scroll":
		x, y, err := parseCoordinate(coordinate)
		if err != nil {
			return err
		}
		return d.MouseScroll(x, y, -3)
	default:
		return fmt.Errorf("unknown computer action %q", action)
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

// ---------------------------------------------------------------------------
// dexbox mcp
// ---------------------------------------------------------------------------

func cmdMCP() *cobra.Command {
	var baseURL string

	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server (stdio transport)",
		Long: `Run a Model Context Protocol (MCP) server over stdin/stdout.

This exposes Dexbox desktop lifecycle tools (list, create, destroy, start,
stop, get) so that IDE AI assistants (Cursor, Claude Code, etc.) can manage
desktops directly.

Requires the dexbox server to be running (dexbox start).

IDE configuration example (Cursor / Claude Code):

  {
    "mcpServers": {
      "dexbox": {
        "command": "dexbox",
        "args": ["mcp"]
      }
    }
  }`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if baseURL == "" {
				addr := config.Listen
				if addr == "" || addr[0] == ':' {
					addr = "localhost" + addr
				}
				baseURL = "http://" + addr
			}
			srv := mcpserver.New(baseURL)
			return srv.Run(context.Background(), &mcp.StdioTransport{})
		},
	}

	c.Flags().StringVar(&baseURL, "base-url", "", "Dexbox server base URL (default: derived from DEXBOX_LISTEN)")
	return c
}
