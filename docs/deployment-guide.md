## Model Manager Deployment / Upgrade Guide

This is the checklist I follow whenever we cut a new release and push it through GitOps. It bakes in all the lessons learned from the Knative → Deployment migration so we don’t repeat the same debugging cycle.

### 1. Build & publish the image

1. Update `cmd/*/main.go` version strings if needed.
2. `docker build -t ghcr.io/oremus-labs/ol-model-manager:<tag> .`
3. `docker push ghcr.io/oremus-labs/ol-model-manager:<tag>`

> Tip: run `go test ./...` before the build so we catch failures locally.

### 2. Wire the Helm values

1. In `apps/workloads/ai-model-manager/chart/values.yaml`:
   - bump `deploymentRevision` (anything sortable, e.g., `v0.X.Y-<date>-<rev>`),
   - set `image.tag` to the new tag,
   - capture any new environment knobs/secrets.
2. If the ApplicationSet enforces `image.tag`, update `clusters/oremus-labs/mgmt/root/appsets/workloads.yaml` for the `ai-model-manager` entry.

### 3. Commit & push both repos

```
# In ol-model-manager (if code changed)
git add ...
git commit -m "release: <notes>"
git push

# In ol-kubernetes-cluster
git add apps/workloads/ai-model-manager/... clusters/.../workloads.yaml
git commit -m "chore: bump model-manager to <tag>"
git push
```

### 4. Refresh Argo CD

```
kubectl apply -f clusters/oremus-labs/mgmt/root/appsets/workloads.yaml
argocd app sync workloads-ai-model-manager --prune
```

Run `argocd app sync workloads-ai-route` if we touched routing values.

### 5. Health checks

1. `kubectl get pods -n ai` (ensure `model-manager-api|worker|sync` are Ready).
2. Port-forward and probe the API:
   ```
   kubectl port-forward -n ai svc/model-manager 18080:80 &
   curl http://localhost:18080/healthz
   curl http://localhost:18080/system/info
   ```
3. Hit the ingress host (e.g., `https://model-manager-api.oremuslabs.app/system/info`).
4. Fire a test `/weights/install` (tiny Hugging Face repo) and watch `/events` stream a `job.running → job.completed` notification.
5. Tear down the port-forward (`kill %<pid>`).

### 6. Post-deploy cleanup

- Delete any throwaway weight directories created during tests (`DELETE /weights` with the API token).
- Tag the release in GitHub if appropriate.
- Update `docs/performance-overhaul.md` or the changelog with anything noteworthy.

Following these steps keeps the image tag, AppSet, and Helm chart in lock-step and saves us from the “why are pods still on the old tag?” routine.
