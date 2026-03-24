package vbox

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SOAPClient provides mouse and keyboard control via the VirtualBox Web Service (SOAP).
type SOAPClient struct {
	endpoint    string
	httpClient  *http.Client
	sessionID   string // IWebsessionManager session
	machineRef  string // IMachine reference
	sessionRef  string // ISession reference
	consoleRef  string // IConsole reference
	mouseRef    string // IMouse reference
	keyboardRef string // IKeyboard reference

	// Stored for automatic reconnection when object references expire.
	vmName   string
	soapUser string
	soapPass string
}

// NewSOAPClient creates a SOAP client pointed at the vboxwebsrv endpoint.
func NewSOAPClient(endpoint string) *SOAPClient {
	return &SOAPClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Connect establishes a SOAP session, locates the VM, and acquires a mouse reference.
func (c *SOAPClient) Connect(vmName, user, pass string) error {
	// 1. Logon
	sessionID, err := c.logon(user, pass)
	if err != nil {
		return fmt.Errorf("SOAP logon: %w", err)
	}
	c.sessionID = sessionID

	// 2. Find machine
	machineRef, err := c.findMachine(sessionID, vmName)
	if err != nil {
		return fmt.Errorf("SOAP findMachine: %w", err)
	}
	c.machineRef = machineRef

	// 3. Lock machine (shared lock for console access)
	sessionRef, err := c.getSessionObject(sessionID)
	if err != nil {
		return fmt.Errorf("SOAP getSessionObject: %w", err)
	}
	c.sessionRef = sessionRef

	if err := c.lockMachine(machineRef, sessionRef, "Shared"); err != nil {
		return fmt.Errorf("SOAP lockMachine: %w", err)
	}

	// 4. Get console → mouse
	consoleRef, err := c.getConsole(sessionRef)
	if err != nil {
		return fmt.Errorf("SOAP getConsole: %w", err)
	}
	c.consoleRef = consoleRef

	mouseRef, err := c.getMouse(consoleRef)
	if err != nil {
		return fmt.Errorf("SOAP getMouse: %w", err)
	}
	c.mouseRef = mouseRef

	keyboardRef, err := c.getKeyboard(consoleRef)
	if err != nil {
		return fmt.Errorf("SOAP getKeyboard: %w", err)
	}
	c.keyboardRef = keyboardRef

	// Store credentials for automatic reconnection only after all refs
	// are successfully acquired, so a failed Connect doesn't leave stale
	// metadata from a partial handshake.
	c.vmName = vmName
	c.soapUser = user
	c.soapPass = pass

	return nil
}

// Disconnect releases the SOAP session.
func (c *SOAPClient) Disconnect() error {
	if c.sessionRef != "" {
		_ = c.unlockMachine(c.sessionRef)
	}
	if c.sessionID != "" {
		_ = c.logoff(c.sessionID)
	}
	c.sessionID = ""
	c.machineRef = ""
	c.sessionRef = ""
	c.consoleRef = ""
	c.mouseRef = ""
	c.keyboardRef = ""
	return nil
}

// MouseMoveAbsolute moves the mouse to absolute coordinates.
func (c *SOAPClient) MouseMoveAbsolute(x, y int) error {
	return c.withReconnect(func() error {
		return c.putMouseEventAbsolute(c.mouseRef, x, y, 0, 0, 0)
	})
}

// MouseClick moves to (x,y), presses button, waits, and releases.
func (c *SOAPClient) MouseClick(x, y, buttonMask int) error {
	return c.withReconnect(func() error {
		if err := c.putMouseEventAbsolute(c.mouseRef, x, y, 0, 0, buttonMask); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
		return c.putMouseEventAbsolute(c.mouseRef, x, y, 0, 0, 0)
	})
}

// MouseDoubleClick performs two rapid clicks.
func (c *SOAPClient) MouseDoubleClick(x, y, buttonMask int) error {
	if err := c.MouseClick(x, y, buttonMask); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return c.MouseClick(x, y, buttonMask)
}

// MouseScroll moves to (x,y) then sends vertical scroll.
func (c *SOAPClient) MouseScroll(x, y, dz int) error {
	return c.withReconnect(func() error {
		if err := c.putMouseEventAbsolute(c.mouseRef, x, y, 0, 0, 0); err != nil {
			return err
		}
		return c.putMouseEvent(c.mouseRef, 0, 0, dz, 0, 0)
	})
}

// MouseDown presses the button at (x,y) without releasing.
func (c *SOAPClient) MouseDown(x, y, buttonMask int) error {
	return c.withReconnect(func() error {
		return c.putMouseEventAbsolute(c.mouseRef, x, y, 0, 0, buttonMask)
	})
}

// MouseUp releases all buttons at (x,y).
func (c *SOAPClient) MouseUp(x, y int) error {
	return c.withReconnect(func() error {
		return c.putMouseEventAbsolute(c.mouseRef, x, y, 0, 0, 0)
	})
}

// --- SOAP call implementations ---

func (c *SOAPClient) logon(user, pass string) (string, error) {
	body := fmt.Sprintf(`<IWebsessionManager_logon xmlns="http://www.virtualbox.org/">
		<username>%s</username>
		<password>%s</password>
	</IWebsessionManager_logon>`, xmlEscape(user), xmlEscape(pass))

	resp, err := c.call("IWebsessionManager_logon", body)
	if err != nil {
		return "", err
	}
	return extractTag(resp, "returnval"), nil
}

func (c *SOAPClient) logoff(sessionID string) error {
	body := fmt.Sprintf(`<IWebsessionManager_logoff xmlns="http://www.virtualbox.org/">
		<refIVirtualBox>%s</refIVirtualBox>
	</IWebsessionManager_logoff>`, sessionID)
	_, err := c.call("IWebsessionManager_logoff", body)
	return err
}

func (c *SOAPClient) findMachine(sessionID, name string) (string, error) {
	body := fmt.Sprintf(`<IVirtualBox_findMachine xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
		<nameOrId>%s</nameOrId>
	</IVirtualBox_findMachine>`, sessionID, xmlEscape(name))

	resp, err := c.call("IVirtualBox_findMachine", body)
	if err != nil {
		return "", err
	}
	return extractTag(resp, "returnval"), nil
}

func (c *SOAPClient) getSessionObject(sessionID string) (string, error) {
	body := fmt.Sprintf(`<IWebsessionManager_getSessionObject xmlns="http://www.virtualbox.org/">
		<refIVirtualBox>%s</refIVirtualBox>
	</IWebsessionManager_getSessionObject>`, sessionID)

	resp, err := c.call("IWebsessionManager_getSessionObject", body)
	if err != nil {
		return "", err
	}
	return extractTag(resp, "returnval"), nil
}

func (c *SOAPClient) lockMachine(machineRef, sessionRef, lockType string) error {
	body := fmt.Sprintf(`<IMachine_lockMachine xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
		<session>%s</session>
		<lockType>%s</lockType>
	</IMachine_lockMachine>`, machineRef, sessionRef, lockType)
	_, err := c.call("IMachine_lockMachine", body)
	return err
}

func (c *SOAPClient) unlockMachine(sessionRef string) error {
	body := fmt.Sprintf(`<ISession_unlockMachine xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
	</ISession_unlockMachine>`, sessionRef)
	_, err := c.call("ISession_unlockMachine", body)
	return err
}

func (c *SOAPClient) getConsole(sessionRef string) (string, error) {
	body := fmt.Sprintf(`<ISession_getConsole xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
	</ISession_getConsole>`, sessionRef)

	resp, err := c.call("ISession_getConsole", body)
	if err != nil {
		return "", err
	}
	return extractTag(resp, "returnval"), nil
}

func (c *SOAPClient) getMouse(consoleRef string) (string, error) {
	body := fmt.Sprintf(`<IConsole_getMouse xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
	</IConsole_getMouse>`, consoleRef)

	resp, err := c.call("IConsole_getMouse", body)
	if err != nil {
		return "", err
	}
	return extractTag(resp, "returnval"), nil
}

func (c *SOAPClient) getKeyboard(consoleRef string) (string, error) {
	body := fmt.Sprintf(`<IConsole_getKeyboard xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
	</IConsole_getKeyboard>`, consoleRef)

	resp, err := c.call("IConsole_getKeyboard", body)
	if err != nil {
		return "", err
	}
	return extractTag(resp, "returnval"), nil
}

// KeyboardPutScancodes sends PS/2 scancodes (as hex strings) to the VM keyboard
// via the SOAP interface. This works on headless VMs unlike keyboardputscancode.
func (c *SOAPClient) KeyboardPutScancodes(hexCodes []string) error {
	if len(hexCodes) == 0 {
		return nil
	}
	return c.withReconnect(func() error {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf(`<IKeyboard_putScancodes xmlns="http://www.virtualbox.org/"><_this>%s</_this>`, c.keyboardRef))
		for _, h := range hexCodes {
			var v int64
			fmt.Sscanf(h, "%x", &v)
			sb.WriteString(fmt.Sprintf("<scancodes>%d</scancodes>", v))
		}
		sb.WriteString("</IKeyboard_putScancodes>")
		_, err := c.call("IKeyboard_putScancodes", sb.String())
		return err
	})
}

