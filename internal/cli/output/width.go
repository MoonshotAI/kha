package output

import (
	"strings"
	"unicode"
)

// This file implements terminal display width measurement with zero
// dependencies (the CLI module may only depend on cobra + yaml.v3).
// The wide-range table below is a pragmatic subset of Unicode UAX #11
// (East Asian Width), covering the ranges that realistically show up in
// agent/session names: CJK, kana, hangul, fullwidth forms, and common
// emoji. It is NOT a complete UAX #11 implementation — unlisted wide
// ranges (e.g. CJK ext G/H, rare symbol blocks) measure as width 1.

// wideRanges lists inclusive rune ranges that occupy two terminal cells.
// Sorted, non-overlapping.
var wideRanges = [][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo
	{0x2E80, 0x303E},   // CJK Radicals Supplement .. CJK Symbols and Punctuation (3000–303F)
	{0x3041, 0x33FF},   // Hiragana, Katakana, Bopomofo, CJK Compatibility
	{0x3400, 0x4DBF},   // CJK Unified Ideographs Extension A
	{0x4E00, 0x9FFF},   // CJK Unified Ideographs
	{0xA000, 0xA4CF},   // Yi Syllables / Yi Radicals
	{0xAC00, 0xD7A3},   // Hangul Syllables
	{0xF900, 0xFAFF},   // CJK Compatibility Ideographs
	{0xFE30, 0xFE6F},   // CJK Compatibility Forms / Small Form Variants
	{0xFF00, 0xFF60},   // Fullwidth Forms
	{0xFFE0, 0xFFE6},   // Fullwidth Signs
	{0x1F300, 0x1F64F}, // emoji: Misc Symbols and Pictographs, Emoticons
	{0x1F680, 0x1F6FF}, // emoji: Transport and Map Symbols
	{0x1F900, 0x1F9FF}, // emoji: Supplemental Symbols and Pictographs
	{0x1FA70, 0x1FAFF}, // emoji: Symbols and Pictographs Extended-A
	{0x20000, 0x3FFFD}, // CJK Unified Ideographs Extension B and beyond
}

// runeWidth returns the terminal cell width of a single rune.
func runeWidth(r rune) int {
	switch {
	case r < 0x20 || (r >= 0x7F && r < 0xA0):
		return 0 // control characters
	case r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF:
		return 0 // zero-width space / ZWNJ / ZWJ / BOM
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		return 0 // combining marks
	}
	lo, hi := 0, len(wideRanges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r < wideRanges[mid][0]:
			hi = mid - 1
		case r > wideRanges[mid][1]:
			lo = mid + 1
		default:
			return 2
		}
	}
	return 1
}

// DisplayWidth returns the number of terminal cells s occupies. The input
// must not contain ANSI escape sequences (table cells in this CLI never
// carry color — colors only appear in `sessions attach` output, which is
// not column-aligned).
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// sanitizeCell prepares untrusted text for a table cell. Tabs and newlines
// are first rendered as plain spaces so they cannot break column layout;
// escape sequences and the remaining control characters are then stripped
// (StripEscapes — a cell comes from server data such as Session titles and
// file/artifact names, which an agent controls, so a crafted value must not
// inject CSI/OSC sequences into the user's terminal). Everything
// stripped is zero display width, so alignment is unaffected.
func sanitizeCell(s string) string {
	return StripEscapes(strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r':
			return ' '
		}
		return r
	}, s))
}
