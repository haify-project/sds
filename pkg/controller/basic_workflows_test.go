package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type execCall struct {
	hosts []string
	cmd   string
}

type fakeDeploymentClient struct {
	execFunc                            func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error)
	pvCreateFunc                        func(ctx context.Context, hosts []string, device string, opts ...deployment.LVMOption) (*deployment.ExecResult, error)
	vgCreateFunc                        func(ctx context.Context, hosts []string, vgName string, devices []string) (*deployment.ExecResult, error)
	lvCreateFunc                        func(ctx context.Context, hosts []string, vgName, lvName, size string) (*deployment.ExecResult, error)
	lvCreateThinPoolFunc                func(ctx context.Context, hosts []string, vgName, poolName, size string) (*deployment.ExecResult, error)
	lvCreateThinVolumeFunc              func(ctx context.Context, hosts []string, vgName, poolName, lvName, size string) (*deployment.ExecResult, error)
	lvCreateThinPoolAllFreeFunc         func(ctx context.Context, hosts []string, vgName, poolName string, metadataBytes uint64) (*deployment.ExecResult, error)
	drbdDetachFunc                      func(ctx context.Context, host, resource string) (*deployment.ExecResult, error)
	lvRemoveFunc                        func(ctx context.Context, hosts []string, lvPath string) (*deployment.ExecResult, error)
	lvExistsFunc                        func(ctx context.Context, host, vgName, lvName string) (bool, error)
	lvThinPoolInFunc                    func(ctx context.Context, host, vgName string) (string, error)
	lvExtendThinPoolMetadataFunc        func(ctx context.Context, hosts []string, vgName, poolName string, sizeBytes uint64) (*deployment.ExecResult, error)
	lvExtendThinPoolAllFreeFunc         func(ctx context.Context, hosts []string, vgName, poolName string) (*deployment.ExecResult, error)
	lvsCacheReportFunc                  func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error)
	lvsThinReportFunc                   func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error)
	probeBlockDeviceFunc                func(ctx context.Context, host, device string) (*deployment.ExecResult, error)
	vgExtendFunc                        func(ctx context.Context, hosts []string, vgName, device string) (*deployment.ExecResult, error)
	vgReduceAndRemovePVFunc             func(ctx context.Context, hosts []string, vgName, device string) (*deployment.ExecResult, error)
	lvCreateCacheVolFunc                func(ctx context.Context, hosts []string, vgName, lvName, device string) (*deployment.ExecResult, error)
	lvConvertToCacheFunc                func(ctx context.Context, hosts []string, vgName, lvName, cacheVol, mode string) (*deployment.ExecResult, error)
	lvUncacheFunc                       func(ctx context.Context, hosts []string, vgName, lvName string) (*deployment.ExecResult, error)
	drbdAttachFunc                      func(ctx context.Context, host, resource string) (*deployment.ExecResult, error)
	zfsCreatePoolFunc                   func(ctx context.Context, hosts []string, poolName string, vdevs []string, opts ...deployment.ZFSOption) (*deployment.ExecResult, error)
	zfsDestroyPoolFunc                  func(ctx context.Context, hosts []string, poolName string) (*deployment.ExecResult, error)
	zfsListPoolsFunc                    func(ctx context.Context, hosts []string) (*deployment.ExecResult, error)
	zfsListSnapshotsFunc                func(ctx context.Context, hosts []string, dataset string) (*deployment.ExecResult, error)
	lvListSnapshotsFunc                 func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error)
	lvMergeSnapshotFunc                 func(ctx context.Context, hosts []string, vgName, snapshotName string) (*deployment.ExecResult, error)
	distributeConfigFunc                func(ctx context.Context, hosts []string, content, remotePath string, opts ...deployment.ConfigOption) (*deployment.ConfigResult, error)
	distributeSecretFunc                func(ctx context.Context, hosts []string, content, relPath string) (*deployment.ConfigResult, error)
	drbdCreateMDFunc                    func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error)
	drbdUpFunc                          func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error)
	drbdDownFunc                        func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error)
	drbdPrimaryFunc                     func(ctx context.Context, host, resource string, force bool) (*deployment.HostResult, error)
	drbdSecondaryFunc                   func(ctx context.Context, host, resource string) (*deployment.HostResult, error)
	drbdStatusFunc                      func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error)
	drbdStatusJSONFunc                  func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error)
	reactorReloadFunc                   func(ctx context.Context, hosts []string) (*deployment.ExecResult, error)
	reactorPromoterStatusByResourceFunc func(ctx context.Context, host, resource string) (*deployment.ReactorPromoterStatus, error)
	execCalls                           []execCall
	pvCreateCalls                       []struct {
		hosts  []string
		device string
	}
	vgCreateCalls []struct {
		hosts   []string
		vgName  string
		devices []string
	}
	lvCreateCalls []struct {
		hosts                []string
		vgName, lvName, size string
	}
	lvCreateThinPoolCalls []struct {
		hosts                  []string
		vgName, poolName, size string
	}
	lvCreateThinVolumeCalls []struct {
		hosts                          []string
		vgName, poolName, lvName, size string
	}
	zfsCreatePoolCalls []struct {
		hosts    []string
		poolName string
		vdevs    []string
		optCount int
	}
	zfsDestroyPoolCalls []struct {
		hosts    []string
		poolName string
	}
	distributedConfigs []struct {
		hosts               []string
		content, remotePath string
	}
	distributedSecrets []struct {
		hosts   []string
		content string
		relPath string
	}
	deleteConfigCalls []struct {
		hosts      []string
		remotePath string
	}
	drbdCreateMDCalls []struct {
		hosts    []string
		resource string
		maxPeers int
	}
	drbdDownCalls []struct {
		hosts    []string
		resource string
	}
	drbdUpCalls []struct {
		hosts    []string
		resource string
	}
	drbdStatusCalls []struct {
		hosts    []string
		resource string
	}
	lvRemoveCalls []struct {
		hosts  []string
		lvPath string
	}
}

func cloneStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func successExecResult(hosts []string, output string) *deployment.ExecResult {
	result := &deployment.ExecResult{Hosts: make(map[string]*deployment.HostResult, len(hosts))}
	for _, host := range hosts {
		result.Hosts[host] = &deployment.HostResult{
			Host:    host,
			Success: true,
			Output:  output,
		}
	}
	return result
}

func (f *fakeDeploymentClient) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string, opts ...deployment.ConfigOption) (*deployment.ConfigResult, error) {
	f.distributedConfigs = append(f.distributedConfigs, struct {
		hosts      []string
		content    string
		remotePath string
	}{hosts: cloneStrings(hosts), content: content, remotePath: remotePath})
	if f.distributeConfigFunc != nil {
		return f.distributeConfigFunc(ctx, hosts, content, remotePath, opts...)
	}
	return &deployment.ConfigResult{Success: true, Path: remotePath}, nil
}

