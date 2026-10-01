import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ResourceProfile } from '../../services/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { toast } from 'sonner';
import { Plus, Trash2, Loader2 } from 'lucide-react';
import { type NodeOpt, type PoolOpt } from './types';

export function CreateResourceDialog({
  open,
  onOpenChange,
  nodes,
  pools,
  profiles,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  nodes: NodeOpt[];
  pools: PoolOpt[];
  profiles: ResourceProfile[];
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [port, setPort] = useState('7000');
  const [protocol, setProtocol] = useState('C');
  const [selectedNodes, setSelectedNodes] = useState<string[]>([]);
  const [storageType, setStorageType] = useState('lvm');
  const [profile, setProfile] = useState('');
  const [labelsInput, setLabelsInput] = useState('');
  // One or more DRBD volumes (volume 0..N). Each has its own size and pool.
  const [volumes, setVolumes] = useState<{ sizeGb: string; pool: string }[]>([
    { sizeGb: '10', pool: '' },
  ]);
  // DRBD options as section/key -> value rows (net/disk/options), optional.
  const [optionRows, setOptionRows] = useState<{ key: string; value: string }[]>(
    [],
  );
  const [showOptions, setShowOptions] = useState(false);

  const noneValue = '__none__';

  const setVolume = (i: number, patch: Partial<{ sizeGb: string; pool: string }>) =>
    setVolumes((prev) =>
      prev.map((v, idx) => (idx === i ? { ...v, ...patch } : v)),
    );
  const addVolume = () =>
    setVolumes((prev) => [...prev, { sizeGb: '10', pool: '' }]);
  const removeVolume = (i: number) =>
    setVolumes((prev) => prev.filter((_, idx) => idx !== i));

  const setOptionRow = (
    i: number,
    patch: Partial<{ key: string; value: string }>,
  ) =>
    setOptionRows((prev) =>
      prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)),
    );
  const addOptionRow = () =>
    setOptionRows((prev) => [...prev, { key: '', value: '' }]);
  const removeOptionRow = (i: number) =>
    setOptionRows((prev) => prev.filter((_, idx) => idx !== i));

  // Pool types as the backend reports them, per storage type.
  const poolTypeFor: Record<string, string> = {
    lvm: 'vg',
    'lvm-thin': 'thin_pool',
    zfs: 'zfs',
  };
  const matchingPools = pools.filter(
    (p) => p.type === poolTypeFor[storageType],
  );
  const selectedProfile = profiles.find((item) => item.name === profile);

  const reset = () => {
    setName('');
    setPort('7000');
    setProtocol('C');
    setSelectedNodes([]);
    setStorageType('lvm');
    setProfile('');
    setLabelsInput('');
    setVolumes([{ sizeGb: '10', pool: '' }]);
    setOptionRows([]);
    setShowOptions(false);
  };

  const createMutation = useMutation({
    mutationFn: (data: Parameters<typeof api.createResource>[0]) =>
      api.createResource(data),
    onSuccess: () => {
      toast.success(`Resource "${name}" created`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
      reset();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const toggleNode = (nodeName: string) => {
    setSelectedNodes((prev) =>
      prev.includes(nodeName)
        ? prev.filter((n) => n !== nodeName)
        : [...prev, nodeName],
    );
  };

  const selectProfile = (value: string) => {
    if (value === noneValue) {
      setProfile('');
      return;
    }
    setProfile(value);
    const selected = profiles.find((item) => item.name === value);
    if (!selected) return;
    if (selected.protocol) setProtocol(selected.protocol);
    if (selected.storageType) setStorageType(selected.storageType);
    if (selected.pool) {
      setVolumes((prev) => prev.map((volume) => ({ ...volume, pool: selected.pool })));
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (selectedNodes.length < 1) {
      toast.error('Select at least 1 node');
      return;
    }
    const parsedVolumes = volumes.map((v) => ({
      sizeGb: parseInt(v.sizeGb, 10),
      pool: v.pool || undefined,
    }));
    if (parsedVolumes.some((v) => !v.sizeGb || v.sizeGb < 1)) {
      toast.error('Every volume needs a size of at least 1 GB');
      return;
    }
    // Collect non-empty option rows into a section/key -> value map.
    const drbdOptions: Record<string, string> = {};
    for (const r of optionRows) {
      const key = r.key.trim();
      if (key) drbdOptions[key] = r.value.trim();
    }
    const labels: Record<string, string> = {};
    for (const part of labelsInput.split(',')) {
      const entry = part.trim();
      if (!entry) continue;
      const separator = entry.indexOf('=');
      if (separator < 1) {
        toast.error(`Invalid label "${entry}". Use key=value.`);
        return;
      }
      const key = entry.slice(0, separator).trim();
      const value = entry.slice(separator + 1).trim();
      if (!key) {
        toast.error(`Invalid label "${entry}". Label keys cannot be empty.`);
        return;
      }
      labels[key] = value;
    }
    createMutation.mutate({
      name,
      port: parseInt(port, 10),
      nodes: selectedNodes,
      protocol,
      storageType,
      volumes: parsedVolumes,
      drbdOptions: Object.keys(drbdOptions).length ? drbdOptions : undefined,
      profile: profile || undefined,
      labels: Object.keys(labels).length ? labels : undefined,
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Create DRBD Resource</DialogTitle>
            <DialogDescription>
              Define a replicated DRBD resource across one or more nodes.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="res-name">Resource Name</Label>
              <Input
                id="res-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g., data"
                required
              />
            </div>

            <div className="space-y-2">
              <Label>Profile (optional)</Label>
              <Select
                value={profile || noneValue}
                onValueChange={selectProfile}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="No profile" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>No profile</SelectItem>
                  {profiles.map((item) => (
                    <SelectItem key={item.name} value={item.name}>
                      {item.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {profiles.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  No resource profiles are configured.
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="res-labels">Labels (optional)</Label>
              <Input
                id="res-labels"
                value={labelsInput}
                onChange={(e) => setLabelsInput(e.target.value)}
                placeholder="environment=prod, team=storage"
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                Comma-separated key=value pairs. Explicit labels override profile labels.
              </p>
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="res-port">Port</Label>
                <Input
                  id="res-port"
                  type="number"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label>Protocol</Label>
                <Select value={protocol} onValueChange={setProtocol}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="C">C (Sync)</SelectItem>
                    <SelectItem value="A">A (Async)</SelectItem>
                    <SelectItem value="B">B (Semi-sync)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="space-y-2">
              <Label>Nodes</Label>
              <div className="space-y-2 rounded-lg border p-3">
                {nodes.map((node) => (
                  <label
                    key={node.name}
                    className="flex cursor-pointer items-center gap-2 text-sm"
                  >
                    <input
                      type="checkbox"
                      className="h-4 w-4 rounded border-input accent-primary"
                      checked={selectedNodes.includes(node.name)}
                      onChange={() => toggleNode(node.name)}
                    />
                    <span>
                      {node.name}{' '}
                      <span className="text-muted-foreground">
                        ({node.address})
                      </span>
                    </span>
                  </label>
                ))}
                {nodes.length === 0 && (
                  <p className="text-sm text-muted-foreground">
                    No nodes available.
                  </p>
                )}
              </div>
              {selectedNodes.length === 1 && (
                <p className="text-xs text-amber-600 dark:text-amber-400">
                  Only 1 node selected — this resource will have no replication.
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label>Storage Type</Label>
              <Select
                value={storageType}
                onValueChange={(v) => {
                  setStorageType(v);
                  // Pools are type-specific; clear each volume's pick.
                  setVolumes((prev) => prev.map((vol) => ({ ...vol, pool: '' })));
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="lvm">LVM</SelectItem>
                  <SelectItem value="lvm-thin">LVM Thin</SelectItem>
                  <SelectItem value="zfs">ZFS</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {/* Volumes (volume 0..N) */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label>Volumes</Label>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={addVolume}
                >
                  <Plus className="h-4 w-4" />
                  Add volume
                </Button>
              </div>
              <div className="space-y-2 rounded-lg border p-3">
                {volumes.map((vol, i) => {
                  const effectivePool = vol.pool || selectedProfile?.pool || '';
                  return (
                  <div key={i} className="flex items-end gap-2">
                    <div className="w-24 space-y-1">
                      <Label className="text-xs text-muted-foreground">
                        {i === 0 ? 'Size (GB)' : `Vol ${i} · GB`}
                      </Label>
                      <Input
                        type="number"
                        min={1}
                        value={vol.sizeGb}
                        onChange={(e) => setVolume(i, { sizeGb: e.target.value })}
                        required
                      />
                    </div>
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs text-muted-foreground">
                        Pool (optional)
                      </Label>
                      <Select
                        value={effectivePool || noneValue}
                        onValueChange={(v) =>
                          setVolume(i, { pool: v === noneValue ? '' : v })
                        }
                      >
                        <SelectTrigger className="w-full">
                          <SelectValue placeholder="Auto-select" />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value={noneValue}>Auto-select</SelectItem>
                          {effectivePool && !matchingPools.some((p) => p.name === effectivePool) && (
                            <SelectItem value={effectivePool}>
                              {effectivePool} (profile)
                            </SelectItem>
                          )}
                          {matchingPools.map((p) => (
                            <SelectItem
                              key={`${p.node}-${p.name}`}
                              value={p.name}
                            >
                              {p.name} ({p.node}) - {p.freeGb}GB free
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-9 w-9 shrink-0 text-muted-foreground hover:text-destructive"
                      onClick={() => removeVolume(i)}
                      disabled={volumes.length === 1}
                      title="Remove volume"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                  );
                })}
              </div>
            </div>

            {/* DRBD options (advanced) */}
            <div className="space-y-2">
              <button
                type="button"
                className="text-sm font-medium text-muted-foreground hover:text-foreground"
                onClick={() => setShowOptions((s) => !s)}
              >
                {showOptions ? '▾' : '▸'} DRBD Options (advanced)
              </button>
              {showOptions && (
                <div className="space-y-2 rounded-lg border p-3">
                  <p className="text-xs text-muted-foreground">
                    Keys are <code className="font-mono">section/key</code> —
                    e.g. <code className="font-mono">net/max-buffers</code>,{' '}
                    <code className="font-mono">disk/on-io-error</code>,{' '}
                    <code className="font-mono">options/auto-promote</code>. See
                    the DRBD 9 user guide.
                  </p>
                  {optionRows.map((row, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <Input
                        className="flex-1"
                        placeholder="net/max-buffers"
                        value={row.key}
                        onChange={(e) => setOptionRow(i, { key: e.target.value })}
                      />
                      <Input
                        className="flex-1"
                        placeholder="8000"
                        value={row.value}
                        onChange={(e) =>
                          setOptionRow(i, { value: e.target.value })
                        }
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-9 w-9 shrink-0 text-muted-foreground hover:text-destructive"
                        onClick={() => removeOptionRow(i)}
                        title="Remove option"
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  ))}
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={addOptionRow}
                  >
                    <Plus className="h-4 w-4" />
                    Add option
                  </Button>
                </div>
              )}
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={createMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={createMutation.isPending || selectedNodes.length < 1}
            >
              {createMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
