import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { api, Resource } from '../../services/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Dialog,
  DialogContent,
  DialogDescription,
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import { type NodeOpt } from './types';

export function MountDialog({
  open,
  onOpenChange,
  resource,
  nodes,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  nodes: NodeOpt[];
}) {
  const multiVolume = resource.volumes.length > 1;
  const [volumeId, setVolumeId] = useState(
    String(resource.volumes[0]?.volumeId ?? 0),
  );

  const [fstype, setFstype] = useState('ext4');
  const [formatNode, setFormatNode] = useState('');
  const [mountPath, setMountPath] = useState('');
  const [mountNode, setMountNode] = useState('');
  const [unmountNode, setUnmountNode] = useState('');

  const vid = parseInt(volumeId, 10) || 0;
  const noneValue = '__none__';

  const formatMutation = useMutation({
    mutationFn: () =>
      api.createFilesystem(
        resource.name,
        vid,
        fstype,
        formatNode || undefined,
      ),
    onSuccess: () => toast.success('Filesystem created'),
    onError: (e: Error) => toast.error(e.message),
  });

  const mountMutation = useMutation({
    mutationFn: () =>
      api.mountResource(
        resource.name,
        vid,
        mountPath,
        undefined,
        mountNode || undefined,
      ),
    onSuccess: () => {
      toast.success(`Mounted ${resource.name} at ${mountPath}`);
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const unmountMutation = useMutation({
    mutationFn: () =>
      api.unmountResource(resource.name, vid, unmountNode || undefined),
    onSuccess: () => {
      toast.success(`Unmounted ${resource.name}`);
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Filesystem / Mount — {resource.name}</DialogTitle>
          <DialogDescription>
            Format, mount, or unmount the resource volume.
          </DialogDescription>
        </DialogHeader>

        {multiVolume && (
          <div className="space-y-2 pt-2">
            <Label>Volume</Label>
            <Select value={volumeId} onValueChange={setVolumeId}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {resource.volumes.map((v) => (
                  <SelectItem key={v.volumeId} value={String(v.volumeId)}>
                    Volume {v.volumeId} ({v.sizeGb} GB)
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        <Tabs defaultValue="format" className="py-2">
          <TabsList>
            <TabsTrigger value="format">Format</TabsTrigger>
            <TabsTrigger value="mount">Mount</TabsTrigger>
            <TabsTrigger value="unmount">Unmount</TabsTrigger>
          </TabsList>

          <TabsContent value="format" className="space-y-4">
            <div className="space-y-2">
              <Label>Filesystem Type</Label>
              <Select value={fstype} onValueChange={setFstype}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ext4">ext4</SelectItem>
                  <SelectItem value="xfs">xfs</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>Node (optional)</Label>
              <Select
                value={formatNode || noneValue}
                onValueChange={(v) =>
                  setFormatNode(v === noneValue ? '' : v)
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder="Auto (Primary)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>Auto (Primary)</SelectItem>
                  {nodes.map((n) => (
                    <SelectItem key={n.name} value={n.name}>
                      {n.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              onClick={() => formatMutation.mutate()}
              disabled={formatMutation.isPending}
            >
              {formatMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Create Filesystem
            </Button>
          </TabsContent>

          <TabsContent value="mount" className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="mount-path">Mount Path</Label>
              <Input
                id="mount-path"
                value={mountPath}
                onChange={(e) => setMountPath(e.target.value)}
                placeholder="/mnt/data"
              />
            </div>
            <div className="space-y-2">
              <Label>Node (optional)</Label>
              <Select
                value={mountNode || noneValue}
                onValueChange={(v) => setMountNode(v === noneValue ? '' : v)}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Auto (Primary)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>Auto (Primary)</SelectItem>
                  {nodes.map((n) => (
                    <SelectItem key={n.name} value={n.name}>
                      {n.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              onClick={() => mountMutation.mutate()}
              disabled={mountMutation.isPending || !mountPath}
            >
              {mountMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Mount
            </Button>
          </TabsContent>

          <TabsContent value="unmount" className="space-y-4">
            <div className="space-y-2">
              <Label>Node (optional)</Label>
              <Select
                value={unmountNode || noneValue}
                onValueChange={(v) =>
                  setUnmountNode(v === noneValue ? '' : v)
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder="Auto (Primary)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>Auto (Primary)</SelectItem>
                  {nodes.map((n) => (
                    <SelectItem key={n.name} value={n.name}>
                      {n.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              variant="destructive"
              onClick={() => unmountMutation.mutate()}
              disabled={unmountMutation.isPending}
            >
              {unmountMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Unmount
            </Button>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}
