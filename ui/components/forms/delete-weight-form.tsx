'use client';

import { useFormState, useFormStatus } from 'react-dom';
import { deleteWeightsAction } from '@/app/actions';

const initialState = { ok: false, message: '' };

interface DeleteWeightFormProps {
  name: string;
}

export function DeleteWeightForm({ name }: DeleteWeightFormProps) {
  const [state, formAction] = useFormState(deleteWeightsAction, initialState);

  return (
    <form action={formAction} className="space-y-2">
      <input type="hidden" name="name" value={name} />
      <SubmitButton idleLabel="Delete weights" pendingLabel="Deleting…" />
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
      className="w-full rounded-md border border-rose-400/40 px-2 py-1 text-xs font-semibold text-rose-200 transition hover:bg-rose-500/10 disabled:opacity-40"
    >
      {pending ? pendingLabel : idleLabel}
    </button>
  );
}
