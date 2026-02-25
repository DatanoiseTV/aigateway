package middleware

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-gateway/internal/models"
	"ai-gateway/internal/services"

	"github.com/patrickmn/go-cache"
)

type AuthMiddleware struct {
	clientService *services.ClientService
	cache         *cache.Cache
}

func NewAuthMiddleware(clientService *services.ClientService) *AuthMiddleware {
	return &AuthMiddleware{
		clientService: clientService,
		cache:         cache.New(5*time.Minute, 10*time.Minute),
	}
}

func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			log.Printf("[AUTH] Missing Authorization header for %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error": "Missing Authorization header"}`, http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			log.Printf("[AUTH] Invalid Authorization header format for %s %s: %s", r.Method, r.URL.Path, authHeader[:min(20, len(authHeader))])
			http.Error(w, `{"error": "Invalid Authorization header format"}`, http.StatusUnauthorized)
			return
		}

		apiKey := parts[1]
		log.Printf("[AUTH] Trying to authenticate API key: %s... for %s %s", apiKey[:min(8, len(apiKey))], r.Method, r.URL.Path)

		client, err := m.getClientFromCacheOrDB(apiKey)
		if err != nil {
			log.Printf("[AUTH] Error looking up client: %v", err)
			http.Error(w, `{"error": "Internal server error"}`, http.StatusInternalServerError)
			return
		}

		if client == nil {
			log.Printf("[AUTH] Invalid API key: %s... for %s %s", apiKey[:min(8, len(apiKey))], r.Method, r.URL.Path)
			http.Error(w, `{"error": "Invalid API key"}`, http.StatusUnauthorized)
			return
		}

		if !client.IsActive {
			log.Printf("[AUTH] Client %s is disabled", client.ID)
			http.Error(w, `{"error": "Client is disabled"}`, http.StatusForbidden)
			return
		}

		log.Printf("[AUTH] Authenticated client %s (%s) for %s %s", client.Name, client.ID, r.Method, r.URL.Path)
		ctx := context.WithValue(r.Context(), ClientContextKey, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *AuthMiddleware) getClientFromCacheOrDB(apiKey string) (*models.Client, error) {
	cacheKey := "client:" + apiKey

	if cached, found := m.cache.Get(cacheKey); found {
		return cached.(*models.Client), nil
	}

	client, err := m.clientService.GetClientByAPIKey(apiKey)
	if err != nil {
		return nil, err
	}

	if client != nil {
		m.cache.Set(cacheKey, client, 5*time.Minute)
	}

	return client, nil
}

type contextKey string

const ClientContextKey contextKey = "client"

func GetClientFromContext(ctx context.Context) *models.Client {
	if client, ok := ctx.Value(ClientContextKey).(*models.Client); ok {
		return client
	}
	return nil
}
