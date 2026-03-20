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
	sessions     map[string]*SOAPClient // vmName → active SOAP client
}

// NewManager creates a VM manager.
func NewManager(soapEndpoint string) *Manager {
	return &Manager{
		soapEndpoint: soapEndpoint,
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

// Stop performs a graceful ACPI shutdown and disconnects SOAP.
func (m *Manager) Stop(ctx context.Context, vmName string) error {
	if err := m.ensureVM(ctx, vmName); err != nil {
		return err
	}
	if soap, ok := m.sessions[vmName]; ok {
		_ = soap.Disconnect()
		delete(m.sessions, vmName)
	}
	return ControlVM(ctx, vmName, "acpipowerbutton")
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
func (m *Manager) ConnectSOAP(ctx context.Context, vmName string) error {
	if _, ok := m.sessions[vmName]; ok {
		return nil // Already connected
	}
	soap := NewSOAPClient(m.soapEndpoint)
	if err := soap.Connect(vmName, "", ""); err != nil {
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
