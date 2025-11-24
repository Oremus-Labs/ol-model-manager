import { API_BASE_URL } from './config';
import type { ActiveService, Architecture, HistoryEntry, Job, Model, ModelInsight, SystemInfo, WeightInfo } from './types';

const API_TOKEN = process.env.MODEL_MANAGER_API_TOKEN;

type JSONValue = Record<string, unknown>;

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T | null> {
  try {
    const res = await fetch(`${API_BASE_URL}${path}`, {
      ...init,
      cache: 'no-store',
      headers: buildHeaders(init?.headers),
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

function buildHeaders(extra?: HeadersInit): Headers {
  const headers = new Headers(extra);
  headers.set('Accept', 'application/json');
  if (API_TOKEN) {
    headers.set('Authorization', `Bearer ${API_TOKEN}`);
  }
  return headers;
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

export async function searchHuggingFace(params: { query: string }): Promise<ModelInsight[]> {
  if (!params.query) {
    return [];
  }
  const search = new URLSearchParams();
  search.set('q', params.query);
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

export async function getActiveService(): Promise<ActiveService | null> {
  const data = await fetchJSON<ActiveService>('/active');
  return data ?? null;
}
