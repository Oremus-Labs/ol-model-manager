'use client';

import { useFormState, useFormStatus } from 'react-dom';
import { activateModelAction } from '@/app/actions';

const initialState = { ok: false, message: '' };

interface ActivateModelFormProps {
  modelId: string;
}

export function ActivateModelForm({ modelId }: ActivateModelFormProps) {
  const [state, formAction] = useFormState(activateModelAction, initialState);

  return (
    <form action={formAction} className="space-y-2 rounded-md border border-slate-800/60 bg-slate-900/40 p-4">
      <input type="hidden" name="id" value={modelId} />
      <SubmitButton idleLabel="Activate model" pendingLabel="Activating…" />
      {state.message && (
        <p className={`text-xs ${state.ok ? 'text-emerald-300' : 'text-rose-300'}`}>{state.message}</p>
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
      className="w-full rounded-md bg-brand-600 px-3 py-2 text-sm font-semibold text-white shadow-md shadow-brand-900/40 transition hover:bg-brand-500 disabled:opacity-40"
    >
      {pending ? pendingLabel : idleLabel}
    </button>
  );
}
