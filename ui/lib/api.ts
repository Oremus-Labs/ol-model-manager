import { API_BASE_URL } from './config';
import type { Architecture, HistoryEntry, Job, Model, ModelInsight, SystemInfo, WeightInfo } from './types';

type JSONValue = Record<string, unknown>;

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T | null> {
  try {
    const res = await fetch(`${API_BASE_URL}${path}`, {
      ...init,
      cache: 'no-store',
      headers: {
        Accept: 'application/json',
        ...(init?.headers || {}),
      },
    });
    if (!res.ok) {
      console.error('fetch failed', path, res.status);
      return null;
    }
    return (await res.json()) as T;
  } catch (err) {
    console.error('fetch error', path, err);
    return null;
  }
}

export async function getSystemInfo(): Promise<SystemInfo | null> {
  return fetchJSON<SystemInfo>('/system/info');
}

export async function getModels(): Promise<Model[]> {
  const data = await fetchJSON<Model[]>('/models');
  return data ?? [];
}

export async function getWeights(): Promise<WeightInfo[]> {
  const data = await fetchJSON<{ weights: WeightInfo[] }>('/weights');
  return data?.weights ?? [];
}

export async function getJobs(limit = 20): Promise<Job[]> {
  const data = await fetchJSON<{ jobs: Job[] }>(`/jobs?limit=${limit}`);
  return data?.jobs ?? [];
}

export async function getHistory(limit = 20): Promise<HistoryEntry[]> {
  const data = await fetchJSON<{ events: HistoryEntry[] }>(`/history?limit=${limit}`);
  return data?.events ?? [];
}

export async function getArchitectures(): Promise<Architecture[]> {
  const data = await fetchJSON<{ architectures: Architecture[] }>('/vllm/supported-models');
  return data?.architectures ?? [];
}

export async function searchHuggingFace(params: { query: string; compatibleOnly?: boolean }): Promise<ModelInsight[]> {
  if (!params.query) {
    return [];
  }
  const search = new URLSearchParams();
  search.set('q', params.query);
  if (params.compatibleOnly) {
    search.set('compatibleOnly', 'true');
  }
  const data = await fetchJSON<{ results: ModelInsight[] }>(`/huggingface/search?${search.toString()}`);
  return data?.results ?? [];
}

export async function getCatalogJSON(modelId: string): Promise<JSONValue | null> {
  return fetchJSON(`/catalog/generate`, {
    method: 'POST',
    body: JSON.stringify({ hfModelId: modelId }),
    headers: { 'Content-Type': 'application/json' },
  });
}
