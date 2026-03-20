package vbox

import "strings"

// scancode maps key names to [make, break] PS/2 scan codes.
var scancode = map[string][2]string{
	"escape": {"01", "81"}, "1": {"02", "82"}, "2": {"03", "83"}, "3": {"04", "84"},
	"4": {"05", "85"}, "5": {"06", "86"}, "6": {"07", "87"}, "7": {"08", "88"},
	"8": {"09", "89"}, "9": {"0a", "8a"}, "0": {"0b", "8b"}, "minus": {"0c", "8c"},
	"equal": {"0d", "8d"}, "backspace": {"0e", "8e"}, "tab": {"0f", "8f"},
	"q": {"10", "90"}, "w": {"11", "91"}, "e": {"12", "92"}, "r": {"13", "93"},
	"t": {"14", "94"}, "y": {"15", "95"}, "u": {"16", "96"}, "i": {"17", "97"},
	"o": {"18", "98"}, "p": {"19", "99"}, "bracketleft": {"1a", "9a"},
	"bracketright": {"1b", "9b"}, "return": {"1c", "9c"}, "ctrl": {"1d", "9d"},
	"a": {"1e", "9e"}, "s": {"1f", "9f"}, "d": {"20", "a0"}, "f": {"21", "a1"},
	"g": {"22", "a2"}, "h": {"23", "a3"}, "j": {"24", "a4"}, "k": {"25", "a5"},
	"l": {"26", "a6"}, "semicolon": {"27", "a7"}, "apostrophe": {"28", "a8"},
	"grave": {"29", "a9"}, "shift": {"2a", "aa"}, "backslash": {"2b", "ab"},
	"z": {"2c", "ac"}, "x": {"2d", "ad"}, "c": {"2e", "ae"}, "v": {"2f", "af"},
	"b": {"30", "b0"}, "n": {"31", "b1"}, "m": {"32", "b2"}, "comma": {"33", "b3"},
	"period": {"34", "b4"}, "slash": {"35", "b5"}, "shift_r": {"36", "b6"},
	"alt": {"38", "b8"}, "space": {"39", "b9"}, "capslock": {"3a", "ba"},
	"f1": {"3b", "bb"}, "f2": {"3c", "bc"}, "f3": {"3d", "bd"}, "f4": {"3e", "be"},
	"f5": {"3f", "bf"}, "f6": {"40", "c0"}, "f7": {"41", "c1"}, "f8": {"42", "c2"},
	"f9": {"43", "c3"}, "f10": {"44", "c4"}, "numlock": {"45", "c5"},
	"scrolllock": {"46", "c6"}, "f11": {"57", "d7"}, "f12": {"58", "d8"},
}

// extendedKeys need E0 prefix for make and break.
var extendedKeys = map[string][2]string{
	"insert": {"52", "d2"}, "delete": {"53", "d3"}, "home": {"47", "c7"},
	"end": {"4f", "cf"}, "pageup": {"49", "c9"}, "pagedown": {"51", "d1"},
	"up": {"48", "c8"}, "down": {"50", "d0"}, "left": {"4b", "cb"},
	"right": {"4d", "cd"}, "ctrl_r": {"1d", "9d"}, "alt_r": {"38", "b8"},
	"super": {"5b", "db"}, "super_r": {"5c", "dc"}, "menu": {"5d", "dd"},
}

// shiftMap maps characters requiring shift to their base key name.
var shiftMap = map[rune]string{
	'!': "1", '@': "2", '#': "3", '$': "4", '%': "5", '^': "6", '&': "7",
	'*': "8", '(': "9", ')': "0", '_': "minus", '+': "equal", '{': "bracketleft",
	'}': "bracketright", '|': "backslash", ':': "semicolon", '"': "apostrophe",
	'~': "grave", '<': "comma", '>': "period", '?': "slash",
}

// charToKey maps non-shifted printable characters to their key name.
var charToKey = map[rune]string{
	'-': "minus", '=': "equal", '[': "bracketleft", ']': "bracketright",
	'\\': "backslash", ';': "semicolon", '\'': "apostrophe", '`': "grave",
	',': "comma", '.': "period", '/': "slash", ' ': "space",
	'1': "1", '2': "2", '3': "3", '4': "4", '5': "5",
	'6': "6", '7': "7", '8': "8", '9': "9", '0': "0",
}

