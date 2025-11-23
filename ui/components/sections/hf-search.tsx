import type { ModelInsight } from '@/lib/types';
import Link from 'next/link';

interface Props {
  query: string;
  compatibleOnly: boolean;
  results: ModelInsight[] | null;
}

export function HuggingFaceSearch({ query, compatibleOnly, results }: Props) {
  const hasQuery = query.trim().length > 0;
  const hits = results ?? [];

  return (
    <div className="space-y-4">
      <form method="get" className="glass-panel flex flex-col gap-3 rounded-2xl border border-white/5 p-4 md:flex-row md:items-end">
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
        <label className="flex items-center gap-2 text-xs font-medium text-slate-300">
          <input
            type="checkbox"
            name="compatibleOnly"
            value="true"
            defaultChecked={compatibleOnly}
            className="h-4 w-4 rounded border-slate-500 bg-slate-800 text-indigo-500 focus:ring-indigo-400"
          />
          vLLM compatible only
        </label>
        <button
          type="submit"
          className="rounded-lg bg-indigo-500 px-4 py-2 text-sm font-semibold text-white shadow hover:bg-indigo-400"
        >
          Search
        </button>
      </form>

      {!hasQuery && <p className="text-sm text-slate-400">Enter a Hugging Face repo ID to inspect metadata, compatibility, and catalog hints.</p>}
      {hasQuery && hits.length === 0 && (
        <p className="text-sm text-slate-300">No models matched that query. Try a different keyword or remove filters.</p>
      )}
      {hits.length > 0 && (
        <div className="grid gap-3 md:grid-cols-2">
          {hits.map((insight) => (
            <article key={insight.huggingFace.id} className="rounded-2xl border border-white/5 bg-slate-900/30 p-4 shadow-inner">
              <p className="text-xs uppercase tracking-wide text-slate-400">{insight.huggingFace.author}</p>
              <h4 className="text-lg font-semibold text-white">{insight.huggingFace.modelId ?? insight.huggingFace.id}</h4>
              <p className="text-xs text-slate-400">{insight.huggingFace.pipelineTag || 'Unknown pipeline'}</p>
              <div className="mt-2 flex flex-wrap gap-2 text-xs">
                {insight.compatible ? (
                  <span className="rounded-full bg-emerald-500/20 px-2 py-0.5 text-emerald-300">vLLM compatible</span>
                ) : (
                  <span className="rounded-full bg-slate-700/50 px-2 py-0.5 text-slate-300">Needs custom runtime</span>
                )}
                {insight.matchedArchitectures?.map((arch) => (
                  <span key={arch} className="rounded-full bg-slate-800 px-2 py-0.5 text-slate-200">
                    {arch}
                  </span>
                ))}
              </div>
              {insight.huggingFace.tags && insight.huggingFace.tags.length > 0 && (
                <p className="mt-2 text-xs text-slate-400 line-clamp-2">{insight.huggingFace.tags.join(', ')}</p>
              )}
              <div className="mt-3 flex gap-2 text-xs">
                <Link
                  href={`https://huggingface.co/${insight.huggingFace.id}`}
                  target="_blank"
                  className="rounded-md border border-white/10 px-3 py-1 text-slate-200 hover:border-white/30"
                >
                  View on Hugging Face
                </Link>
                {insight.suggestedCatalog?.hfModelId && (
                  <span className="rounded-md border border-emerald-500/40 px-3 py-1 text-emerald-300">
                    Suggested ID: {insight.suggestedCatalog.hfModelId}
                  </span>
                )}
              </div>
            </article>
          ))}
        </div>
      )}
    </div>
  );
}
