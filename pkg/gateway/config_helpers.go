package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type configStoreReader interface {
	GetConfig(path string) (string, bool)
}

type configStoreWriter interface {
	SetConfig(path, content string)
}

func gatewayConfigPath(pluginID string) string {
	return filepath.Join(DrbdReactorConfigDir, fmt.Sprintf("%s.toml", pluginID))
}

func (m *Manager) readGatewayConfig(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err == nil {
		return string(content), nil
	}

	if reader, ok := m.deployment.(configStoreReader); ok {
		if value, exists := reader.GetConfig(path); exists {
			return value, nil
		}
	}

	return "", fmt.Errorf("failed to read config: %w", err)
}

func (m *Manager) persistGatewayConfig(ctx context.Context, resource, pluginID, content string) error {
	path := gatewayConfigPath(pluginID)

	if writer, ok := m.deployment.(configStoreWriter); ok {
		writer.SetConfig(path, content)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil && !os.IsPermission(err) && !os.IsNotExist(err) {
		return fmt.Errorf("failed to write local config: %w", err)
	}

	return m.writeReactorConfig(ctx, resource, pluginID, content)
}

func splitConfigLines(content string) ([]string, bool) {
	hasTrailingNewline := strings.HasSuffix(content, "\n")
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return []string{}, hasTrailingNewline
	}
	return strings.Split(trimmed, "\n"), hasTrailingNewline
}

func joinConfigLines(lines []string, trailingNewline bool) string {
	if len(lines) == 0 {
		if trailingNewline {
			return "\n"
		}
		return ""
	}

	content := strings.Join(lines, "\n")
	if trailingNewline {
		content += "\n"
	}
	return content
}

func findLineIndex(lines []string, match func(string) bool) int {
	for idx, line := range lines {
		if match(line) {
			return idx
		}
	}
	return -1
}

func insertLineBefore(lines []string, newLine string, match func(string) bool) ([]string, error) {
	idx := findLineIndex(lines, match)
	if idx < 0 {
		return nil, fmt.Errorf("failed to find insertion point")
	}

	lines = append(lines[:idx], append([]string{newLine}, lines[idx:]...)...)
	return lines, nil
}

func removeLine(lines []string, match func(string) bool) ([]string, bool) {
	for idx, line := range lines {
		if match(line) {
			return append(lines[:idx], lines[idx+1:]...), true
		}
	}
	return lines, false
}

func splitKeyValueTokens(content string) []string {
	return strings.Fields(strings.TrimSpace(content))
}

func parseOCFParams(content string, skipTokens int) map[string]string {
	fields := splitKeyValueTokens(content)
	if len(fields) <= skipTokens {
		return map[string]string{}
	}

	params := make(map[string]string)
	currentKey := ""
	for _, field := range fields[skipTokens:] {
		if strings.Contains(field, "=") {
			parts := strings.SplitN(field, "=", 2)
			currentKey = parts[0]
			params[currentKey] = parts[1]
			continue
		}

		if currentKey == "" {
			continue
		}

		if params[currentKey] == "" {
			params[currentKey] = field
		} else {
			params[currentKey] += " " + field
		}
	}

	return params
}

