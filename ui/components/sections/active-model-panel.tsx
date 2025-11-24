import type { ActiveService, Model } from '@/lib/types';
import { formatDistanceToNow } from '@/lib/utils';

interface Props {
  service: ActiveService | null;
  models: Model[];
}

export function ActiveModelPanel({ service, models }: Props) {
  if (!service || service.status === 'none' || !service.inferenceservice) {
    return <p className="text-sm text-slate-300">No model is currently running on Venus. Select a catalog entry to activate it.</p>;
  }

  const metadata = service.inferenceservice.metadata as Record<string, any>;
  const status = service.inferenceservice.status as Record<string, any>;
  const conditions = Array.isArray(status?.conditions) ? (status.conditions as Record<string, any>[]) : [];
  const readyCondition = conditions.find((c) => c.type === 'Ready');
  const modelName = metadata?.name ?? 'active-llm';
  const storageUri = findStorageUri(service, models);

  return (
    <div className="rounded-2xl border border-white/5 bg-slate-900/40 p-5 shadow-inner">
      <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
        <div>
          <p className="text-xs uppercase tracking-wide text-slate-400">InferenceService</p>
          <h3 className="text-xl font-semibold text-white">{modelName}</h3>
          {readyCondition ? (
            <p className={`text-sm ${readyCondition.status === 'True' ? 'text-emerald-300' : 'text-amber-300'}`}>
              {readyCondition.reason ?? 'Unknown'} {readyCondition.status !== 'True' && readyCondition.message ? `– ${readyCondition.message}` : ''}
            </p>
          ) : (
            <p className="text-sm text-slate-300">Waiting for Knative to report status.</p>
          )}
        </div>
        {storageUri && (
          <div className="rounded-full bg-white/10 px-4 py-1 text-xs text-slate-100">
            storage: <span className="font-mono">{storageUri}</span>
          </div>
        )}
      </div>
      <div className="mt-4 grid gap-3 text-sm text-slate-300 md:grid-cols-2">
        <div>
          <p className="text-xs uppercase tracking-wide text-slate-500">Observed Generation</p>
          <p>{status?.observedGeneration ?? 'n/a'}</p>
        </div>
        <div>
          <p className="text-xs uppercase tracking-wide text-slate-500">Last transition</p>
          {readyCondition?.lastTransitionTime ? (
            <p>{formatDistanceToNow(readyCondition.lastTransitionTime)}</p>
          ) : (
            <p>n/a</p>
          )}
        </div>
      </div>
      {conditions.length > 0 && (
        <div className="mt-4 rounded-xl border border-white/5 bg-slate-950/40 p-3 text-xs text-slate-200">
          <p className="mb-2 font-semibold text-white">Conditions</p>
          <div className="space-y-1">
            {conditions.map((condition) => (
              <div key={`${condition.type}-${condition.status}`} className="flex items-start justify-between gap-4">
                <span className="font-semibold">{condition.type}</span>
                <span className={condition.status === 'True' ? 'text-emerald-300' : 'text-amber-300'}>
                  {condition.status}
                  {condition.reason ? ` · ${condition.reason}` : ''}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function findStorageUri(service: ActiveService, models: Model[]): string | null {
  const storage = service.inferenceservice?.spec as Record<string, any>;
  const uri = storage?.predictor?.model?.storageUri as string | undefined;
  if (uri) {
    return uri;
  }
  const maybeModel = models.find((model) => uriMatchesModel(model, storage));
  return maybeModel?.storageUri ?? null;
}

function uriMatchesModel(model: Model, storage?: Record<string, any>): boolean {
  if (!storage) return false;
  return storage?.predictor?.model?.storageUri === model.storageUri;
}