func (f *fakeDeploymentClient) DistributeSecret(ctx context.Context, hosts []string, content, relPath string) (*deployment.ConfigResult, error) {
	f.distributedSecrets = append(f.distributedSecrets, struct {
		hosts   []string
		content string
		relPath string
	}{hosts: cloneStrings(hosts), content: content, relPath: relPath})
	if f.distributeSecretFunc != nil {
		return f.distributeSecretFunc(ctx, hosts, content, relPath)
	}
	return &deployment.ConfigResult{Success: true, Path: relPath}, nil
}

func (f *fakeDeploymentClient) DeleteConfig(ctx context.Context, hosts []string, remotePath string) error {
	f.deleteConfigCalls = append(f.deleteConfigCalls, struct {
		hosts      []string
		remotePath string
	}{hosts: cloneStrings(hosts), remotePath: remotePath})
	return nil
}

func (f *fakeDeploymentClient) Exec(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
	f.execCalls = append(f.execCalls, execCall{hosts: cloneStrings(hosts), cmd: cmd})
	if f.execFunc != nil {
		return f.execFunc(ctx, hosts, cmd, opts...)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) PVCreate(ctx context.Context, hosts []string, device string, opts ...deployment.LVMOption) (*deployment.ExecResult, error) {
	f.pvCreateCalls = append(f.pvCreateCalls, struct {
		hosts  []string
		device string
	}{hosts: cloneStrings(hosts), device: device})
	if f.pvCreateFunc != nil {
		return f.pvCreateFunc(ctx, hosts, device, opts...)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) VGCreate(ctx context.Context, hosts []string, vgName string, devices []string) (*deployment.ExecResult, error) {
	f.vgCreateCalls = append(f.vgCreateCalls, struct {
		hosts   []string
		vgName  string
		devices []string
	}{hosts: cloneStrings(hosts), vgName: vgName, devices: cloneStrings(devices)})
	if f.vgCreateFunc != nil {
		return f.vgCreateFunc(ctx, hosts, vgName, devices)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVCreate(ctx context.Context, hosts []string, vgName, lvName, size string) (*deployment.ExecResult, error) {
	f.lvCreateCalls = append(f.lvCreateCalls, struct {
		hosts                []string
		vgName, lvName, size string
	}{hosts: cloneStrings(hosts), vgName: vgName, lvName: lvName, size: size})
	if f.lvCreateFunc != nil {
		return f.lvCreateFunc(ctx, hosts, vgName, lvName, size)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVCreateThinPool(ctx context.Context, hosts []string, vgName, poolName, size string) (*deployment.ExecResult, error) {
	f.lvCreateThinPoolCalls = append(f.lvCreateThinPoolCalls, struct {
		hosts                  []string
		vgName, poolName, size string
	}{hosts: cloneStrings(hosts), vgName: vgName, poolName: poolName, size: size})
	if f.lvCreateThinPoolFunc != nil {
		return f.lvCreateThinPoolFunc(ctx, hosts, vgName, poolName, size)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVCreateThinVolume(ctx context.Context, hosts []string, vgName, poolName, lvName, size string) (*deployment.ExecResult, error) {
	f.lvCreateThinVolumeCalls = append(f.lvCreateThinVolumeCalls, struct {
		hosts                          []string
		vgName, poolName, lvName, size string
	}{hosts: cloneStrings(hosts), vgName: vgName, poolName: poolName, lvName: lvName, size: size})
	if f.lvCreateThinVolumeFunc != nil {
		return f.lvCreateThinVolumeFunc(ctx, hosts, vgName, poolName, lvName, size)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVCreateSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName, size string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVCreateThinSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVIsThin(ctx context.Context, host, vgName, lvName string) (bool, error) {
	return false, nil
}

func (f *fakeDeploymentClient) LVExtendThinPoolMetadata(ctx context.Context, hosts []string, vgName, poolName string, sizeBytes uint64) (*deployment.ExecResult, error) {
	if f.lvExtendThinPoolMetadataFunc != nil {
		return f.lvExtendThinPoolMetadataFunc(ctx, hosts, vgName, poolName, sizeBytes)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVExtendThinPoolAllFree(ctx context.Context, hosts []string, vgName, poolName string) (*deployment.ExecResult, error) {
	if f.lvExtendThinPoolAllFreeFunc != nil {
		return f.lvExtendThinPoolAllFreeFunc(ctx, hosts, vgName, poolName)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVSCacheReport(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
	if f.lvsCacheReportFunc != nil {
		return f.lvsCacheReportFunc(ctx, hosts, vgName)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVSThinReport(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
	if f.lvsThinReportFunc != nil {
		return f.lvsThinReportFunc(ctx, hosts, vgName)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ProbeBlockDevice(ctx context.Context, host, device string) (*deployment.ExecResult, error) {
	if f.probeBlockDeviceFunc != nil {
		return f.probeBlockDeviceFunc(ctx, host, device)
	}
	return successExecResult([]string{host}, ""), nil
}

func (f *fakeDeploymentClient) VGExtend(ctx context.Context, hosts []string, vgName, device string) (*deployment.ExecResult, error) {
	if f.vgExtendFunc != nil {
		return f.vgExtendFunc(ctx, hosts, vgName, device)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) VGReduceAndRemovePV(ctx context.Context, hosts []string, vgName, device string) (*deployment.ExecResult, error) {
	if f.vgReduceAndRemovePVFunc != nil {
		return f.vgReduceAndRemovePVFunc(ctx, hosts, vgName, device)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVCreateCacheVol(ctx context.Context, hosts []string, vgName, lvName, device string) (*deployment.ExecResult, error) {
	if f.lvCreateCacheVolFunc != nil {
		return f.lvCreateCacheVolFunc(ctx, hosts, vgName, lvName, device)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVConvertToCache(ctx context.Context, hosts []string, vgName, lvName, cacheVol, mode string) (*deployment.ExecResult, error) {
	if f.lvConvertToCacheFunc != nil {
		return f.lvConvertToCacheFunc(ctx, hosts, vgName, lvName, cacheVol, mode)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVUncache(ctx context.Context, hosts []string, vgName, lvName string) (*deployment.ExecResult, error) {
	if f.lvUncacheFunc != nil {
		return f.lvUncacheFunc(ctx, hosts, vgName, lvName)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVThinPoolIn(ctx context.Context, host, vgName string) (string, error) {
	if f.lvThinPoolInFunc != nil {
		return f.lvThinPoolInFunc(ctx, host, vgName)
	}
	return "", nil
}

func (f *fakeDeploymentClient) LVExists(ctx context.Context, host, vgName, lvName string) (bool, error) {
	if f.lvExistsFunc != nil {
		return f.lvExistsFunc(ctx, host, vgName, lvName)
	}
	return true, nil
}

func (f *fakeDeploymentClient) LVSizeBytes(ctx context.Context, host, vgName, lvName string) (uint64, error) {
	return 6442450944, nil
}

func (f *fakeDeploymentClient) VGFreeBytes(ctx context.Context, host, vgName string) (uint64, error) {
	return 3 << 30, nil
}

func (f *fakeDeploymentClient) LVCreateThinPoolAllFree(ctx context.Context, hosts []string, vgName, poolName string, metadataBytes uint64) (*deployment.ExecResult, error) {
	if f.lvCreateThinPoolAllFreeFunc != nil {
		return f.lvCreateThinPoolAllFreeFunc(ctx, hosts, vgName, poolName, metadataBytes)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) DRBDDetach(ctx context.Context, host, resource string) (*deployment.ExecResult, error) {
	if f.drbdDetachFunc != nil {
		return f.drbdDetachFunc(ctx, host, resource)
	}
	return successExecResult([]string{host}, ""), nil
}

func (f *fakeDeploymentClient) DRBDAttach(ctx context.Context, host, resource string) (*deployment.ExecResult, error) {
	if f.drbdAttachFunc != nil {
		return f.drbdAttachFunc(ctx, host, resource)
	}
	return successExecResult([]string{host}, ""), nil
}

func (f *fakeDeploymentClient) LVRemoveSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVListSnapshots(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
	if f.lvListSnapshotsFunc != nil {
		return f.lvListSnapshotsFunc(ctx, hosts, vgName)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVMergeSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*deployment.ExecResult, error) {
	if f.lvMergeSnapshotFunc != nil {
		return f.lvMergeSnapshotFunc(ctx, hosts, vgName, snapshotName)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSCreatePool(ctx context.Context, hosts []string, poolName string, vdevs []string, opts ...deployment.ZFSOption) (*deployment.ExecResult, error) {
	f.zfsCreatePoolCalls = append(f.zfsCreatePoolCalls, struct {
		hosts    []string
		poolName string
		vdevs    []string
		optCount int
	}{hosts: cloneStrings(hosts), poolName: poolName, vdevs: cloneStrings(vdevs), optCount: len(opts)})
	if f.zfsCreatePoolFunc != nil {
		return f.zfsCreatePoolFunc(ctx, hosts, poolName, vdevs, opts...)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSDestroyPool(ctx context.Context, hosts []string, poolName string) (*deployment.ExecResult, error) {
	f.zfsDestroyPoolCalls = append(f.zfsDestroyPoolCalls, struct {
		hosts    []string
		poolName string
	}{hosts: cloneStrings(hosts), poolName: poolName})
	if f.zfsDestroyPoolFunc != nil {
		return f.zfsDestroyPoolFunc(ctx, hosts, poolName)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSListPools(ctx context.Context, hosts []string) (*deployment.ExecResult, error) {
	if f.zfsListPoolsFunc != nil {
		return f.zfsListPoolsFunc(ctx, hosts)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSCreateDataset(ctx context.Context, hosts []string, datasetName string, opts ...deployment.ZFSOption) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSCreateThinDataset(ctx context.Context, hosts []string, poolName, datasetName, size string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSDestroyDataset(ctx context.Context, hosts []string, datasetName string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSSnapshot(ctx context.Context, hosts []string, dataset, snapshotName string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSRollback(ctx context.Context, hosts []string, dataset, snapshotName string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSClone(ctx context.Context, hosts []string, snapshot, clonePath string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSListSnapshots(ctx context.Context, hosts []string, dataset string) (*deployment.ExecResult, error) {
	if f.zfsListSnapshotsFunc != nil {
		return f.zfsListSnapshotsFunc(ctx, hosts, dataset)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSDestroySnapshot(ctx context.Context, hosts []string, snapshot string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSSetQuota(ctx context.Context, hosts []string, dataset, quota string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ZFSResizeVolume(ctx context.Context, hosts []string, volumePath, newSize string) (*deployment.ExecResult, error) {
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) DRBDUp(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
	f.drbdUpCalls = append(f.drbdUpCalls, struct {
		hosts    []string
		resource string
	}{hosts: cloneStrings(hosts), resource: resource})
	if f.drbdUpFunc != nil {
		return f.drbdUpFunc(ctx, hosts, resource)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) DRBDDown(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
	f.drbdDownCalls = append(f.drbdDownCalls, struct {
		hosts    []string
		resource string
	}{hosts: cloneStrings(hosts), resource: resource})
	if f.drbdDownFunc != nil {
		return f.drbdDownFunc(ctx, hosts, resource)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) LVRemove(ctx context.Context, hosts []string, lvPath string) (*deployment.ExecResult, error) {
	f.lvRemoveCalls = append(f.lvRemoveCalls, struct {
		hosts  []string
		lvPath string
	}{hosts: cloneStrings(hosts), lvPath: lvPath})
	if f.lvRemoveFunc != nil {
		return f.lvRemoveFunc(ctx, hosts, lvPath)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) DRBDPrimary(ctx context.Context, host, resource string, force bool) (*deployment.HostResult, error) {
	if f.drbdPrimaryFunc != nil {
		return f.drbdPrimaryFunc(ctx, host, resource, force)
	}
	return &deployment.HostResult{Host: host, Success: true}, nil
}

func (f *fakeDeploymentClient) DRBDSecondary(ctx context.Context, host, resource string) (*deployment.HostResult, error) {
	if f.drbdSecondaryFunc != nil {
		return f.drbdSecondaryFunc(ctx, host, resource)
	}
	return &deployment.HostResult{Host: host, Success: true}, nil
}

func (f *fakeDeploymentClient) DRBDCreateMD(ctx context.Context, hosts []string, resource string, maxPeers int) (*deployment.ExecResult, error) {
	f.drbdCreateMDCalls = append(f.drbdCreateMDCalls, struct {
		hosts    []string
		resource string
		maxPeers int
	}{hosts: cloneStrings(hosts), resource: resource, maxPeers: maxPeers})
	if f.drbdCreateMDFunc != nil {
		return f.drbdCreateMDFunc(ctx, hosts, resource)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) DRBDStatus(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
	f.drbdStatusCalls = append(f.drbdStatusCalls, struct {
		hosts    []string
		resource string
	}{hosts: cloneStrings(hosts), resource: resource})
	if f.drbdStatusFunc != nil {
		return f.drbdStatusFunc(ctx, hosts, resource)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) DRBDStatusJSON(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
	if f.drbdStatusJSONFunc != nil {
		return f.drbdStatusJSONFunc(ctx, hosts, resource)
	}
	// No JSON status by default: an empty output makes parseNodeStatesFromJSON
	// return an error so the caller keeps its text-parsed states.
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ReactorReload(ctx context.Context, hosts []string) (*deployment.ExecResult, error) {
	if f.reactorReloadFunc != nil {
		return f.reactorReloadFunc(ctx, hosts)
	}
	return successExecResult(hosts, ""), nil
}

func (f *fakeDeploymentClient) ReactorPromoterStatusByResource(ctx context.Context, host, resource string) (*deployment.ReactorPromoterStatus, error) {
	if f.reactorPromoterStatusByResourceFunc != nil {
		return f.reactorPromoterStatusByResourceFunc(ctx, host, resource)
	}
	return nil, assert.AnError
}

func (f *fakeDeploymentClient) ReactorStatusJSON(ctx context.Context, host string) (*deployment.ReactorStatus, error) {
	return nil, assert.AnError
}

func newBasicTestController(dep deploymentClient) *Controller {
	ctrl := &Controller{
		logger:     zap.NewNop(),
		deployment: dep,
		hosts:      []string{},
		hostsMap:   make(map[string]string),
	}
	ctrl.nodes = NewNodeManager(ctrl)
	ctrl.storage = NewStorageManager(ctrl)
	ctrl.resources = NewResourceManager(ctrl)
	ctrl.snapshots = NewSnapshotManager(ctrl)
	ctrl.resources.SetDeployment(dep)
	ctrl.gateway = gateway.New(nil, nil, zap.NewNop(), nil)
	ctrl.schedules = NewScheduleManager(ctrl)
	ctrl.backups = NewBackupManager(ctrl)
	return ctrl
}

func TestNodeManagerRegisterAndUnregister(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if cmd == "hostname" {
				return successExecResult(hosts, "node1.local\n"), nil
			}
			if strings.Contains(cmd, "os-release") {
				return successExecResult(hosts, "PRETTY_NAME=\"TestOS 1.2\"\n"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	node, err := ctrl.nodes.RegisterNode(context.Background(), "node1", "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "node1.local", node.Hostname)
	assert.Equal(t, "TestOS 1.2", node.Version)
	assert.Equal(t, "10.0.0.1", ctrl.ResolveHost("node1"))
	assert.Equal(t, "10.0.0.1", ctrl.ResolveHost("node1.local"))
	assert.Contains(t, ctrl.GetHosts(), "10.0.0.1")
	assert.Equal(t, []string{"10.0.0.1"}, ctrl.gateway.Hosts())

	listed, err := ctrl.nodes.ListNodes(context.Background())
	require.NoError(t, err)
	assert.Len(t, listed, 1)

	byName, err := ctrl.nodes.GetNode(context.Background(), "node1")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", byName.Address)
	byHostname, err := ctrl.nodes.GetNode(context.Background(), "node1.local")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", byHostname.Address)

	require.NoError(t, ctrl.nodes.UnregisterNode(context.Background(), "node1"))
	listed, err = ctrl.nodes.ListNodes(context.Background())
	require.NoError(t, err)
	assert.Empty(t, listed)
	assert.Equal(t, "node1", ctrl.ResolveHost("node1"))
	assert.Equal(t, "node1.local", ctrl.ResolveHost("node1.local"))
	assert.NotContains(t, ctrl.GetHosts(), "10.0.0.1")
	assert.Empty(t, ctrl.gateway.Hosts())
	_, err = ctrl.nodes.GetNode(context.Background(), "10.0.0.1")
	assert.ErrorContains(t, err, "node not found")
}

func TestParseNodeEnvironmentVersion(t *testing.T) {
	assert.Equal(t, "Debian GNU/Linux 12 (bookworm)", parseNodeEnvironmentVersion("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\n"))
	assert.Equal(t, "6.12.0-custom", parseNodeEnvironmentVersion("6.12.0-custom"))
	assert.Equal(t, "unknown", parseNodeEnvironmentVersion(""))
}

func TestParseRoleFromStatusUsesLocalRole(t *testing.T) {
	output := "res1 role:Secondary\n  node1 role:Primary\n"
	assert.Equal(t, "Secondary", parseRoleFromStatus(output))
}

func TestParseRoleFromStatusSkipsVerboseCommandEcho(t *testing.T) {
	// "drbdadm status --verbose" echoes the underlying drbdsetup command on
	// the first line; the local resource line follows it.
	output := "drbdsetup status res1 --verbose\nres1 node-id:0 role:Primary suspended:no force-io-failures:no\n  volume:0 minor:0 disk:UpToDate backing_dev:/dev/vg0/res1_data quorum:yes\n      open:yes blocked:no\n  node2 node-id:1 connection:Connected role:Secondary tls:no congested:no\n    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no\n"
	assert.Equal(t, "Primary", parseRoleFromStatus(output))
}

func TestParseNodeStatesFromVerboseStatusWithCommandEcho(t *testing.T) {
	output := "drbdsetup status res1 --verbose\n" +
		"res1 node-id:0 role:Secondary suspended:no force-io-failures:no\n" +
		"  volume:0 minor:0 disk:UpToDate backing_dev:/dev/vg0/res1_data quorum:yes\n" +
		"      open:no blocked:no\n" +
		"  node2 node-id:1 connection:Connected role:Secondary tls:no congested:no\n" +
		"      ap-in-flight:0 rs-in-flight:0\n" +
		"    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no\n" +
		"  node3 node-id:2 connection:Connected role:Primary tls:no congested:no\n" +
		"      ap-in-flight:0 rs-in-flight:0\n" +
		"    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no\n"
	states := parseNodeStatesFromStatus(output, []string{"node1", "node2", "node3"})
	require.Len(t, states, 3)
	assert.Equal(t, "Secondary", states["node1"].Role)
	assert.Equal(t, "UpToDate", states["node1"].DiskState)
	assert.Equal(t, "Secondary", states["node2"].Role)
	assert.Equal(t, "UpToDate", states["node2"].DiskState)
	assert.Equal(t, "Primary", states["node3"].Role)
	assert.Equal(t, "UpToDate", states["node3"].DiskState)
}

func TestParseNodeStatesFromVerboseStatus(t *testing.T) {
	output := "res1 role:Primary suspended:no\n  volume:0 minor:1 disk:UpToDate\n  node2 connection:Connected role:Secondary\n    volume:0 replication:Established peer-disk:UpToDate peer-client:no\n"
	states := parseNodeStatesFromStatus(output, []string{"node1", "node2"})
	require.Len(t, states, 2)
	assert.Equal(t, "Primary", states["node1"].Role)
	assert.Equal(t, "UpToDate", states["node1"].DiskState)
	assert.Equal(t, "Secondary", states["node2"].Role)
	assert.Equal(t, "UpToDate", states["node2"].DiskState)
}

func TestParseNodeStatesIgnoresDisklessTiebreakerPeer(t *testing.T) {
	// Real output from the diskful Primary of a 2-diskful + 1-tiebreaker
	// resource. The tiebreaker (orange3) is NOT in nodeAddresses (it lives in
	// DisklessNodes), so its "peer-disk:Diskless" line must not be attributed
	// to the preceding diskful node (orange2).
	output := "data role:Primary\n" +
		"  disk:UpToDate open:no\n" +
		"  orange2 role:Secondary\n" +
		"    peer-disk:UpToDate\n" +
		"  orange3 role:Secondary\n" +
		"    peer-disk:Diskless peer-client:yes\n"
	states := parseNodeStatesFromStatus(output, []string{"orange1", "orange2"})
	require.Len(t, states, 2)
	assert.Equal(t, "UpToDate", states["orange1"].DiskState)
	assert.Equal(t, "Secondary", states["orange2"].Role)
	// Must stay UpToDate — the tiebreaker's Diskless must not bleed onto orange2.
	assert.Equal(t, "UpToDate", states["orange2"].DiskState)
}

func TestParseVolumesFromVerboseStatus(t *testing.T) {
	output := "res1 role:Primary suspended:no\n  volume:0 minor:7 disk:UpToDate size:6291456\n  volume:1 minor:8 disk:UpToDate size:1048576\n  node2 connection:Connected role:Secondary\n    volume:0 replication:Established peer-disk:UpToDate\n"
	volumes := parseVolumesFromStatus(output)
	require.Len(t, volumes, 2)
	assert.Equal(t, 0, volumes[0].id)
	assert.Equal(t, "/dev/drbd7", volumes[0].device)
	assert.Equal(t, 1, volumes[1].id)
	assert.Equal(t, "/dev/drbd8", volumes[1].device)
}

func TestNodeHealthCheckUsesAddressForKnownNode(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			switch {
			case strings.Contains(cmd, "drbdadm --version"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "DRBDADM_VERSION=9.2.12\n"), nil
			case strings.Contains(cmd, "drbd-reactor --version"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "drbd-reactor 1.8.0\n"), nil
			case strings.Contains(cmd, "systemctl is-active drbd-reactor"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "active\n"), nil
			case strings.Contains(cmd, "find /usr/lib/ocf/resource.d"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "Filesystem\nIPaddr2\n"), nil
			default:
				return successExecResult(hosts, ""), nil
			}
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1", Hostname: "node1.local"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node1.local"] = "10.0.0.1"

	health, err := ctrl.nodes.HealthCheck(context.Background(), "node1")
	require.NoError(t, err)
	assert.True(t, health.DrbdInstalled)
	assert.Equal(t, "9.2.12", health.DrbdVersion)
	assert.True(t, health.DrbdReactorInstalled)
	assert.Equal(t, "1.8.0", health.DrbdReactorVersion)
	assert.True(t, health.DrbdReactorRunning)
	assert.Equal(t, []string{"Filesystem", "IPaddr2"}, health.AvailableAgents)
}

func TestStorageManagerGetPoolResolvesNodeAndParsesOutput(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			require.Equal(t, []string{"10.0.0.1"}, hosts)
			require.Contains(t, cmd, "vgs")
			return successExecResult(hosts, "sds_data-pool|10737418240B|5368709120B\n"), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1", Hostname: "node1.local"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	pool, err := ctrl.storage.GetPool(context.Background(), "data-pool", "node1")
	require.NoError(t, err)
	assert.Equal(t, "sds_data-pool", pool.Name)
	assert.Equal(t, uint64(10), pool.TotalGB)
	assert.Equal(t, uint64(5), pool.FreeGB)
}

func TestStorageManagerAddAndDeletePoolUseNormalizedName(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	err := ctrl.storage.AddDiskToPool(context.Background(), "data-pool", "/dev/sdb", "node1")
	require.NoError(t, err)
	require.Len(t, dep.pvCreateCalls, 1)
	assert.Equal(t, []string{"10.0.0.1"}, dep.pvCreateCalls[0].hosts)
	require.NotEmpty(t, dep.execCalls)
	assert.Equal(t, "sudo vgextend sds_data-pool /dev/sdb", dep.execCalls[len(dep.execCalls)-1].cmd)

	err = ctrl.storage.DeletePool(context.Background(), "data-pool", "node1")
	require.NoError(t, err)
	assert.Equal(t, "sudo vgremove -f sds_data-pool", dep.execCalls[len(dep.execCalls)-1].cmd)
}

func TestStorageManagerCreatePoolPersistsDatabaseState(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	err = ctrl.storage.CreatePool(context.Background(), "data-pool", "lvm-thin", "node1", []string{"/dev/sdb", "/dev/sdc"}, 100)
	require.NoError(t, err)

	stored, err := ctrl.db.GetPool(context.Background(), "sds_data-pool")
	require.NoError(t, err)
	assert.Equal(t, "thin_pool", stored.Type)
	assert.Equal(t, "node1", stored.Node)
	assert.Equal(t, "/dev/sdb,/dev/sdc", stored.Devices)
	require.Len(t, dep.lvCreateThinPoolCalls, 1)
	assert.Equal(t, "sds_data-pool", dep.lvCreateThinPoolCalls[0].vgName)
}

func TestStorageManagerCreateZFSPoolPersistsThinState(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	err = ctrl.storage.CreateZFSPool(context.Background(), "tank", "node1", []string{"/dev/nvme0n1"})
	require.NoError(t, err)

	require.Len(t, dep.zfsCreatePoolCalls, 1)
	assert.Equal(t, []string{"10.0.0.1"}, dep.zfsCreatePoolCalls[0].hosts)
	assert.Equal(t, "sds_tank", dep.zfsCreatePoolCalls[0].poolName)
	assert.Equal(t, []string{"/dev/nvme0n1"}, dep.zfsCreatePoolCalls[0].vdevs)
	assert.Equal(t, 0, dep.zfsCreatePoolCalls[0].optCount)

	stored, err := ctrl.db.GetPool(context.Background(), "sds_tank")
	require.NoError(t, err)
	assert.Equal(t, "zfs", stored.Type)
	assert.Equal(t, "node1", stored.Node)
	assert.Equal(t, "/dev/nvme0n1", stored.Devices)
}

func TestStorageManagerAddDiskUpdatesPersistedPoolDevices(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{
		Name:    "sds_data-pool",
		Type:    "vg",
		Node:    "node1",
		Devices: "/dev/sdb",
	}))

	err = ctrl.storage.AddDiskToPool(context.Background(), "data-pool", "/dev/sdc", "node1")
	require.NoError(t, err)

	stored, err := ctrl.db.GetPool(context.Background(), "sds_data-pool")
	require.NoError(t, err)
	assert.Equal(t, "/dev/sdb,/dev/sdc", stored.Devices)
}

func TestStorageManagerDeletePoolRemovesPersistedState(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{
		Name: "sds_data-pool",
		Type: "vg",
		Node: "node1",
	}))

	err = ctrl.storage.DeletePool(context.Background(), "data-pool", "node1")
	require.NoError(t, err)
	_, err = ctrl.db.GetPool(context.Background(), "sds_data-pool")
	assert.ErrorContains(t, err, "not found")
}

func TestStorageManagerGetPoolFallsBackToDatabase(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return nil, assert.AnError
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{
		Name:    "sds_data-pool",
		Type:    "vg",
		Node:    "node1",
		TotalGB: 123,
		FreeGB:  45,
		Devices: "/dev/sdb,/dev/sdc",
	}))

	pool, err := ctrl.storage.GetPool(context.Background(), "data-pool", "node1")
	require.NoError(t, err)
	assert.Equal(t, "sds_data-pool", pool.Name)
	assert.Equal(t, uint64(123), pool.TotalGB)
	assert.Equal(t, uint64(45), pool.FreeGB)
	assert.Equal(t, []string{"/dev/sdb", "/dev/sdc"}, pool.Devices)
}

func TestStorageManagerGetPoolFallsBackToZFS(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			require.Equal(t, []string{"10.0.0.1"}, hosts)
			switch {
			case strings.Contains(cmd, "vgs"):
				return successExecResult(hosts, ""), nil
			case strings.Contains(cmd, "zpool list -Hp -o name,size,free,cap sds_tank"):
				return successExecResult(hosts, "sds_tank\t21474836480\t10737418240\t50%\n"), nil
			default:
				t.Fatalf("unexpected command: %s", cmd)
				return nil, nil
			}
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	pool, err := ctrl.storage.GetPool(context.Background(), "tank", "node1")
	require.NoError(t, err)
	assert.Equal(t, "sds_tank", pool.Name)
	assert.Equal(t, "zfs", pool.Type)
	assert.Equal(t, uint64(20), pool.TotalGB)
	assert.Equal(t, uint64(10), pool.FreeGB)
}

func TestStorageManagerDeletePoolUsesZFSPathFromPersistedType(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{
		Name: "sds_tank",
		Type: "zfs",
		Node: "node1",
	}))

	err = ctrl.storage.DeletePool(context.Background(), "tank", "node1")
	require.NoError(t, err)
	require.Len(t, dep.zfsDestroyPoolCalls, 1)
	assert.Equal(t, []string{"10.0.0.1"}, dep.zfsDestroyPoolCalls[0].hosts)
	assert.Equal(t, "sds_tank", dep.zfsDestroyPoolCalls[0].poolName)
	assert.Empty(t, dep.execCalls)

	_, err = ctrl.db.GetPool(context.Background(), "sds_tank")
	assert.ErrorContains(t, err, "not found")
}

func TestStorageManagerDeletePoolFallsBackToZFS(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			require.Equal(t, []string{"10.0.0.1"}, hosts)
			require.Equal(t, "sudo vgremove -f sds_tank", cmd)
			return nil, assert.AnError
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	err := ctrl.storage.DeletePool(context.Background(), "tank", "node1")
	require.NoError(t, err)
	require.Len(t, dep.execCalls, 1)
	require.Len(t, dep.zfsDestroyPoolCalls, 1)
	assert.Equal(t, "sds_tank", dep.zfsDestroyPoolCalls[0].poolName)
}

func TestResourceManagerCreateResourceUsesNormalizedPool(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	err := ctrl.resources.CreateResource(context.Background(), "res1", 7001, []string{"node1", "node2"}, "", 10, "data-pool", "lvm", nil)
	require.NoError(t, err)
	require.Len(t, dep.lvCreateCalls, 2)
	assert.Equal(t, "sds_data-pool", dep.lvCreateCalls[0].vgName)
	assert.Equal(t, "res1_data", dep.lvCreateCalls[0].lvName)
	assert.Equal(t, []string{"10.0.0.1"}, dep.lvCreateCalls[0].hosts)
	require.Len(t, dep.distributedConfigs, 1)
	assert.Contains(t, dep.distributedConfigs[0].content, "/dev/sds_data-pool/res1_data")
	assert.Contains(t, dep.distributedConfigs[0].remotePath, "/etc/drbd.d/res1.res")
}

func TestResourceManagerCreateResourcePersistsInitialVolume(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	err = ctrl.resources.CreateResourceWithVolumesMetadata(context.Background(), "res1", 7001, []string{"node1", "node2"}, "", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}}, nil, ResourceMetadata{
			Profile: "production",
			Labels:  map[string]string{"app": "postgres"},
		})
	require.NoError(t, err)
	resource, err := ctrl.db.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	assert.Equal(t, "production", resource.Profile)
	assert.Equal(t, map[string]string{"app": "postgres"}, resource.Labels)

	volumes, err := ctrl.db.ListVolumes(context.Background(), "res1")
	require.NoError(t, err)
	require.Len(t, volumes, 1)
	assert.Equal(t, "res1_data", volumes[0].VolumeName)
	assert.Equal(t, 0, volumes[0].VolumeID)
	assert.Equal(t, "sds_data-pool", volumes[0].Pool)
	assert.Equal(t, 10, volumes[0].SizeGB)
	assert.Equal(t, "/dev/sds_data-pool/res1_data", volumes[0].Device)
}

func TestResourceManagerCreateResourceEnablesDRBDBootUnit(t *testing.T) {
	// After a successful create the native sds-drbd-up.service oneshot must be
	// installed AND enabled on every diskful node, so a rebooted node re-runs
	// `drbdadm adjust all` and rejoins replication without a manual `drbdadm
	// adjust`. We must NOT rely on the packaged drbd.service: it is an LSB unit
	// that cannot be enabled (empty Default-Start).
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	err := ctrl.resources.CreateResource(context.Background(), "res1", 7001, []string{"node1", "node2"}, "", 10, "data-pool", "lvm", nil)
	require.NoError(t, err)

	var installHosts []string
	var installCmd string
	for _, call := range dep.execCalls {
		// The single install command both writes the unit file to
		// /etc/systemd/system/sds-drbd-up.service and enables it.
		if strings.Contains(call.cmd, "/etc/systemd/system/sds-drbd-up.service") &&
			strings.Contains(call.cmd, "systemctl enable sds-drbd-up.service") {
			installHosts = call.hosts
			installCmd = call.cmd
			break
		}
	}
	require.NotNil(t, installHosts,
		"expected an exec call that writes and enables sds-drbd-up.service; got %+v", dep.execCalls)
	// The boot bring-up must install the helper script, activate LVM first
	// (so backing devices exist), and adjust each resource INDEPENDENTLY.
	// It must not use `up all`/`adjust all`, which abort on a foreign resource
	// that is already up ("minor exists") and leave the rest Diskless.
	assert.Contains(t, installCmd, drbdBootScriptPath)
	assert.Contains(t, installCmd, "vgchange -ay")
	assert.Contains(t, installCmd, `adjust "$res"`)
	assert.NotContains(t, installCmd, "adjust all")
	assert.NotContains(t, installCmd, "up all\n")
	// The un-enableable LSB drbd.service must never be the mechanism.
	for _, call := range dep.execCalls {
		assert.NotContains(t, call.cmd, "systemctl enable drbd.service",
			"must not attempt to enable the un-enableable LSB drbd.service")
	}
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, installHosts,
		"boot unit must be installed and enabled on both diskful nodes")
}

func TestResourceManagerCreateResourceSucceedsWhenBootUnitEnableFails(t *testing.T) {
	// Installing/enabling the boot unit is best-effort: a failure must never
	// fail resource creation, since the resource is already up at that point.
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "sds-drbd-up.service") {
				return nil, context.DeadlineExceeded
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	err := ctrl.resources.CreateResource(context.Background(), "res1", 7001, []string{"node1", "node2"}, "", 10, "data-pool", "lvm", nil)
	require.NoError(t, err, "boot-unit enable failure must not fail resource create")
}

func TestResourceManagerDeleteResourceRemovesDatabaseRecord(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	require.NoError(t, ctrl.db.SaveVolume(context.Background(), &database.Volume{
		ResourceName: "res1",
		VolumeName:   "res1_data",
		VolumeID:     0,
		Pool:         "sds_data-pool",
		SizeGB:       10,
		Device:       "/dev/sds_data-pool/res1_data",
	}))

	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	ctrl.resources.mu.Lock()
	ctrl.resources.hosts = []string{"wrong-host"}
	ctrl.resources.mu.Unlock()

	err = ctrl.resources.DeleteResource(context.Background(), "res1", true)
	require.NoError(t, err)
	require.Len(t, dep.drbdDownCalls, 1)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, dep.drbdDownCalls[0].hosts)
	require.Len(t, dep.deleteConfigCalls, 1)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, dep.deleteConfigCalls[0].hosts)
	_, err = ctrl.db.GetResource(context.Background(), "res1")
	assert.ErrorContains(t, err, "not found")
	volumes, err := ctrl.db.ListVolumes(context.Background(), "res1")
	require.NoError(t, err)
	assert.Empty(t, volumes)

	// The backing LV must be removed on all resource hosts — the database
	// records were the last knowledge of which LVs belonged to the resource.
	foundLvremove := false
	for _, call := range dep.execCalls {
		if strings.Contains(call.cmd, "lvremove -f sds_data-pool/res1_data") {
			foundLvremove = true
			assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, call.hosts)
		}
	}
	assert.True(t, foundLvremove, "backing LV was not removed")
}

func TestResourceManagerGetResourceUsesResourceSpecificHosts(t *testing.T) {
	dep := &fakeDeploymentClient{
		drbdStatusFunc: func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
			return successExecResult(hosts, "resource role:Primary\n"), nil
		},
	}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))

	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	ctrl.resources.mu.Lock()
	ctrl.resources.hosts = []string{"wrong-host"}
	ctrl.resources.mu.Unlock()

	_, err = ctrl.resources.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	require.Len(t, dep.drbdStatusCalls, 1)
	assert.Equal(t, []string{"10.0.0.1"}, dep.drbdStatusCalls[0].hosts)
}

func TestResourceManagerGetResourceFallsBackToPersistedVolumes(t *testing.T) {
	dep := &fakeDeploymentClient{
		drbdStatusFunc: func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	require.NoError(t, ctrl.db.SaveVolume(context.Background(), &database.Volume{
		ResourceName: "res1",
		VolumeName:   "res1_data",
		VolumeID:     0,
		Pool:         "sds_data-pool",
		SizeGB:       10,
		Device:       "/dev/sds_data-pool/res1_data",
	}))

	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	info, err := ctrl.resources.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	require.Len(t, info.Volumes, 1)
	assert.Equal(t, uint32(0), info.Volumes[0].VolumeID)
	assert.Equal(t, uint64(10), info.Volumes[0].SizeGB)
	assert.Equal(t, "/dev/drbd/by-res/res1/0", info.Volumes[0].Device)
}

func TestResourceManagerAddVolumePersistsMetadata(t *testing.T) {
	config := "resource res1 {\n    volume 0 {\n        device    minor 1;\n        disk      /dev/sds_data-pool/res1_data;\n        meta-disk internal;\n    }\n}\n"
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/res1.res") {
				return successExecResult(hosts, config), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	err = ctrl.resources.AddVolume(context.Background(), "res1", "res1_logs", "data-pool", 20)
	require.NoError(t, err)
	require.Len(t, dep.distributedConfigs, 1)
	assert.Contains(t, dep.distributedConfigs[0].content, "volume 1 {")
	assert.Contains(t, dep.distributedConfigs[0].content, "disk      /dev/sds_data-pool/res1_logs;")
	var sawAdjust bool
	for _, call := range dep.execCalls {
		if call.cmd == "sudo drbdadm adjust res1" {
			sawAdjust = true
			assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, call.hosts)
		}
	}
	assert.True(t, sawAdjust)
	volumes, err := ctrl.db.ListVolumes(context.Background(), "res1")
	require.NoError(t, err)
	require.Len(t, volumes, 1)
	assert.Equal(t, "res1_logs", volumes[0].VolumeName)
	assert.Equal(t, 1, volumes[0].VolumeID)
	assert.Equal(t, 20, volumes[0].SizeGB)
}

func TestResourceManagerRemoveVolumeUpdatesConfigAndDatabase(t *testing.T) {
	config := "resource res1 {\n    volume 0 {\n        device    minor 1;\n        disk      /dev/sds_data-pool/res1_data;\n        meta-disk internal;\n    }\n\n    volume 1 {\n        device    minor 2;\n        disk      /dev/sds_data-pool/res1_logs;\n        meta-disk internal;\n    }\n}\n"
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/res1.res") {
				return successExecResult(hosts, config), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	require.NoError(t, ctrl.db.SaveVolume(context.Background(), &database.Volume{
		ResourceName: "res1",
		VolumeName:   "res1_logs",
		VolumeID:     1,
		Pool:         "sds_data-pool",
		SizeGB:       20,
		Device:       "/dev/sds_data-pool/res1_logs",
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	err = ctrl.resources.RemoveVolume(context.Background(), "res1", 1)
	require.NoError(t, err)
	require.Len(t, dep.distributedConfigs, 1)
	assert.NotContains(t, dep.distributedConfigs[0].content, "volume 1 {")
	require.NotEmpty(t, dep.execCalls)
	var sawAdjust, sawLVRemove bool
	for _, call := range dep.execCalls {
		if call.cmd == "sudo drbdadm adjust res1" {
			sawAdjust = true
		}
		if strings.Contains(call.cmd, "lvremove -f /dev/sds_data-pool/res1_logs") {
			sawLVRemove = true
		}
	}
	assert.True(t, sawAdjust)
	assert.True(t, sawLVRemove)
	volumes, err := ctrl.db.ListVolumes(context.Background(), "res1")
	require.NoError(t, err)
	assert.Empty(t, volumes)
}

// A failed lvremove on any node must surface as an error, not a silent success
// that leaves an orphaned LV and a lopsided DRBD resource.
func TestResourceManagerRemoveVolumeErrorsWhenBackingRemovalFails(t *testing.T) {
	config := "resource res1 {\n    volume 1 {\n        device    minor 2;\n        disk      /dev/sds_data-pool/res1_logs;\n        meta-disk internal;\n    }\n}\n"
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/res1.res") {
				return successExecResult(hosts, config), nil
			}
			if strings.Contains(cmd, "lvremove") {
				res := successExecResult(hosts, "")
				for _, h := range res.Hosts {
					h.Success = false
					h.Output = "Logical volume is used by another device."
				}
				return res, nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "res1", Port: 7001, Nodes: "node1,node2", Protocol: "C", Replicas: 2,
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	err = ctrl.resources.RemoveVolume(context.Background(), "res1", 1)
	require.Error(t, err, "RemoveVolume must not report success when lvremove fails")
}

func TestResourceManagerResizeVolumeUpdatesBackendAndMetadata(t *testing.T) {
	config := "resource res1 {\n    volume 1 {\n        device    minor 2;\n        disk      /dev/sds_data-pool/res1_logs;\n        meta-disk internal;\n    }\n}\n"
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/res1.res") {
				return successExecResult(hosts, config), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	require.NoError(t, ctrl.db.SaveVolume(context.Background(), &database.Volume{
		ResourceName: "res1",
		VolumeName:   "res1_logs",
		VolumeID:     1,
		Pool:         "sds_data-pool",
		SizeGB:       20,
		Device:       "/dev/sds_data-pool/res1_logs",
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	err = ctrl.resources.ResizeVolume(context.Background(), "res1", 1, 50)
	require.NoError(t, err)
	var sawLVResize, sawDRBDResize bool
	for _, call := range dep.execCalls {
		if call.cmd == "sudo lvresize -L 50G -y /dev/sds_data-pool/res1_logs" {
			sawLVResize = true
		}
		if call.cmd == "sudo drbdadm resize res1/1" {
			sawDRBDResize = true
		}
	}
	assert.True(t, sawLVResize)
	assert.True(t, sawDRBDResize)
	volumes, err := ctrl.db.ListVolumes(context.Background(), "res1")
	require.NoError(t, err)
	require.Len(t, volumes, 1)
	assert.Equal(t, 50, volumes[0].SizeGB)
}

func TestResourceManagerMakeHaUsesResourceNodesOnly(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			switch {
			case strings.Contains(cmd, "drbdadm status"):
				return successExecResult(hosts, "res1 role:Primary\n"), nil
			default:
				return successExecResult(hosts, ""), nil
			}
		},
	}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"
	ctrl.resources.mu.Lock()
	ctrl.resources.hosts = []string{"wrong-host"}
	ctrl.resources.mu.Unlock()

	configPath, err := ctrl.resources.MakeHa(context.Background(), "res1", nil, "", "", "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "/etc/drbd-reactor.d/sds-ha-res1.toml", configPath)
	require.Len(t, dep.distributedConfigs, 1)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, dep.distributedConfigs[0].hosts)
}

func TestResourceManagerCreateFilesystemOnlySupportsBtrfs(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			require.Equal(t, []string{"10.0.0.1"}, hosts)
			require.Equal(t, "sudo mkfs.btrfs -f /dev/drbd/by-res/res1/0", cmd)
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	err := ctrl.resources.CreateFilesystemOnly(context.Background(), "res1", 0, "btrfs", "node1")
	require.NoError(t, err)
	require.Len(t, dep.execCalls, 1)
}

func TestResourceManagerRemoveHaUsesResourceNodesOnly(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	require.NoError(t, ctrl.db.SaveHaConfig(context.Background(), &database.HaConfig{
		Resource: "res1",
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"
	ctrl.resources.mu.Lock()
	ctrl.resources.hosts = []string{"wrong-host"}
	ctrl.resources.mu.Unlock()

	err = ctrl.resources.RemoveHa(context.Background(), "res1")
	require.NoError(t, err)
	require.Len(t, dep.deleteConfigCalls, 1)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, dep.deleteConfigCalls[0].hosts)
	_, err = ctrl.db.GetHaConfig(context.Background(), "res1")
	assert.ErrorContains(t, err, "not found")
}

func TestResourceManagerRemoveHaStopsVIP(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	require.NoError(t, ctrl.db.SaveHaConfig(context.Background(), &database.HaConfig{
		Resource: "res1",
		VIP:      "192.168.1.50/24",
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	require.NoError(t, ctrl.resources.RemoveHa(context.Background(), "res1"))

	var sawStop bool
	for _, call := range dep.execCalls {
		if call.cmd == "systemctl stop service-ip@192.168.1.50-24.service" {
			sawStop = true
			assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, call.hosts)
		}
	}
	assert.True(t, sawStop, "RemoveHa must explicitly stop the VIP service-ip unit; exec calls: %+v", dep.execCalls)
}

func TestSelfHaDisableScriptStopsVIP(t *testing.T) {
	script := generateSelfHaDisableScript([]string{"10.0.0.2"}, "10.0.0.1", "10.0.0.1", "192.168.1.50/24")
	assert.Contains(t, script, "systemctl stop service-ip@192.168.1.50-24.service",
		"self-HA disable script must explicitly stop the VIP service-ip unit")

	// A missing VIP (disable retry) must not emit a bogus stop command.
	scriptNoVIP := generateSelfHaDisableScript([]string{"10.0.0.2"}, "10.0.0.1", "10.0.0.1", "")
	assert.NotContains(t, scriptNoVIP, "stop service-ip@")
}

func TestVipServiceIPInstance(t *testing.T) {
	assert.Equal(t, "192.168.1.50-24", vipServiceIPInstance("192.168.1.50/24"))
	assert.Equal(t, "192.168.1.50-32", vipServiceIPInstance("192.168.1.50"))
	assert.Equal(t, "", vipServiceIPInstance(""))
	assert.Equal(t, "", vipServiceIPInstance("  "))
}

func TestResourceManagerEvictHaUsesResourceNodesOnly(t *testing.T) {
	dep := &fakeDeploymentClient{
		reactorPromoterStatusByResourceFunc: func(ctx context.Context, host, resource string) (*deployment.ReactorPromoterStatus, error) {
			if host == "10.0.0.1" {
				return &deployment.ReactorPromoterStatus{
					DRBDResource: resource,
					PrimaryOn:    "node1",
					Status:       "active",
				}, nil
			}
			return nil, assert.AnError
		},
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			switch {
			case strings.HasPrefix(cmd, "sudo drbd-reactorctl evict sds-ha-res1"):
				return successExecResult(hosts, ""), nil
			default:
				return successExecResult(hosts, ""), nil
			}
		},
	}
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"
	ctrl.resources.mu.Lock()
	ctrl.resources.hosts = []string{"wrong-host"}
	ctrl.resources.mu.Unlock()

	err = ctrl.resources.EvictHa(context.Background(), "res1")
	require.NoError(t, err)

	var sawRemoteStatus, sawEvict bool
	for _, call := range dep.execCalls {
		if call.cmd == "drbdadm status res1" {
			sawRemoteStatus = true
			assert.Equal(t, []string{"10.0.0.1"}, call.hosts)
		}
		if call.cmd == "sudo drbd-reactorctl evict sds-ha-res1" {
			sawEvict = true
			assert.Equal(t, []string{"10.0.0.1"}, call.hosts)
		}
	}
	assert.False(t, sawRemoteStatus)
	assert.True(t, sawEvict)
}