func quotedCommandContent(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimSuffix(trimmed, ",")
	trimmed = strings.Trim(trimmed, "\"")
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

func parseISCSITargetLine(line string) (map[string]string, bool) {
	content, ok := quotedCommandContent(line)
	if !ok || !strings.Contains(content, "ocf:heartbeat:iSCSITarget ") {
		return nil, false
	}
	return parseOCFParams(content, 2), true
}

func buildISCSITargetLine(iqn, portals, username, password string, allowed []string, implementation string) string {
	return fmt.Sprintf(
		`        "ocf:heartbeat:iSCSITarget target iqn=%s portals=%s incoming_username=%s incoming_password=%s allowed_initiators=%s implementation=%s",`,
		iqn,
		portals,
		username,
		password,
		strings.Join(allowed, " "),
		implementation,
	)
}

func parseISCSILUNLine(line string) (map[string]string, bool) {
	content, ok := quotedCommandContent(line)
	if !ok || !strings.Contains(content, "ocf:heartbeat:iSCSILogicalUnit ") {
		return nil, false
	}
	return parseOCFParams(content, 2), true
}

func buildISCSILUNLine(lunNumber int, iqn, device string) string {
	serial := generateSerialFromIQN(iqn, lunNumber)
	return fmt.Sprintf(
		`        "ocf:heartbeat:iSCSILogicalUnit lu%d target_iqn=%s lun=%d path=%s product_id=%s scsi_sn=%s",`,
		lunNumber,
		iqn,
		lunNumber,
		device,
		serial,
		serial,
	)
}

func parseNFSExportLine(line string) (map[string]string, bool) {
	content, ok := quotedCommandContent(line)
	if !ok || !strings.Contains(content, "ocf:heartbeat:exportfs ") {
		return nil, false
	}
	return parseOCFParams(content, 2), true
}

func buildNFSExportLine(exportID int, directory, fsid, clientSpec, options string) string {
	return fmt.Sprintf(
		`        "ocf:heartbeat:exportfs export_%d_0 directory=%s fsid=%s clientspec=%s options=%s",`,
		exportID,
		directory,
		fsid,
		clientSpec,
		options,
	)
}

func parseNVMeSubsystemLine(line string) (map[string]string, bool) {
	content, ok := quotedCommandContent(line)
	if !ok || !strings.Contains(content, "ocf:heartbeat:nvmet-subsystem ") {
		return nil, false
	}
	return parseOCFParams(content, 2), true
}

func buildNVMeSubsystemLine(nqn string, allowed []string, serial string) string {
	if len(allowed) == 0 {
		return fmt.Sprintf(`        "ocf:heartbeat:nvmet-subsystem subsys nqn=%s serial=%s",`, nqn, serial)
	}
	return fmt.Sprintf(
		`        "ocf:heartbeat:nvmet-subsystem subsys nqn=%s allowed_initiators=%s serial=%s",`,
		nqn,
		strings.Join(allowed, " "),
		serial,
	)
}

func parseNVMeNamespaceLine(line string) (map[string]string, bool) {
	content, ok := quotedCommandContent(line)
	if !ok || !strings.Contains(content, "ocf:heartbeat:nvmet-namespace ") {
		return nil, false
	}
	return parseOCFParams(content, 2), true
}

func parseNVMePortLine(line string) (map[string]string, bool) {
	content, ok := quotedCommandContent(line)
	if !ok || !strings.Contains(content, "ocf:heartbeat:nvmet-port ") {
		return nil, false
	}
	return parseOCFParams(content, 2), true
}

func buildNVMeNamespaceLine(namespaceID int, nqn, device string) string {
	uuid := generateUUID()
	return fmt.Sprintf(
		`        "ocf:heartbeat:nvmet-namespace ns_%d nqn=%s namespace_id=%d backing_path=%s uuid=%s nguid=%s",`,
		namespaceID,
		nqn,
		namespaceID,
		device,
		uuid,
		uuid,
	)
}

func uniqueSortedValues(values []string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func removeValue(values []string, target string) []string {
	target = strings.TrimSpace(target)
	var result []string
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			continue
		}
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func parseAllowedList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "ALL") {
		return nil
	}
	return uniqueSortedValues(strings.Fields(raw))
}

func formatAllowedList(values []string) []string {
	values = uniqueSortedValues(values)
	if len(values) == 0 {
		return nil
	}
	return values
}

func parseIntParam(params map[string]string, key string) (int, error) {
	value, ok := params[key]
	if !ok || strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("missing %s", key)
	}
	return strconv.Atoi(strings.TrimSpace(value))
}

func nextExportID(lines []string) int {
	maxID := 0
	for _, line := range lines {
		content, ok := quotedCommandContent(line)
		if !ok || !strings.Contains(content, "ocf:heartbeat:exportfs ") {
			continue
		}

		fields := splitKeyValueTokens(content)
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "export_") {
			continue
		}

		parts := strings.Split(fields[1], "_")
		if len(parts) < 2 {
			continue
		}

		id, err := strconv.Atoi(parts[1])
		if err == nil && id > maxID {
			maxID = id
		}
	}

	return maxID + 1
}

// resolveNFSExportPath turns the user-supplied export path into the
// directory that is mounted and exported. An absolute path is honored
// verbatim — when an admin asks for /srv/nfs-test, clients mount
// <vip>:/srv/nfs-test, not a path nested under the gateway base directory.
// Relative or empty paths land under DefaultExportBasePath/<resource> so
// quick setups stay grouped and collision-free.
func resolveNFSExportPath(resource, exportPath string) (string, error) {
	exportPath = strings.TrimSpace(exportPath)
	if exportPath == "" {
		return filepath.Join(DefaultExportBasePath, resource), nil
	}
	if !strings.HasPrefix(exportPath, "/") {
		return filepath.Join(DefaultExportBasePath, resource, exportPath), nil
	}

	cleaned := filepath.Clean(exportPath)
	if cleaned == "/" {
		return "", fmt.Errorf("export path must not be the filesystem root")
	}
	// The export directory becomes a mount point owned by the gateway;
	// refuse paths that would shadow system directories or SDS state.
	forbidden := []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var/lib/sds"}
	for _, prefix := range forbidden {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return "", fmt.Errorf("export path %s would shadow %s; choose a dedicated directory (e.g. /srv/...)", cleaned, prefix)
		}
	}
	return cleaned, nil
}
