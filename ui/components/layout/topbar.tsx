import { activateModelAction, clearHistoryAction, clearJobsAction, deactivateModelAction } from '@/app/actions';
import type { ActiveService, HistoryEntry, Job, Model } from '@/lib/types';
import { formatDistanceToNow } from '@/lib/utils';
import { Search } from 'lucide-react';

const DEFAULT_ACTION_STATE = { ok: false, message: '' };
const ACTIVATE_ACTION = activateModelAction.bind(null, DEFAULT_ACTION_STATE);
const DEACTIVATE_ACTION = deactivateModelAction.bind(null, DEFAULT_ACTION_STATE);
const CLEAR_JOBS_ACTION = clearJobsAction.bind(null, DEFAULT_ACTION_STATE);
const CLEAR_HISTORY_ACTION = clearHistoryAction.bind(null, DEFAULT_ACTION_STATE);

interface Props {
  activeService: ActiveService | null;
  models: Model[];
  jobs: Job[];
  history: HistoryEntry[];
}

const jobStatuses: Record<string, string> = {
  pending: 'Pending',
  running: 'In progress',
  completed: 'Completed',
  failed: 'Failed',
};

export function TopBar({ activeService, models, jobs, history }: Props) {
  const runningJobs = jobs.filter((job) => job.status === 'pending' || job.status === 'running');
  const latestEvents = history.slice(0, 3);
  const activeSpec = activeService?.inferenceservice?.spec as Record<string, any> | undefined;
  const predictor = activeSpec?.predictor as Record<string, any> | undefined;
  const modelSpec = predictor?.model as Record<string, any> | undefined;
  const activeStorage = typeof modelSpec?.storageUri === 'string' ? modelSpec.storageUri : undefined;
  const activeName = models.find((m) => activeStorage?.includes(m.id))?.displayName;

  return (
    <header className="sticky top-0 z-30 flex flex-col gap-4 border-b border-white/5 bg-slate-950/80 px-4 py-4 backdrop-blur sm:flex-row sm:items-center sm:justify-between sm:px-6">
      <form action="/" method="get" className="relative w-full sm:max-w-md">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-500" />
        <input
          type="search"
          name="q"
          placeholder="Quick search (models, weights, jobs...)"
          className="w-full rounded-2xl border border-slate-800 bg-slate-900/80 py-2 pl-10 pr-3 text-sm text-white placeholder:text-slate-500 focus:border-brand-500 focus:outline-none"
        />
      </form>
      <div className="flex flex-1 flex-wrap items-center justify-end gap-3">
        <Popover label={`Jobs (${runningJobs.length})`} badge={runningJobs.length} panelClass="w-80">
          {runningJobs.length === 0 && <p className="text-sm text-slate-300">No active jobs.</p>}
          {runningJobs.map((job) => (
            <article key={job.id} className="rounded-lg border border-white/10 bg-slate-900/70 p-3 text-sm text-slate-200">
              <p className="font-medium">{job.type}</p>
              <p className="text-xs text-slate-400">{jobStatuses[job.status] ?? job.status}</p>
              {job.message && <p className="mt-1 text-xs text-slate-400 line-clamp-2">{job.message}</p>}
              <p className="mt-1 text-xs text-slate-500">{formatDistanceToNow(job.updatedAt)}</p>
            </article>
          ))}
          <a href="#jobs" className="text-xs font-semibold text-brand-400 underline-offset-2 hover:underline">
            View job center
          </a>
          <form action={CLEAR_JOBS_ACTION} className="mt-2 flex items-center justify-between gap-2 text-xs text-slate-400">
            <input type="hidden" name="status" value="completed" />
            <button type="submit" className="rounded-md border border-white/10 px-2 py-1 font-semibold text-slate-200 hover:border-white/40">
              Clear completed jobs
            </button>
          </form>
        </Popover>

        <Popover label="Recent activity" badge={latestEvents.length} panelClass="w-80">
          {latestEvents.length === 0 && <p className="text-sm text-slate-300">No lifecycle events logged yet.</p>}
          {latestEvents.map((event) => (
            <article key={event.id} className="rounded-lg border border-white/10 bg-slate-900/70 p-3 text-sm text-slate-200">
              <p className="font-medium">{event.event.replace(/_/g, ' ')}</p>
              {event.modelId && <p className="text-xs text-slate-400">Model: {event.modelId}</p>}
              <p className="text-xs text-slate-500">{formatDistanceToNow(event.createdAt)}</p>
            </article>
          ))}
          <a href="#history" className="text-xs font-semibold text-brand-400 underline-offset-2 hover:underline">
            View full timeline
          </a>
          <form action={CLEAR_HISTORY_ACTION} className="mt-2 text-xs">
            <button type="submit" className="rounded-md border border-white/10 px-2 py-1 font-semibold text-slate-200 hover:border-white/40">
              Clear activity log
            </button>
          </form>
        </Popover>

        <ActiveModelMenu activeService={activeService} activeName={activeName} models={models} />

        <div className="hidden text-right text-xs text-slate-400 md:block">
          <p className="text-slate-200">Platform Operator</p>
          <p>model-manager@oremuslabs.app</p>
        </div>
        <div className="flex h-10 w-10 items-center justify-center rounded-full bg-gradient-to-br from-brand-500 to-purple-500 text-sm font-semibold text-white">
          MM
        </div>
      </div>
    </header>
  );
}

