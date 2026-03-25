package vbox

import (
	"encoding/binary"
	"fmt"
	"os"
	"unicode/utf16"
)

// patchNVRAMForDVDBoot injects UEFI boot variables directly into a VM's NVRAM
// file after it has been initialised by "modifynvram inituefivarstore".
//
// Three variables are written into the authenticated variable store:
//
//	Boot0000  — active EFI load option whose device path is a short-form
//	            Media File Path pointing to \EFI\BOOT\bootaa64.efi. OVMF
//	            expands this at boot time, finds the file on the installer
//	            DVD, and executes it without showing a "press any key" prompt.
//	BootOrder — [0x0000], so Boot0000 is the first (and only) entry tried.
//	Timeout   — 0x0000, causing OVMF to boot immediately with no menu.
//
// The AUTHENTICATED_VARIABLE_HEADER layout (60 bytes):
//
//	 0  StartId(2) State(1) Reserved(1) Attributes(4)
//	 8  MonotonicCount(8)
//	16  Timestamp/EFI_TIME(16)
//	32  PubKeyIndex(4)
//	36  NameSize(4)
//	40  DataSize(4)
//	44  VendorGuid(16)
func patchNVRAMForDVDBoot(nvramPath string) error {
	data, err := os.ReadFile(nvramPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if len(data) < 0x60 {
		return fmt.Errorf("file too small (%d bytes)", len(data))
	}

	// The EFI Firmware Volume header at offset 0 has HeaderLength at offset
	// 0x30. The VARIABLE_STORE_HEADER (24 bytes) immediately follows it, so
	// variable data begins at fvHeaderLen+24.
	fvHeaderLen := int(binary.LittleEndian.Uint16(data[0x30:]))
	varDataOff := fvHeaderLen + 24

	// Walk past any variables already present (StartId == 0x55AA).
	pos := varDataOff
	for pos+2 <= len(data) {
		startId := binary.LittleEndian.Uint16(data[pos:])
		if startId == 0xFFFF || startId == 0x0000 {
			break // unwritten erased flash — start writing here
		}
		if startId != 0x55AA {
			return fmt.Errorf("unexpected NVRAM startId 0x%04X at offset 0x%X", startId, pos)
		}
		if pos+60 > len(data) {
			return fmt.Errorf("truncated variable header at offset 0x%X", pos)
		}
		nameSize := int(binary.LittleEndian.Uint32(data[pos+36:]))
		dataSize := int(binary.LittleEndian.Uint32(data[pos+40:]))
		pos = align4(pos + 60 + nameSize + dataSize)
	}

	// EFI Global Variable GUID {8BE4DF61-93CA-11D2-AA0D-00E098032B8C}
	// encoded as mixed-endian in the NVRAM file.
	var efiGlobalGUID [16]byte
	copy(efiGlobalGUID[:], []byte{
		0x61, 0xDF, 0xE4, 0x8B, // uint32 0x8BE4DF61 LE
		0xCA, 0x93, // uint16 0x93CA LE
		0xD2, 0x11, // uint16 0x11D2 LE
		0xAA, 0x0D, 0x00, 0xE0, 0x98, 0x03, 0x2B, 0x8C,
	})

	const nvBS = 0x07 // NV | BootService | Runtime

	var vars []byte
	bootEntry := buildUEFILoadOption("Windows Boot Manager", `\EFI\BOOT\bootaa64.efi`)
	vars = appendAuthVar(vars, efiGlobalGUID, "Boot0000", nvBS, bootEntry)
	vars = appendAuthVar(vars, efiGlobalGUID, "BootOrder", nvBS, []byte{0x00, 0x00})
	vars = appendAuthVar(vars, efiGlobalGUID, "Timeout", nvBS, []byte{0x00, 0x00})

	if pos+len(vars) > len(data) {
		return fmt.Errorf("variable area too small (need %d bytes at 0x%X, file is %d bytes)",
			len(vars), pos, len(data))
	}
	copy(data[pos:], vars)
	return os.WriteFile(nvramPath, data, 0600)
}

// appendAuthVar serialises a single UEFI authenticated variable and appends it
// to buf, padding to a 4-byte boundary so the next entry is correctly aligned.
func appendAuthVar(buf []byte, guid [16]byte, name string, attrs uint32, payload []byte) []byte {
	nameUCS2 := encodeUCS2(name + "\x00")
	var hdr [60]byte
	binary.LittleEndian.PutUint16(hdr[0:], 0x55AA) // StartId
	hdr[2] = 0x3F                                  // State: VAR_ADDED
	binary.LittleEndian.PutUint32(hdr[4:], attrs)  // Attributes
	// hdr[8:36]: MonotonicCount, Timestamp, PubKeyIndex — all zero
	binary.LittleEndian.PutUint32(hdr[36:], uint32(len(nameUCS2))) // NameSize
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(payload)))  // DataSize
	copy(hdr[44:], guid[:])                                        // VendorGuid
	buf = append(buf, hdr[:]...)
	buf = append(buf, nameUCS2...)
	buf = append(buf, payload...)
	for len(buf)%4 != 0 {
		buf = append(buf, 0xFF) // pad with erased-flash value
	}
	return buf
}

// buildUEFILoadOption constructs a binary EFI_LOAD_OPTION with a short-form
// Media File Path device path. OVMF expands short-form paths by scanning all
// connected filesystems for the named file at boot time.
func buildUEFILoadOption(description, filePath string) []byte {
	desc := encodeUCS2(description + "\x00")

	// Media File Path DP node: type=0x04 (Media), subtype=0x04 (File Path).
	fpBytes := encodeUCS2(filePath + "\x00")
	node := make([]byte, 4+len(fpBytes))
	node[0] = 0x04
	node[1] = 0x04
	binary.LittleEndian.PutUint16(node[2:], uint16(len(node)))
	copy(node[4:], fpBytes)

	// End Entire Device Path: type=0x7F, subtype=0xFF, length=4.
	filePathList := append(node, 0x7F, 0xFF, 0x04, 0x00)

	var b []byte
	b = append(b, 0x01, 0x00, 0x00, 0x00) // Attributes: LOAD_OPTION_ACTIVE
	b = binary.LittleEndian.AppendUint16(b, uint16(len(filePathList)))
	b = append(b, desc...)
	b = append(b, filePathList...)
	return b
}

// encodeUCS2 encodes a Go string as UCS-2 little-endian bytes.
func encodeUCS2(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	b := make([]byte, len(u16)*2)
	for i, r := range u16 {
		binary.LittleEndian.PutUint16(b[i*2:], r)
	}
	return b
}

func align4(n int) int {
	return (n + 3) &^ 3
}