// keyAliases maps common alternate names to canonical key names.
var keyAliases = map[string]string{
	"enter":      "return",
	"esc":        "escape",
	"lshift":     "shift",
	"rshift":     "shift_r",
	"lctrl":      "ctrl",
	"rctrl":      "ctrl_r",
	"lalt":       "alt",
	"ralt":       "alt_r",
	"lsuper":     "super",
	"rsuper":     "super_r",
	"win":        "super",
	"windows":    "super",
	"meta":       "super",
	"cmd":        "super",
	"del":        "delete",
	"ins":        "insert",
	"pgup":       "pageup",
	"pgdn":       "pagedown",
	"page_up":    "pageup",
	"page_down":  "pagedown",
	"arrowup":    "up",
	"arrowdown":  "down",
	"arrowleft":  "left",
	"arrowright": "right",
	"bksp":       "backspace",
}

// normalizeKey maps a key name to its canonical form.
func normalizeKey(name string) string {
	lower := strings.ToLower(name)
	if alias, ok := keyAliases[lower]; ok {
		return alias
	}
	return lower
}

// singleKeyScancodes returns the make and break scancodes for a single key.
func singleKeyScancodes(name string) (make_, break_ []string, ok bool) {
	name = normalizeKey(name)
	if codes, found := scancode[name]; found {
		return []string{codes[0]}, []string{codes[1]}, true
	}
	if codes, found := extendedKeys[name]; found {
		return []string{"e0", codes[0]}, []string{"e0", codes[1]}, true
	}
	return nil, nil, false
}

// TextToScancodes converts a text string into PS/2 scancodes. Each character is
// pressed and released in sequence, with shift applied where needed.
func TextToScancodes(text string) []string {
	var codes []string
	shiftMake, shiftBreak, _ := singleKeyScancodes("shift")

	for _, ch := range text {
		// Uppercase letter
		if ch >= 'A' && ch <= 'Z' {
			key := strings.ToLower(string(ch))
			mk, brk, ok := singleKeyScancodes(key)
			if !ok {
				continue
			}
			codes = append(codes, shiftMake...)
			codes = append(codes, mk...)
			codes = append(codes, brk...)
			codes = append(codes, shiftBreak...)
			continue
		}

		// Lowercase letter
		if ch >= 'a' && ch <= 'z' {
			mk, brk, ok := singleKeyScancodes(string(ch))
			if !ok {
				continue
			}
			codes = append(codes, mk...)
			codes = append(codes, brk...)
			continue
		}

		// Shifted special character
		if base, ok := shiftMap[ch]; ok {
			mk, brk, ok := singleKeyScancodes(base)
			if !ok {
				continue
			}
			codes = append(codes, shiftMake...)
			codes = append(codes, mk...)
			codes = append(codes, brk...)
			codes = append(codes, shiftBreak...)
			continue
		}

		// Direct char mapping (digits, punctuation)
		if key, ok := charToKey[ch]; ok {
			mk, brk, ok := singleKeyScancodes(key)
			if !ok {
				continue
			}
			codes = append(codes, mk...)
			codes = append(codes, brk...)
			continue
		}

		// Newline → Return
		if ch == '\n' {
			mk, brk, ok := singleKeyScancodes("return")
			if !ok {
				continue
			}
			codes = append(codes, mk...)
			codes = append(codes, brk...)
			continue
		}

		// Tab
		if ch == '\t' {
			mk, brk, ok := singleKeyScancodes("tab")
			if !ok {
				continue
			}
			codes = append(codes, mk...)
			codes = append(codes, brk...)
			continue
		}
	}

	return codes
}

// KeyToScancodes parses a key specification like "ctrl+a", "Return", or
// "shift+F5" and returns the scancodes to press all modifiers, press the key,
// then release in reverse order.
func KeyToScancodes(spec string) []string {
	parts := strings.Split(spec, "+")
	var allMake, allBreak []string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		mk, brk, ok := singleKeyScancodes(part)
		if !ok {
			continue
		}
		allMake = append(allMake, mk...)
		// Prepend to allBreak so release order is reversed.
		allBreak = append(brk, allBreak...)
	}

	return append(allMake, allBreak...)
}
