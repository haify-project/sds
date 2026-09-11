import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Loader2, Lock } from 'lucide-react';

import { getAIConfig, saveAIConfig, type AIConfig } from '@/lib/aiClient';
import { PageHeader } from '@/components/PageHeader';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';

/**
 * Settings — currently the Copilot's model, and nothing else.
 *
 * The page exists because changing which LLM answers used to mean editing an
 * environment file on whichever node was active and restarting sds-ai. That
 * restart is not a restart: sds-ai.service is in the drbd-reactor promoter's
 * start list for sds-meta, so stopping it demotes the resource and moves the
 * VIP, the controller and the Copilot to another node. Renaming a model cost a
 * control-plane outage.
 *
 * It does not now. The save swaps the model on the running agent and writes it
 * to the replicated volume, so it survives the next promotion without one
 * happening here.
 */
export function SettingsPage() {
  const queryClient = useQueryClient();

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['ai-config'],
    queryFn: getAIConfig,
  });

  return (
    <div>
      <PageHeader
        title="Settings"
        description="How the Copilot is configured. Changes apply to the running agent — no restart, no failover."
      />

      {isLoading ? (
        <Skeleton className="h-64 w-full max-w-2xl" />
      ) : isError ? (
        <Card className="max-w-2xl">
          <CardContent className="px-5 py-4">
            <p className="text-[13px] text-status-bad-text">
              Could not read the Copilot's settings: {(error as Error).message}
            </p>
            <p className="mt-2 text-[12.5px] text-muted-foreground">
              The Copilot runs as its own service. If it is not running, the rest of the
              console is unaffected.
            </p>
          </CardContent>
        </Card>
      ) : data ? (
        <CopilotModelCard
          config={data}
          onSaved={() => queryClient.invalidateQueries({ queryKey: ['ai-config'] })}
        />
      ) : null}
    </div>
  );
}

function CopilotModelCard({
  config,
  onSaved,
}: {
  config: AIConfig;
  onSaved: () => void;
}) {
  const [baseUrl, setBaseUrl] = useState(config.llmBaseUrl);
  const [model, setModel] = useState(config.llmModel);
  const [apiKey, setApiKey] = useState('');

  // The form is seeded from the server and the server is the authority: a save
  // elsewhere, or a failover to a node with different settings, must not leave
  // this showing what the operator typed ten minutes ago as if it were live.
  useEffect(() => {
    setBaseUrl(config.llmBaseUrl);
    setModel(config.llmModel);
  }, [config.llmBaseUrl, config.llmModel]);

  const save = useMutation({
    mutationFn: () =>
      saveAIConfig({
        llmBaseUrl: baseUrl.trim(),
        llmModel: model.trim(),
        // Blank means keep. Sending an empty string would be indistinguishable
        // from clearing the key, which is why the backend treats it as "unset".
        ...(apiKey.trim() ? { llmApiKey: apiKey.trim() } : {}),
      }),
    onSuccess: (next) => {
      setApiKey('');
      toast.success(`Copilot is now answering with ${next.llmModel}`);
      onSaved();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const dirty =
    baseUrl.trim() !== config.llmBaseUrl ||
    model.trim() !== config.llmModel ||
    apiKey.trim() !== '';

  return (
    <Card className="max-w-2xl gap-0 py-5">
      <CardContent className="space-y-5 px-5">
        <div>
          <h2 className="text-[14.5px] font-semibold">Copilot model</h2>
          <p className="mt-1 text-[12.5px] text-muted-foreground">
            Any OpenAI-compatible endpoint. The change reaches the running agent
            immediately; requests already in flight finish on the old model.
          </p>
        </div>

        {!config.editable ? (
          <div className="flex items-start gap-2.5 rounded-lg border border-border bg-muted px-3.5 py-3">
            <Lock aria-hidden className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <div className="text-[12.5px] leading-relaxed">
              <p className="font-medium">Read-only</p>
              <p className="mt-0.5 text-muted-foreground">
                {config.readOnlyReason ?? 'this backend does not accept settings writes'}.
                The Copilot serves unauthenticated on loopback and this console
                proxies it, so a form that accepts an API key stays shut until a
                token is configured. Set <code className="font-mono">SDS_AI_TOKEN</code>{' '}
                (or write <code className="font-mono">/etc/sds/token</code>) and restart
                the Copilot to enable editing.
              </p>
            </div>
          </div>
        ) : null}

        <div className="grid gap-4">
          <Field
            label="Base URL"
            hint="The gateway's OpenAI-compatible root, e.g. https://api.deepseek.com"
          >
            <Input
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              disabled={!config.editable || save.isPending}
              className="font-mono text-[13px]"
              spellCheck={false}
            />
          </Field>

          <Field label="Model" hint="Sent verbatim as the model name">
            <Input
              value={model}
              onChange={(e) => setModel(e.target.value)}
              disabled={!config.editable || save.isPending}
              className="font-mono text-[13px]"
              spellCheck={false}
            />
          </Field>

          <Field
            label="API key"
            hint={
              config.hasApiKey
                ? 'A key is configured. Leave blank to keep it.'
                : 'No key is configured yet.'
            }
          >
            <Input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              disabled={!config.editable || save.isPending}
              placeholder={config.hasApiKey ? '••••••••  unchanged' : 'sk-…'}
              className="font-mono text-[13px]"
              autoComplete="off"
            />
          </Field>
        </div>

        <div className="flex items-center gap-3 border-t border-border pt-4">
          <Button
            disabled={!config.editable || !dirty || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? <Loader2 className="mr-2 size-4 animate-spin" /> : null}
            Save
          </Button>
          {/* Saving is not proof the endpoint answers — that takes a question.
              Say so rather than let a green toast imply a working gateway. */}
          <p className="text-[12px] text-muted-foreground">
            Saved settings are applied and kept, not tested. Ask the Copilot something
            to confirm the endpoint answers.
          </p>
        </div>

        <div className="border-t border-border pt-4">
          <div className="eyebrow">Embedder</div>
          <div className="mt-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className="font-mono text-[13px]">{config.embModel || 'not reported'}</span>
            <span className="font-mono text-[12.5px] text-muted-foreground">
              {config.embDim} dimensions
            </span>
          </div>
          {/* Not an oversight that this has no form. */}
          <p className="mt-2 text-[12px] leading-relaxed text-muted-foreground">
            Shown, not settable. The knowledge base was indexed with this embedder at
            this dimension and can only be searched by the same one — changing it here
            would not re-index anything, it would make every stored vector
            unreadable. Re-indexing is an ingest, not a setting.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid gap-1.5">
      <Label className="text-[13px]">{label}</Label>
      {children}
      <p className="text-[11.5px] text-muted-foreground">{hint}</p>
    </div>
  );
}
