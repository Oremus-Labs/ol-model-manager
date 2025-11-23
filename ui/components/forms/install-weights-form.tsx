'use client';

import { useFormState, useFormStatus } from 'react-dom';
import { installWeightsAction } from '@/app/actions';

const initialState = { ok: false, message: '' };

export function InstallWeightsForm() {
  const [state, formAction] = useFormState(installWeightsAction, initialState);

  return (
    <form action={formAction} className="space-y-4">
      <div className="grid gap-4 md:grid-cols-2">
        <label className="flex flex-col text-sm">
          Hugging Face Model ID
          <input
            name="hfModelId"
            placeholder="Qwen/Qwen2.5-0.5B-Instruct"
            className="mt-1 rounded-md border border-slate-700 bg-slate-900/60 px-3 py-2 text-base focus:border-brand-500 focus:outline-none"
            required
          />
        </label>
        <label className="flex flex-col text-sm">
          Target Folder
          <input
            name="target"
            placeholder="qwen2.5-0.5b-instruct"
            className="mt-1 rounded-md border border-slate-700 bg-slate-900/60 px-3 py-2 text-base focus:border-brand-500 focus:outline-none"
          />
        </label>
        <label className="flex flex-col text-sm">
          Git Revision / Branch
          <input
            name="revision"
            placeholder="main"
            className="mt-1 rounded-md border border-slate-700 bg-slate-900/60 px-3 py-2 text-base focus:border-brand-500 focus:outline-none"
          />
        </label>
        <label className="flex flex-col text-sm">
          API Token
          <input
            name="token"
            type="password"
            placeholder="Paste MODEL_MANAGER_API_TOKEN"
            className="mt-1 rounded-md border border-slate-700 bg-slate-900/60 px-3 py-2 text-base focus:border-brand-500 focus:outline-none"
            required
          />
        </label>
      </div>
      <label className="flex items-center gap-2 text-sm text-slate-300">
        <input type="checkbox" name="overwrite" className="accent-brand-500" />
        Overwrite existing directory if it exists
      </label>
      <SubmitButton idleLabel="Install weights" pendingLabel="Queuing install…" />
      {state.message && (
        <p className={`text-sm ${state.ok ? 'text-emerald-300' : 'text-rose-300'}`}>{state.message}</p>
      )}
    </form>
  );
}

function SubmitButton({ idleLabel, pendingLabel }: { idleLabel: string; pendingLabel: string }) {
  const { pending } = useFormStatus();
  return (
    <button
      type="submit"
      disabled={pending}
      className="inline-flex w-full items-center justify-center rounded-md bg-gradient-to-r from-brand-500 to-purple-500 px-4 py-2 font-semibold shadow-lg shadow-brand-900/40 transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40"
    >
      {pending ? pendingLabel : idleLabel}
    </button>
  );
}
