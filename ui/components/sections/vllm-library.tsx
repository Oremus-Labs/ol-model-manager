import type { Architecture } from '@/lib/types';

interface Props {
  items: Architecture[];
}

const curated = [
  {
    label: 'Qwen2.5-0.5B Instruct',
    id: 'Qwen/Qwen2.5-0.5B-Instruct',
    notes: '1 GPU · 96GB VRAM',
  },
  {
    label: 'Nous Hermes 2 Mixtral 8x7B',
    id: 'NousResearch/Nous-Hermes-2-Mixtral-8x7B-DPO',
    notes: 'Multi-GPU recommended',
  },
  {
    label: 'Meta Llama3 8B Instruct',
    id: 'meta-llama/Meta-Llama-3-8B-Instruct',
    notes: 'Requires trust_remote_code',
  },
];

export function VLLMLibrary({ items }: Props) {
  return (
    <div className="space-y-6" id="architectures">
      <div className="grid gap-4 md:grid-cols-3">
        {curated.map((model) => (
          <div key={model.id} className="glass-panel p-4 text-sm text-slate-300">
            <p className="text-xs uppercase tracking-wide text-slate-400">Curated loadout</p>
            <h4 className="text-lg font-semibold text-white">{model.label}</h4>
            <p className="text-xs text-slate-400">{model.notes}</p>
            <p className="mt-2 truncate rounded border border-white/10 bg-slate-900/60 px-2 py-1 text-xs font-mono text-slate-200">
              {model.id}
            </p>
            <button
              className="mt-3 inline-flex items-center rounded-lg bg-slate-800 px-3 py-1 text-xs text-slate-200"
              onClick={() => navigator.clipboard?.writeText(model.id)}
              type="button"
            >
              Copy ID
            </button>
          </div>
        ))}
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {items.map((arch) => (
          <div key={arch.name} className="rounded-2xl border border-white/5 bg-slate-900/30 p-4 text-sm">
            <p className="text-sm font-semibold text-white">{arch.name}</p>
            {arch.description && <p className="text-xs text-slate-400">{arch.description}</p>}
            <p className="text-xs text-slate-500">{arch.className}</p>
          </div>
        ))}
      </div>
    </div>
  );
}
