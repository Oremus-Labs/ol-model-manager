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
- New `/models/status` endpoint serves the cached snapshot instantly; `curl https://model-manager-api.oremuslabs.app/models/status` now reports the ISVC URL, deployment replica counts, and pod readiness.
- `kubectl logs deploy/model-manager-sync` and `deploy/model-manager-api` show the informers starting up, and `timeout 5s curl -Ns …/events` includes live `model.status.updated` payloads when pods cycle.
Phase 4 – UI/client integration hooks
REST+WS contract

Document event payloads, job fields, HF records.
Provide /events sample.
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
