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

Backups & cleanup endpoints

- Datastore migrations now create a `backups` table and the API exposes `GET /backups` + `POST /backups` for tracking PVC/S3 snapshots. `/cleanup/weights` accepts a list of cached model directories and kicks off deletion jobs that publish `job.completed` frames once the directories are removed.
- CLI parity landed via `mllm backups list|record` and `mllm cleanup weights --name <dir>` complete with table/JSON output.
- Verification (2025‑11‑25):
  - Rebuilt/pushed `ghcr.io/oremus-labs/ol-model-manager:0.5.17-go`, bumped the Helm chart/AppSet to the new tag + deployment revision, applied `clusters/…/appsets/workloads.yaml`, and `argocd app sync workloads-ai-model-manager` until Healthy. `kubectl get deploy model-manager-{api,worker,sync}` now shows the 0.5.17-go image and the worker log prints `worker connected to Redis queue; waiting for jobs`.
  - Recorded a manual backup via `go run ./cmd/mllm --config ~/.config/mllm/config.yaml backups record --type manual --location pvc://venus-model-storage/snapshot-20251125 --notes pre-phase4-verify`, then confirmed it with `mllm backups list` (ID `52078c80-2494-4365-aba3-57b0a369d5ce`).
  - Installed `sshleifer/tiny-gpt2` into `cleanup-demo` (`mllm weights install … --watch`) and immediately removed it with `mllm cleanup weights --name cleanup-demo`; `mllm weights list` now only shows the long-lived `deepseek-live-demo` payload, proving the cleanup route scrubs the PVC directory and returns structured results.
  - `timeout 5s curl -Ns -H "Accept: text/event-stream" -H "Authorization: Bearer $MM_TOKEN" https://model-manager-api.oremuslabs.app/events` demonstrates the richer stream (seed markers + the recent job completion IDs), so the UI/CLI can rely on live telemetry for installs without polling.

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

**Next Up (Phase 5B–5D)**

- Notifications: backend `/notifications` CRUD + Slack/webhook sender, CLI verbs `mllm notify list/add/remove/test`.
- API tokens: `/tokens` issuance/rotation with scopes; CLI `mllm tokens issue/revoke/list`.
- Policy/audit: surface history via `/audit` + `mllm audit list --since 24h`, policy templating `mllm policy apply`.
- Runtime diagnostics: `mllm runtime logs <pod>`, `runtime events --since`, `runtime diagnose` (pulls describe/log summaries).
- Rollout UX: `mllm models promote`, blue/green tracking, `mllm models history --follow`.
- Backup & cleanup: `/backups` endpoints + CLI `mllm backup run/list/restore`, `mllm cleanup orphaned-weights --dry-run`.
- Advanced UX: global search (`mllm search <query>` hitting a combined endpoint), JSONPath render (`-o jsonpath=`), shell completions + plugin loader.

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

### Phase 6 – Docker-Desktop-grade backend/CLI polish

To unlock a faithful Docker Desktop-like UI we need a final backend/CLI sweep. Each sub-phase below is designed to be implementable in one development burst (code + tests + docs + deploy) so we never lose context. Once Phase 6A–6D land, all UI features can rely on first-class APIs/commands rather than bespoke glue.

#### Phase 6A – Job lifecycle + dashboard summaries
1. **Datastore extensions**: Add `attempt`, `max_attempts`, `cancelled_at`, and `logs` (JSON) fields to the `jobs` table/migrations. Update `internal/store` structs + helpers accordingly.
2. **Job controller APIs**:
   - `POST /jobs/:id/cancel` → sets status `cancelled` if pending/running, emits `job.cancelled`.
   - `POST /jobs/:id/retry` → requeues a failed/cancelled job if `attempt < maxAttempts`.
   - `GET /jobs/:id/logs` → returns structured history/log entries (source: worker logutil + datastore `logs` field).
3. **Worker support**: Runner honors `cancelled` jobs (stop processing), increments attempt count, records log lines into the job record, and supports backoff before retrying.
4. **CLI parity**: Add `mllm jobs cancel <id>`, `mllm jobs retry <id>`, `mllm jobs logs <id> [--follow]` using SSE for live log streaming.
5. **System summary API**: Implement `/system/summary` returning cards for catalog size, weights installed, active runtime health, job queue depth, HF cache stats, PVC usage, alert banners. Backed by cached status/metrics structures.
6. **Docs/tests**: Update README + this roadmap, expand handler and store tests, `go test ./...`.

