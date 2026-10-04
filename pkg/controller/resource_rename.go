package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/database"
)

// Renaming a resource.
//
// A resource's name is in its DRBD config file name and first line, in its
// backing volumes' names ("<name>_data", "<name>_vol<K>") and in its
// database records. The Proxmox plugin needs to change it: PVE reassigns a
// disk to another VM by renaming the volume, and turns a VM into a template
// by renaming its disks to base-*.
//
// It is done offline: the resource goes down on every participant, the
// backing volumes and the config are renamed, and it comes up under the new
// name, with its data, node ids and port untouched. So it is refused while
// the resource is Primary anywhere, and refused for everything that refers
// to the name from elsewhere — an HA config, a gateway, backups and their
// schedules, a snapshot schedule, existing snapshots (named after the old
// backing volume), WAN replication and encryption (the LUKS container is
// named after the backing volume) — rather than chasing every reference.

var resourceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// RenameResource renames resource to newName.
func (rm *ResourceManager) RenameResource(ctx context.Context, resource, newName string) error {
	if rm.deployment == nil || rm.controller.db == nil {
		return fmt.Errorf("deployment client or database not available")
	}
	if !resourceNameRe.MatchString(newName) {
		return fmt.Errorf("%q is not a valid resource name (letters, digits, _ and -, at most 63)", newName)
	}
	dbRes, vols, err := rm.renamePreconditions(ctx, resource, newName)
	if err != nil {
		return err
	}
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	diskless := rm.disklessParticipantHosts(ctx, resource)
	all := append(append([]string(nil), hosts...), diskless...)
	if err := rm.assertNoSnapshots(ctx, hosts, vols); err != nil {
		return err
	}

	oldPath, newPath := "/etc/drbd.d/"+resource+".res", "/etc/drbd.d/"+newName+".res"
	conf, err := rm.readResourceConfig(ctx, hosts, oldPath, resource)
	if err != nil {
		return err
	}
	renames := make(map[string]string, len(vols))
	for _, v := range vols {
		renames[v.VolumeName] = renamedBacking(v.VolumeName, resource, newName)
	}
	newConf, err := renameInConfig(conf, resource, newName, renames)
	if err != nil {
		return err
	}

	rm.controller.logger.Info("Renaming resource", zap.String("from", resource), zap.String("to", newName))
	if err := rm.execAllSuccess(ctx, all, "sudo drbdadm down "+resource, "take "+resource+" down for the rename"); err != nil {
		return err
	}
	undo := func(renamed []*database.Volume) {
		for _, v := range renamed {
			_, _ = rm.deployment.Exec(ctx, hosts, renameBackingCmd(v, renames[v.VolumeName], v.VolumeName))
		}
		_, _ = rm.deployment.DistributeConfig(ctx, all, conf, oldPath)
		_, _ = rm.deployment.Exec(ctx, all, "sudo rm -f "+newPath+"; sudo drbdadm up "+resource)
	}
	var renamed []*database.Volume
	for _, v := range vols {
		if err := rm.execAllSuccess(ctx, hosts, renameBackingCmd(v, v.VolumeName, renames[v.VolumeName]),
			"rename backing volume "+v.VolumeName); err != nil {
			undo(renamed)
			return err
		}
		renamed = append(renamed, v)
	}
	if _, err := rm.deployment.DistributeConfig(ctx, all, newConf, newPath); err != nil {
		undo(renamed)
		return fmt.Errorf("write the renamed config: %w", err)
	}
	if err := rm.execAllSuccess(ctx, all, "sudo rm -f "+oldPath+" && sudo drbdadm up "+newName,
		"bring "+newName+" up"); err != nil {
		undo(renamed)
		return err
	}

	// The records follow the nodes: from here the resource is newName.
	dbRes.Name = newName
	if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
		return fmt.Errorf("renamed on the nodes, but the database could not record it: %w", err)
	}
	for _, v := range vols {
		oldVol := v.VolumeName
		if err := rm.controller.db.DeleteVolume(ctx, resource, oldVol); err != nil {
			rm.controller.logger.Warn("Rename: could not drop an old volume record", zap.String("volume", oldVol), zap.Error(err))
		}
		v.ResourceName, v.VolumeName = newName, renames[oldVol]
		v.Device = strings.Replace(v.Device, "/"+oldVol, "/"+v.VolumeName, 1)
		if err := rm.controller.db.SaveVolume(ctx, v); err != nil {
			return fmt.Errorf("renamed on the nodes, but volume %s could not be recorded: %w", v.VolumeName, err)
		}
	}
	return rm.controller.db.DeleteResource(ctx, resource)
}

