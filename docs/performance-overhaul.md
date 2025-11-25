Phase 0 – Foundation (services & scaffolding)
Split the repo into packages

Create internal/api (HTTP handlers), internal/events (pub/sub client), internal/jobs (queue interfaces), internal/hf (cache).
Keep cmd/server (control plane) but prepare separate entrypoints (cmd/worker, cmd/sync).
Introduce state store + redis clients

Add Postgres/SQLite DAO module (using sqlc or pgx).
Add Redis client (Streams for jobs, Pub/Sub for events, KV for caching). Environment-driven config.
WebSocket/SSE plumbing

Control-plane server now exposes /events (WS or SSE).
Implement auth (reuse API token) and event multiplexer.
Kubernetes deployment changes

Replace Knative Service with Deployment + Service (we already have Helm chart: add new deployments for api, worker, sync).
Update chart templates to include Redis/Postgres connection secrets, liveness probes, etc.
Git workflow for this phase

Work locally, run go test ./....
git add all new packages + chart changes, git commit -m "feat: add control plane scaffolding" (short logical commits).
Build/push API image:
docker build -t ghcr.io/oremus-labs/ol-model-manager:0.X.0 .
docker push ghcr.io/oremus-labs/ol-model-manager:0.X.0
Update ol-kubernetes-cluster values (deploymentRevision, image.tag) and commit/push there.
argocd app sync workloads-ai-model-manager; watch until Healthy.
kubectl get pods -n ai, confirm model-manager-api, model-manager-worker, etc.
Hit /health, /events to ensure base server runs.

✅ Current status (Nov 24):
- `internal/api` + middleware/metrics + `/events` SSE endpoint landed.
- Redis/Postgres config + clients wired; SQLite and Postgres both supported.
- Event bus publishes job updates; handlers stream them via SSE (now seeded + flushed).
- Redis Stream queue + worker wiring complete: `/weights/install` publishes to the stream, `cmd/worker` consumes via a consumer group, and jobs only run inline when Redis is unavailable.
- New binaries `cmd/worker` and `cmd/sync` run as Deployments behind Traefik ingress; Knative Service fully removed.
Phase 1 – Jobs queue + worker
Create job producer API

/weights/install now enqueues to Redis Stream (job ID, HF args, target path).
Response returns jobId. Write entry in Postgres with status=pending.
Implement Go worker

cmd/worker: consumes from Redis Stream, performs download, writes progress events (job.progress, job.completed) via Pub/Sub and updates Postgres row.
Use hf module to pull metadata, track bytes downloaded.
Add WebSocket event fan-out

Control-plane listens to Redis Pub/Sub and pushes serialized events to connected clients (job progress, status updates).
Provide /jobs/{id} REST for history.
Git/GitOps steps

Commit worker code + Dockerfile target (docker build -f Dockerfile.worker ...). Tag image 0.X.1.
Helm chart: add Deployment model-manager-worker, configs, RBAC to access PVC. Commit to ol-kubernetes-cluster.
Push both repos; argocd app sync workloads-ai-model-manager.
Verify: kubectl logs deploy/model-manager-worker, run curl -X POST /weights/install ... and watch kubectl get pods -w to ensure job executes; check WebSocket stream with wscat or curl SSE.

**Verification – 2025-11-24**
- Built/tagged `ghcr.io/oremus-labs/ol-model-manager:0.5.3-go`, pushed, and rolled it out via GitOps (`kubectl apply -f clusters/…/appsets/workloads.yaml` + `argocd app sync workloads-ai-model-manager`).
- Confirmed `model-manager-api`, `model-manager-worker`, and `model-manager-sync` Deployments restarted with the new env vars (`REDIS_JOB_STREAM`, `REDIS_JOB_GROUP`) and passed `/healthz` behind the Traefik ingress (`https://model-manager-api.oremuslabs.app/healthz`).
- Exercised `POST /weights/install` with `hfModelId=sshleifer/tiny-gpt2`; response returned job `9fb94e3d-478d-48fd-abcc-90e7038cdb3b` and `storageUri=pvc://venus-model-storage/sshleifer/tiny-gpt2`.
- `GET /jobs/9fb94e3d-478d-48fd-abcc-90e7038cdb3b` reported `status=completed` and `progress=100` after Redis processed the queue entry (size ≈4.5 MiB).
- `curl -N https://model-manager-api.oremuslabs.app/events` now seeds immediate `job.completed` SSE payloads without polling, so the UI can reflect progress live.
- Follow-up fix: SSE frames now carry the job ID in the SSE `id` header, preserve the datastore timestamp, and bracket the replay backlog with `stream.seed.start/complete` markers so clients can distinguish history from live updates.
- Helm chart now provisions a dedicated Redis deployment + PVC (`model-manager-redis`) managed by the Longhorn storage class for automatic scheduling on the cluster; api/worker/sync default their `REDIS_ADDR` to the in-cluster service unless overridden, so the worker stream consumer no longer falls back to heartbeat mode and there is no per-node host-path prep required.
- Verified rollout via `argocd app sync workloads-ai-model-manager`; the new `model-manager-redis` Deployment and PVC (`longhorn` storage class) reported Healthy/Bound, and `kubectl logs deploy/model-manager-worker` now shows `worker connected to Redis queue; waiting for jobs`.
- Triggered `POST /weights/install` for `sshleifer/tiny-gpt2` (target `sshleifer-tiny-gpt2-redis-test`) using the public API; job `b05eec49-7f45-4ef7-b0ff-b5269eb17cdf` moved to `status=completed`, the weights landed under `/mnt/models/sshleifer-tiny-gpt2-redis-test`, and the SSE stream emitted the corresponding `job.completed` event.

