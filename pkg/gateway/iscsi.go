// Package gateway provides iSCSI gateway functionality
package gateway

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"go.uber.org/zap"
)

// iSCSIManager handles iSCSI gateway operations
type iSCSIManager struct {
	*Manager
}

// NewISCSIManager creates a new iSCSI gateway manager
func NewISCSIManager(m *Manager) *iSCSIManager {
	return &iSCSIManager{Manager: m}
}

// CreateISCSIGateway creates an iSCSI gateway with drbd-reactor configuration
func (i *iSCSIManager) CreateISCSIGateway(ctx context.Context, req *v1.CreateISCSIGatewayRequest) (*v1.CreateISCSIGatewayResponse, error) {
	i.logger.Info("Creating iSCSI gateway",
		zap.String("resource", req.Resource),
		zap.String("iqn", req.Iqn),
		zap.String("service_ip", req.ServiceIp))

	// Parse service IP
	serviceIP, err := parseServiceIP(req.ServiceIp)
	if err != nil {
		return &v1.CreateISCSIGatewayResponse{
			Success: false,
			Message: fmt.Sprintf("invalid service IP: %v", err),
		}, err
	}

	// Fail early with a clear message if the OCF agents/tools an iSCSI gateway
	// needs are not installed on the resource's nodes, rather than writing a
	// promoter config that silently fails to start.
	if res, rerr := i.resources.GetResource(ctx, req.Resource); rerr == nil && res != nil {
		if err := i.checkGatewayPrereqs(ctx, res.Nodes,
			[]string{"Filesystem", "IPaddr2", "iSCSITarget", "iSCSILogicalUnit"},
			[]string{"targetcli"}); err != nil {
			return &v1.CreateISCSIGatewayResponse{Success: false, Message: err.Error()}, err
		}
	}

	// Auto-provision the cluster-private state volume when the resource is one
	// short, so a single-volume resource can be exported without a manual
	// add-volume step first.
	if err := i.resources.EnsureGatewayVolumes(ctx, req.Resource, 2); err != nil {
		return &v1.CreateISCSIGatewayResponse{
			Success: false,
			Message: fmt.Sprintf("failed to provision gateway state volume: %v", err),
		}, err
	}

	// Get volume info from resource - iSCSI requires at least 2 volumes
	// Volume 0: cluster-private, Volume 1+: LUNs exposed to initiators
	resInfo, err := i.resources.GetResource(ctx, req.Resource)
	if err != nil {
		return &v1.CreateISCSIGatewayResponse{
			Success: false,
			Message: fmt.Sprintf("failed to get resource info: %v", err),
		}, err
	}

	if len(resInfo.Volumes) < 2 {
		return &v1.CreateISCSIGatewayResponse{
			Success: false,
			Message: fmt.Sprintf("iSCSI gateway requires at least 2 volumes (got %d): volume 0 for cluster-private, volume 1+ for LUNs", len(resInfo.Volumes)),
		}, fmt.Errorf("resource %s has insufficient volumes for iSCSI gateway (need >= 2, got %d)", req.Resource, len(resInfo.Volumes))
	}

	// Get DRBD device for the resource
	drbdDevice, err := i.getDRBDDevice(ctx, req.Resource)
	if err != nil {
		i.logger.Warn("Failed to get DRBD device, using fallback",
			zap.String("resource", req.Resource),
			zap.Error(err))
		drbdDevice = "/dev/drbd0"
	}

	i.logger.Info("Using DRBD device for iSCSI gateway",
		zap.String("resource", req.Resource),
		zap.String("device", drbdDevice),
		zap.Int("volume_count", len(resInfo.Volumes)),
		zap.Int("lun_count", len(resInfo.Volumes)-1))

	// Generate drbd-reactor configuration
	// Promote on one of the resource's own nodes and make sure the
	// cluster-private volume carries a filesystem BEFORE reactor takes
	// over: its Filesystem agent mounts but never formats.
	// Only the cluster-private volume is formatted. The exported volume must
	// stay raw — it is a block device handed to an initiator, which puts its
	// own filesystem on it. Passing volumes[0] here formatted the operator's
	// data volume as gateway scratch; see clusterPrivateAndPayload.
	clusterPrivateDev, _ := clusterPrivateAndPayload(resInfo.Volumes, drbdDevice)
	if err := i.ensureGatewayPrerequisites(ctx, req.Resource, resInfo.Nodes, clusterPrivateDev); err != nil {
		return &v1.CreateISCSIGatewayResponse{
			Success: false,
			Message: err.Error(),
		}, err
	}

	config, err := i.generateISCSIGatewayConfig(req, serviceIP, drbdDevice, resInfo.Volumes)
	if err != nil {
		return &v1.CreateISCSIGatewayResponse{
			Success: false,
			Message: fmt.Sprintf("failed to generate config: %v", err),
		}, err
	}

	// Write configuration to all nodes
	pluginID := fmt.Sprintf("sds-iscsi-%s", req.Resource)
	if err := i.writeReactorConfig(ctx, req.Resource, pluginID, config); err != nil {
		return &v1.CreateISCSIGatewayResponse{
			Success: false,
			Message: fmt.Sprintf("failed to write config: %v", err),
		}, err
	}

	configPath := filepath.Join(DrbdReactorConfigDir, fmt.Sprintf("%s.toml", pluginID))

	return &v1.CreateISCSIGatewayResponse{
		Success:    true,
		Message:    "iSCSI gateway configuration created successfully",
		ConfigPath: configPath,
	}, nil
}