✅ **Phase 6A (2025-11-25)**  
- Job schema now tracks attempts, maxAttempts, cancellation timestamps, and structured log entries; Redis worker respects cancellations and publishes `job.log` SSE events.  
- New endpoints: `POST /jobs/:id/cancel`, `POST /jobs/:id/retry`, `GET /jobs/:id/logs`, plus `/system/summary` for dashboard cards. CLI gained `mllm jobs cancel|retry|logs`, enhanced `mllm status` output, and job log streaming.  
- Store/count helpers + summary endpoint power the Docker Desktop-style dashboard cards (weights usage, job counts, queue depth, alerts).  
- Verification: `go test ./...`, manual CLI flows (`mllm jobs retry --watch`, `mllm jobs logs --follow`, `mllm status`) against the live API after enabling the new routes.  
- Provisioned a dedicated PostgreSQL 15 instance (`helm upgrade --install model-manager-postgres bitnami/postgresql ... --set global.storageClass=longhorn`) and pointed the model-manager Helm release at it via new Helm parameters (`datastore.driver=postgres`, `datastore.dsn=postgresql://modelmanager:modelmanager-secret@model-manager-postgres-postgresql.ai.svc.cluster.local:5432/modelmanager?sslmode=disable`). Re-running the large weight install + `mllm jobs cancel` no longer produces `SQLITE_BUSY`, proving cancellation/log writes behave cleanly on Postgres. 2025-11-25 validation: job `4d817214-9082-4f03-b2c2-94852dae0f72` was cancelled mid-download and `mllm jobs logs` returned the persisted `Job cancelled via API` entry while the earlier queue-only cancel left no log, as expected.

#### Phase 6B – Activation/deactivation workflows + playbooks
1. **Runtime endpoints**:
   - `POST /runtime/activate` (model ID + options) orchestrates catalog selection, KServe apply, and history logging.
   - `POST /runtime/deactivate` and `POST /runtime/promote` support blue/green or direct swaps with safety checks.
2. **Eventing**: emit `model.activation.started|completed|failed` with metadata (model, attempts, duration).
3. **Playbooks**: Add `/playbooks` CRUD storing curated install+activate templates (JSON/YAML). Support `POST /playbooks/:name/run`.
4. **CLI commands**: `mllm runtime activate`, `mllm runtime deactivate`, `mllm runtime switch`, plus `mllm playbooks list/get/run`. Include prompts (`--yes`) and `--watch` for rollout progress.
5. **Verification**: Integration smoke tests (mocked KServe), doc updates, full deploy pipeline.

✅ **Phase 6B (2025-11-25)**  
- Added `/runtime/activate`, `/runtime/deactivate`, and `/runtime/promote` so the UI/CLI can surface richer activation metadata (strategy, previous model, requester) without parsing the legacy `/models/*` endpoints. All paths reuse the shared `activateModelInternal` helper so events/history stay consistent, and `/runtime/promote` sanity-checks the current active model before flipping.  
- Introduced datastore-backed playbooks (`store.Playbook`) along with CRUD + `/playbooks/:name/run`. Running a playbook schedules weight installs via the same queue helpers (returning job IDs + storage URIs) and optionally auto-activates when possible; otherwise the API marks the activation step as `pending_install` so the CLI/UI can finish the workflow.  
- `mllm runtime activate|deactivate|status|switch` now call the runtime endpoints and fallback to the legacy routes if the cluster is still on an older build. `mllm playbooks list|get|apply|delete|run` provides Docker-Desktop-style automation, including `--watch` and `--auto-activate` to wait for installs and promote as soon as they complete. Responses reuse the existing SSE activation watcher so `--watch` streams lifecycle events in real-time.

#### Phase 6C – Global search, support bundle, quick actions
1. **Search API**: Add `/search?q=` returning aggregated results (models, weights, jobs, HF models, notifications). Support filters/pagination.
2. **CLI**: implement `mllm search <term> [--type ...]` printing ranked cards with “suggested next action” hints and `--open` to jump to details.
3. **Support bundle**: `/support/bundle` packages recent API/worker/sync logs + metrics snapshots. CLI `mllm support bundle` downloads the archive locally (optionally uploads to S3).
4. **Audit**: ensure search/support actions record audit log entries surfaced via existing `/history`.

✅ **Phase 6C (2025-11-25)**  
- `/search` now fans out across catalog models, cached weights, jobs, cached Hugging Face metadata, and notification channels. The handler ranks matches by fuzzy-score, annotates suggested next actions (e.g., `mllm runtime activate <id>` or `mllm jobs logs <id>`), and records an audit history entry for every request. The CLI gained `mllm search` with `--type`, `--limit`, and `--open` flags so operators can surface the same data from their terminal.  
- Added `/support/bundle` which snapshots the latest system summary, runtime status, weight stats, job list, history, notification config, Hugging Face cache, and Prometheus metrics into a ZIP archive. `mllm support bundle --output support-bundle-<timestamp>.zip` downloads the archive locally for escalations. Both API and CLI flows write history entries so audit timelines capture bundle generation.

