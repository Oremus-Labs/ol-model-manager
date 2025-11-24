# Event Stream & Realtime Contract

The Model Manager exposes a server-sent events (SSE) stream at `/events`. Clients connect with `Accept: text/event-stream` and an optional `Authorization: Bearer MODEL_MANAGER_API_TOKEN`.

```bash
export MM_TOKEN=…  # MODEL_MANAGER_API_TOKEN
curl -Ns \
  -H "Accept: text/event-stream" \
  -H "Authorization: Bearer $MM_TOKEN" \
  https://model-manager-api.oremuslabs.app/events
```

The handler seeds the five most recent jobs before switching to live mode. Each SSE frame has:

```json
{
  "id": "uuid-or-seed",
  "type": "event.type",
  "timestamp": "RFC3339",
  "data": { … }
}
```

## Event Types

| Event | Payload Preview | Notes |
| --- | --- | --- |
| `stream.seed.start` / `stream.seed.complete` | `{ "count": 5 }` | Brackets the job backlog sent when a client first connects. |
| `job.pending` / `job.running` / `job.completed` | Full `jobs.Job` struct | Fired by the job manager as Redis workers update installations. `result.storageUri` indicates the PVC path (e.g. `pvc://venus-model-storage/Qwen/Qwen2.5-0.5B-Instruct`). |
| `model.activation.started` | `{ "modelId": "…", "displayName": "…", "runtime": "vllm-runtime", "storageUri": "…", "hfModelId": "…" }` | Emitted immediately after `/models/activate` validates the catalog entry. |
| `model.activation.completed` | `{ "modelId": "…", "displayName": "…", "action": "created|updated" }` | Fired when the KServe client reports success. `model.activation.failed` includes `{ "error": "…" }`. |
| `model.deactivation.started` / `model.deactivation.completed` / `model.deactivation.failed` | Similar payloads to activation | Provide instant feedback for `/models/deactivate`. |
| `model.status.updated` | See below | Produced by the informer-backed runtime monitor whenever the KServe InferenceService, predictor Deployment, or pods change state. |
| `hf.refresh.started` | `{ "queryCount": 6 }` | Sync service kicked off metadata discovery. |
| `hf.refresh.completed` | `{ "count": 150, "duration": "3.2s" }` | Hugging Face cache refreshed successfully. Failure emits `hf.refresh.failed` with `{ "error": "..." }`. |

Example `model.status.updated` payload:

```json
{
  "inferenceService": {
    "name": "active-llm",
    "url": "https://active-llm-ai.oremuslabs.app",
    "ready": "False",
    "conditions": [
      {"type":"IngressReady","status":"True"},
      {"type":"PredictorReady","status":"False","reason":"ProgressDeadlineExceeded","message":"ReplicaSet \"active-llm-predictor-9d7cfcb99\" has timed out progressing."},
      {"type":"Ready","status":"False","reason":"ProgressDeadlineExceeded"}
    ]
  },
  "deployments": [
    {
      "name":"active-llm-predictor",
      "readyReplicas":1,
      "replicas":2,
      "availableReplicas":1,
      "updatedReplicas":1,
      "observedGeneration":2,
      "conditions":[{"type":"Available","status":"True"},{"type":"Progressing","status":"False","reason":"ProgressDeadlineExceeded","message":"ReplicaSet \"active-llm-predictor-9d7cfcb99\" has timed out progressing."}]
    }
  ],
  "pods": [
    {
      "name":"active-llm-predictor-5b99f4d5cc-8bj54",
      "phase":"Running",
      "readyContainers":1,
      "totalContainers":1,
      "gpuRequests":{"amd.com/gpu":"1"},
      "gpuLimits":{"amd.com/gpu":"1"},
      "containers":[{"name":"kserve-container","state":"Running","ready":true,"startedAt":"2025-11-24T14:53:59Z"}]
    },
    {
      "name":"active-llm-predictor-9d7cfcb99-xjknf",
      "phase":"Pending",
      "gpuRequests":{"amd.com/gpu":"1"},
      "conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":"Insufficient amd.com/gpu"}]
    }
  ],
  "gpuAllocations":{"amd.com/gpu":"2"},
  "updatedAt":"2025-11-24T22:30:25.083047098Z"
}
```

## Related REST Endpoints

| Endpoint | Method | Description |
| --- | --- | --- |
| `/events` | GET | SSE stream described above. Requires API token for destructive events (activations, installs). |
| `/jobs` / `/jobs/:id` | GET | Lists historical install/deletion jobs. Matches payload returned via `job.*` events. |
| `/models/status` | GET | Snapshot version of `model.status.updated` suitable for dashboards or health checks. |
| `/huggingface/search?q=term` | GET | Served from the background cache primed by `hf.refresh.*` events. |

These contracts are now fixed so UI/automation clients can rely on a stable schema without additional polling logic.

## GraphQL Endpoint

The GraphQL API lives at `/graphql` (same host as the REST API). Both `GET` and `POST` are enabled. Example request:

```bash
export MM_TOKEN=…
curl -s -X POST https://model-manager-api.oremuslabs.app/graphql \
  -H "Authorization: Bearer $MM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "query": "{ models { id displayName runtime } jobs(limit:3) { id status message } runtimeStatus { inferenceService { ready url } } }"
      }'
```

Available fields mirror the REST contract:

- `models` / `model(id: ID!)` – pull catalog details.
- `jobs(limit: Int)` / `job(id: ID!)` – read async install/delete work items.
- `runtimeStatus` – same payload as `GET /models/status`.
- `huggingFaceModels(query: String, limit: Int, pipelineTag: String)` – list cached metadata or run a live search.

The handler also enables GraphiQL in development (`GET /graphql`) so UI teams can explore the schema interactively.
