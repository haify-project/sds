import { useState } from 'react';
import { useQuery, useMutation } from '@tanstack/react-query';
import { api } from '@/services/api';
import { toast } from 'sonner';
import {
  Loader2,
  ChevronDown,
  ChevronRight,
  FileCode,
  RotateCw,
  Save,
} from 'lucide-react';
import { Button } from '@/components/ui/button';

// ==================== drbd-reactor Promoter TOML editor ====================

export function TomlEditorSection({ resource }: { resource: string }) {
  const [open, setOpen] = useState(false);
  const [content, setContent] = useState<string | null>(null);

  // Lazily load the promoter TOML the first time the section is expanded.
  const { data, isFetching, isError, error, refetch } = useQuery({
    queryKey: ['ha-toml', resource],
    queryFn: () => api.getHaToml(resource),
    enabled: open,
  });

  // Seed the editable buffer from the server whenever a fresh copy arrives and
  // the user hasn't started editing yet.
  const serverContent = data?.content ?? '';
  if (open && content === null && data) {
    setContent(serverContent);
  }

  const syncMutation = useMutation({
    mutationFn: (text: string) => api.syncHaToml(resource, text),
    onSuccess: (res) => toast.success(res.message || 'TOML synced'),
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <div className="space-y-2">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 text-[12.5px] font-medium text-muted-foreground hover:text-foreground"
      >
        {open ? (
          <ChevronDown className="h-4 w-4" />
        ) : (
          <ChevronRight className="h-4 w-4" />
        )}
        <FileCode className="h-4 w-4" />
        <span>
          Promoter config <span className="font-mono">{resource}.toml</span>
        </span>
      </button>

      {open && (
        <div className="space-y-2">
          {data?.path && (
            <p className="font-mono text-xs break-all text-muted-foreground">
              {data.path}
            </p>
          )}

          {isFetching && content === null ? (
            <div className="flex items-center gap-2 text-[13px] text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading TOML...
            </div>
          ) : isError ? (
            <p className="text-xs text-destructive">
              Could not load TOML: {(error as Error).message}
            </p>
          ) : (
            <textarea
              value={content ?? ''}
              onChange={(e) => setContent(e.target.value)}
              spellCheck={false}
              rows={12}
              className="w-full rounded-md border border-input bg-transparent p-3 font-mono text-xs outline-none transition-[color,box-shadow] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:bg-input/30"
            />
          )}

          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={
                syncMutation.isPending ||
                content === null ||
                content.trim() === ''
              }
              onClick={() => content !== null && syncMutation.mutate(content)}
            >
              {syncMutation.isPending ? (
                <Loader2 className="mr-1 h-3 w-3 animate-spin" />
              ) : (
                <Save className="mr-1 h-3 w-3" />
              )}
              Sync
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={isFetching}
              onClick={() => {
                setContent(null);
                void refetch();
              }}
            >
              <RotateCw className="mr-1 h-3 w-3" />
              Reload
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
