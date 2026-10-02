package controller

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/gateway"
)

func gatewayServiceHost(serviceIP string) string {
	serviceIP = strings.TrimSpace(serviceIP)
	if serviceIP == "" {
		return ""
	}

	ip, _, err := net.ParseCIDR(serviceIP)
	if err == nil {
		return ip.String()
	}

	host, _, found := strings.Cut(serviceIP, "/")
	if found {
		return host
	}
	return serviceIP
}

func gatewayExportDirectory(resource, exportPath string) string {
	exportPath = strings.TrimSpace(exportPath)
	if exportPath == "" {
		return filepath.Join(gateway.DefaultExportBasePath, resource)
	}
	if strings.HasPrefix(exportPath, gateway.DefaultExportBasePath+string(filepath.Separator)) {
		return exportPath
	}
	return filepath.Join(gateway.DefaultExportBasePath, resource, strings.TrimPrefix(exportPath, "/"))
}

func stringifyGatewayValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case []string:
		return strings.Join(typed, ",")
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if value := stringifyGatewayValue(item); value != "" {
				parts = append(parts, value)
			}
		}
		return strings.Join(parts, ",")
	case map[string]string:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", key, typed[key]))
		}
		return strings.Join(parts, ",")
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			value := stringifyGatewayValue(typed[key])
			if value == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s=%s", key, value))
		}
		return strings.Join(parts, ",")
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

func formatGatewayNodeStates(nodeStates map[string]*ResourceNodeState) string {
	if len(nodeStates) == 0 {
		return ""
	}

	keys := make([]string, 0, len(nodeStates))
	for key := range nodeStates {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		state := nodeStates[key]
		if state == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"%s:%s/%s/%s",
			key,
			state.Role,
			state.DiskState,
			state.Replication,
		))
	}

	return strings.Join(parts, ",")
}

func (s *Server) gatewayConfigOptions(ctx context.Context, resource string) map[string]string {
	options := make(map[string]string)
	if s.ctrl.db == nil {
		return options
	}

	dbGateway, err := s.ctrl.db.GetGatewayByResource(ctx, resource)
	if err != nil {
		return options
	}

	for key, value := range dbGateway.Config {
		switch typed := value.(type) {
		case map[string]interface{}:
			for subKey, subValue := range typed {
				stringValue := stringifyGatewayValue(subValue)
				if stringValue == "" {
					continue
				}
				options[key+"."+subKey] = stringValue
			}
		default:
			stringValue := stringifyGatewayValue(value)
			if stringValue == "" {
				continue
			}
			options[key] = stringValue
		}
	}

	if dbGateway.Status != "" {
		options["gateway_status"] = dbGateway.Status
	}
	if dbGateway.ActiveNode != "" {
		options["active_node"] = dbGateway.ActiveNode
	}

	return options
}

func gatewayInfoFromDBRecord(record *database.Gateway) *gateway.GatewayInfo {
	if record == nil {
		return nil
	}
	return &gateway.GatewayInfo{
		ID:       record.Resource,
		Name:     record.Name,
		Type:     string(record.Type),
		Resource: record.Resource,
	}
}

func (s *Server) getGatewayInfo(ctx context.Context, resource string) (*gateway.GatewayInfo, error) {
	if s.gateway != nil {
		gw, err := s.gateway.GetGateway(ctx, resource)
		if err == nil {
			return gw, nil
		}
	}

	if s.ctrl.db != nil {
		record, err := s.ctrl.db.GetGatewayByResource(ctx, resource)
		if err == nil {
			return gatewayInfoFromDBRecord(record), nil
		}
	}

	return nil, fmt.Errorf("gateway not found: %s", resource)
}

func (s *Server) listGatewayInfos(ctx context.Context) ([]*gateway.GatewayInfo, error) {
	if s.gateway != nil {
		gateways, err := s.gateway.ListGateways(ctx)
		if err == nil && len(gateways) > 0 {
			return gateways, nil
		}
	}

	if s.ctrl.db != nil {
		records, err := s.ctrl.db.ListGateways(ctx)
		if err != nil {
			return nil, err
		}
		gateways := make([]*gateway.GatewayInfo, 0, len(records))
		for _, record := range records {
			gateways = append(gateways, gatewayInfoFromDBRecord(record))
		}
		return gateways, nil
	}

	return []*gateway.GatewayInfo{}, nil
}

func (s *Server) gatewayRuntimeInfo(ctx context.Context, resource string) (string, string, map[string]string) {
	options := make(map[string]string)
	state := "configured"
	activeNode := ""

	if s.resources == nil {
		return state, activeNode, options
	}

	info, err := s.resources.GetResource(ctx, resource)
	if err != nil || info == nil {
		return state, activeNode, options
	}

	if info.Role != "" {
		options["role"] = info.Role
	}
	if len(info.Nodes) > 0 {
		options["nodes"] = strings.Join(info.Nodes, ",")
	}
	if len(info.Volumes) > 0 {
		options["volumes"] = strconv.Itoa(len(info.Volumes))
	}
	if nodeStates := formatGatewayNodeStates(info.NodeStates); nodeStates != "" {
		options["node_states"] = nodeStates
	}

	for nodeName, nodeState := range info.NodeStates {
		if nodeState != nil && strings.EqualFold(nodeState.Role, "Primary") {
			activeNode = nodeName
			break
		}
	}
	// "started" must mean the promoter's services actually came up, not merely
	// that the DRBD resource is Primary. A gateway whose target failed to start
	// (missing agent, mount error, ...) is reported as "failed" so the UI does
	// not show a broken gateway as running.
	if activeNode != "" {
		if s.gateway != nil && s.gateway.GatewayServiceActive(ctx, s.ctrl.ResolveHost(activeNode), resource) {
			state = "started"
		} else {
			state = "failed"
		}
	}

	return state, activeNode, options
}

func (s *Server) enrichGatewayInfo(ctx context.Context, gw *gateway.GatewayInfo) *sdspb.GatewayInfo {
	options := s.gatewayConfigOptions(ctx, gw.Resource)
	state, node, runtimeOptions := s.gatewayRuntimeInfo(ctx, gw.Resource)
	for key, value := range runtimeOptions {
		options[key] = value
	}
	if state == "configured" && options["gateway_status"] != "" {
		state = options["gateway_status"]
	}
	if node == "" && options["active_node"] != "" {
		node = options["active_node"]
	}

	info := &sdspb.GatewayInfo{
		Id:       gw.ID,
		Name:     gw.Name,
		Type:     gw.Type,
		State:    state,
		Node:     node,
		Resource: gw.Resource,
		Options:  options,
	}

	if gw.Type == "nfs" || info.Path == "" {
		info.Path = options["export_directory"]
	}

	return info
}
