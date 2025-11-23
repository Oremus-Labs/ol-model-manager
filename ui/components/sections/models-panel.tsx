import type { Model } from '@/lib/types';
import { ActivateModelForm } from '../forms/activate-model-form';

interface Props {
  models: Model[];
}

export function ModelsPanel({ models }: Props) {
  if (!models.length) {
    return <p className="text-sm text-slate-300">No catalog entries detected. Git-sync may still be warming up.</p>;
  }

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {models.map((model) => (
        <div key={model.id} className="rounded-2xl border border-white/5 bg-slate-900/40 p-5 shadow-card">
          <div className="flex items-start justify-between">
            <div>
              <p className="text-xs uppercase tracking-wide text-slate-400">Model ID</p>
              <h3 className="text-xl font-semibold text-white">{model.displayName ?? model.id}</h3>
              <p className="text-sm text-slate-400">{model.hfModelId}</p>
            </div>
            <span className="rounded-full bg-slate-800 px-3 py-1 text-xs text-slate-300">{model.runtime ?? 'vLLM'}</span>
          </div>
          <div className="mt-4 grid gap-2 text-sm text-slate-300">
            <p>
              <span className="text-slate-400">Storage URI:</span>{' '}
              {model.storageUri ?? 'hf://'}
            </p>
            {model.resources && (
              <p>
                <span className="text-slate-400">Resources:</span>{' '}
                {Object.entries(model.resources)
                  .map(([k, v]) => `${k}: ${JSON.stringify(v)}`)
                  .join(' | ')}
              </p>
            )}
          </div>
          <div className="mt-6">
            <ActivateModelForm modelId={model.id} />
          </div>
        </div>
      ))}
    </div>
  );
}
