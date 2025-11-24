package graphqlapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/handler"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/status"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
)

// CatalogProvider exposes read-only catalog access.
type CatalogProvider interface {
	All() []*catalog.Model
	Get(id string) *catalog.Model
}

// HFStore exposes cached Hugging Face access.
type HFStore interface {
	ListHFModels() ([]vllm.HuggingFaceModel, error)
}

// DiscoveryProvider fetches live Hugging Face metadata when needed.
type DiscoveryProvider interface {
	SearchModels(vllm.SearchOptions) ([]*vllm.ModelInsight, error)
}

// Config wires the GraphQL schema.
type Config struct {
	Catalog   CatalogProvider
	Store     *store.Store
	Runtime   status.Provider
	HFCache   HFStore
	Discovery DiscoveryProvider
}

// NewHandler returns an http.Handler that serves /graphql requests.
func NewHandler(cfg Config) (http.Handler, error) {
	builder := schemaBuilder{cfg: cfg}
	schema, err := builder.buildSchema()
	if err != nil {
		return nil, err
	}

	return handler.New(&handler.Config{
		Schema:   schema,
		Pretty:   true,
		GraphiQL: true,
	}), nil
}

type schemaBuilder struct {
	cfg Config
}

func (b schemaBuilder) buildSchema() (*graphql.Schema, error) {
	jsonScalar := graphql.NewScalar(graphql.ScalarConfig{
		Name: "JSON",
		Serialize: func(value interface{}) interface{} {
			return value
		},
	})

	conditionType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Condition",
		Fields: graphql.Fields{
			"type":               {Type: graphql.NewNonNull(graphql.String)},
			"status":             {Type: graphql.NewNonNull(graphql.String)},
			"reason":             {Type: graphql.String},
			"message":            {Type: graphql.String},
			"lastTransitionTime": {Type: graphql.String},
		},
	})

	envVarType := graphql.NewObject(graphql.ObjectConfig{
		Name: "EnvVar",
		Fields: graphql.Fields{
			"name":  {Type: graphql.String},
			"value": {Type: graphql.String},
		},
	})

	resourceEntryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "ResourceEntry",
		Fields: graphql.Fields{
			"name":  {Type: graphql.String},
			"value": {Type: graphql.String},
		},
	})

	resourceType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Resources",
		Fields: graphql.Fields{
			"requests": {Type: graphql.NewList(resourceEntryType)},
			"limits":   {Type: graphql.NewList(resourceEntryType)},
		},
	})

	modelType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Model",
		Fields: graphql.Fields{
			"id":              {Type: graphql.NewNonNull(graphql.String)},
			"displayName":     {Type: graphql.String},
			"hfModelId":       {Type: graphql.String},
			"servedModelName": {Type: graphql.String},
			"storageUri":      {Type: graphql.String},
			"runtime":         {Type: graphql.String},
			"env":             {Type: graphql.NewList(envVarType)},
			"resources":       {Type: resourceType},
		},
	})

	jobType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Job",
		Fields: graphql.Fields{
			"id":        {Type: graphql.NewNonNull(graphql.ID)},
			"type":      {Type: graphql.NewNonNull(graphql.String)},
			"status":    {Type: graphql.NewNonNull(graphql.String)},
			"stage":     {Type: graphql.String},
			"progress":  {Type: graphql.Int},
			"message":   {Type: graphql.String},
			"payload":   {Type: jsonScalar},
			"result":    {Type: jsonScalar},
			"createdAt": {Type: graphql.String},
			"updatedAt": {Type: graphql.String},
		},
	})

	containerType := graphql.NewObject(graphql.ObjectConfig{
		Name: "ContainerStatus",
		Fields: graphql.Fields{
			"name":         {Type: graphql.String},
			"state":        {Type: graphql.String},
			"ready":        {Type: graphql.Boolean},
			"restartCount": {Type: graphql.Int},
			"reason":       {Type: graphql.String},
			"message":      {Type: graphql.String},
			"startedAt":    {Type: graphql.String},
			"finishedAt":   {Type: graphql.String},
		},
	})

	podType := graphql.NewObject(graphql.ObjectConfig{
		Name: "PodStatus",
		Fields: graphql.Fields{
			"name":            {Type: graphql.String},
			"phase":           {Type: graphql.String},
			"readyContainers": {Type: graphql.Int},
			"totalContainers": {Type: graphql.Int},
			"restarts":        {Type: graphql.Int},
			"hostIP":          {Type: graphql.String},
			"podIP":           {Type: graphql.String},
			"nodeName":        {Type: graphql.String},
			"reason":          {Type: graphql.String},
			"message":         {Type: graphql.String},
			"startTime":       {Type: graphql.String},
			"conditions":      {Type: graphql.NewList(conditionType)},
			"containers":      {Type: graphql.NewList(containerType)},
			"gpuRequests":     {Type: jsonScalar},
			"gpuLimits":       {Type: jsonScalar},
		},
	})

	deploymentType := graphql.NewObject(graphql.ObjectConfig{
		Name: "DeploymentStatus",
		Fields: graphql.Fields{
			"name":                {Type: graphql.String},
			"readyReplicas":       {Type: graphql.Int},
			"availableReplicas":   {Type: graphql.Int},
			"replicas":            {Type: graphql.Int},
			"updatedReplicas":     {Type: graphql.Int},
			"observedGeneration":  {Type: graphql.Int},
			"conditions":          {Type: graphql.NewList(conditionType)},
			"lastUpdateTimestamp": {Type: graphql.String},
		},
	})

	inferenceType := graphql.NewObject(graphql.ObjectConfig{
		Name: "InferenceServiceStatus",
		Fields: graphql.Fields{
			"name":       {Type: graphql.String},
			"url":        {Type: graphql.String},
			"ready":      {Type: graphql.String},
			"conditions": {Type: graphql.NewList(conditionType)},
		},
	})

	runtimeStatusType := graphql.NewObject(graphql.ObjectConfig{
		Name: "RuntimeStatus",
		Fields: graphql.Fields{
			"inferenceService": {Type: inferenceType},
			"deployments":      {Type: graphql.NewList(deploymentType)},
			"pods":             {Type: graphql.NewList(podType)},
			"gpuAllocations":   {Type: jsonScalar},
			"updatedAt":        {Type: graphql.String},
		},
	})

	hfModelType := graphql.NewObject(graphql.ObjectConfig{
		Name: "HuggingFaceModel",
		Fields: graphql.Fields{
			"id":          {Type: graphql.String},
			"modelId":     {Type: graphql.String},
			"author":      {Type: graphql.String},
			"downloads":   {Type: graphql.Int},
			"likes":       {Type: graphql.Int},
			"tags":        {Type: graphql.NewList(graphql.String)},
			"pipelineTag": {Type: graphql.String},
		},
	})

	queryFields := graphql.Fields{
		"models": {
			Type: graphql.NewList(modelType),
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if b.cfg.Catalog == nil {
					return []interface{}{}, nil
				}
				return mapModels(b.cfg.Catalog.All()), nil
			},
		},
		"model": {
			Type: modelType,
			Args: graphql.FieldConfigArgument{
				"id": {Type: graphql.NewNonNull(graphql.ID)},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if b.cfg.Catalog == nil {
					return nil, nil
				}
				if id, ok := p.Args["id"].(string); ok {
					return mapModel(b.cfg.Catalog.Get(id)), nil
				}
				return nil, nil
			},
		},
		"jobs": {
			Type: graphql.NewList(jobType),
			Args: graphql.FieldConfigArgument{
				"limit": {Type: graphql.Int},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if b.cfg.Store == nil {
					return []interface{}{}, nil
				}
				limit := 25
				if l, ok := p.Args["limit"].(int); ok && l > 0 {
					limit = l
				}
				jobs, err := b.cfg.Store.ListJobs(limit)
				if err != nil {
					return nil, err
				}
				return mapJobs(jobs), nil
			},
		},
		"job": {
			Type: jobType,
			Args: graphql.FieldConfigArgument{
				"id": {Type: graphql.NewNonNull(graphql.ID)},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if b.cfg.Store == nil {
					return nil, nil
				}
				id, _ := p.Args["id"].(string)
				job, err := b.cfg.Store.GetJob(id)
				if err != nil {
					return nil, err
				}
				return mapJob(job), nil
			},
		},
		"runtimeStatus": {
			Type: runtimeStatusType,
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if b.cfg.Runtime == nil {
					return nil, nil
				}
				status := b.cfg.Runtime.CurrentStatus()
				return mapRuntimeStatus(status), nil
			},
		},
		"huggingFaceModels": {
			Type: graphql.NewList(hfModelType),
			Args: graphql.FieldConfigArgument{
				"query":       {Type: graphql.String},
				"limit":       {Type: graphql.Int},
				"pipelineTag": {Type: graphql.String},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				query, _ := p.Args["query"].(string)
				limit := 50
				if l, ok := p.Args["limit"].(int); ok && l > 0 {
					limit = l
				}
				pipelineTag, _ := p.Args["pipelineTag"].(string)
				if query != "" && b.cfg.Discovery != nil {
					results, err := b.cfg.Discovery.SearchModels(vllm.SearchOptions{
						Query:       query,
						PipelineTag: pipelineTag,
						Limit:       limit,
					})
					if err != nil {
						return nil, err
					}
					return mapHFInsights(results, limit), nil
				}
				if b.cfg.HFCache == nil {
					return []interface{}{}, nil
				}
				models, err := b.cfg.HFCache.ListHFModels()
				if err != nil {
					return nil, err
				}
				return mapHFModels(models, limit), nil
			},
		},
	}

	schema, err := graphql.NewSchema(graphql.SchemaConfig{
		Query: graphql.NewObject(graphql.ObjectConfig{
			Name:   "Query",
			Fields: queryFields,
		}),
	})
	if err != nil {
		return nil, err
	}
	return &schema, nil
}

