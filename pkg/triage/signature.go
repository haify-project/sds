package triage

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Signatures: what makes two lines the same problem.
//
// A degrading cluster does not produce one error, it produces the same error
// several hundred times with a different timestamp, a different offset and a
// different peer each time. Handed to an assistant unnormalised, those are
// three hundred facts competing for a context window and one of them is the
// problem. Normalised, they are one finding with a count, and the count is
// itself information: twice in an hour is a blip, four hundred times is a loop.
//
// The substitutions below are deliberately conservative. Every one of them
// removes something that is *known* to vary between two occurrences of one
// problem — a time, an address, a byte offset — and nothing removes a word.
// Over-normalising is the worse failure: two distinct problems merged into one
// finding hide each other, and nothing downstream can tell they were merged.

var (
	// The leading "<timestamp> <hostname>" of a syslog-shaped line. Both are
	// per-occurrence: the host is which node logged it, not what happened, and
	// leaving it in splits one fault across two nodes into two findings — which
	// is exactly the shape of a cluster problem. The node is not lost by this;
	// it is carried on the finding, where it belongs.
	//
	// Only a line that opens with a real timestamp gets its second token read
	// as a hostname. dmesg -T's `[Sat Sep  6 ...] drbd haify-meta: ...` has no
	// host field at all, and a rule loose enough to strip one there would eat
	// the subsystem name.
	reJournalISO = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})? +\S+ `)
	reJournalSys = regexp.MustCompile(`^[A-Z][a-z]{2} +\d+ \d{2}:\d{2}:\d{2} +\S+ `)

	// Timestamps in the shapes the collectors actually emit: journalctl's
	// short-iso, dmesg -T's bracketed date, and dmesg's raw seconds counter.
	reISOTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	reDmesgT  = regexp.MustCompile(`\[[A-Z][a-z]{2} [A-Z][a-z]{2} ?\d+ \d{2}:\d{2}:\d{2} \d{4}\]`)
	reUptime  = regexp.MustCompile(`\[\s*\d+\.\d+\]`)

	// Identity that varies per occurrence and never per problem.
	reUUID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reHex  = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`)
	reIPv4 = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`)
	rePID  = regexp.MustCompile(`\[\d+\]`)

	reSpace = regexp.MustCompile(`\s+`)
)

// Signature reduces a log line to what would be the same on the next
// occurrence of the same problem.
func Signature(line string) string {
	s := strings.TrimSpace(line)
	if m := reJournalISO.FindString(s); m != "" {
		s = "<time> <host> " + s[len(m):]
	} else if m := reJournalSys.FindString(s); m != "" {
		s = "<time> <host> " + s[len(m):]
	}
	s = reISOTime.ReplaceAllString(s, "<time>")
	s = reDmesgT.ReplaceAllString(s, "<time>")
	s = reUptime.ReplaceAllString(s, "<time>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reIPv4.ReplaceAllString(s, "<addr>")
	s = rePID.ReplaceAllString(s, "[<pid>]")
	s = replaceStandaloneNumbers(s)
	s = reSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// replaceStandaloneNumbers collapses a number that stands on its own, and only
// then. It is hand-written rather than a regexp because the rule needs to look
// at what precedes the digits and Go's regexp has no lookbehind — so `drbd0`
// and `ext4` keep their digits and `offset 4096` does not.
func replaceStandaloneNumbers(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		// A digit run is only a number when what precedes it is not a letter.
		if i > 0 && isWordByte(s[i-1]) {
			start := i
			for i < len(s) && isWordByte(s[i]) {
				i++
			}
			b.WriteString(s[start:i])
			continue
		}
		j := i
		for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
			j++
		}
		// A unit suffix belongs to the number, not to the next word.
		k := j
		for k < len(s) && isUnitByte(s[k]) {
			k++
		}
		if k < len(s) && isWordByte(s[k]) {
			k = j // not a unit, just the next word starting with K/M/G
		}
		if j < len(s) && isWordByte(s[j]) && k == j {
			// 4k5, 3rd — part of a token, keep it.
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		b.WriteString("<n>")
		i = k
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWordByte(c byte) bool {
	return c == '_' || isDigit(c) ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isUnitByte covers the size suffixes the storage collectors emit. It is not a
// general unit table: a letter that is not one of these is the next word.
func isUnitByte(c byte) bool {
	switch c {
	case 'K', 'M', 'G', 'T', 'P', 'i', 'B', 'k', 'b', '%':
		return true
	}
	return false
}

// signatureID is a short stable handle for a signature, so a caller can refer
// to one finding across two calls without quoting the whole line back.
func signatureID(sig string) string {
	sum := sha256.Sum256([]byte(sig))
	return "sig-" + hex.EncodeToString(sum[:4])
}
