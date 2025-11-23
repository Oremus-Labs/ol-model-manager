import { formatBytes } from '@/lib/utils';
import type { SystemInfo } from '@/lib/types';

interface Props {
  data: SystemInfo | null;
}

const StatCard = ({ label, value }: { label: string; value: string }) => (
  <div className="rounded-2xl border border-white/5 bg-slate-900/40 p-4 shadow-inner">
    <p className="text-xs uppercase tracking-wide text-slate-400">{label}</p>
    <p className="mt-2 text-2xl font-semibold text-white">{value}</p>
  </div>
);

export function SystemOverview({ data }: Props) {
  if (!data) {
    return <p className="text-sm text-rose-200">Unable to reach the Model Manager API.</p>;
  }

  return (
    <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
      <StatCard label="Version" value={data.version ?? 'n/a'} />
      <StatCard
        label="Models in catalog"
        value={String(data.catalog?.count ?? 0)}
      />
      <StatCard
        label="Cached weights"
        value={`${String(data.storage?.modelCount ?? 0)} (${formatBytes(data.storage?.usedBytes)})`}
      />
      <StatCard
        label="GPU Profiles"
        value={String(data.gpuProfiles?.length ?? 0)}
      />
    </div>
  );
}
