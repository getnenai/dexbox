package vbox

import (
	"context"
	"fmt"
	"time"
)

// VMStatus describes the current state of a VM.
type VMStatus struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	GuestAdditions bool   `json:"guest_additions"`
}

// Manager orchestrates VM lifecycle and SOAP connections.
type Manager struct {
	soapEndpoint string
	soapUser     string
	soapPass     string
	sessions     map[string]*SOAPClient // vmName → active SOAP client
}

// NewManager creates a VM manager.
func NewManager(soapEndpoint, user, pass string) *Manager {
	return &Manager{
		soapEndpoint: soapEndpoint,
		soapUser:     user,
		soapPass:     pass,
		sessions:     make(map[string]*SOAPClient),
	}
}

// SOAPClient returns the active SOAP client for a VM, or nil.
func (m *Manager) SOAPClient(vmName string) *SOAPClient {
	return m.sessions[vmName]
}

// ensureVM returns an error if the named VM is not registered with VirtualBox.
func (m *Manager) ensureVM(ctx context.Context, vmName string) error {
	if !VMExists(ctx, vmName) {
		return fmt.Errorf("VM %q does not exist", vmName)
	}
	return nil
}

// Start boots a VM headless and connects SOAP.
func (m *Manager) Start(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}
	state, err := VMState(ctx, vmName)
	if err != nil {
		return err
	}

	switch state {
	case "running":
		// Already running, just ensure SOAP is connected.
		return m.ConnectSOAP(ctx, vmName)
	case "paused":
		if err := ControlVM(ctx, vmName, "resume"); err != nil {
			return err
		}
	case "saved":
		if err := StartVM(ctx, vmName, true); err != nil {
			return err
		}
	default:
		if err := StartVM(ctx, vmName, true); err != nil {
			return err
		}
	}

	// Wait for Guest Additions
	if err := m.waitForGuestAdditions(ctx, vmName); err != nil {
		return err
	}

	return m.ConnectSOAP(ctx, vmName)
}

// Stop performs a graceful shutdown and disconnects SOAP.
//
// It runs PowerShell Stop-Computer inside the guest via Guest Additions for a
// reliable OS-initiated shutdown, then polls until the VM reaches poweroff
// state. Guest Additions are required; if they are not available, Stop returns
// an error (use Poweroff / --force for a hard power-off).
//
// Stop is idempotent: if the VM is already powered off or aborted it returns
// nil immediately.
func (m *Manager) Stop(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}

	// If the VM is already off, there is nothing to do.
	state, err := VMState(ctx, vmName)
	if err != nil {
		return err
	}
	if state == "poweroff" || state == "aborted" {
		return nil
	}

	if soap, ok := m.sessions[vmName]; ok {
		_ = soap.Disconnect()
		delete(m.sessions, vmName)
	}

	if !GuestAdditionsReady(ctx, vmName) {
		return fmt.Errorf("VM %q does not have Guest Additions running; cannot shut down gracefully (use --force to poweroff)", vmName)
	}

	// Use PowerShell Stop-Computer (matches the bash tool's invocation
	// pattern which is proven to work via guestcontrol). GuestRun will
	// return an error because the shutdown kills the guestcontrol
	// session — we ignore it and poll for poweroff state instead.
	GuestRun(ctx, vmName, m.soapUser, m.soapPass,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		"-NoProfile", "-NonInteractive", "-Command", "Stop-Computer -Force")

	return m.waitForPoweroff(ctx, vmName, 60*time.Second)
}

// waitForPoweroff polls until the VM reaches the "poweroff" state or the
// timeout expires.
func (m *Manager) waitForPoweroff(ctx context.Context, vmName string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout waiting for VM %q to power off", vmName)
		case <-ticker.C:
			state, err := VMState(ctx, vmName)
			if err != nil {
				continue
			}
			if state == "poweroff" || state == "aborted" {
				return nil
			}
		}
	}
}