func (c *SOAPClient) putMouseEventAbsolute(mouseRef string, x, y, dz, dw, buttonState int) error {
	body := fmt.Sprintf(`<IMouse_putMouseEventAbsolute xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
		<x>%d</x>
		<y>%d</y>
		<dz>%d</dz>
		<dw>%d</dw>
		<buttonState>%d</buttonState>
	</IMouse_putMouseEventAbsolute>`, mouseRef, x, y, dz, dw, buttonState)
	_, err := c.call("IMouse_putMouseEventAbsolute", body)
	return err
}

func (c *SOAPClient) putMouseEvent(mouseRef string, dx, dy, dz, dw, buttonState int) error {
	body := fmt.Sprintf(`<IMouse_putMouseEvent xmlns="http://www.virtualbox.org/">
		<_this>%s</_this>
		<dx>%d</dx>
		<dy>%d</dy>
		<dz>%d</dz>
		<dw>%d</dw>
		<buttonState>%d</buttonState>
	</IMouse_putMouseEvent>`, mouseRef, dx, dy, dz, dw, buttonState)
	_, err := c.call("IMouse_putMouseEvent", body)
	return err
}

// reconnect tears down the current session and re-establishes it.
func (c *SOAPClient) reconnect() error {
	_ = c.Disconnect()
	return c.Connect(c.vmName, c.soapUser, c.soapPass)
}