function ActiveModelMenu({
  activeService,
  activeName,
  models,
}: {
  activeService: ActiveService | null;
  activeName?: string;
  models: Model[];
}) {
  const hasActive = activeService?.status === 'active' && activeService.inferenceservice;
  const label = hasActive ? activeName ?? 'Running model' : 'No active model';

  return (
    <Popover label={label ?? 'Model control'} badge={hasActive ? 1 : 0} panelClass="w-96">
      {hasActive ? (
        <div className="space-y-3 text-sm text-slate-200">
          <p className="font-semibold">{label}</p>
          <p className="text-xs text-slate-400">
            InferenceService: {activeService?.inferenceservice?.metadata?.name ?? 'active-llm'}
          </p>
          <form action={DEACTIVATE_ACTION}>
            <button
              type="submit"
              className="w-full rounded-md border border-rose-400/40 px-3 py-2 text-sm font-semibold text-rose-200 transition hover:bg-rose-500/10"
            >
              Stop model
            </button>
          </form>
        </div>
      ) : (
        <p className="text-sm text-slate-300">Select a catalog entry to activate it on Venus.</p>
      )}
      <div className="mt-3 rounded-lg border border-white/10 bg-slate-950/50 p-3">
        {models.length === 0 ? (
          <p className="text-sm text-slate-400">Catalog is still syncing. Check back shortly to activate a model.</p>
        ) : (
          <form action={ACTIVATE_ACTION} className="space-y-3">
            <label className="text-xs uppercase tracking-wide text-slate-400">
              Deploy catalog entry
              <select
                name="id"
                className="mt-1 w-full rounded-md border border-white/10 bg-slate-900/70 px-3 py-2 text-sm text-white focus:border-brand-400 focus:outline-none"
              >
                {models.map((model) => (
                  <option key={model.id} value={model.id}>
                    {model.displayName ?? model.id}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="submit"
              className="w-full rounded-md bg-brand-600 px-3 py-2 text-sm font-semibold text-white shadow-md shadow-brand-900/40 transition hover:bg-brand-500"
            >
              Activate selection
            </button>
          </form>
        )}
      </div>
    </Popover>
  );
}

function Popover({
  children,
  label,
  badge,
  panelClass,
}: {
  children: React.ReactNode;
  label: string;
  badge?: number;
  panelClass?: string;
}) {
  return (
    <details className="relative">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-full border border-white/10 bg-slate-900/60 px-4 py-1.5 text-sm text-white [&::-webkit-details-marker]:hidden">
        {label}
        {typeof badge === 'number' && (
          <span className="rounded-full bg-white/10 px-2 text-xs text-slate-200">{badge}</span>
        )}
      </summary>
      <div
        className={`absolute right-0 mt-2 max-h-[20rem] overflow-y-auto rounded-2xl border border-white/10 bg-slate-950/95 p-4 shadow-2xl shadow-black/70 backdrop-blur ${panelClass}`}
      >
        <div className="space-y-3">{children}</div>
      </div>
    </details>
  );
}