// generateISCSIGatewayConfig generates drbd-reactor TOML configuration for iSCSI gateway
func (i *iSCSIManager) generateISCSIGatewayConfig(req *v1.CreateISCSIGatewayRequest, serviceIP *ServiceIP, drbdDevice string, volumes []*ResourceVolumeInfo) (string, error) {
	// Template for iSCSI gateway - matches linstor-gateway pattern
	tmpl := `# SDS iSCSI Gateway Configuration
# Generated by SDS Controller
# Resource: {{ .Resource }}
# IQN: {{ .IQN }}
# Service IP: {{ .ServiceIP }}

[[promoter]]

  [promoter.metadata]
    linstor-gateway-schema-version = 1

  [promoter.resources]

    [promoter.resources.{{ .Resource }}]
      on-drbd-demote-failure = "reboot-immediate"
      runner = "systemd"
      stop-services-on-exit = true
      target-as = "Requires"

      start = [
        "ocf:heartbeat:Filesystem fs_cluster_private device={{ .DRBDDevice }} directory={{ .ClusterPrivatePath }} fstype={{ .FSType }} run_fsck=no",
        "ocf:heartbeat:IPaddr2 service_ip0 ip={{ .IPAddress }} cidr_netmask={{ .Prefix }}",
        "ocf:heartbeat:iSCSITarget target iqn={{ .IQN }} portals={{ .Portal }} {{ .CHAPArgs }}allowed_initiators={{ .AllowedInitiators }} implementation={{ .Implementation }}",
{{ range $idx, $lun := .LUNs }}
        "ocf:heartbeat:iSCSILogicalUnit lu{{ $lun.Number }} target_iqn={{ $.IQN }} lun={{ $lun.Number }} path={{ $lun.Device }} product_id={{ $lun.Serial }} scsi_sn={{ $lun.Serial }}",
{{ end }}
      ]
`
	// portblock/portunblock removed: on failover the unblock step did not
	// reliably clear the block's DROP rule on the new active node, firewalling
	// clients off the iSCSI port (verified on real node failover). The data
	// fencing is provided by DRBD (the demoted node goes Secondary, losing
	// write access) plus the target being stopped and the VIP moving away.

	ipAddr := serviceIP.IP.String()
	prefix := serviceIP.Prefix
	portal := fmt.Sprintf("%s:%d", ipAddr, DefaultISCSIPort)

	// Prepare LUNs - Volume 0 is cluster-private, volumes 1+ are exposed as LUNs.
	// Each LUN needs a unique serial number based on IQN + volume number.
	type LUN struct {
		Number int
		Device string
		Serial string
	}

	// Which volume is the gateway's own scratch and which is the operator's is
	// decided by name, not by position — see clusterPrivateAndPayload. Doing it
	// by position handed the initiator the 1 GiB state volume.
	clusterPrivateDev, payload := clusterPrivateAndPayload(volumes, drbdDevice)

	// LUN numbering stays 1-based and independent of the DRBD volume number:
	// LUN 0 has special meaning to some initiators, and the payload volume is
	// now usually volume 0.
	luns := make([]LUN, 0, len(payload))
	for i, vol := range payload {
		n := i + 1
		luns = append(luns, LUN{
			Number: n,
			Device: vol.Device,
			Serial: generateSerialFromIQN(req.Iqn, n),
		})
	}

	// Default values
	username := req.Username
	password := req.Password
	implementation := req.Implementation
	allowedInitiators := strings.Join(req.AllowedInitiators, " ")

	// Only emit CHAP arguments when credentials were actually supplied.
	// Writing literal "username"/"password" placeholders (the old default)
	// forced CHAP auth with bogus credentials on a gateway the user meant to
	// leave open.
	chapArgs := ""
	if username != "" && password != "" {
		chapArgs = fmt.Sprintf("incoming_username=%s incoming_password=%s ", username, password)
	}
	if implementation == "" || implementation == "lio" {
		// "lio" historically meant the long-gone lio_node toolchain; every
		// current distro ships targetcli, which the agent calls "lio-t".
		implementation = "lio-t"
	}
	// An empty allowed_initiators list stays empty: the OCF agent treats
	// each token as an initiator WWN, so a made-up "ALL" fails validation
	// ("WWN not valid"). linstor-gateway passes the empty string too.
	_ = allowedInitiators

	clusterPrivatePath := filepath.Join(DefaultClusterPrivateMountPath, req.Resource)

	data := struct {
		Resource           string
		IQN                string
		ServiceIP          string
		IPAddress          string
		Prefix             int
		Portal             string
		FSType             string
		ClusterPrivatePath string
		ISCSIPort          int
		CHAPArgs           string
		AllowedInitiators  string
		Implementation     string
		LUNs               []LUN
		DRBDDevice         string
	}{
		Resource:           req.Resource,
		IQN:                req.Iqn,
		ServiceIP:          req.ServiceIp,
		IPAddress:          ipAddr,
		Prefix:             prefix,
		Portal:             portal,
		FSType:             DefaultFSType,
		DRBDDevice:         clusterPrivateDev,
		ClusterPrivatePath: clusterPrivatePath,
		ISCSIPort:          DefaultISCSIPort,
		CHAPArgs:           chapArgs,
		AllowedInitiators:  allowedInitiators,
		Implementation:     implementation,
		LUNs:               luns,
	}

	return executeTemplate(tmpl, data)
}