// Poweroff immediately cuts power to the VM (data loss risk).
func (m *Manager) Poweroff(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}
	if soap, ok := m.sessions[vmName]; ok {
		_ = soap.Disconnect()
		delete(m.sessions, vmName)
	}
	return ControlVM(ctx, vmName, "poweroff")
}

// Pause freezes the VM in memory (instant).
func (m *Manager) Pause(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}
	return ControlVM(ctx, vmName, "pause")
}

// Suspend saves VM state to disk and powers off.
func (m *Manager) Suspend(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}
	if soap, ok := m.sessions[vmName]; ok {
		_ = soap.Disconnect()
		delete(m.sessions, vmName)
	}
	return ControlVM(ctx, vmName, "savestate")
}

// Resume resumes a VM from paused or saved state.
func (m *Manager) Resume(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}
	state, err := VMState(ctx, vmName)
	if err != nil {
		return err
	}

	switch state {
	case "paused":
		if err := ControlVM(ctx, vmName, "resume"); err != nil {
			return err
		}
	case "saved":
		if err := StartVM(ctx, vmName, true); err != nil {
			return err
		}
	default:
		return fmt.Errorf("VM %q is in state %q, cannot resume", vmName, state)
	}

	return m.ConnectSOAP(ctx, vmName)
}

// Status returns the current state of a VM.
func (m *Manager) Status(ctx context.Context, vmName string) (VMStatus, error) {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return VMStatus{}, err
	}
	state, err := VMState(ctx, vmName)
	if err != nil {
		return VMStatus{}, err
	}
	ga := GuestAdditionsReady(ctx, vmName)
	return VMStatus{
		Name:           vmName,
		State:          state,
		GuestAdditions: ga,
	}, nil
}

// Create creates a new VM with the given configuration.
func (m *Manager) Create(ctx context.Context, name string, cfg VMConfig) error {
	return CreateVM(ctx, name, cfg)
}

// Delete unregisters and removes a VM.
func (m *Manager) Delete(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}
	if soap, ok := m.sessions[vmName]; ok {
		_ = soap.Disconnect()
		delete(m.sessions, vmName)
	}
	return DeleteVM(ctx, vmName)
}

// List returns all dexbox-managed VMs and their states.
func (m *Manager) List(ctx context.Context) ([]VMStatus, error) {
	names, err := ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	var result []VMStatus
	for _, name := range names {
		status, err := m.Status(ctx, name)
		if err != nil {
			continue
		}
		result = append(result, status)
	}
	return result, nil
}

// ConnectSOAP establishes a SOAP session for a running VM.
// If a session already exists, it is reused. Use ReconnectSOAP to force a
// fresh session (e.g. after a VM reboot invalidates object references).
func (m *Manager) ConnectSOAP(ctx context.Context, vmName string) error {
	if _, ok := m.sessions[vmName]; ok {
		return nil // Already connected
	}
	return m.reconnectSOAP(vmName)
}

// ReconnectSOAP tears down any existing SOAP session and establishes a fresh one.
// Call this when a VM has rebooted and SOAP object references are stale.
func (m *Manager) ReconnectSOAP(ctx context.Context, vmName string) error {
	return m.reconnectSOAP(vmName)
}

func (m *Manager) reconnectSOAP(vmName string) error {
	if old, ok := m.sessions[vmName]; ok {
		_ = old.Disconnect()
		delete(m.sessions, vmName)
	}
	soap := NewSOAPClient(m.soapEndpoint)
	if err := soap.Connect(vmName, m.soapUser, m.soapPass); err != nil {
		return err
	}
	m.sessions[vmName] = soap
	return nil
}

// waitForGuestAdditions polls until Guest Additions are active or context expires.
func (m *Manager) waitForGuestAdditions(ctx context.Context, vmName string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for Guest Additions on VM %q", vmName)
		case <-ticker.C:
			if GuestAdditionsReady(ctx, vmName) {
				return nil
			}
		}
	}
}