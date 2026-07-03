import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  api,
  OcfAgentParameter,
  OcfAgentSpec,
  ResourceAgentSummary,
} from '@/services/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { Switch } from '@/components/ui/switch';
import { Separator } from '@/components/ui/separator';
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip';
import {
  ArrowDown,
  ArrowUp,
  Info,
  Loader2,
  Plus,
  Trash2,
} from 'lucide-react';

const agentKey = (provider: string, name: string) => `${provider}:${name}`;

/**
 * OcfAgentBuilder lets the user browse the OCF resource agents available on the
 * nodes, pick one, fill in a parameter form generated from its OCF metadata,
 * and compose an ordered list of agents. The built list is surfaced to the
 * parent via onChange so the Create-HA flow can submit it as ocf_agents.
 */
export function OcfAgentBuilder({
  agents,
  onChange,
}: {
  agents: OcfAgentSpec[];
  onChange: (agents: OcfAgentSpec[]) => void;
}) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['ha-resource-agents'],
    queryFn: () => api.getResourceAgents(),
  });

  const [search, setSearch] = useState('');
  // Whether the searchable-combobox dropdown is showing.
  const [open, setOpen] = useState(false);
  const [selectedKey, setSelectedKey] = useState('');
  const [instance, setInstance] = useState('');
  // Parameter values keyed by parameter name.
  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, boolean>>({});

  const allAgents = data?.agents ?? [];

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return allAgents;
    return allAgents.filter(
      (a) =>
        a.name.toLowerCase().includes(q) ||
        a.provider.toLowerCase().includes(q) ||
        (a.shortdesc ?? '').toLowerCase().includes(q),
    );
  }, [allAgents, search]);

  // Cap the dropdown to a handful of matches; with an empty query this shows
  // the first few agents as a hint of what is available.
  const shown = useMemo(() => filtered.slice(0, 8), [filtered]);

  const selectedSummary: ResourceAgentSummary | undefined = useMemo(() => {
    if (!selectedKey) return undefined;
    return allAgents.find((a) => agentKey(a.provider, a.name) === selectedKey);
  }, [allAgents, selectedKey]);

  const { data: metadata, isFetching: metaLoading } = useQuery({
    queryKey: ['ha-agent-meta', selectedSummary?.provider, selectedSummary?.name],
    queryFn: () =>
      api.getResourceAgentMetadata(
        selectedSummary!.provider,
        selectedSummary!.name,
      ),
    enabled: Boolean(selectedSummary),
  });

  const onSelectAgent = (key: string) => {
    setSelectedKey(key);
    setErrors({});
    const summary = allAgents.find((a) => agentKey(a.provider, a.name) === key);
    // Reset the instance name to a sensible default the user can override.
    setInstance(summary ? `${summary.name.toLowerCase()}_${agents.length + 1}` : '');
    setValues({});
  };

  // Prefill parameter defaults once metadata arrives for the picked agent.
  const paramDefaults = useMemo(() => {
    const defaults: Record<string, string> = {};
    (metadata?.parameters ?? []).forEach((p) => {
      if (p.default) defaults[p.name] = p.default;
    });
    return defaults;
  }, [metadata]);

  const valueFor = (p: OcfAgentParameter): string =>
    values[p.name] ?? paramDefaults[p.name] ?? '';

  const setValue = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
    setErrors((prev) => ({ ...prev, [name]: false }));
  };

  const handleAdd = () => {
    if (!selectedSummary || !metadata) return;

    const nextErrors: Record<string, boolean> = {};
    if (!instance.trim()) nextErrors.__instance = true;
    for (const p of metadata.parameters) {
      if (p.required && !valueFor(p).trim()) {
        nextErrors[p.name] = true;
      }
    }
    if (Object.keys(nextErrors).length > 0) {
      setErrors(nextErrors);
      return;
    }

    // Only carry non-empty params.
    const params: Record<string, string> = {};
    for (const p of metadata.parameters) {
      const v = valueFor(p).trim();
      if (v) params[p.name] = v;
    }

    const spec: OcfAgentSpec = {
      provider: selectedSummary.provider,
      name: selectedSummary.name,
      instance: instance.trim(),
      params,
    };
    onChange([...agents, spec]);

    // Reset the picker for the next agent.
    setSelectedKey('');
    setInstance('');
    setValues({});
    setErrors({});
    setSearch('');
  };

  const removeAt = (idx: number) =>
    onChange(agents.filter((_, i) => i !== idx));

  const moveBy = (idx: number, delta: number) => {
    const target = idx + delta;
    if (target < 0 || target >= agents.length) return;
    const next = [...agents];
    const [item] = next.splice(idx, 1);
    next.splice(target, 0, item);
    onChange(next);
  };

  return (
    <TooltipProvider>
      <div className="space-y-4">
        {/* Built agent list */}
        {agents.length > 0 && (
          <div className="space-y-2">
            {agents.map((a, idx) => (
              <div
                key={`${a.provider}:${a.name}:${a.instance}:${idx}`}
                className="flex items-center gap-2 rounded-md border p-2"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <Badge variant="secondary" className="font-mono">
                      ocf:{a.provider}:{a.name}
                    </Badge>
                    <span className="truncate font-mono text-xs text-muted-foreground">
                      {a.instance}
                    </span>
                  </div>
                  {Object.keys(a.params).length > 0 && (
                    <p className="mt-1 truncate font-mono text-xs text-muted-foreground">
                      {Object.entries(a.params)
                        .map(([k, v]) => `${k}=${v}`)
                        .join(' ')}
                    </p>
                  )}
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  disabled={idx === 0}
                  onClick={() => moveBy(idx, -1)}
                  title="Move up"
                >
                  <ArrowUp className="h-3.5 w-3.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  disabled={idx === agents.length - 1}
                  onClick={() => moveBy(idx, 1)}
                  title="Move down"
                >
                  <ArrowDown className="h-3.5 w-3.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  onClick={() => removeAt(idx)}
                  title="Remove"
                >
                  <Trash2 className="h-3.5 w-3.5 text-destructive" />
                </Button>
              </div>
            ))}
          </div>
        )}

        {/* Agent picker — single-step searchable combobox */}
        <div className="space-y-1.5">
          <Label>OCF Resource Agent</Label>
          <div className="relative">
            <Input
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                setOpen(true);
              }}
              onFocus={() => setOpen(true)}
              // Delay via mousedown-preventDefault on rows, so the click
              // selects before this blur closes the list.
              onBlur={() => setOpen(false)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') setOpen(false);
              }}
              placeholder={
                isLoading
                  ? 'Loading agents...'
                  : 'Search OCF agents (provider or name)...'
              }
            />
            {open && !isLoading && (
              <div className="absolute z-50 mt-1 max-h-64 w-full overflow-auto rounded-md border bg-popover p-1 shadow-md">
                {shown.length === 0 ? (
                  <p className="px-2 py-1.5 text-sm text-muted-foreground">
                    No matching agents.
                  </p>
                ) : (
                  shown.map((a) => {
                    const key = agentKey(a.provider, a.name);
                    return (
                      <button
                        type="button"
                        key={key}
                        // Select on mousedown so it fires before the input blur
                        // that would otherwise close the list first.
                        onMouseDown={(e) => {
                          e.preventDefault();
                          onSelectAgent(key);
                          setSearch(`${a.provider}:${a.name}`);
                          setOpen(false);
                        }}
                        className="flex w-full flex-col items-start gap-0.5 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent hover:text-accent-foreground"
                      >
                        <span className="font-mono">
                          {a.provider}:{a.name}
                        </span>
                        {a.shortdesc ? (
                          <span className="text-xs text-muted-foreground">
                            {a.shortdesc}
                          </span>
                        ) : null}
                      </button>
                    );
                  })
                )}
              </div>
            )}
          </div>
          {isError && (
            <p className="text-xs text-destructive">
              Could not load agents: {(error as Error).message}
            </p>
          )}
        </div>

        {/* Parameter form */}
        {selectedSummary && (
          <div className="space-y-4 rounded-md border p-3">
            {metaLoading ? (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Loading metadata...
              </div>
            ) : metadata ? (
              <>
                <div>
                  <p className="font-mono text-sm font-semibold">
                    {metadata.provider}:{metadata.name}
                  </p>
                  {metadata.shortdesc && (
                    <p className="text-xs text-muted-foreground">
                      {metadata.shortdesc}
                    </p>
                  )}
                </div>

                <div className="space-y-1.5">
                  <Label>
                    Instance Name <span className="text-destructive">*</span>
                  </Label>
                  <Input
                    value={instance}
                    onChange={(e) => {
                      setInstance(e.target.value);
                      setErrors((prev) => ({ ...prev, __instance: false }));
                    }}
                    placeholder="e.g. vip_data"
                    aria-invalid={errors.__instance || undefined}
                  />
                  <p className="text-xs text-muted-foreground">
                    Unique OCF instance id for this agent.
                  </p>
                </div>

                {metadata.parameters.length > 0 && (
                  <>
                    <Separator />
                    <div className="space-y-4">
                      {metadata.parameters.map((p) => (
                        <ParamField
                          key={p.name}
                          param={p}
                          value={valueFor(p)}
                          invalid={Boolean(errors[p.name])}
                          onChange={(v) => setValue(p.name, v)}
                        />
                      ))}
                    </div>
                  </>
                )}

                <Button type="button" size="sm" onClick={handleAdd}>
                  <Plus className="mr-1 h-4 w-4" />
                  Add Agent
                </Button>
              </>
            ) : null}
          </div>
        )}
      </div>
    </TooltipProvider>
  );
}