#### Phase 6D – Notifications, metrics polish, alerts
1. **Notification history**: Expand `/notifications` with `/notifications/:name/history` and CLI `mllm notify history`.
2. **Metrics summary**: Provide `/metrics/summary` (JSON) aggregating Prom series (SSE connections, queue depth, job throughput), plus CLI `mllm metrics top` for textual dashboards.
3. **Alerting**: Add PVC/job failure threshold detection in the API; emit `alert.triggered` SSE events and expose current alerts via `/system/summary`.

✅ **Phase 6D (2025-11-25)**  
- `/notifications/:name/history` returns recent channel events (create/update/delete/test) so the CLI/ UI can audit activity; `mllm notify history <name> --limit 20` wraps the API with table/JSON output.  
- `/metrics/summary` aggregates queue depth, job counts, weight usage, alerts, and selected Prometheus gauges (`model_manager_job_queue_depth`, `model_manager_sse_connections`, `model_manager_hf_models_cached`). `mllm metrics top` prints a dashboard-style view while `-o json` enables scripting.  
- Storage alerts now emit history + SSE events on threshold crossing (`alert.triggered` / `alert.resolved`), so dashboards can subscribe instead of polling. `/system/summary` still reports the active alert state.

Each sub-phase ends with:
- `go test ./...`
- Docker image build/push (`ghcr.io/oremus-labs/ol-model-manager:<tag>`)
- Helm/GitOps tag bump + `argocd app sync workloads-ai-model-manager`
- Live verification using `mllm` against `https://model-manager-api.oremuslabs.app`

After Phase 6, the backend and CLI expose the same high-level affordances Docker Desktop relies on (job controls, activation buttons, templates, quick search, summaries), so building the UI clone becomes a pure frontend effort.

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
- ✅ **Phase 4 (Runtime control & placement intelligence) – 2025-11-24**
  - Added `mllm runtime status|activate|deactivate` with `--watch`, `--details`, and `--wait` flags. Status pulls the cached `/models/status` payload (pod-level readiness, GPU allocations, informer timestamp) and disallows `--watch` with `-o json` to avoid malformed output. Activation waits stream `model.activation.*` SSE events with automatic reconnection and fallback polling so the CLI reports lifecycle milestones exactly when the backend emits them.
  - Introduced `mllm recommend profiles|gpu|compatibility` wired to the `/recommendations/*` and `/models/:id/compatibility` endpoints. The commands print curated GPU profiles, recommended vLLM flags, and compatibility verdicts (table or JSON) so operators can preflight deployments before staging weights.
  - Verification: `go test ./...` is green; ran `go run ./cmd/mllm --config ~/.config/mllm/config.yaml runtime status --details` to confirm pod summaries render, plus `mllm recommend profiles`, `mllm recommend gpu amd-mi210-venus`, and `mllm recommend compatibility qwen2.5-0.5b-instruct --gpu amd-mi210-venus` against `https://model-manager-api.oremuslabs.app` (token from prod context). All commands returned successful, human-friendly output using the live control plane.
- ✅ **Phase 5 (Secret management foundations) – 2025-11-24**
  - Backend now exposes `/secrets` (list/get) plus `/secrets/:name` (PUT/DELETE) guarded by the API token. The handlers talk directly to Kubernetes Secrets in the Model Manager namespace via a new `internal/secrets.Manager`, tagging managed secrets with `model-manager.oremuslabs.app/managed-secret=true`.
  - Helm RBAC extends the service account permissions to `secrets` so the API Deployment can create/update/delete those resources.
  - CLI adds `mllm secrets list|get|set|delete` with `--data key=value` and `--from-file key=path` helpers plus confirmation prompts for destructive operations. Output honors `-o json` for automation.
  - Verification: `go test ./...` (including the updated handler tests) passes locally. Functional verification on the live cluster requires building/pushing the new API image and syncing the GitOps repo so the `/secrets` endpoints are available behind `model-manager-api.oremuslabs.app`.

- 🚧 **Phase 5B (Notification, token, and policy/audit management) – 2025-11-24**
  - Added `/notifications` CRUD backed by the datastore so multiple channels (Slack/webhooks/etc.) can be stored; CLI gained `mllm notify list|add|delete|test`.
  - `/notifications/test` still uses the configured Slack webhook to send an ad-hoc message so operators can validate alerting. Use `mllm notify test --message "..."` for a one-off smoke test.
  - Introduced `/tokens` (list/issue/delete) and `mllm tokens list|issue|revoke`; issuing returns the plaintext token once while storing only the hash for future validation.
  - Exposed `/policies` CRUD plus `/history?since=` filtering so the CLI can apply/delete policies (`mllm policy list|apply|delete`) and query audit history via `mllm audit list --since 24h`.
  - Upcoming work: `/notifications` CRUD, `/tokens` management, audit/policy endpoints, backup orchestration, runtime diagnostics, rollout orchestration, and JSONPath/global search UX.

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
