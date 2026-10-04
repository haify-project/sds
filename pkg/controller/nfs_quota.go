package controller

import (
	"context"
	"fmt"
	"hash/crc32"
	"strings"

	"github.com/haify-project/sds/pkg/gateway"
)

// Directory quotas for NFS exports.
//
// An NFS gateway exports one filesystem; several exports are directories in
// it, and nothing stopped one of them filling the whole volume. ext4 project
// quotas cap a directory tree: the tree is tagged with a project id (inherited
// by everything created in it) and the id gets a block limit.
//
// It needs the filesystem made with the project feature and mounted with
// prjquota. Gateways created from this version on have both. An older one
// does not, and the feature can only be added to an unmounted filesystem
// (`tune2fs -O quota,project <device>`, then recreate the gateway), so the
// error says that. The id is derived from the export path, so it is the same
// whichever node serves the gateway after a failover; it lives in the
// filesystem, which moves with it.

// projectID is an export directory's ext4 project id: stable, nonzero, and
// clear of the small ids people assign by hand.
func projectID(dir string) uint32 {
	return crc32.ChecksumIEEE([]byte(dir))%4000000000 + 100000
}

// SetNFSExportQuota limits an NFS export directory of resource to limitBytes;
// zero removes the limit.
func (rm *ResourceManager) SetNFSExportQuota(ctx context.Context, resource, exportPath string, limitBytes uint64) error {
	dir, err := gateway.ResolveNFSExportPath(resource, exportPath)
	if err != nil {
		return err
	}
	hosts, err := rm.failoverHosts(ctx, resource)
	if err != nil {
		return err
	}
	id := projectID(dir)
	// Run where the export's filesystem is mounted from DRBD; the others skip.
	script := fmt.Sprintf(`set -e
dir=%[1]q
[ -d "$dir" ] || { echo SKIP; exit 0; }
dev=$(findmnt -n -o SOURCE -T "$dir" 2>/dev/null || true)
case "$dev" in /dev/drbd*) ;; *) echo SKIP; exit 0 ;; esac
mp=$(findmnt -n -o TARGET -T "$dir")
findmnt -n -o OPTIONS -T "$dir" | grep -qw prjquota || { echo NOPRJQUOTA; exit 0; }
command -v setquota >/dev/null 2>&1 || { echo NOSETQUOTA; exit 0; }
chattr -R -p %[2]d +P "$dir"
setquota -P %[2]d 0 %[3]d 0 0 "$mp"
echo DONE
`, dir, id, (limitBytes+1023)/1024)
	res, err := rm.deployment.Exec(ctx, hosts, "echo "+base64Std(script)+" | base64 -d | sudo /bin/bash")
	if err != nil {
		return err
	}
	var seen []string
	for _, h := range hosts {
		hr := res.Hosts[h]
		if hr == nil {
			continue
		}
		out := strings.TrimSpace(hr.Output)
		switch {
		case !hr.Success:
			return fmt.Errorf("setting the quota on %s failed: %s", rm.nodeLabel(h), out)
		case strings.HasSuffix(out, "DONE"):
			return nil
		case strings.HasSuffix(out, "NOPRJQUOTA"):
			return fmt.Errorf("the filesystem of %s is not mounted with project quotas: it predates them. Add the "+
				"feature while it is unmounted (`tune2fs -O quota,project <device>`) and recreate the gateway", dir)
		case strings.HasSuffix(out, "NOSETQUOTA"):
			return fmt.Errorf("%s has no setquota; install the quota package on the gateway nodes", rm.nodeLabel(h))
		}
		seen = append(seen, rm.nodeLabel(h))
	}
	return fmt.Errorf("%s is not mounted on any of %s; is the NFS gateway of %s running?", dir, strings.Join(seen, ", "), resource)
}