function ParamField({
  param,
  value,
  invalid,
  onChange,
}: {
  param: OcfAgentParameter;
  value: string;
  invalid: boolean;
  onChange: (value: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <Label className="flex items-center gap-2">
        <span className="font-mono">{param.name}</span>
        {param.required && <span className="text-destructive">*</span>}
        {param.longdesc && (
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="inline-flex cursor-help text-muted-foreground">
                <Info className="h-3.5 w-3.5" />
              </span>
            </TooltipTrigger>
            <TooltipContent className="max-w-xs whitespace-pre-wrap">
              {param.longdesc}
            </TooltipContent>
          </Tooltip>
        )}
      </Label>
      {param.type === 'boolean' ? (
        <div className="flex items-center gap-2">
          <Switch
            checked={value === 'true'}
            onCheckedChange={(checked) => onChange(checked ? 'true' : 'false')}
          />
          <span className="text-xs text-muted-foreground">
            {value === 'true' ? 'true' : 'false'}
          </span>
        </div>
      ) : param.type === 'integer' ? (
        <Input
          type="number"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-invalid={invalid || undefined}
        />
      ) : (
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-invalid={invalid || undefined}
        />
      )}
      {param.shortdesc && (
        <p className="text-xs text-muted-foreground">{param.shortdesc}</p>
      )}
    </div>
  );
}
