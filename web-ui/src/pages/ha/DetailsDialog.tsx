import { HaConfig } from '@/services/api';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog';

// ==================== Details Dialog ====================

export function DetailsDialog({
  config,
  onOpenChange,
}: {
  config: HaConfig | null;
  onOpenChange: (open: boolean) => void;
}) {
  const rows = config
    ? ([
        ['Resource', config.resource, true],
        ['Virtual IP', config.vip, true],
        ['Mount Point', config.mountPoint || '-', true],
        ['Filesystem', config.fsType || '-', false],
      ] as [string, string, boolean][])
    : [];

  return (
    <Dialog open={!!config} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>HA Configuration Details</DialogTitle>
        </DialogHeader>
        <div className="space-y-1 text-[13px]">
          {rows.map(([label, value, mono]) => (
            <div
              key={label}
              className="flex justify-between border-b border-border py-2 last:border-0"
            >
              <span className="text-muted-foreground">{label}</span>
              <span className={mono ? 'font-mono tabular-nums' : 'font-medium'}>
                {value}
              </span>
            </div>
          ))}
          {config?.services && config.services.length > 0 && (
            <div className="pt-3">
              <div className="eyebrow">Services</div>
              <div className="mt-2 flex flex-wrap gap-2">
                {config.services.map((service) => (
                  <span
                    key={service}
                    className="rounded-[5px] border border-border bg-muted px-2 py-1 font-mono text-xs"
                  >
                    {service}
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
