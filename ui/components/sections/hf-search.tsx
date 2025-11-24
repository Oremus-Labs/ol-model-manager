import Link from 'next/link';
import { installWeightsAction } from '@/app/actions';
import type { ModelInsight } from '@/lib/types';

const INSTALL_ACTION = installWeightsAction.bind(null, { ok: false, message: '' });

interface Props {
  query: string;
  results: ModelInsight[] | null;
}

export function HuggingFaceSearch({ query, results }: Props) {
  const hasQuery = query.trim().length > 0;
  const hits = results ?? [];

  return (
    <div className="space-y-4">
      <form
        method="get"
        className="glass-panel flex flex-col gap-3 rounded-2xl border border-white/5 p-4 md:flex-row md:items-end"
      >
        <label className="flex-1 text-sm text-slate-200">
          <span className="mb-1 block text-xs uppercase tracking-wide text-slate-400">Hugging Face model ID or keyword</span>
          <input
            type="text"
            name="q"
            placeholder="e.g. Qwen/Qwen2.5-0.5B-Instruct"
            defaultValue={query}
            className="w-full rounded-lg border border-white/10 bg-slate-900/40 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-white/40 focus:outline-none"
          />
        </label>
        <button type="submit" className="rounded-lg bg-indigo-500 px-4 py-2 text-sm font-semibold text-white shadow hover:bg-indigo-400">
          Search
        </button>
      </form>

      {!hasQuery && (
        <p className="text-sm text-slate-400">
          Enter a Hugging Face repo ID to inspect metadata, catalog hints, and trigger a one-click install on the Venus PVC.
        </p>
      )}
      {hasQuery && hits.length === 0 && (
        <p className="text-sm text-slate-300">No models matched that query. Try a different keyword or filter.</p>
      )}
      {hits.length > 0 && (
        <div className="grid gap-4 md:grid-cols-2">
          {hits.map((insight) => (
            <SearchResultCard key={insight.huggingFace.id} insight={insight} />
          ))}
        </div>
      )}
    </div>
  );
}

function SearchResultCard({ insight }: { insight: ModelInsight }) {
  const modelId = insight.huggingFace.modelId ?? insight.huggingFace.id;
  const fallbackId = modelId ?? insight.huggingFace.id ?? 'model';
  const canonicalId = insight.huggingFace.modelId || insight.huggingFace.id || fallbackId;
  const defaultTarget = canonicalId || slugifyModel(fallbackId);

  return (
    <article className="rounded-2xl border border-white/5 bg-slate-900/30 p-4 shadow-card">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-xs uppercase tracking-wide text-slate-400">{insight.huggingFace.author}</p>
          <h4 className="text-lg font-semibold text-white">{modelId}</h4>
          <p className="text-xs text-slate-400">{insight.huggingFace.pipelineTag || 'Unknown pipeline'}</p>
        </div>
        <span className="rounded-full bg-white/10 px-3 py-1 text-xs text-white">{insight.compatible ? 'vLLM ready' : 'Custom runtime'}</span>
      </div>
      {insight.huggingFace.tags && insight.huggingFace.tags.length > 0 && (
        <p className="mt-2 text-xs text-slate-400 line-clamp-2">{insight.huggingFace.tags.join(', ')}</p>
      )}

      <div className="mt-4 flex flex-col gap-3 text-sm">
        <form action={INSTALL_ACTION} className="space-y-2 rounded-xl border border-emerald-400/20 bg-emerald-500/5 p-3">
          <input type="hidden" name="hfModelId" value={modelId} />
          <input type="hidden" name="target" value={defaultTarget} />
          <p className="text-xs text-emerald-200">
            Default target <span className="font-mono">{defaultTarget}</span> inside /mnt/models using the cluster API token.
          </p>
          <button
            type="submit"
            className="w-full rounded-md bg-gradient-to-r from-emerald-500 to-teal-500 px-3 py-2 text-sm font-semibold text-white"
          >
            Install with defaults
          </button>
        </form>

        <details className="rounded-xl border border-white/10 bg-slate-950/40 p-3 text-sm text-slate-200">
          <summary className="cursor-pointer text-sm font-semibold text-white">Advanced install &amp; config</summary>
          <div className="mt-3 space-y-3 text-sm">
            <form action={INSTALL_ACTION} className="space-y-3">
              <label className="text-xs uppercase tracking-wide text-slate-400">
                Target directory
                <input
                  type="text"
                  name="target"
                  defaultValue={defaultTarget}
                  className="mt-1 w-full rounded-md border border-white/10 bg-slate-900/70 px-3 py-2 text-sm text-white focus:border-brand-400 focus:outline-none"
                />
              </label>
              <label className="text-xs uppercase tracking-wide text-slate-400">
                Git revision / branch
                <input
                  type="text"
                  name="revision"
                  placeholder="main"
                  className="mt-1 w-full rounded-md border border-white/10 bg-slate-900/70 px-3 py-2 text-sm text-white focus:border-brand-400 focus:outline-none"
                />
              </label>
              <label className="text-xs uppercase tracking-wide text-slate-400">
                Limit to specific files (optional)
                <textarea
                  name="files"
                  placeholder="config.json&#10;pytorch_model.bin"
                  className="mt-1 w-full rounded-md border border-white/10 bg-slate-900/70 px-3 py-2 text-sm text-white focus:border-brand-400 focus:outline-none"
                  rows={3}
                />
              </label>
              <label className="flex items-center gap-2 text-xs text-slate-300">
                <input type="checkbox" name="overwrite" className="rounded border-slate-600 bg-slate-900 text-brand-500 focus:ring-brand-500" />
                Overwrite if target exists
              </label>
              <input type="hidden" name="hfModelId" value={modelId} />
              <button
                type="submit"
                className="w-full rounded-md border border-white/20 px-3 py-2 text-sm font-semibold text-white transition hover:bg-white/10"
              >
                Install with custom options
              </button>
            </form>
            {insight.suggestedCatalog && (
              <div className="rounded-lg border border-white/10 bg-slate-900/60 p-3 text-xs text-slate-300">
                <p className="mb-2 font-semibold text-white">Suggested catalog config</p>
                <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all font-mono text-[11px] leading-tight">
                  {JSON.stringify(insight.suggestedCatalog, null, 2)}
                </pre>
              </div>
            )}
          </div>
        </details>
      </div>

      <div className="mt-4 flex flex-wrap gap-2 text-xs">
        <Link
          href={`https://huggingface.co/${insight.huggingFace.id}`}
          target="_blank"
          className="rounded-md border border-white/10 px-3 py-1 text-slate-200 hover:border-white/30"
        >
          View on Hugging Face
        </Link>
        {insight.suggestedCatalog?.hfModelId && (
          <span className="rounded-md border border-emerald-400/40 px-3 py-1 text-emerald-200">
            {insight.suggestedCatalog.hfModelId}
          </span>
        )}
      </div>
    </article>
  );
}

function slugifyModel(source: string): string {
  return source
    .toLowerCase()
    .replace(/[^\w]+/g, '-')
    .replace(/^-+|-+$/g, '');
}
