import type { HistoryEntry } from '@/lib/types';
import { formatDate } from '@/lib/utils';

interface Props {
  events: HistoryEntry[];
}

export function HistoryPanel({ events }: Props) {
  if (!events.length) {
    return <p className="text-sm text-slate-300">No lifecycle events yet.</p>;
  }

  return (
    <div className="relative pl-4">
      <span className="absolute left-1 top-0 bottom-0 w-px bg-gradient-to-b from-brand-500 to-transparent" />
      <div className="space-y-5">
        {events.map((event) => (
          <div key={event.id} className="rounded-xl border border-white/5 bg-slate-900/30 p-4">
            <p className="text-xs uppercase tracking-wide text-slate-400">{formatDate(event.createdAt)}</p>
            <p className="text-base font-semibold text-white">{event.event.replaceAll('_', ' ')}</p>
            {event.modelId && <p className="text-sm text-slate-300">Model: {event.modelId}</p>}
          </div>
        ))}
      </div>
    </div>
  );
}
