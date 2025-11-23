import type { Architecture } from '@/lib/types';

interface Props {
  items: Architecture[];
}

export function ArchitecturesPanel({ items }: Props) {
  if (!items.length) {
    return <p className="text-sm text-slate-300">Unable to reach the vLLM GitHub manifest.</p>;
  }

  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {items.map((arch) => (
        <div key={arch.name} className="rounded-2xl border border-white/5 bg-gradient-to-br from-slate-900 via-slate-900/70 to-slate-900/30 p-4">
          <h4 className="text-lg font-semibold text-white">{arch.name}</h4>
          <p className="text-sm text-slate-300">{arch.className}</p>
          {arch.description && <p className="mt-2 text-xs text-slate-400">{arch.description}</p>}
        </div>
      ))}
    </div>
  );
}
