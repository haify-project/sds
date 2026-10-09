package controller

import (
	"context"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/haify-project/haify/pkg/deployment"
)

// fillZFSCompression sets each pool's compression algorithm and achieved
// ratio, read from its root dataset on its node. addrs maps a pool's Node to
// the address to ask. Best effort: a node that does not answer leaves its
// pools without the figures, which the API reports as unknown.
//
// OpenZFS 2.2 and later compress by default, so a ZFS pool is very likely
// compressing already; without this nothing said whether, or how well.
func (sm *StorageManager) fillZFSCompression(ctx context.Context, pools []*PoolInfo, addrs map[string]string) {
	byHost := map[string][]*PoolInfo{}
	for _, p := range pools {
		if addr := addrs[p.Node]; addr != "" {
			byHost[addr] = append(byHost[addr], p)
		}
	}
	if len(byHost) == 0 {
		return
	}
	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	res, err := sm.controller.deployment.Exec(ctx, hosts,
		"echo "+base64Std(deployment.ZFSPoolPropertiesScript)+" | base64 -d | sudo /bin/sh")
	if err != nil || res == nil {
		sm.controller.logger.Debug("Could not read ZFS pool compression", zap.Error(err))
		return
	}
	for host, hr := range res.Hosts {
		if hr == nil || !hr.Success {
			continue
		}
		props := parseZFSPoolProperties(hr.Output)
		for _, p := range byHost[host] {
			if v, ok := props[p.Name]; ok {
				p.Compression, p.CompressRatio = v.compression, v.ratio
			}
		}
	}
}

type zfsPoolProps struct {
	compression string
	ratio       float64
}

// parseZFSPoolProperties reads `zfs get -H -p -o name,property,value` output.
// compressratio is printed as "1.85" with -p and as "1.85x" without; both are
// accepted.
func parseZFSPoolProperties(out string) map[string]zfsPoolProps {
	props := map[string]zfsPoolProps{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) != 3 {
			continue
		}
		v := props[f[0]]
		switch f[1] {
		case "compression":
			v.compression = f[2]
		case "compressratio":
			if r, err := strconv.ParseFloat(strings.TrimSuffix(f[2], "x"), 64); err == nil {
				v.ratio = r
			}
		default:
			continue
		}
		props[f[0]] = v
	}
	return props
}