// GetISCSIGatewayStatus gets the status of an iSCSI gateway
func (i *iSCSIManager) GetISCSIGatewayStatus(ctx context.Context, resource string) (map[string]interface{}, error) {
	status := map[string]interface{}{
		"type":     "iscsi",
		"resource": resource,
		"status":   "unknown",
	}

	// Check if the gateway config exists
	configPath := filepath.Join(DrbdReactorConfigDir, fmt.Sprintf("sds-iscsi-%s.toml", resource))
	if _, err := os.Stat(configPath); err != nil {
		status["status"] = "not_configured"
		return status, nil
	}

	status["status"] = "configured"
	status["config_path"] = configPath

	// Check if resource is primary
	if resInfo, err := i.resources.GetResource(ctx, resource); err == nil {
		status["role"] = resInfo.Role
		status["nodes"] = resInfo.Nodes
		status["volumes"] = len(resInfo.Volumes)
	}

	return status, nil
}

// DeleteISCSIGateway deletes an iSCSI gateway
func (i *iSCSIManager) DeleteISCSIGateway(ctx context.Context, resource string) error {
	i.logger.Info("Deleting iSCSI gateway", zap.String("resource", resource))

	configFile := fmt.Sprintf("sds-iscsi-%s.toml", resource)
	configPath := filepath.Join(DrbdReactorConfigDir, configFile)

	// Remove config from all nodes
	for _, host := range i.hosts {
		rmCmd := fmt.Sprintf("sudo rm -f %s", configPath)
		if err := i.deployment.Exec(ctx, []string{host}, rmCmd); err != nil {
			i.logger.Warn("Failed to delete config",
				zap.String("node", host),
				zap.Error(err))
		}
	}

	// Reload drbd-reactor
	if err := i.reloadDrbdReactor(ctx); err != nil {
		return err
	}

	i.logger.Info("iSCSI gateway deleted", zap.String("resource", resource))
	return nil
}

