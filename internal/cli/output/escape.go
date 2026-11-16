package output

import (
	"strings"
	"unicode/utf8"
)

// This file holds the terminal-escape stripping shared by the table cells
// (sanitizeCell in width.go) and the event renderer (cli/internal/render —
// every server- or agent-provided string passes through before reaching a
// tty). Agent output is prompt-injection territory: printed raw, a
// crafted name or message could move the cursor, clear the screen, or write
// the clipboard (OSC 52) through the user's terminal. Everything stripped
// here is zero display width, so column alignment is unaffected.

// StripEscapes removes terminal control characters and escape sequences from
// untrusted text before it reaches a tty: C0/C1 controls (a lone \n
// survives), CSI sequences (ESC [ ... final-byte), OSC sequences (ESC ] ...
// terminated by BEL or ESC \), n-byte escape sequences with intermediate
// bytes (ESC 0x20–0x2F... one final byte 0x30–0x7E — the charset designations
// like ESC ( 0), and any other two-character ESC-initiated sequence.
func StripEscapes(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool {
		return r != '\n' && (r < 0x20 || (r >= 0x7f && r <= 0x9f))
	}) {
		return s // fast path: nothing to strip
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			i = skipEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\n':
			b.WriteByte('\n')
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// other C0/DEL/C1 controls dropped
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence starting at
// s[i] (s[i] == ESC).
func skipEscape(s string, i int) int {
	i++ // past ESC
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameter/intermediate bytes, one final byte 0x40–0x7E
		for i++; i < len(s) && (s[i] < 0x40 || s[i] > 0x7e); i++ {
		}
		if i < len(s) {
			i++ // the final byte
		}
	case ']': // OSC: payload until BEL or ST (ESC \)
		for i++; i < len(s); i++ {
			if s[i] == '\a' {
				i++
				break
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				i += 2
				break
			}
		}
	default: // ESC + intermediate bytes (0x20–0x2F) + one final byte
		// (0x30–0x7E): the n-byte forms — charset designations (ESC ( 0),
		// ESC ) 1, ESC % G, ... Without this the final byte of such a
		// sequence leaked through as printable text.
		if s[i] >= 0x20 && s[i] <= 0x2f {
			for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
				i++
			}
			if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
				i++ // the final byte
			}
		} else {
			i++ // any other two-character escape
		}
	}
	return i
}