func mapModels(models []*catalog.Model) []interface{} {
	if len(models) == 0 {
		return []interface{}{}
	}
	result := make([]interface{}, 0, len(models))
	for _, m := range models {
		result = append(result, mapModel(m))
	}
	return result
}

func mapModel(model *catalog.Model) map[string]interface{} {
	if model == nil {
		return nil
	}
	env := make([]map[string]interface{}, 0, len(model.Env))
	for _, e := range model.Env {
		env = append(env, map[string]interface{}{
			"name":  e.Name,
			"value": e.Value,
		})
	}
	res := map[string]interface{}{}
	if model.Resources != nil {
		res["requests"] = mapResourceEntries(model.Resources.Requests)
		res["limits"] = mapResourceEntries(model.Resources.Limits)
	}
	return map[string]interface{}{
		"id":              model.ID,
		"displayName":     model.DisplayName,
		"hfModelId":       model.HFModelID,
		"servedModelName": model.ServedModelName,
		"storageUri":      model.StorageURI,
		"runtime":         model.Runtime,
		"env":             env,
		"resources":       res,
	}
}

func mapResourceEntries(entries map[string]string) []map[string]interface{} {
	if len(entries) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(entries))
	for k, v := range entries {
		out = append(out, map[string]interface{}{
			"name":  k,
			"value": v,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ni := out[i]["name"].(string)
		nj := out[j]["name"].(string)
		return ni < nj
	})
	return out
}

