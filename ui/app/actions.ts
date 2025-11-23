'use server';

import { revalidatePath } from 'next/cache';
import { API_BASE_URL } from '@/lib/config';

type ActionState = {
  ok: boolean;
  message: string;
};

async function authorizedFetch(path: string, token: string, init?: RequestInit) {
  if (!token) {
    throw new Error('API token is required');
  }
  const res = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
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
    const token = formData.get('token')?.toString().trim() || '';
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
    const response = await authorizedFetch('/weights/install', token, {
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
    const token = formData.get('token')?.toString().trim() || '';
    const name = formData.get('name')?.toString();
    if (!name) {
      return { ok: false, message: 'Weight directory required' };
    }
    await authorizedFetch(`/weights/${encodeURIComponent(name)}`, token, {
      method: 'DELETE',
    });
    revalidatePath('/');
    return { ok: true, message: `Deleted ${name}` };
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : 'Failed to delete weights' };
  }
}

export async function activateModelAction(_: ActionState, formData: FormData): Promise<ActionState> {
  try {
    const token = formData.get('token')?.toString().trim() || '';
    const id = formData.get('id')?.toString();
    if (!id) {
      return { ok: false, message: 'Model ID required' };
    }
    await authorizedFetch('/models/activate', token, {
      method: 'POST',
      body: JSON.stringify({ id }),
    });
    revalidatePath('/');
    return { ok: true, message: `Activating ${id}` };
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : 'Failed to activate model' };
  }
}
