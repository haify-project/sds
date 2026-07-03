import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation } from '@tanstack/react-query';
import { api, OcfAgentSpec } from '@/services/api';
import { OcfAgentBuilder } from '@/components/OcfAgentBuilder';
import { buildPromoterTomlPreview } from '@/lib/toml';
import { toast } from 'sonner';
import { ArrowLeft, Loader2 } from 'lucide-react';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';

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

  // Resources without an existing HA config are eligible for creation.
  const resourcesWithoutHA = (resources?.resources ?? []).filter(
    (r) => !configs.some((ha) => ha.resource === r.name)
  );

  const [resource, setResource] = useState('');
  const [vip, setVip] = useState('');
  const [mountPoint, setMountPoint] = useState('');
  const [fstype, setFstype] = useState('ext4');
  const [services, setServices] = useState('');
  const [ocfAgents, setOcfAgents] = useState<OcfAgentSpec[]>([]);

  // Parsed the same way makeHa sends it, so the preview matches what the
  // backend actually receives.
  const parsedServices = useMemo(
    () => services.split(',').map((s) => s.trim()).filter(Boolean),
    [services],
  );

  // Live drbd-reactor promoter TOML preview, rebuilt from the current form
  // state. Mirrors the backend generatePromoterConfig output exactly.
  const promoterPreview = useMemo(
    () =>
      buildPromoterTomlPreview({
        resource,
        vip,
        mountPoint,
        services: parsedServices,
        ocfAgents,
      }),
    [resource, vip, mountPoint, parsedServices, ocfAgents],
  );

  const mutation = useMutation({
    mutationFn: () =>
      api.makeHa(resource, {
        vip,
        mountPoint: mountPoint || undefined,
        fstype: mountPoint ? fstype : undefined,
        services: services
          ? services.split(',').map((s) => s.trim()).filter(Boolean)
          : undefined,
        ocfAgents: ocfAgents.length > 0 ? ocfAgents : undefined,
      }),
    onSuccess: () => {
      toast.success('HA configuration created');
      setOcfAgents([]);
      navigate('/ha');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Button variant="outline" size="icon" onClick={() => navigate('/ha')}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <div>
          <h3 className="text-lg font-semibold">Create HA Configuration</h3>
          <p className="text-sm text-muted-foreground">
            Attach a floating VIP and automatic failover to a DRBD resource.
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
          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Resource &amp; VIP</CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="space-y-1.5">
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

                <div className="space-y-1.5">
                  <Label>Virtual IP (CIDR)</Label>
                  <Input
                    value={vip}
                    onChange={(e) => setVip(e.target.value)}
                    placeholder="192.168.1.100/24"
                    required
                  />
                  <p className="text-xs text-muted-foreground">
                    The VIP that will float between nodes.
                  </p>
                </div>

                <div className="space-y-1.5">
                  <Label>Mount Point (optional)</Label>
                  <Input
                    value={mountPoint}
                    onChange={(e) => setMountPoint(e.target.value)}
                    placeholder="/mnt/data"
                  />
                  <p className="text-xs text-muted-foreground">
                    Path where the DRBD device will be mounted.
                  </p>
                </div>

                {mountPoint && (
                  <div className="space-y-1.5">
                    <Label>Filesystem Type</Label>
                    <Select value={fstype} onValueChange={setFstype}>
                      <SelectTrigger className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="ext4">ext4</SelectItem>
                        <SelectItem value="xfs">XFS</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                )}

                <div className="space-y-1.5">
                  <Label>Services (comma-separated, optional)</Label>
                  <Input
                    value={services}
                    onChange={(e) => setServices(e.target.value)}
                    placeholder="mysql.service, nginx.service"
                  />
                  <p className="text-xs text-muted-foreground">
                    Systemd services to start/stop with the resource.
                  </p>
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">OCF Agents (optional)</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1.5">
                <p className="text-xs text-muted-foreground">
                  Extra OCF resource agents appended to the promoter start list
                  after the built-in mount/VIP items.
                </p>
                <OcfAgentBuilder agents={ocfAgents} onChange={setOcfAgents} />
              </CardContent>
            </Card>
          </div>

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
            <Button
              type="button"
              variant="outline"
              onClick={() => navigate('/ha')}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending || !resource || !vip}>
              {mutation.isPending && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
              Create
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
