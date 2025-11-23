package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

type ActivateRequest struct {
	ID string `json:"id" binding:"required"`
}

type DeactivateRequest struct{}

// Health check endpoint
func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// List all available models
func listModelsHandler(c *gin.Context) {
	if err := catalog.ReloadCatalog(); err != nil {
		log.Printf("Failed to reload catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload model catalog"})
		return
	}

	c.JSON(http.StatusOK, catalog.ListModels())
}

// Get details for a specific model
func getModelHandler(c *gin.Context) {
	modelID := c.Param("id")

	if err := catalog.ReloadCatalog(); err != nil {
		log.Printf("Failed to reload catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload model catalog"})
		return
	}

	model := catalog.GetModel(modelID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Model not found"})
		return
	}

	c.JSON(http.StatusOK, model)
}

// Activate a model
func activateModelHandler(c *gin.Context) {
	var req ActivateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("Activating model: %s", req.ID)

	// Reload catalog and get model
	if err := catalog.ReloadCatalog(); err != nil {
		log.Printf("Failed to reload catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload model catalog"})
		return
	}

	model := catalog.GetModel(req.ID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Model not found"})
		return
	}

	// Activate the model
	result, err := kserveClient.ActivateModel(model)
	if err != nil {
		log.Printf("Failed to activate model %s: %v", req.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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

// Deactivate the active model
func deactivateModelHandler(c *gin.Context) {
	log.Println("Deactivating active model")

	result, err := kserveClient.DeactivateModel()
	if err != nil {
		log.Printf("Failed to deactivate model: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Println("Successfully deactivated model")
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Active model deactivated",
		"result":  result,
	})
}

// Get active model information
func getActiveModelHandler(c *gin.Context) {
	inferenceService, err := kserveClient.GetActiveInferenceService()
	if err != nil {
		log.Printf("Failed to get active model: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if inferenceService == nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "none",
			"message": "No active model",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":           "active",
		"inferenceservice": inferenceService,
	})
}

// Refresh catalog
func refreshCatalogHandler(c *gin.Context) {
	log.Println("Manually refreshing model catalog")

	if err := catalog.ReloadCatalog(); err != nil {
		log.Printf("Failed to refresh catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to refresh model catalog"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Catalog refreshed",
		"models":  catalog.ListModels(),
	})
}
