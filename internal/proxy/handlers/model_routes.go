package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

func writeModelRouteError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// ModelRoutesHandler returns all model routes
func ModelRoutesHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routes := db.GetAllModelRoutes(database)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"routes": routes,
			"count":  len(routes),
		})
	}
}

// CreateModelRouteHandler creates a new model route
func CreateModelRouteHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var route models.ModelRoute
		if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
			writeModelRouteError(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		// Validate required fields
		if route.ClientModel == "" || route.TargetModel == "" {
			writeModelRouteError(w, "client_model and target_model are required", http.StatusBadRequest)
			return
		}

		// Default to "google" provider if not specified
		route.TargetProvider = db.NormalizeProvider(route.TargetProvider)
		if err := db.ValidateRouteProvider(route.ClientModel, route.TargetProvider); err != nil {
			writeModelRouteError(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}

		route.IsActive = true

		if err := db.CreateModelRoute(database, &route); err != nil {
			writeModelRouteError(w, "Failed to create route (possibly duplicate): "+err.Error(), http.StatusConflict)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(route)
	}
}

// UpdateModelRouteHandler updates an existing model route
func UpdateModelRouteHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			writeModelRouteError(w, "Invalid route ID", http.StatusBadRequest)
			return
		}

		var route models.ModelRoute
		if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
			writeModelRouteError(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if route.ClientModel == "" || route.TargetModel == "" {
			writeModelRouteError(w, "client_model and target_model are required", http.StatusBadRequest)
			return
		}
		route.TargetProvider = db.NormalizeProvider(route.TargetProvider)
		if err := db.ValidateRouteProvider(route.ClientModel, route.TargetProvider); err != nil {
			writeModelRouteError(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}

		route.ID = uint(id)
		if err := db.UpdateModelRoute(database, &route); err != nil {
			writeModelRouteError(w, "Failed to update route: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(route)
	}
}

// DeleteModelRouteHandler deletes a model route
func DeleteModelRouteHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			writeModelRouteError(w, "Invalid route ID", http.StatusBadRequest)
			return
		}

		if err := db.DeleteModelRoute(database, uint(id)); err != nil {
			writeModelRouteError(w, "Failed to delete route", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true}`))
	}
}

// ResetModelRoutesHandler resets routes to YAML defaults
func ResetModelRoutesHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.ResetModelRoutes(database); err != nil {
			writeModelRouteError(w, "Failed to reset routes", http.StatusInternalServerError)
			return
		}

		routes := db.GetAllModelRoutes(database)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"routes":  routes,
			"count":   len(routes),
		})
	}
}