// AddLUN adds a LUN to an existing iSCSI gateway
func (i *iSCSIManager) AddLUN(ctx context.Context, resource string, lunNumber int, device string) error {
	i.logger.Info("Adding LUN to iSCSI gateway",
		zap.String("resource", resource),
		zap.Int("lun", lunNumber),
		zap.String("device", device))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return err
	}

	lines, trailingNewline := splitConfigLines(content)
	for _, line := range lines {
		if params, ok := parseISCSILUNLine(line); ok && params["lun"] == fmt.Sprintf("%d", lunNumber) {
			return fmt.Errorf("LUN %d already exists", lunNumber)
		}
	}

	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	targetParams, _ := parseISCSITargetLine(lines[targetIdx])
	iqn := targetParams["iqn"]
	if iqn == "" {
		return fmt.Errorf("failed to parse target IQN from config")
	}

	newLine := buildISCSILUNLine(lunNumber, iqn, device)
	// Insert the new LUN as the last entry of the start array (before its
	// closing bracket); portunblock no longer exists to anchor on.
	lines, err = insertLineBefore(lines, newLine, func(line string) bool {
		return strings.TrimSpace(line) == "]"
	})
	if err != nil {
		return err
	}

	return i.persistGatewayConfig(ctx, resource, pluginID, joinConfigLines(lines, trailingNewline))
}

// RemoveLUN removes a LUN from an iSCSI gateway
func (i *iSCSIManager) RemoveLUN(ctx context.Context, resource string, lunNumber int) error {
	i.logger.Info("Removing LUN from iSCSI gateway",
		zap.String("resource", resource),
		zap.Int("lun", lunNumber))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return err
	}

	lines, trailingNewline := splitConfigLines(content)
	lines, removed := removeLine(lines, func(line string) bool {
		params, ok := parseISCSILUNLine(line)
		return ok && params["lun"] == fmt.Sprintf("%d", lunNumber)
	})
	if !removed {
		return fmt.Errorf("LUN %d not found", lunNumber)
	}

	return i.persistGatewayConfig(ctx, resource, pluginID, joinConfigLines(lines, trailingNewline))
}

// ListLUNs lists all configured LUNs for an iSCSI gateway.
func (i *iSCSIManager) ListLUNs(ctx context.Context, resource string) ([]map[string]string, error) {
	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return nil, err
	}

	var luns []map[string]string
	for _, line := range strings.Split(content, "\n") {
		params, ok := parseISCSILUNLine(line)
		if !ok {
			continue
		}
		luns = append(luns, map[string]string{
			"lun":        params["lun"],
			"device":     params["path"],
			"target_iqn": params["target_iqn"],
		})
	}

	return luns, nil
}

// ==================== Target Management ====================

// CreateTarget creates an iSCSI target on the gateway
func (i *iSCSIManager) CreateTarget(ctx context.Context, resource string) error {
	i.logger.Info("Creating iSCSI target", zap.String("resource", resource))

	// Use targetcli or configuration files to create the target
	// This is typically done by the OCF resource agent during start

	return fmt.Errorf("CreateTarget: managed by OCF resource agent")
}

// DeleteTarget deletes an iSCSI target
func (i *iSCSIManager) DeleteTarget(ctx context.Context, resource string) error {
	i.logger.Info("Deleting iSCSI target", zap.String("resource", resource))

	return fmt.Errorf("DeleteTarget: use gateway deletion instead")
}

// ListTargets lists all iSCSI targets
func (i *iSCSIManager) ListTargets(ctx context.Context, host string) ([]string, error) {
	files, err := os.ReadDir(DrbdReactorConfigDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read config directory: %w", err)
	}

	var targets []string
	for _, file := range files {
		if !strings.HasPrefix(file.Name(), "sds-iscsi-") || !strings.HasSuffix(file.Name(), ".toml") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(DrbdReactorConfigDir, file.Name()))
		if err != nil {
			continue
		}

		for _, line := range strings.Split(string(content), "\n") {
			if params, ok := parseISCSITargetLine(line); ok && params["iqn"] != "" {
				targets = append(targets, params["iqn"])
			}
		}
	}

	return uniqueSortedValues(targets), nil
}

// ==================== ACL Management ====================

// AddInitiator adds an allowed initiator to the iSCSI gateway
func (i *iSCSIManager) AddInitiator(ctx context.Context, resource, initiatorIQN string) error {
	i.logger.Info("Adding initiator ACL",
		zap.String("resource", resource),
		zap.String("iqn", initiatorIQN))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return err
	}

	lines, trailingNewline := splitConfigLines(content)
	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	params, _ := parseISCSITargetLine(lines[targetIdx])
	allowed := parseAllowedList(params["allowed_initiators"])
	allowed = append(allowed, initiatorIQN)
	lines[targetIdx] = buildISCSITargetLine(
		params["iqn"],
		params["portals"],
		params["incoming_username"],
		params["incoming_password"],
		formatAllowedList(allowed),
		params["implementation"],
	)

	return i.persistGatewayConfig(ctx, resource, pluginID, joinConfigLines(lines, trailingNewline))
}

