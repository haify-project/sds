package controller

import (
	"fmt"
	"strings"
)

// applyDrbdOptions edits an existing DRBD .res config in place, applying each
// "section/key" -> value entry. Keys without a section go to the resource-level
// "options" section; "disk/..." keys go to the disk-options sub-block inside
// "volume 0". Existing keys are replaced, missing keys and missing blocks are
// created. Volumes, on-sections and unrelated settings are preserved, so this
// is safe to run against a live, multi-volume resource.
func applyDrbdOptions(config string, raw map[string]string) (string, error) {
	if strings.TrimSpace(config) == "" {
		return "", fmt.Errorf("empty resource config")
	}
	lines := strings.Split(config, "\n")
	for k, v := range raw {
		section, key := "options", strings.TrimSpace(k)
		if i := strings.Index(k, "/"); i >= 0 {
			section = strings.ToLower(strings.TrimSpace(k[:i]))
			key = strings.TrimSpace(k[i+1:])
		}
		if key == "" {
			return "", fmt.Errorf("invalid option key %q", k)
		}
		path := []string{section}
		if section == "disk" {
			// disk options live inside the per-volume block, not at top level.
			path = []string{"volume 0", "disk"}
		}
		var err error
		lines, err = setOptionAtPath(lines, path, key, strings.TrimSpace(v))
		if err != nil {
			return "", err
		}
	}
	return strings.Join(lines, "\n"), nil
}

func braceDelta(line string) int {
	return strings.Count(line, "{") - strings.Count(line, "}")
}

func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// isBlockHeader reports whether a line opens a "<header> {" block (e.g.
// "options {", "volume 0 {"). It excludes value lines like "disk /dev/x;".
func isBlockHeader(line, header string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, header) {
		return false
	}
	rest := strings.TrimSpace(t[len(header):])
	return strings.HasPrefix(rest, "{")
}

// resourceBlock returns the open/close line indices of the outer "resource { }"
// block (its braces), spanning the whole config.
func resourceBlock(lines []string) (open, close int, err error) {
	open = -1
	for i, l := range lines {
		if strings.Contains(l, "{") {
			open = i
			break
		}
	}
	if open == -1 {
		return 0, 0, fmt.Errorf("no resource block found")
	}
	depth := 0
	for j := open; j < len(lines); j++ {
		depth += braceDelta(lines[j])
		if depth == 0 {
			return open, j, nil
		}
	}
	return 0, 0, fmt.Errorf("unbalanced braces in config")
}

// findChildBlock finds a direct child block named header inside the parent
// block (parentOpen..parentClose), returning its open/close brace lines.
func findChildBlock(lines []string, parentOpen, parentClose int, header string) (open, close int, found bool) {
	depth := 0 // relative to just inside the parent
	for i := parentOpen + 1; i < parentClose; i++ {
		if depth == 0 && isBlockHeader(lines[i], header) {
			d := 0
			for j := i; j <= parentClose; j++ {
				d += braceDelta(lines[j])
				if d == 0 {
					return i, j, true
				}
			}
			return 0, 0, false
		}
		depth += braceDelta(lines[i])
		if depth < 0 {
			depth = 0
		}
	}
	return 0, 0, false
}

// createChildBlock inserts an empty "<header> {\n}" block just before the
// parent's closing brace and returns the updated lines plus the new block's
// open/close indices.
func createChildBlock(lines []string, parentOpen, parentClose int, header string) ([]string, int, int) {
	indent := indentOf(lines[parentOpen]) + "    "
	block := []string{indent + header + " {", indent + "}"}
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:parentClose]...)
	out = append(out, block...)
	out = append(out, lines[parentClose:]...)
	return out, parentClose, parentClose + 1
}

// setKeyInBlock replaces or inserts a "key value;" line as a direct child of
// the block (open..close).
func setKeyInBlock(lines []string, open, close int, key, value string) []string {
	indent := indentOf(lines[open]) + "    "
	newLine := fmt.Sprintf("%s%s %s;", indent, key, value)
	depth := 0
	for i := open + 1; i < close; i++ {
		t := strings.TrimSpace(lines[i])
		if depth == 0 && !strings.Contains(t, "{") {
			if f := strings.Fields(t); len(f) >= 1 && f[0] == key {
				lines[i] = newLine
				return lines
			}
		}
		depth += braceDelta(lines[i])
		if depth < 0 {
			depth = 0
		}
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:close]...)
	out = append(out, newLine)
	out = append(out, lines[close:]...)
	return out
}

// setOptionAtPath walks the block path (e.g. ["net"] or ["volume 0","disk"]),
// creating blocks as needed, then sets key=value in the deepest block.
func setOptionAtPath(lines []string, path []string, key, value string) ([]string, error) {
	open, close, err := resourceBlock(lines)
	if err != nil {
		return nil, err
	}
	for _, header := range path {
		childOpen, childClose, found := findChildBlock(lines, open, close, header)
		if !found {
			lines, childOpen, childClose = createChildBlock(lines, open, close, header)
		}
		open, close = childOpen, childClose
	}
	return setKeyInBlock(lines, open, close, key, value), nil
}
