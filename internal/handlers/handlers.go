// Package handlers provides HTTP request handlers for the model manager API.
package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/kserve"
)

// Handler encapsulates dependencies for HTTP handlers.
type Handler struct {
	catalog *catalog.Catalog
	kserve  *kserve.Client
}

// New creates a new Handler instance.
func New(cat *catalog.Catalog, ks *kserve.Client) *Handler {
	return &Handler{
		catalog: cat,
		kserve:  ks,
	}
}

type activateRequest struct {
	ID string `json:"id" binding:"required"`
}

// Health returns the health status of the service.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ListModels returns all available models.
func (h *Handler) ListModels(c *gin.Context) {
	if err := h.catalog.Reload(); err != nil {
		log.Printf("Failed to reload catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to reload model catalog",
		})
		return
	}

	c.JSON(http.StatusOK, h.catalog.List())
}

// GetModel returns details for a specific model.
func (h *Handler) GetModel(c *gin.Context) {
	modelID := c.Param("id")

	if err := h.catalog.Reload(); err != nil {
		log.Printf("Failed to reload catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to reload model catalog",
		})
		return
	}

	model := h.catalog.Get(modelID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Model not found",
		})
		return
	}

	c.JSON(http.StatusOK, model)
}

// ActivateModel activates a model by creating/updating the InferenceService.
func (h *Handler) ActivateModel(c *gin.Context) {
	var req activateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	log.Printf("Activating model: %s", req.ID)

	// Reload catalog and get model
	if err := h.catalog.Reload(); err != nil {
		log.Printf("Failed to reload catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to reload model catalog",
		})
		return
	}

	model := h.catalog.Get(req.ID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Model not found",
		})
		return
	}

	// Activate the model
	result, err := h.kserve.Activate(model)
	if err != nil {
		log.Printf("Failed to activate model %s: %v", req.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	log.Printf("Successfully activated model: %s", req.ID)
	c.JSON(http.StatusOK, gin.H{
		"status":           "success",
		"message":          "Model " + req.ID + " activated",
		"model":            model,
		"inferenceservice": result,
	})
}

// DeactivateModel deactivates the active model.
func (h *Handler) DeactivateModel(c *gin.Context) {
	log.Println("Deactivating active model")

	result, err := h.kserve.Deactivate()
	if err != nil {
		log.Printf("Failed to deactivate model: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	log.Println("Successfully deactivated model")
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Active model deactivated",
		"result":  result,
	})
}

// GetActiveModel returns information about the currently active model.
func (h *Handler) GetActiveModel(c *gin.Context) {
	isvc, err := h.kserve.GetActive()
	if err != nil {
		log.Printf("Failed to get active model: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	if isvc == nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "none",
			"message": "No active model",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":           "active",
		"inferenceservice": isvc,
	})
}

// RefreshCatalog forces a catalog reload.
func (h *Handler) RefreshCatalog(c *gin.Context) {
	log.Println("Manually refreshing model catalog")

	if err := h.catalog.Reload(); err != nil {
		log.Printf("Failed to refresh catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to refresh model catalog",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Catalog refreshed",
		"models":  h.catalog.List(),
	})
}