// renamePreconditions loads the resource and refuses what a rename would
// leave pointing at the old name.
func (rm *ResourceManager) renamePreconditions(ctx context.Context, resource, newName string) (*database.Resource, []*database.Volume, error) {
	db := rm.controller.db
	dbRes, err := db.GetResource(ctx, resource)
	if err != nil || dbRes == nil {
		return nil, nil, fmt.Errorf("resource %q not found", resource)
	}
	if existing, err := db.GetResource(ctx, newName); err == nil && existing != nil {
		return nil, nil, fmt.Errorf("resource %q already exists", newName)
	}
	refuse := func(why string) (*database.Resource, []*database.Volume, error) {
		return nil, nil, fmt.Errorf("cannot rename %s: %s", resource, why)
	}
	switch {
	case dbRes.WANMode:
		return refuse("it is WAN-replicated")
	case dbRes.Encrypted:
		return refuse("it is encrypted, and its LUKS containers are named after its backing volumes")
	}
	if ha, err := db.GetHaConfig(ctx, resource); err == nil && ha != nil {
		return refuse("it has an HA config; delete it first")
	}
	if gws, err := db.ListGateways(ctx); err == nil {
		for _, g := range gws {
			if g.Resource == resource {
				return refuse("it has a gateway; delete it first")
			}
		}
	}
	if s, err := db.GetSnapshotSchedule(ctx, resource); err == nil && s != nil {
		return refuse("it has a snapshot schedule")
	}
	if backups, err := db.ListBackups(ctx, resource, ""); err == nil && len(backups) > 0 {
		return refuse("it has backups, which are recorded under its name")
	}
	if info, err := rm.GetResource(ctx, resource); err == nil {
		for node, st := range info.NodeStates {
			if strings.EqualFold(st.Role, "Primary") {
				return refuse("it is Primary on " + node + "; stop what uses it first")
			}
		}
	}
	vols, err := db.ListVolumes(ctx, resource)
	if err != nil {
		return nil, nil, err
	}
	return dbRes, vols, nil
}

// assertNoSnapshots refuses a rename while a backing volume has snapshots:
// they are named after the old backing volume and would be orphaned.
func (rm *ResourceManager) assertNoSnapshots(ctx context.Context, hosts []string, vols []*database.Volume) error {
	for _, v := range vols {
		cmd := fmt.Sprintf("sudo lvs --noheadings -o origin %s 2>/dev/null | grep -qw %s && echo has-snapshots || true", v.Pool, v.VolumeName)
		if strings.HasPrefix(v.Device, "/dev/zvol/") {
			cmd = fmt.Sprintf("sudo zfs list -H -t snapshot -d 1 -o name %s/%s 2>/dev/null | grep -q @ && echo has-snapshots || true", v.Pool, v.VolumeName)
		}
		res, err := rm.deployment.Exec(ctx, hosts, cmd)
		if err != nil {
			return fmt.Errorf("check %s for snapshots: %w", v.VolumeName, err)
		}
		for host, hr := range res.Hosts {
			if strings.Contains(hr.Output, "has-snapshots") {
				return fmt.Errorf("cannot rename: %s has snapshots on %s, which are named after it; delete them first", v.VolumeName, host)
			}
		}
	}
	return nil
}

// renamedBacking is a backing volume's name under the new resource name.
func renamedBacking(backing, oldName, newName string) string {
	if strings.HasPrefix(backing, oldName+"_") {
		return newName + backing[len(oldName):]
	}
	return backing
}

func renameBackingCmd(v *database.Volume, from, to string) string {
	if strings.HasPrefix(v.Device, "/dev/zvol/") {
		return fmt.Sprintf("sudo zfs rename %s/%s %s/%s", v.Pool, from, v.Pool, to)
	}
	return fmt.Sprintf("sudo lvrename %s %s %s", v.Pool, from, to)
}

// renameInConfig renames the resource and its backing devices in its DRBD
// config. Backing paths are matched whole, up to the terminating ';'.
func renameInConfig(conf, oldName, newName string, backings map[string]string) (string, error) {
	header := regexp.MustCompile(`(?m)^(\s*resource\s+)"?` + regexp.QuoteMeta(oldName) + `"?(\s*\{)`)
	if !header.MatchString(conf) {
		return "", fmt.Errorf("the config does not declare resource %s", oldName)
	}
	out := header.ReplaceAllString(conf, "${1}"+newName+"${2}")
	for from, to := range backings {
		out = strings.ReplaceAll(out, "/"+from+";", "/"+to+";")
	}
	return out, nil
}