// RemoveInitiator removes an initiator from the iSCSI gateway
func (i *iSCSIManager) RemoveInitiator(ctx context.Context, resource, initiatorIQN string) error {
	i.logger.Info("Removing initiator ACL",
		zap.String("resource", resource),
		zap.String("iqn", initiatorIQN))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return err
	}

	lines, trailingNewline := splitConfigLines(content)
	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	params, _ := parseISCSITargetLine(lines[targetIdx])
	current := parseAllowedList(params["allowed_initiators"])
	if len(current) == 0 {
		return fmt.Errorf("gateway currently allows all initiators; cannot remove a specific initiator without first defining an explicit ACL")
	}

	updated := removeValue(current, initiatorIQN)
	if len(updated) == len(current) {
		return fmt.Errorf("initiator not found: %s", initiatorIQN)
	}

	lines[targetIdx] = buildISCSITargetLine(
		params["iqn"],
		params["portals"],
		params["incoming_username"],
		params["incoming_password"],
		formatAllowedList(updated),
		params["implementation"],
	)

	return i.persistGatewayConfig(ctx, resource, pluginID, joinConfigLines(lines, trailingNewline))
}

// ListInitiators lists all initiators for an iSCSI gateway
func (i *iSCSIManager) ListInitiators(ctx context.Context, resource string) ([]string, error) {
	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return nil, err
	}

	for _, line := range strings.Split(content, "\n") {
		if params, ok := parseISCSITargetLine(line); ok {
			allowed := parseAllowedList(params["allowed_initiators"])
			if len(allowed) == 0 {
				return []string{"ALL"}, nil
			}
			return allowed, nil
		}
	}

	return nil, fmt.Errorf("failed to locate iSCSI target definition")
}

// ==================== CHAP Authentication ====================

// SetCHAP sets CHAP authentication for the iSCSI gateway
func (i *iSCSIManager) SetCHAP(ctx context.Context, resource, username, password string, mutual bool) error {
	i.logger.Info("Setting CHAP authentication",
		zap.String("resource", resource),
		zap.String("username", username),
		zap.Bool("mutual", mutual))

	if mutual {
		return fmt.Errorf("mutual CHAP is not supported by the current iSCSI gateway config writer")
	}

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return err
	}

	lines, trailingNewline := splitConfigLines(content)
	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	params, _ := parseISCSITargetLine(lines[targetIdx])
	lines[targetIdx] = buildISCSITargetLine(
		params["iqn"],
		params["portals"],
		username,
		password,
		formatAllowedList(parseAllowedList(params["allowed_initiators"])),
		params["implementation"],
	)

	return i.persistGatewayConfig(ctx, resource, pluginID, joinConfigLines(lines, trailingNewline))
}

// GetCHAP gets CHAP authentication settings
func (i *iSCSIManager) GetCHAP(ctx context.Context, resource string) (username, password string, mutual bool, err error) {
	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return "", "", false, err
	}

	for _, line := range strings.Split(content, "\n") {
		if params, ok := parseISCSITargetLine(line); ok {
			return params["incoming_username"], params["incoming_password"], false, nil
		}
	}

	return "", "", false, fmt.Errorf("failed to locate iSCSI target definition")
}

// ==================== Helper Functions ====================

// generateIQN generates an IQN for a given resource
func generateIQN(resource string) string {
	// Format: iqn.2024-01.com.example:sds.resource-name
	return fmt.Sprintf("iqn.2024-01.com.example:sds.%s", resource)
}

// validateIQN validates an IQN format
func validateIQN(iqn string) error {
	if !strings.HasPrefix(iqn, "iqn.") {
		return fmt.Errorf("invalid IQN format: must start with 'iqn.'")
	}

	parts := strings.Split(iqn, ":")
	if len(parts) < 2 {
		return fmt.Errorf("invalid IQN format: missing colon separator")
	}

	return nil
}

// parsePortal parses an iSCSI portal string (host:port)
func parsePortal(portal string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(portal)
	if err != nil {
		return "", 0, err
	}

	port := 3260 // default iSCSI port
	if portStr != "" {
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
			return "", 0, fmt.Errorf("invalid port number: %s", portStr)
		}
	}

	return host, port, nil
}
