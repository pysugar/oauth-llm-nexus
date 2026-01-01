package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
)

// GenAIHandler handles /genai/v1beta/models/{model}:generateContent
func GenAIHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		model := chi.URLParam(r, "model")

		// Get token: Check for explicit account header, else use Primary/Default
		var cachedToken *token.CachedToken
		var err error

		if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
			cachedToken, err = tokenMgr.GetTokenByIdentifier(accountHeader)
			if err != nil {
				writeGenAIError(w, "Account not found: "+accountHeader, http.StatusUnauthorized)
				return
			}
		} else {
			cachedToken, err = tokenMgr.GetPrimaryOrDefaultToken()
			if err != nil {
				writeGenAIError(w, "No valid token", http.StatusUnauthorized)
				return
			}
		}

		// Parse request body
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			writeGenAIError(w, "Invalid request", http.StatusBadRequest)
			return
		}

		log.Printf("📨 GenAI request: model=%s", model)

		// Build Cloud Code API payload (wrapped format)
		payload := map[string]interface{}{
			"project":     cachedToken.ProjectID,
			"requestId":   "agent-" + uuid.New().String(),
			"request":     reqBody,
			"model":       model,
			"userAgent":   "antigravity",
			"requestType": "gemini",
		}

		resp, err := upstreamClient.GenerateContent(cachedToken.AccessToken, payload)
		if err != nil {
			writeGenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(body)
			return
		}

		// Unwrap response: Cloud Code API returns {"response": {...}}
		var wrapped map[string]interface{}
		if err := json.Unmarshal(body, &wrapped); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
			return
		}

		if inner, ok := wrapped["response"]; ok {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(inner)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		}
	}
}

// GenAIStreamHandler handles /genai/v1beta/models/{model}:streamGenerateContent
func GenAIStreamHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		model := chi.URLParam(r, "model")

		// Get token: Check for explicit account header, else use Primary/Default
		var cachedToken *token.CachedToken
		var err error

		if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
			cachedToken, err = tokenMgr.GetTokenByIdentifier(accountHeader)
			if err != nil {
				writeGenAIError(w, "Account not found: "+accountHeader, http.StatusUnauthorized)
				return
			}
		} else {
			cachedToken, err = tokenMgr.GetPrimaryOrDefaultToken()
			if err != nil {
				writeGenAIError(w, "No valid token", http.StatusUnauthorized)
				return
			}
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			writeGenAIError(w, "Invalid request", http.StatusBadRequest)
			return
		}

		log.Printf("📨 GenAI stream request: model=%s", model)

		payload := map[string]interface{}{
			"project":     cachedToken.ProjectID,
			"requestId":   "agent-" + uuid.New().String(),
			"request":     reqBody,
			"model":       model,
			"userAgent":   "antigravity",
			"requestType": "gemini",
		}

		resp, err := upstreamClient.StreamGenerateContent(cachedToken.AccessToken, payload)
		if err != nil {
			writeGenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeGenAIError(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		// Stream with response unwrapping
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					fmt.Fprintf(w, "data: [DONE]\n\n")
					flusher.Flush()
					break
				}

				// Parse and unwrap response field
				var wrapped map[string]interface{}
				if err := json.Unmarshal([]byte(data), &wrapped); err != nil {
					// Pass through if can't parse
					fmt.Fprintf(w, "data: %s\n\n", data)
					flusher.Flush()
					continue
				}

				if inner, ok := wrapped["response"]; ok {
					innerBytes, _ := json.Marshal(inner)
					fmt.Fprintf(w, "data: %s\n\n", string(innerBytes))
				} else {
					fmt.Fprintf(w, "data: %s\n\n", data)
				}
				flusher.Flush()
			}
		}
	}
}

func writeGenAIError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    status,
			"message": message,
			"status":  "ERROR",
		},
	})
}
