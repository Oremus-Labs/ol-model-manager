export interface SystemInfo {
  version: string;
  catalog?: {
    root?: string;
    modelsDir?: string;
    count?: number;
    lastRefresh?: string;
  };
  weights?: {
    path?: string;
    pvcName?: string;
  };
  state?: {
    path?: string;
    enabled?: boolean;
  };
  auth?: {
    enabled?: boolean;
  };
  cache?: {
    catalogTTL?: string;
    huggingfaceTTL?: string;
    vllmTTL?: string;
    recommendationsTTL?: string;
  };
  persistence?: {
    driver?: string;
    dsn?: string;
    pvcName?: string;
    stateDir?: string;
  };
  notifications?: {
    slackWebhookConfigured?: boolean;
    pvcAlertThreshold?: number;
  };
  gpu?: {
    profilesPath?: string;
    inventorySource?: string;
  };
  storage?: WeightStats;
  gpuProfiles?: GPUProfile[];
  recentJobs?: Job[];
  recentHistory?: HistoryEntry[];
}

export interface Model {
  id: string;
  displayName?: string;
  hfModelId?: string;
  runtime?: string;
  storageUri?: string;
  vllm?: Record<string, unknown>;
  resources?: Record<string, unknown>;
}

export interface WeightInfo {
  path: string;
  name: string;
  sizeBytes: number;
  sizeHuman: string;
  modifiedTime: string;
  fileCount: number;
}

export interface WeightStats {
  totalBytes?: number;
  totalHuman?: string;
  usedBytes?: number;
  usedHuman?: string;
  availableBytes?: number;
  availableHuman?: string;
  modelCount?: number;
  models?: WeightInfo[];
}

export type JobStatus = 'pending' | 'running' | 'completed' | 'failed';

export interface Job {
  id: string;
  type: string;
  status: JobStatus;
  stage?: string;
  progress?: number;
  message?: string;
  payload?: Record<string, unknown>;
  result?: Record<string, unknown>;
  error?: string;
  createdAt: string;
  updatedAt: string;
}

export interface HistoryEntry {
  id: string;
  event: string;
  modelId?: string;
  metadata?: Record<string, unknown>;
  createdAt: string;
}

export interface GPUProfile {
  name: string;
  description?: string;
  memoryGB?: number;
  vendor?: string;
  labels?: Record<string, string>;
}

export interface Architecture {
  name: string;
  className?: string;
  description?: string;
}

export interface HuggingFaceModelMeta {
  id: string;
  modelId?: string;
  author?: string;
  sha?: string;
  downloads?: number;
  likes?: number;
  tags?: string[];
  pipelineTag?: string;
  config?: Record<string, unknown>;
}

export interface ModelInsight {
  huggingFace: HuggingFaceModelMeta;
  compatible: boolean;
  matchedArchitectures?: string[];
  suggestedCatalog?: Model;
  recommendedFiles?: string[];
  notes?: string[];
}