func mapJobs(jobs []store.Job) []interface{} {
	out := make([]interface{}, 0, len(jobs))
	for i := range jobs {
		out = append(out, mapJob(&jobs[i]))
	}
	return out
}

func mapJob(job *store.Job) map[string]interface{} {
	if job == nil {
		return nil
	}
	return map[string]interface{}{
		"id":        job.ID,
		"type":      job.Type,
		"status":    job.Status,
		"stage":     job.Stage,
		"progress":  job.Progress,
		"message":   job.Message,
		"payload":   job.Payload,
		"result":    job.Result,
		"createdAt": job.CreatedAt.Format(time.RFC3339),
		"updatedAt": job.UpdatedAt.Format(time.RFC3339),
	}
}

func mapRuntimeStatus(status status.RuntimeStatus) map[string]interface{} {
	result := map[string]interface{}{
		"updatedAt": status.UpdatedAt.Format(time.RFC3339),
	}
	if status.InferenceService != nil {
		result["inferenceService"] = mapInference(status.InferenceService)
	}
	if len(status.Deployments) > 0 {
		deps := make([]map[string]interface{}, 0, len(status.Deployments))
		for _, dep := range status.Deployments {
			deps = append(deps, mapDeployment(dep))
		}
		result["deployments"] = deps
	}
	if len(status.Pods) > 0 {
		pods := make([]map[string]interface{}, 0, len(status.Pods))
		for _, pod := range status.Pods {
			pods = append(pods, mapPod(pod))
		}
		result["pods"] = pods
	}
	if len(status.GPUAllocations) > 0 {
		result["gpuAllocations"] = status.GPUAllocations
	}
	return result
}

