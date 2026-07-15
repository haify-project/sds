import { useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation } from '@tanstack/react-query';
import { api, HaStartItem, OcfAgentSpec } from '@/services/api';
import { OcfAgentBuilder } from '@/components/OcfAgentBuilder';
import {
  buildPromoterTomlPreviewOrdered,
  mountUnitFor,
  vipUnitFor,
  type PreviewStartItem,
} from '@/lib/toml';
import { toast } from 'sonner';
import { ArrowLeft, GripVertical, Loader2, Plus, Trash2 } from 'lucide-react';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { Separator } from '@/components/ui/separator';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from '@dnd-kit/core';
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';

// One entry in the ordered start[] editor. systemd/mount units (service, mount,
// vip) and OCF resource agents are PEERS in one list — the order is exactly what
// the promoter runs, so a correct stack (e.g. Filesystem -> IPaddr2 -> service)
// is expressible. Each item carries a stable id for drag-and-drop.
type StartItem =
  | { id: string; kind: 'service'; unit: string }
  | { id: string; kind: 'mount'; path: string; fstype: string }
  | { id: string; kind: 'vip'; cidr: string }
  | { id: string; kind: 'ocf'; agent: OcfAgentSpec };

export function CreateHAPage() {
  const navigate = useNavigate();

  const { data: haConfigs } = useQuery({
    queryKey: ['ha'],
    queryFn: () => api.getHaConfigs(),
  });
  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const configs = haConfigs?.configs ?? [];
  const resourcesWithoutHA = (resources?.resources ?? []).filter(
    (r) => !configs.some((ha) => ha.resource === r.name),
  );

  const [resource, setResource] = useState('');
  const [items, setItems] = useState<StartItem[]>([]);

  const idSeq = useRef(0);
  const newId = () => `start-item-${idSeq.current++}`;

  const addItem = (item: StartItem) => setItems((prev) => [...prev, item]);
  const removeItem = (id: string) =>
    setItems((prev) => prev.filter((it) => it.id !== id));
  const patchItem = (id: string, patch: Partial<StartItem>) =>
    setItems((prev) =>
      prev.map((it) => (it.id === id ? ({ ...it, ...patch } as StartItem) : it)),
    );

  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    setItems((prev) => {
      const oldIndex = prev.findIndex((it) => it.id === active.id);
      const newIndex = prev.findIndex((it) => it.id === over.id);
      if (oldIndex < 0 || newIndex < 0) return prev;
      return arrayMove(prev, oldIndex, newIndex);
    });
  };

  // Resolve each editor item to its promoter start[] entry for the live preview.
  const previewItems: PreviewStartItem[] = useMemo(
    () =>
      items.map((it): PreviewStartItem => {
        switch (it.kind) {
          case 'service':
            return { kind: 'unit', unit: it.unit };
          case 'mount':
            return { kind: 'unit', unit: it.path ? mountUnitFor(it.path) : '' };
          case 'vip':
            return { kind: 'unit', unit: it.cidr ? vipUnitFor(it.cidr) : '' };
          case 'ocf':
            return { kind: 'ocf', agent: it.agent };
        }
      }),
    [items],
  );
  const promoterPreview = useMemo(
    () => buildPromoterTomlPreviewOrdered(resource, previewItems),
    [resource, previewItems],
  );

  const mutation = useMutation({
    mutationFn: () => {
      // start_items defines the ordered start[] verbatim. vip/mount are also sent
      // so the backend runs provisioning (service-ip precondition, .mount unit).
      const firstMount = items.find((it) => it.kind === 'mount') as
        | Extract<StartItem, { kind: 'mount' }>
        | undefined;
      const firstVip = items.find((it) => it.kind === 'vip') as
        | Extract<StartItem, { kind: 'vip' }>
        | undefined;

      const startItems: HaStartItem[] = [];
      for (const it of items) {
        if (it.kind === 'ocf') {
          startItems.push({ ocf: it.agent });
        } else {
          const unit =
            it.kind === 'service'
              ? it.unit.trim()
              : it.kind === 'mount'
                ? (it.path ? mountUnitFor(it.path) : '')
                : it.cidr
                  ? vipUnitFor(it.cidr)
                  : '';
          if (unit) startItems.push({ systemdUnit: unit });
        }
      }

      return api.makeHa(resource, {
        vip: firstVip?.cidr || undefined,
        mountPoint: firstMount?.path || undefined,
        fstype: firstMount?.path ? firstMount.fstype : undefined,
        startItems,
      });
    },
    onSuccess: () => {
      toast.success('HA configuration created');
      setItems([]);
      navigate('/ha');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const canSubmit = Boolean(resource) && items.length > 0;

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Button variant="outline" size="icon" onClick={() => navigate('/ha')}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <div>
          <h3 className="text-lg font-semibold">Create HA Configuration</h3>
          <p className="text-sm text-muted-foreground">
            Compose the promoter start sequence. systemd/mount units and OCF
            resource agents are peers — drag to set the exact start order.
          </p>
        </div>
      </div>

      {resourcesWithoutHA.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            No resources available for HA configuration. All resources already
            have HA configured.
          </CardContent>
        </Card>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            mutation.mutate();
          }}
        >
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Resource</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="max-w-md space-y-1.5">
                <Label>DRBD Resource</Label>
                <Select value={resource} onValueChange={setResource}>
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder="Select a resource..." />
                  </SelectTrigger>
                  <SelectContent>
                    {resourcesWithoutHA.map((r) => (
                      <SelectItem key={r.name} value={r.name}>
                        {r.name} ({r.nodes.join(', ')})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>

          <Card className="mt-6">
            <CardHeader>
              <CardTitle className="text-base">Start sequence</CardTitle>
              <p className="text-sm text-muted-foreground">
                Items start top-to-bottom and stop in reverse. A service that
                needs the data and VIP must sit after the Mount and VIP entries.
              </p>
            </CardHeader>
            <CardContent className="space-y-4">
              {items.length > 0 ? (
                <DndContext
                  sensors={sensors}
                  collisionDetection={closestCenter}
                  onDragEnd={handleDragEnd}
                >
                  <SortableContext
                    items={items.map((it) => it.id)}
                    strategy={verticalListSortingStrategy}
                  >
                    <div className="space-y-2">
                      {items.map((it, idx) => (
                        <SortableStartRow
                          key={it.id}
                          index={idx}
                          item={it}
                          onRemove={() => removeItem(it.id)}
                          onPatch={(patch) => patchItem(it.id, patch)}
                        />
                      ))}
                    </div>
                  </SortableContext>
                </DndContext>
              ) : (
                <p className="rounded-md border border-dashed py-6 text-center text-sm text-muted-foreground">
                  Empty — add systemd/mount units and OCF resource agents below.
                </p>
              )}

              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => addItem({ id: newId(), kind: 'service', unit: '' })}
                >
                  <Plus className="mr-1 h-4 w-4" /> Systemd service
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    addItem({ id: newId(), kind: 'mount', path: '', fstype: 'ext4' })
                  }
                >
                  <Plus className="mr-1 h-4 w-4" /> Mount
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => addItem({ id: newId(), kind: 'vip', cidr: '' })}
                >
                  <Plus className="mr-1 h-4 w-4" /> VIP
                </Button>
              </div>

              <Separator />

              <div className="space-y-1.5">
                <Label>Add OCF Resource Agent</Label>
                <OcfAgentBuilder
                  agents={[]}
                  onChange={(arr) => {
                    const spec = arr[arr.length - 1];
                    if (spec) addItem({ id: newId(), kind: 'ocf', agent: spec });
                  }}
                />
              </div>
            </CardContent>
          </Card>

          <Card className="mt-6">
            <CardHeader>
              <CardTitle className="text-base">
                DRBD Reactor Promoter Config (preview)
              </CardTitle>
            </CardHeader>
            <CardContent>
              <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-md border bg-muted p-4 font-mono text-xs leading-relaxed text-muted-foreground">
                {promoterPreview}
              </pre>
            </CardContent>
          </Card>

          <Separator className="my-6" />

          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={() => navigate('/ha')}>
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending || !canSubmit}>
              {mutation.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Create
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}

/**
 * One draggable row in the ordered start sequence. The grip is the only drag
 * target so the inline editors stay interactive. The editor shown depends on the
 * item kind; OCF items are read-only (add via the builder, reorder/remove here).
 */
function SortableStartRow({
  index,
  item,
  onRemove,
  onPatch,
}: {
  index: number;
  item: StartItem;
  onRemove: () => void;
  onPatch: (patch: Partial<StartItem>) => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: item.id });
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.6 : 1,
  };

  const kindLabel =
    item.kind === 'service'
      ? 'systemd'
      : item.kind === 'mount'
        ? 'mount'
        : item.kind === 'vip'
          ? 'vip'
          : 'ocf';

  return (
    <div
      ref={setNodeRef}
      style={style}
      className="flex items-start gap-2 rounded-md border bg-background p-2"
    >
      <button
        type="button"
        className="mt-1 flex h-7 w-6 shrink-0 cursor-grab touch-none items-center justify-center text-muted-foreground hover:text-foreground active:cursor-grabbing"
        title="Drag to reorder"
        aria-label="Drag to reorder"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="h-4 w-4" />
      </button>

      <span className="mt-1 w-6 shrink-0 text-center text-xs text-muted-foreground">
        {index + 1}
      </span>
      <Badge variant="secondary" className="mt-0.5 shrink-0 font-mono">
        {kindLabel}
      </Badge>

      <div className="min-w-0 flex-1">
        {item.kind === 'service' && (
          <Input
            value={item.unit}
            onChange={(e) => onPatch({ unit: e.target.value })}
            placeholder="mysql.service"
            className="font-mono"
          />
        )}
        {item.kind === 'mount' && (
          <div className="flex flex-wrap items-center gap-2">
            <Input
              value={item.path}
              onChange={(e) => onPatch({ path: e.target.value })}
              placeholder="/mnt/data"
              className="min-w-[10rem] flex-1 font-mono"
            />
            <Select
              value={item.fstype}
              onValueChange={(v) => onPatch({ fstype: v })}
            >
              <SelectTrigger className="w-28">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="ext4">ext4</SelectItem>
                <SelectItem value="xfs">xfs</SelectItem>
              </SelectContent>
            </Select>
            {item.path && (
              <span className="font-mono text-xs text-muted-foreground">
                → {mountUnitFor(item.path)}
              </span>
            )}
          </div>
        )}
        {item.kind === 'vip' && (
          <div className="flex flex-wrap items-center gap-2">
            <Input
              value={item.cidr}
              onChange={(e) => onPatch({ cidr: e.target.value })}
              placeholder="192.168.1.100/24"
              className="min-w-[10rem] flex-1 font-mono"
            />
            {item.cidr && vipUnitFor(item.cidr) && (
              <span className="font-mono text-xs text-muted-foreground">
                → {vipUnitFor(item.cidr)}
              </span>
            )}
          </div>
        )}
        {item.kind === 'ocf' && (
          <div>
            <div className="flex items-center gap-2">
              <Badge variant="outline" className="font-mono">
                ocf:{item.agent.provider}:{item.agent.name}
              </Badge>
              <span className="truncate font-mono text-xs text-muted-foreground">
                {item.agent.instance}
              </span>
            </div>
            {Object.keys(item.agent.params).length > 0 && (
              <p className="mt-1 truncate font-mono text-xs text-muted-foreground">
                {Object.entries(item.agent.params)
                  .map(([k, v]) => `${k}=${v}`)
                  .join(' ')}
              </p>
            )}
          </div>
        )}
      </div>

      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="h-7 w-7 shrink-0"
        onClick={onRemove}
        title="Remove"
      >
        <Trash2 className="h-3.5 w-3.5 text-destructive" />
      </Button>
    </div>
  );
}
