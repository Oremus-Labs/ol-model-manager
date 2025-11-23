import type { Job } from '@/lib/types';
import { formatDate, jobStatusColor } from '@/lib/utils';

interface Props {
  jobs: Job[];
}

export function JobsPanel({ jobs }: Props) {
  if (!jobs.length) {
    return <p className="text-sm text-slate-300">No asynchronous work logged yet.</p>;
  }

  return (
    <div className="space-y-4">
      {jobs.map((job) => (
        <div key={job.id} className="rounded-2xl border border-white/5 bg-slate-900/30 p-4">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-white font-semibold">{job.type.replace('_', ' ')}</span>
            <span className={`rounded-full px-3 py-1 text-xs font-semibold ${jobStatusColor(job.status)}`}>
              {job.status}
            </span>
            {typeof job.progress === 'number' && (
              <span className="text-xs text-slate-300">{job.progress}%</span>
            )}
          </div>
          <p className="mt-2 text-sm text-slate-200">{job.message || job.stage || 'Queued'}</p>
          <p className="text-xs text-slate-400">Updated {formatDate(job.updatedAt)}</p>
          {typeof job.payload?.hfModelId === 'string' && (
            <p className="text-xs text-slate-400">Model: {job.payload.hfModelId}</p>
          )}
        </div>
      ))}
    </div>
  );
}