Phase 2 – Hugging Face cache + background sync
Background sync service (cmd/sync)

Periodically pulls HF metadata via API, stores sanitized entries in Redis (key hf:models:<id>) and Postgres (for history).
Emits hf.refresh.completed events.
API

/huggingface/search: query Redis cache; fallback to live call triggers background refresh.
/huggingface/refresh: enqueues refresh job (or signals sync service).
UI API

Add endpoints to fetch curated lists, vLLM compatibility, etc.
Git workflow

Build/push new sync image (docker build -f Dockerfile.sync ...).
Helm chart: add Deployment model-manager-sync.
Commit/push code & chart, sync via Argo.
Verify via logs (kubectl logs deploy/model-manager-sync) and API hitting /huggingface/search.

**Verification – 2025-11-24**
- Introduced `internal/hfcache` + datastore schema so Hugging Face snapshots persist to SQLite/Redis.
- `cmd/sync` now mounts the `model-manager-state` PVC, consumes Redis/events, and on startup refreshes the top text-generation models (GitOps tag `ghcr.io/oremus-labs/ol-model-manager:0.5.6-go`).
- `kubectl logs deploy/model-manager-sync -n ai | tail` shows `refreshed 7 Hugging Face models`; SSE emits `hf.refresh.completed` with the cached count.
- `/huggingface/search?q=Qwen` now returns the cached models instantly (see curl response captured during verification) and falls back to live discovery only when `compatibleOnly=true`.
Phase 3 – Kubernetes informers & live status
Informer module

In control-plane create shared informer factories for serving.kserve.io/v1beta1 InferenceServices and Deployments/Pods.
On events, update in-memory cache (e.g., sync.Map) and emit status events via Pub/Sub (model.status.updated).
Update /models/status to serve from this cache (instant).
Deployment/Argo

No new image necessary unless code changed: rebuild/push 0.X.3, update Helm with new env if needed.
argocd app sync ...
Verify: kubectl logs deploy/model-manager-api | grep informer, ensure events fire when scaling an InferenceService.

**Verification – 2025-11-24**
- Added `internal/status.Manager` which watches the active InferenceService + labeled Deployments/Pods and emits `model.status.updated` SSE events whenever readiness changes.
- `/models/status` now returns enriched data (deployment conditions, per-pod reasons/messages, container state, GPU request/limit maps, and aggregated GPU allocations) so the UI can render detailed health cards without extra API calls.
- SSE stream carries `model.activation.started/completed/failed`, `model.deactivation.*`, `hf.refresh.started/completed/failed`, and `model.status.updated` payloads—verified via `timeout 5s curl -Ns …/events`, which now shows the richer event types alongside historical job completions.
- `kubectl logs deploy/model-manager-sync` and `deploy/model-manager-api` show the informers starting up, and `curl https://model-manager-api.oremuslabs.app/models/status` reflects the live GPU totals and pod-level telemetry after scaling the InferenceService.
Phase 4 – UI/client integration hooks
REST+WS contract