func mapInference(isvc *status.InferenceServiceStatus) map[string]interface{} {
	return map[string]interface{}{
		"name":       isvc.Name,
		"url":        isvc.URL,
		"ready":      isvc.Ready,
		"conditions": mapConditions(isvc.Conditions),
	}
}

func mapDeployment(dep status.DeploymentStatus) map[string]interface{} {
	result := map[string]interface{}{
		"name":               dep.Name,
		"readyReplicas":      dep.ReadyReplicas,
		"availableReplicas":  dep.AvailableReplicas,
		"replicas":           dep.Replicas,
		"updatedReplicas":    dep.UpdatedReplicas,
		"observedGeneration": dep.ObservedGeneration,
		"conditions":         mapConditions(dep.Conditions),
	}
	if !dep.LastUpdateTimestamp.IsZero() {
		result["lastUpdateTimestamp"] = dep.LastUpdateTimestamp.Format(time.RFC3339)
	}
	return result
}

func mapPod(pod status.PodStatus) map[string]interface{} {
	result := map[string]interface{}{
		"name":            pod.Name,
		"phase":           pod.Phase,
		"readyContainers": pod.ReadyContainers,
		"totalContainers": pod.TotalContainers,
		"restarts":        pod.Restarts,
		"hostIP":          pod.HostIP,
		"podIP":           pod.PodIP,
		"nodeName":        pod.NodeName,
		"reason":          pod.Reason,
		"message":         pod.Message,
		"conditions":      mapConditions(pod.Conditions),
		"containers":      mapContainers(pod.Containers),
		"gpuRequests":     pod.GPURequests,
		"gpuLimits":       pod.GPULimits,
	}
	if pod.StartTime != nil {
		result["startTime"] = pod.StartTime.Format(time.RFC3339)
	}
	return result
}

func mapContainers(containers []status.ContainerStatusSummary) []map[string]interface{} {
	if len(containers) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(containers))
	for _, ctr := range containers {
		entry := map[string]interface{}{
			"name":         ctr.Name,
			"state":        ctr.State,
			"ready":        ctr.Ready,
			"restartCount": ctr.RestartCount,
			"reason":       ctr.Reason,
			"message":      ctr.Message,
		}
		if ctr.StartedAt != nil {
			entry["startedAt"] = ctr.StartedAt.Format(time.RFC3339)
		}
		if ctr.FinishedAt != nil {
			entry["finishedAt"] = ctr.FinishedAt.Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out
}

func mapConditions(conds []status.Condition) []map[string]interface{} {
	if len(conds) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(conds))
	for _, cond := range conds {
		entry := map[string]interface{}{
			"type":    cond.Type,
			"status":  cond.Status,
			"reason":  cond.Reason,
			"message": cond.Message,
		}
		if !cond.LastTransitionTime.IsZero() {
			entry["lastTransitionTime"] = cond.LastTransitionTime.Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out
}

func mapHFModels(models []vllm.HuggingFaceModel, limit int) []map[string]interface{} {
	if len(models) == 0 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(models))
	for _, m := range models {
		out = append(out, mapHFModel(m))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func mapHFModel(model vllm.HuggingFaceModel) map[string]interface{} {
	return map[string]interface{}{
		"id":          model.ID,
		"modelId":     model.ModelID,
		"author":      model.Author,
		"downloads":   model.Downloads,
		"likes":       model.Likes,
		"tags":        model.Tags,
		"pipelineTag": model.PipelineTag,
	}
}

func mapHFInsights(insights []*vllm.ModelInsight, limit int) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(insights))
	for _, insight := range insights {
		if insight == nil || insight.HFModel == nil {
			continue
		}
		entry := mapHFModel(*insight.HFModel)
		entry["compatible"] = insight.Compatible
		entry["matchedArchitectures"] = insight.MatchedArchitectures
		if insight.SuggestedCatalog != nil {
			entry["suggestedCatalog"] = mapModel(insight.SuggestedCatalog)
		}
		if len(insight.RecommendedFiles) > 0 {
			entry["recommendedFiles"] = insight.RecommendedFiles
		}
		if len(insight.Notes) > 0 {
			entry["notes"] = insight.Notes
		}
		out = append(out, entry)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// EncodeGraphQLQuery is a helper for GraphQL testing (form-encoded JSON bodies).
func EncodeGraphQLQuery(query string) string {
	query = strings.TrimSpace(query)
	data, _ := json.Marshal(map[string]string{"query": query})
	return string(data)
}
