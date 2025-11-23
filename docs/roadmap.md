# Model Manager Modernization Roadmap

This document captures the multi–phase program we are executing to turn the Model Manager
into a first‑class platform for discovering, staging, validating, and activating large language
models on the Venus GPU fleet. Each phase includes the major deliverables, APIs, and
infrastructure changes so we can track progress even when context resets.

## Phase 0 – Design & Prerequisites _(complete in this change)_

- Document the desired end‑state (this file) and the persistence/data requirements
  (`docs/persistence.md`).
- Decide on the persistence stack (file‑backed SQLite stored on a PVC) and document the
  schema that future phases will implement.
- Capture OpenAPI expansion requirements and publish an initial spec skeleton to avoid ad‑hoc
  endpoint growth.
- Define GPU inventory sources (Kubernetes node labels + optional DaemonSet feed) and cache
  TTL expectations for Hugging Face/VLLM/GitHub calls.

## Phase 1 – Foundation

- Extend `config.Config` + `/system/info` to surface all tunables (storage paths, PVC name,
  DB DSN, cache TTLs, Git/HF tokens, GPU profiles, notification hooks).
- Introduce a persistence layer abstraction that connects to SQLite on a mounted PVC while
  maintaining backward compatibility with the existing Bolt store during the migration window.
- Ship `/openapi.json` + embedded Swagger UI backed by the new spec; add a `/system/changelog`
  endpoint.
- Update Helm values + secrets for new env vars, DB PVC, and tokens.

## Phase 2 – Discovery & Intelligence

- `/huggingface/search` proxy with query/filters (architecture, parameters, license, quantization,
  GPU tags) and cached responses.
- `/vllm/architectures` + `/vllm/model/:arch` enriched with descriptions and required kwargs.
- `/models/:id/details` merging catalog JSON, HF metadata, GPU fit estimates, and recommended
  runtime flags.
- Recommendation service that ingests Venus GPU inventory and produces tensor parallelism,
  swap space, and quantization guidance.

## Phase 3 – Weights & Storage Operations

- Redesign `/weights/install` as an asynchronous pipeline with job IDs, resumable stages,
  checksum verification, and overwrite safeguards.
- `/weights` endpoint with filtering (status, pinned, orphaned), metadata (last access, size),
  and multi‑location support (Venus, future PVCs).
- `/weights/purge` policy engine (LRU/age) with alert hooks when PVC usage breaches thresholds.
- Prometheus gauges for disk usage + job durations.

## Phase 4 – Catalog & Deployment Automation

- Catalog generator/validator that enforces schemas, Hugging Face validation, GPU compatibility,
  and produces diffs against Git state.
- Auto PR workflow (`/catalog/pr`) that writes JSON, commits to a feature branch, pushes, and opens
  a GitHub PR with templates/labels.
- Deployment orchestration: activate/deactivate/dry‑run/rollback endpoints plus manifest preview
  and Knative/KServe health integration.
- Deployment history captured in the persistence layer.

## Phase 5 – Security & Governance

- API token lifecycle (issue/list/rotate/revoke) with scopes + hashed storage.
- RBAC roles (viewer/operator/admin) enforced across routes, with optional SSO claim mapping.
- Signed job/history records and notification hooks (Slack/webhook/email) for success/failure events.
- Audit log export endpoints.

## Phase 6 – UI Experience

- New enterprise shell (sidebar + top bar) with global search, notifications, profile menu.
- Discovery dashboard with curated collections, filters, HF README previews, and “one click install”.
- Guided install wizard, deployment cards, async job center, history timeline with drawers,
  settings pages, and persistent user preferences (favorites, API token).
- Responsive design, skeleton loaders, and rich empty states.

## Phase 7 – Observability & Automation

- Prometheus metrics for PVC usage, job queue depth, API latency, catalog reload time.
- Alerting for PVC thresholds + GPU compatibility failures.
- Background reconciliation tasks for HF/VLLM cache refresh, stale weight cleanup, and catalog syncs.
- Activity feeds and CSV/JSON exports for compliance.

Each phase will be implemented via normal pull requests across both `ol-model-manager` and
`ol-kubernetes-cluster`, with GitOps deployments validated via Argo CD. This roadmap is the
source of truth for sequencing and can be updated as requirements evolve.