- Added `docs/events.md` documenting every SSE payload (`job.*`, `model.activation.*`, `model.status.updated`, `hf.refresh.*`) plus curl examples and references to `/jobs` + `/models/status`.
- `/events` sampling verified via `timeout 10s curl -Ns …/events` which now surfaces `model.activation.started/completed` frames (job `qwen2.5-0.5b-instruct` tested at 2025‑11‑24T22:30Z).
- GraphQL endpoint exposed at `/graphql` (powered by `github.com/graphql-go/graphql`) covers models, jobs, runtime status, and Hugging Face cache queries; documented in `docs/events.md` and validated via `curl -X POST …/graphql -d '{"query":"{ models { id } jobs(limit:1) { id status } }"}'`.

Optional GraphQL layer

If we go GraphQL, add cmd/api resolvers for queries/subscriptions (gqlgen). But can skip if REST+WS suffices.
No major Kubernetes change here.

Phase 5 – Observability & polish
Metrics/logging

Expose Prometheus metrics: job durations, HF refresh times, active connections.
Add structured logging.
Failure handling

Retries/backoff for workers, job cancellation endpoint, disk cleanup jobs.
CI/CD & verification checklist for every phase
go test ./... before committing.
docker build ... && docker push ghcr.io/...:0.X.Y for each service changed.
Update Helm chart values (apps/workloads/ai-model-manager/chart/values.yaml) and templates as needed; bump deploymentRevision.
In ol-kubernetes-cluster, git add and git commit -m "chore: bump model-manager to 0.X.Y"; push.
Run argocd app sync workloads-ai-model-manager. Watch until Healthy.
Post-deploy, validate:
kubectl get pods -n ai (all new deployments running).
Hit /health, /events, /jobs endpoints.
Run a sample install job and observe real-time updates via WebSocket or API.
For background sync, see logs and cached responses.

## CLI & Control-Plane Roadmap (`mllm`)

We are building a Docker/Kubectl-grade CLI that interacts with every facet of the control plane.

### CLI Foundation
- Go/cobra binary, config in `~/.config/mllm/config.yaml` with multiple contexts.
- Global flags: `--context`, `--namespace`, `-o table|json|yaml`, `--watch`.
- Core commands: `mllm status`, `mllm login`, `mllm config set-context/use-context`, `mllm completion bash|zsh|fish`.

### Model Lifecycle
- `mllm models list/get/describe/history`.
- YAML authoring workflow: `init`, `validate`, `apply`, `diff`, `delete`.
- Rollouts: `mllm models promote <id> --strategy blue-green`, `rollout status`, `models history`.

### Weights Management
- `mllm weights list/install/delete/prune/usage/verify`.
- Future: rsync/export commands for off-cluster backups.

### Jobs & Diagnostics
- `mllm jobs list/describe/logs --follow`.
- Cancellation and retry controls once backend supports them.

### Runtime & Activations
- `mllm runtime status --watch`, `runtime events --since`.
- `mllm runtime logs <pod> --container kserve`, `runtime diagnose`, future `runtime scale/exec`.

### Hugging Face Discovery
- `mllm hf search/describe/cache refresh`.
- Show compatibility, recommended catalog snippets, file breakdown.

### Secrets, Policy, Notifications
- `mllm secrets list/set/get/delete`.
- `mllm notify set --slack-webhook ...`.
- `mllm policy list/apply`, `audit list --since`.

### Backup & Maintenance
- `mllm backup run`, `backup list/restore`.
- `mllm cleanup orphaned-weights --dry-run`.

### Plugins & Advanced UX
- `mllm plugin list/install/remove`, pass-through to `mllm-*` binaries.
- JSONPath output filtering, context awareness, progress spinners.

### Backend Support Needed
- Activation staging/rollout endpoints, placement recommendations.
- Managed secrets API, audit log exports, backup triggers.
- Job log storage, cancellation API, notifications/webhooks.
- YAML schema publication and validation endpoints.

Implementation phases:
1. CLI scaffolding + config + `status`/`models list`.
2. YAML workflow (init/validate/apply/diff).
3. Job/weights commands with streaming logs.
4. Activation rollout & placement intelligence.
5. Secrets/notifications/backup.
6. Plugins, completion, documentation polish.

**Verification – 2025-11-24**
- `cmd/mllm` bootstrap landed with cobra-based root command, persistent config (`~/.config/mllm/config.yaml`), and initial verbs:
  - `mllm config set-context`, `use-context`, `current-context`, and `view`.
  - `mllm status` (pulls `/system/info`) and `mllm models list/get`.
