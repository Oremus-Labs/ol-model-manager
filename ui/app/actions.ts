'use server';

import { revalidatePath } from 'next/cache';
import { API_BASE_URL } from '@/lib/config';

type ActionState = {
  ok: boolean;
  message: string;
};

const API_TOKEN = process.env.MODEL_MANAGER_API_TOKEN ?? '';

async function authorizedFetch(path: string, init?: RequestInit) {
  if (!API_TOKEN) {
    throw new Error('MODEL_MANAGER_API_TOKEN env var is not configured');
  }
  const res = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${API_TOKEN}`,
      ...(init?.headers || {}),
    },
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    const error = body?.error || res.statusText;
    throw new Error(typeof error === 'string' ? error : 'Request failed');
  }
  return body;
}

export async function installWeightsAction(_: ActionState, formData: FormData): Promise<ActionState> {
  try {
    const hfModelId = formData.get('hfModelId')?.toString().trim();
    if (!hfModelId) {
      return { ok: false, message: 'Hugging Face model ID is required' };
    }
    const payload = {
      hfModelId,
      revision: formData.get('revision')?.toString().trim() || undefined,
      target: formData.get('target')?.toString().trim() || undefined,
      overwrite: formData.get('overwrite') === 'on',
    };
    const response = await authorizedFetch('/weights/install', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    revalidatePath('/');
    if (response?.job) {
      return { ok: true, message: `Queued job ${response.job.id}` };
    }
    return { ok: true, message: `Weights ready at ${response?.inferenceModelPath || 'PVC'}` };
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : 'Failed to install weights' };
  }
}

export async function deleteWeightsAction(_: ActionState, formData: FormData): Promise<ActionState> {
  try {
    const name = formData.get('name')?.toString();
    if (!name) {
      return { ok: false, message: 'Weight directory required' };
    }
    await authorizedFetch(`/weights`, {
      method: 'DELETE',
      body: JSON.stringify({ name }),
      headers: { 'Content-Type': 'application/json' },
    });
    revalidatePath('/');
    return { ok: true, message: `Deleted ${name}` };
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : 'Failed to delete weights' };
  }
}

export async function activateModelAction(_: ActionState, formData: FormData): Promise<ActionState> {
  try {
    const id = formData.get('id')?.toString();
    if (!id) {
      return { ok: false, message: 'Model ID required' };
    }
    await authorizedFetch('/models/activate', {
      method: 'POST',
      body: JSON.stringify({ id }),
    });
    revalidatePath('/');
    return { ok: true, message: `Activating ${id}` };
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : 'Failed to activate model' };
  }
}

export async function deactivateModelAction(_: ActionState, __?: FormData): Promise<ActionState> {
  try {
    await authorizedFetch('/models/deactivate', {
      method: 'POST',
    });
    revalidatePath('/');
    return { ok: true, message: 'Deactivated active model' };
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : 'Failed to deactivate model' };
  }
}