// isStaleRefError returns true if the error indicates an expired SOAP object reference.
func isStaleRefError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Invalid managed object reference")
}

// withReconnect runs fn, and if it fails with a stale reference error,
// reconnects the SOAP session and retries once.
func (c *SOAPClient) withReconnect(fn func() error) error {
	err := fn()
	if !isStaleRefError(err) || c.vmName == "" {
		return err
	}
	if reconnErr := c.reconnect(); reconnErr != nil {
		return fmt.Errorf("reconnect after stale ref failed: %w (original: %v)", reconnErr, err)
	}
	return fn()
}

// call sends a SOAP request and returns the raw response body.
func (c *SOAPClient) call(action, body string) (string, error) {
	envelope := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope
  xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/"
  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
  xmlns:xsd="http://www.w3.org/2001/XMLSchema">
<SOAP-ENV:Body>
%s
</SOAP-ENV:Body>
</SOAP-ENV:Envelope>`, body)

	req, err := http.NewRequest("POST", c.endpoint, bytes.NewBufferString(envelope))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", action)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("SOAP call %s: %w", action, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("SOAP read response: %w", err)
	}

	respStr := string(respBody)

	// Check for SOAP fault
	if strings.Contains(respStr, "SOAP-ENV:Fault") {
		faultMsg := extractTag(respStr, "faultstring")
		if faultMsg == "" {
			faultMsg = "unknown SOAP fault"
		}
		return "", fmt.Errorf("SOAP fault in %s: %s", action, faultMsg)
	}

	return respStr, nil
}

// xmlEscape escapes a string for safe inclusion in XML.
func xmlEscape(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}

// extractTag extracts the text content of the first occurrence of a tag.
func extractTag(xmlStr, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(xmlStr, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(xmlStr[start:], close)
	if end < 0 {
		return ""
	}
	return xmlStr[start : start+end]
}