- `go test ./...` covers the new package, and running `mllm status --server https://model-manager-api.oremuslabs.app --token $MM_TOKEN` shows live control-plane stats.
- ✅ **Phase 2 (YAML workflow) – 2025-11-24**
  - Added `mllm models init|validate|apply|diff` with YAML authoring helpers, manifest-to-JSON conversion, and validation summaries that mirror backend checks.
  - API helpers (`PostJSON`, `PostRawJSON`) plus diffing via `cmp.Diff` enable parity with `kubectl apply` workflows, and validation results print both SSE-friendly JSON and tabular reports.
  - Verification: created `/tmp/qwen-manifest.yaml` via `mllm models init --id qwen2.5-0.5b-instruct ...`, then ran `mllm models validate ...` and `mllm models apply ...` against `https://model-manager-api.oremuslabs.app` using the production token; outputs showed pass/warn checks and successfully hit `/catalog/validate`. `mllm models diff ...` returned the structured differences between the new manifest and the live catalog while `go test ./...` stayed green.
- ✅ **Phase 3 (Jobs & weights UX + GPU guard) – 2025-11-24**
  - Introduced `mllm jobs list|get|watch` for tailing Redis-backed installs, and `mllm weights list|usage|info|install|delete` for PVC management. `--watch` streams SSE frames, so large downloads show progress without polling.
  - Added runtime-awareness to `mllm weights install`: if the active InferenceService monopolises the lone GPU, the CLI prompts (or `--preempt-active` auto-confirms) to deactivate `active-llm`, waits for the pending pods to drain, then re-activates the model after installation. This prevents the “pod never schedules” failure mode.
  - Verified end-to-end by staging multiple `sshleifer/tiny-gpt2` targets, exercising `mllm jobs watch <job-id>`, and confirming the worker logs and SSE stream report `weight_install_completed`. After forcing a GPU contention scenario, the CLI now warns, deactivates, and reactivates automatically once jobs finish.

---

## UI Track – Docker Desktop Clone

### Phase A – Architecture & Layout Mapping
- Audit Docker Desktop’s Containers, Images, Extensions, Settings screens to capture spacing, typography, and interactions.
- Define navigation: left rail (Dashboard, Models, Jobs, Weights, HF Library, Settings) and top bar (search, cluster selector, user avatar, sync pill).
- Scaffold Next.js routes/layouts matching that IA.

### Phase B – Design System & Components
- Recreate Docker’s visual language (color palette, gradients, typography, corner radii).
- Build reusable primitives: Nav rail, top bar, cards, statistic tiles, tables, drawers, bottom log panel, toasts, modals.
- Implement a global layout wrapper (sidebar + top bar) shared by all pages.

### Phase C – View Implementations
1. **Dashboard** – live tiles (Active Model, Jobs, HF cache, Activity timeline) with streaming data.
2. **Models** – table view with right-hand drawer showing overview, config, events, logs; quick actions (Deploy, Validate).
3. **Jobs** – card/list view with progress rings, filters, detail drawer + log stream.
4. **Weights** – list of installed weights (path, size, last access) with actions (delete, validate).
5. **HF Library** – marketplace grid with search/filter, curated collections, detail drawer for staging defaults.
6. **Settings** – tabs (General, HF credentials, GPU profiles, API tokens, notifications) using Docker-like controls.

### Phase D – Real-time Data Binding
- Build a WebSocket client (React context) that subscribes to backend events and exposes hooks (`useJobEvents`, `useModelStatus`).
- Combine REST (React Query) for initial fetch with live patches from WS events.
- Log drawers stream job/model logs via event bus; provide pause/clear controls identical to Docker Desktop.
- Surface connection health/offline states in the top bar.

### Phase E – Polish & Quality
- Add keyboard shortcuts (Ctrl+K global search, Cmd+R refresh view).
- Implement light/dark theming toggle (matching Docker’s).
- Responsive behaviors: sidebar collapse, drawers convert to modals on narrow widths.
- Testing: Storybook visual regression, Playwright E2E for install/deploy/monitor flows.

### Delivery Workflow
1. Develop UI behind a `DESKTOP_UI` feature flag; progressively replace legacy pages.
2. For each milestone:
   - `git add` and `git commit -m "ui: add docker-style <view>"`.
   - Build/push UI image: `docker build -f ui/Dockerfile -t ghcr.io/oremus-labs/model-manager-ui:<tag> .` then push.
   - Update `ol-kubernetes-cluster/apps/workloads/model-manager-ui` values (`image.tag`, `deploymentRevision`), commit/push.
   - Run `argocd app sync workloads-model-manager-ui`; verify via browser and `kubectl logs`.
3. Once complete, remove feature flag and document the new experience (screenshots, quickstart video).
