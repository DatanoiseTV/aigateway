package handlers

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"ai-gateway/internal/config"
	"ai-gateway/internal/models"
	"ai-gateway/internal/providers"
	"ai-gateway/internal/services"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func KnownProviderTypes() []string {
	return []string{
		"gemini",
		"openai",
		"anthropic",
		"mistral",
		"perplexity",
		"xai",
		"cohere",
		"azure-openai",
		"ollama",
		"lmstudio",
	}
}

type AdminHandler struct {
	cfg           *config.Config
	clientService *services.ClientService
	statsService  *services.StatsService
	geminiService *services.GeminiService
	dashboardHub  *services.DashboardHub
	templates     *template.Template
}

type PageData struct {
	Title     string
	User      string
	Data      interface{}
	CSRFToken string
}

func NewAdminHandler(cfg *config.Config, clientService *services.ClientService, statsService *services.StatsService, geminiService *services.GeminiService, dashboardHub *services.DashboardHub) (*AdminHandler, error) {
	tmpl := template.New("admin").Funcs(template.FuncMap{
		"formatDate":     formatDate,
		"formatInt":      formatInt,
		"formatDuration": formatDuration,
		"percentUsed":    percentUsed,
		"add":            func(a, b int) int { return a + b },
		"toJson":         func(v interface{}) (string, error) { b, err := json.Marshal(v); return string(b), err },
	})

	tmpl, err := tmpl.Parse(string(adminTemplates))
	if err != nil {
		return nil, err
	}

	gob.Register(time.Time{})

	return &AdminHandler{
		cfg:           cfg,
		clientService: clientService,
		statsService:  statsService,
		geminiService: geminiService,
		dashboardHub:  dashboardHub,
		templates:     tmpl,
	}, nil
}

func (h *AdminHandler) RegisterRoutes(r *chi.Mux) {
	r.Group(func(r chi.Router) {
		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/dashboard", http.StatusFound)
		})
		r.Get("/admin/login", h.ShowLogin)
		r.Post("/admin/login", h.HandleLogin)
		r.Post("/admin/logout", h.HandleLogout)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(60 * time.Second))
		r.Use(h.RequireAuth)

		r.Get("/admin", h.Dashboard)
		r.Get("/admin/dashboard", h.Dashboard)
		r.Get("/admin/clients", h.ListClients)
		r.Post("/admin/clients", h.CreateClient)
		r.Get("/admin/clients/{id}", h.ShowClient)
		r.Post("/admin/clients/{id}/update", h.UpdateClient)
		r.Post("/admin/clients/{id}/delete", h.DeleteClient)
		r.Post("/admin/clients/{id}/regenerate", h.RegenerateKey)
		r.Post("/admin/clients/{id}/toggle", h.ToggleClient)
		r.Get("/admin/clients/{id}/test", h.TestClientConnection)
		r.Get("/admin/clients/{id}/fetch-models", h.FetchClientModels)
		r.Post("/admin/clients/{id}/update-models", h.UpdateClientModels)
		r.Get("/admin/stats", h.ShowStats)
		r.Get("/admin/stats/api", h.GetAPISTats)
		r.Get("/admin/ws", h.HandleDashboardWS)
	})
}

func (h *AdminHandler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("admin_session")
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}

		if cookie.Value == "" {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (h *AdminHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login.html", PageData{
		Title:     "Admin Login",
		CSRFToken: "login-csrf-token",
	})
}

func (h *AdminHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	username := r.Form.Get("username")
	password := r.Form.Get("password")

	if username != h.cfg.Admin.Username {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	err := bcrypt.CompareHashAndPassword([]byte(h.cfg.Admin.PasswordHash), []byte(password))
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	cookie := &http.Cookie{
		Name:     "admin_session",
		Value:    "authenticated",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.Server.HTTPS.Enabled,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(24 * time.Hour),
	}

	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/admin/dashboard", http.StatusFound)
}

func (h *AdminHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	cookie := &http.Cookie{
		Name:   "admin_session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/admin/login", http.StatusFound)
}

func (h *AdminHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	stats, _ := h.statsService.GetGlobalStats()
	recentLogs, _ := h.statsService.GetRecentRequests("", 20)
	modelUsage, _ := h.statsService.GetModelUsage()
	recentStats, _ := h.statsService.GetRecentStats(5)

	h.render(w, "dashboard.html", PageData{
		Title: "Dashboard",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Stats":       stats,
			"RecentLogs":  recentLogs,
			"ModelUsage":  modelUsage,
			"RecentStats": recentStats,
		},
	})
}

func (h *AdminHandler) ListClients(w http.ResponseWriter, r *http.Request) {
	clients, _ := h.clientService.GetAllClients()
	clientStats, _ := h.statsService.GetAllClientStats()

	statsMap := make(map[string]models.ClientStats)
	for _, cs := range clientStats {
		statsMap[cs.ClientID] = cs
	}

	h.render(w, "clients.html", PageData{
		Title: "Clients",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Clients":     clients,
			"ClientStats": statsMap,
			"Providers":   KnownProviderTypes(),
		},
	})
}

func (h *AdminHandler) CreateClient(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := r.Form.Get("name")
	description := r.Form.Get("description")
	keyType := r.Form.Get("key_type")
	if keyType == "" {
		keyType = "gemini"
	}
	keyPrefix := r.Form.Get("key_prefix")
	backend := r.Form.Get("backend")
	if backend == "" {
		backend = "gemini"
	}
	backendAPIKey := r.Form.Get("backend_api_key")
	backendBaseURL := r.Form.Get("backend_base_url")
	backendDefaultModel := r.Form.Get("backend_default_model")
	systemPrompt := r.Form.Get("system_prompt")
	toolMode := r.Form.Get("tool_mode")

	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	client, apiKey, err := h.clientService.CreateClient(name, description, keyType, keyPrefix, h.cfg)
	if err == nil {
		client.Backend = backend
		client.BackendAPIKey = backendAPIKey
		client.BackendBaseURL = backendBaseURL
		client.BackendDefaultModel = backendDefaultModel
		client.SystemPrompt = systemPrompt
		client.ToolMode = toolMode
		h.clientService.UpdateClient(client)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, "client_created.html", PageData{
		Title: "Client Created",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Client": client,
			"APIKey": apiKey,
		},
	})
}

func (h *AdminHandler) ShowClient(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	client, err := h.clientService.GetClientByID(id)
	if err != nil || client == nil {
		http.Error(w, "Client not found", http.StatusNotFound)
		return
	}

	clientStats, _ := h.statsService.GetClientStats(id)
	recentLogs, _ := h.statsService.GetRecentRequests(id, 50)

	h.render(w, "client_detail.html", PageData{
		Title: client.Name,
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Client":     client,
			"Stats":      clientStats,
			"RecentLogs": recentLogs,
			"Providers":  KnownProviderTypes(),
		},
	})
}

func (h *AdminHandler) UpdateClient(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	r.ParseForm()
	name := r.Form.Get("name")
	description := r.Form.Get("description")
	isActive := r.Form.Get("is_active") == "on"
	backend := r.Form.Get("backend")
	backendAPIKey := r.Form.Get("backend_api_key")
	backendBaseURL := r.Form.Get("backend_base_url")
	backendDefaultModel := r.Form.Get("backend_default_model")
	systemPrompt := r.Form.Get("system_prompt")
	toolMode := r.Form.Get("tool_mode")
	rateLimitMinute := parseInt(r.Form.Get("rate_limit_minute"), 60)
	rateLimitHour := parseInt(r.Form.Get("rate_limit_hour"), 1000)
	rateLimitDay := parseInt(r.Form.Get("rate_limit_day"), 10000)
	quotaInputTokens := parseInt(r.Form.Get("quota_input_tokens"), 1000000)
	quotaOutputTokens := parseInt(r.Form.Get("quota_output_tokens"), 500000)
	quotaRequests := parseInt(r.Form.Get("quota_requests"), 1000)
	maxInputTokens := parseInt(r.Form.Get("max_input_tokens"), 1000000)
	maxOutputTokens := parseInt(r.Form.Get("max_output_tokens"), 8192)
	modelsList := r.Form.Get("models_list")

	client, err := h.clientService.GetClientByID(id)
	if err != nil || client == nil {
		http.Error(w, "Client not found", http.StatusNotFound)
		return
	}

	client.Name = name
	client.Description = description
	client.IsActive = isActive
	client.Backend = backend
	client.BackendAPIKey = backendAPIKey
	client.BackendBaseURL = backendBaseURL
	client.BackendDefaultModel = backendDefaultModel
	client.SystemPrompt = systemPrompt
	client.ToolMode = toolMode
	client.RateLimitMinute = rateLimitMinute
	client.RateLimitHour = rateLimitHour
	client.RateLimitDay = rateLimitDay
	client.QuotaInputTokensDay = quotaInputTokens
	client.QuotaOutputTokensDay = quotaOutputTokens
	client.QuotaRequestsDay = quotaRequests
	client.MaxInputTokens = maxInputTokens
	client.MaxOutputTokens = maxOutputTokens
	if modelsList != "" {
		client.BackendModels = modelsList
	}

	err = h.clientService.UpdateClient(client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/clients/"+id, http.StatusFound)
}

func (h *AdminHandler) ToggleClient(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	client, err := h.clientService.GetClientByID(id)
	if err != nil || client == nil {
		http.Error(w, "Client not found", http.StatusNotFound)
		return
	}

	client.IsActive = !client.IsActive
	h.clientService.UpdateClient(client)

	http.Redirect(w, r, "/admin/clients/"+id, http.StatusFound)
}

func (h *AdminHandler) DeleteClient(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	err := h.clientService.DeleteClient(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/clients", http.StatusFound)
}

func (h *AdminHandler) RegenerateKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	keyType := r.Form.Get("key_type")
	if keyType == "" {
		keyType = "gemini"
	}
	keyPrefix := r.Form.Get("key_prefix")

	apiKey, err := h.clientService.RegenerateAPIKey(id, keyType, keyPrefix)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	client, _ := h.clientService.GetClientByID(id)

	h.render(w, "client_created.html", PageData{
		Title: "API Key Regenerated",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Client": client,
			"APIKey": apiKey,
			"Regen":  true,
		},
	})
}

func (h *AdminHandler) ShowStats(w http.ResponseWriter, r *http.Request) {
	historical7, _ := h.statsService.GetHistoricalStats(7)
	historical30, _ := h.statsService.GetHistoricalStats(30)
	hourly24, _ := h.statsService.GetHourlyStats(24)
	modelStats, _ := h.statsService.GetModelStats(7)
	clientStats, _ := h.statsService.GetClientStats2(7)
	stats, _ := h.statsService.GetGlobalStats()

	h.render(w, "stats.html", PageData{
		Title: "Statistics",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Historical7":  historical7,
			"Historical30": historical30,
			"Hourly24":     hourly24,
			"ModelStats":   modelStats,
			"ClientStats":  clientStats,
			"Stats":        stats,
		},
	})
}

func (h *AdminHandler) ShowSettings(w http.ResponseWriter, r *http.Request) {
	h.render(w, "settings.html", PageData{
		Title: "Settings",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Config":    h.cfg,
			"Providers": KnownProviderTypes(),
		},
	})
}

func (h *AdminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// Update each provider's API key and default model from the form
	for name, pcfg := range h.cfg.Providers {
		apiKey := r.Form.Get("provider_" + name + "_api_key")
		if apiKey != "" || pcfg.APIKey != "" {
			pcfg.APIKey = apiKey
		}
		defaultModel := r.Form.Get("provider_" + name + "_default_model")
		if defaultModel != "" {
			pcfg.DefaultModel = defaultModel
		}
		baseURL := r.Form.Get("provider_" + name + "_base_url")
		if baseURL != "" {
			pcfg.BaseURL = baseURL
		}
		h.cfg.Providers[name] = pcfg
	}

	// Handle adding a new provider
	newName := r.Form.Get("new_provider_name")
	newType := r.Form.Get("new_provider_type")
	if newName != "" && newType != "" {
		h.cfg.Providers[newName] = config.ProviderConfig{
			Type:           newType,
			APIKey:         r.Form.Get("new_provider_api_key"),
			BaseURL:        r.Form.Get("new_provider_base_url"),
			DefaultModel:   r.Form.Get("new_provider_default_model"),
			TimeoutSeconds: 120,
		}
	}

	// Update Gemini allowed models if the gemini provider still exists
	allowedModels := r.Form["allowed_models"]
	if len(allowedModels) > 0 {
		if p, ok := h.cfg.Providers["gemini"]; ok {
			p.AllowedModels = allowedModels
			h.cfg.Providers["gemini"] = p
		}
	}

	config.Save(h.cfg)

	http.Redirect(w, r, "/admin/clients", http.StatusFound)
}

func (h *AdminHandler) GetAPISTats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.statsService.GetGlobalStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"total_requests":%d,"total_input_tokens":%d,"total_output_tokens":%d,"active_clients":%d,"total_clients":%d,"error_rate":%.2f}`,
		stats.TotalRequestsToday, stats.TotalInputTokensToday, stats.TotalOutputTokensToday, stats.ActiveClients, stats.TotalClients, stats.ErrorRate)
}

func (h *AdminHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	msg, ok, err := h.geminiService.TestConnection()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"message":"%s"}`, msg)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":%v,"message":"%s"}`, ok, msg)
}

func (h *AdminHandler) GetModels(w http.ResponseWriter, r *http.Request) {
	var models []string
	if p := h.cfg.GetProvider("gemini"); p != nil {
		models = p.AllowedModels
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"models":[%s]}`, formatStringArray(models))
}

func (h *AdminHandler) FetchModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.geminiService.FetchAvailableModels()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"error":"%s"}`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":true,"models":[%s]}`, formatStringArray(models))
}

func (h *AdminHandler) TestClientConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	client, err := h.clientService.GetClientByID(id)
	if err != nil || client == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"message":"Client not found"}`)
		return
	}

	pcfg := config.ProviderConfig{
		Type:           client.Backend,
		APIKey:         client.BackendAPIKey,
		BaseURL:        client.BackendBaseURL,
		DefaultModel:   client.BackendDefaultModel,
		TimeoutSeconds: 30,
	}

	provider, err := providers.BuildSingleProvider(client.Backend, pcfg)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"message":"Failed to build provider: %s"}`, err.Error())
		return
	}

	msg, ok, testErr := provider.TestConnection()
	if testErr != nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"message":"Error: %s"}`, testErr.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":%v,"message":"%s"}`, ok, msg)
}

func (h *AdminHandler) FetchClientModels(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	client, err := h.clientService.GetClientByID(id)
	if err != nil || client == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"error":"Client not found"}`)
		return
	}

	pcfg := config.ProviderConfig{
		Type:           client.Backend,
		APIKey:         client.BackendAPIKey,
		BaseURL:        client.BackendBaseURL,
		DefaultModel:   client.BackendDefaultModel,
		TimeoutSeconds: 30,
	}

	provider, err := providers.BuildSingleProvider(client.Backend, pcfg)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"error":"Failed to build provider: %s"}`, err.Error())
		return
	}

	models, err := provider.FetchModels()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":false,"error":"%s"}`, err.Error())
		return
	}

	client.BackendModels = formatModelArray(models)
	h.clientService.UpdateClient(client)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":true,"models":[%s]}`, formatStringArray(models))
}

func (h *AdminHandler) UpdateClientModels(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()

	models := r.Form["models"]

	client, err := h.clientService.GetClientByID(id)
	if err != nil || client == nil {
		http.Error(w, "Client not found", http.StatusNotFound)
		return
	}

	client.BackendModels = formatModelArray(models)
	h.clientService.UpdateClient(client)

	http.Redirect(w, r, "/admin/clients/"+id, http.StatusFound)
}

func formatModelArray(models []string) string {
	if len(models) == 0 {
		return "[]"
	}
	result := "["
	for i, m := range models {
		if i > 0 {
			result += ","
		}
		result += fmt.Sprintf(`"%s"`, m)
	}
	result += "]"
	return result
}

// HandleDashboardWS upgrades an HTTP connection to WebSocket for real-time dashboard updates.
func (h *AdminHandler) HandleDashboardWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] Upgrade failed: %v", err)
		return
	}
	h.dashboardHub.Register(conn)
}

func formatStringArray(arr []string) string {
	result := ""
	for i, s := range arr {
		if i > 0 {
			result += ","
		}
		result += fmt.Sprintf(`"%s"`, s)
	}
	return result
}

func (h *AdminHandler) render(w http.ResponseWriter, name string, data PageData) {
	err := h.templates.ExecuteTemplate(w, name, data)
	if err != nil {
		log.Printf("Template error for %s: %v", name, err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func formatDate(t time.Time) string {
	return t.Format("Jan 02, 2006 15:04")
}

func formatDuration(ms int) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func formatInt(n interface{}) string {
	switch v := n.(type) {
	case int:
		if v == 0 {
			return "0"
		}
		return fmt.Sprintf("%d", v)
	case int64:
		if v == 0 {
			return "0"
		}
		return fmt.Sprintf("%d", v)
	case float64:
		if v == 0 {
			return "0"
		}
		return fmt.Sprintf("%.0f", v)
	default:
		return "0"
	}
}

func percentUsed(used, limit interface{}) int {
	var usedVal, limitVal int64
	switch v := used.(type) {
	case int:
		usedVal = int64(v)
	case int64:
		usedVal = v
	default:
		usedVal = 0
	}
	switch v := limit.(type) {
	case int:
		limitVal = int64(v)
	case int64:
		limitVal = v
	default:
		limitVal = 0
	}
	if limitVal == 0 {
		return 0
	}
	return int((usedVal * 100) / limitVal)
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	if n == 0 {
		return def
	}
	return n
}

var adminTemplates = []byte(`
{{define "login.html"}}
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Login - AI Gateway</title>
    <link rel="stylesheet" href="/static/style.css">
    <style>
        body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; }
        .hidden { display: none; }
    </style>
    <script>
        document.addEventListener('DOMContentLoaded', function() {
            window.showModal = function(id) { var el = document.getElementById(id); if(el) el.classList.remove('hidden'); };
            window.hideModal = function(id) { var el = document.getElementById(id); if(el) el.classList.add('hidden'); };
        });
    </script>
</head>
<body class="bg-gradient-to-br from-gray-900 via-gray-800 to-gray-900 min-h-screen flex items-center justify-center">
    <div class="w-full max-w-md">
        <div class="text-center mb-8">
            <div class="inline-flex items-center justify-center w-16 h-16 rounded-2xl bg-blue-600 mb-4">
                <svg class="w-8 h-8 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"/>
                </svg>
            </div>
            <h1 class="text-3xl font-bold text-white">AI Gateway</h1>
            <p class="text-gray-400 mt-2">Sign in to your admin account</p>
        </div>
        
        <div class="bg-gray-800/50 backdrop-blur-sm border border-gray-700 rounded-2xl p-8">
            <form method="POST" action="/admin/login">
                <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
                
                <div class="mb-6">
                    <label class="block text-gray-300 text-sm font-medium mb-2">Username</label>
                    <input type="text" name="username" placeholder="admin" 
                        class="w-full px-4 py-3 bg-gray-900/50 border border-gray-600 text-white rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition-all">
                </div>
                
                <div class="mb-8">
                    <label class="block text-gray-300 text-sm font-medium mb-2">Password</label>
                    <input type="password" name="password" placeholder="Enter password"
                        class="w-full px-4 py-3 bg-gray-900/50 border border-gray-600 text-white rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition-all">
                </div>
                
                <button type="submit" 
                    class="w-full bg-gradient-to-r from-blue-600 to-blue-700 text-white font-semibold py-3 px-4 rounded-xl hover:from-blue-700 hover:to-blue-800 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 focus:ring-offset-gray-900 transition-all">
                    Sign In
                </button>
            </form>
        </div>
        
        <p class="text-center text-gray-500 text-sm mt-6">
            AI Gateway Gateway
        </p>
    </div>
</body>
</html>
{{end}}

{{define "dashboard.html"}}
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Dashboard - AI Gateway</title>
    <link rel="stylesheet" href="/static/style.css">
    <script src="/static/chart.js"></script>
    <style>body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; }</style>
    <script>window.chartColors = ['#3B82F6','#10B981','#8B5CF6','#F59E0B','#EF4444','#EC4899','#06B6D4','#F97316','#84CC16','#E879F9'];</script>
</head>
<body class="bg-gray-900 min-h-screen">
    <!-- Top Navigation -->
    <nav class="bg-gray-800/80 backdrop-blur-md border-b border-gray-700 sticky top-0 z-50">
        <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center space-x-3">
                    <div class="w-8 h-8 bg-gradient-to-br from-blue-500 to-blue-700 rounded-lg flex items-center justify-center">
                        <svg class="w-5 h-5 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>
                        </svg>
                    </div>
                    <span class="text-xl font-bold text-white">AI Gateway</span>
                </div>
                
                <div class="flex items-center space-x-1">
                    <a href="/admin/dashboard" class="px-3 py-2 rounded-lg text-sm font-medium text-white bg-gray-700">Dashboard</a>
                    <a href="/admin/clients" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Clients</a>
                    <a href="/admin/stats" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Stats</a>
                    <a href="https://github.com/DatanoiseTV/aigateway" target="_blank" class="px-3 py-2 rounded-lg text-gray-300 hover:text-white hover:bg-gray-700">
                        <svg class="w-5 h-5" fill="currentColor" viewBox="0 0 24 24">
                            <path fill-rule="evenodd" clip-rule="evenodd" d="M12 2C6.477 2 2 6.477 2 12c0 4.42 2.865 8.17 6.839 9.49.5.092.682-.217.682-.482 0-.237-.008-.866-.013-1.7-2.782.604-3.369-1.34-3.369-1.34-.454-1.156-1.11-1.464-1.11-1.464-.908-.62.069-.608.069-.608 1.003.07 1.531 1.03 1.531 1.03.892 1.529 2.341 1.087 2.91.831.092-.646.35-1.086.636-1.336-2.22-.253-4.555-1.11-4.555-4.943 0-1.091.39-1.984 1.029-2.683-.103-.253-.446-1.27.098-2.647 0 0 .84-.269 2.75 1.025A9.578 9.578 0 0112 6.836c.85.004 1.705.114 2.504.336 1.909-1.294 2.747-1.025 2.747-1.025.546 1.377.203 2.394.1 2.647.64.699 1.028 1.592 1.028 2.683 0 3.842-2.339 4.687-4.566 4.935.359.309.678.919.678 1.852 0 1.336-.012 2.415-.012 2.743 0 .267.18.578.688.48C19.138 20.167 22 16.418 22 12c0-5.523-4.477-10-10-10z"/>
                        </svg>
                    </a>
                    <form method="POST" action="/admin/logout" class="ml-2">
                        <button type="submit" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">
                            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"/>
                            </svg>
                        </button>
                    </form>
                </div>
            </div>
        </div>
    </nav>

    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <!-- Stats Grid -->
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-400 text-sm font-medium">Total Requests</p>
                        <p id="stat-requests" class="text-3xl font-bold text-white mt-1">{{(index .Data "Stats").TotalRequestsToday}}</p>
                    </div>
                    <div class="w-12 h-12 bg-blue-500/20 rounded-xl flex items-center justify-center">
                        <svg class="w-6 h-6 text-blue-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/>
                        </svg>
                    </div>
                </div>
            </div>
            
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-400 text-sm font-medium">Input Tokens</p>
                        <p id="stat-input-tokens" class="text-3xl font-bold text-white mt-1">{{formatInt (index .Data "Stats").TotalInputTokensToday}}</p>
                    </div>
                    <div class="w-12 h-12 bg-green-500/20 rounded-xl flex items-center justify-center">
                        <svg class="w-6 h-6 text-green-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/>
                        </svg>
                    </div>
                </div>
            </div>
            
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-400 text-sm font-medium">Output Tokens</p>
                        <p id="stat-output-tokens" class="text-3xl font-bold text-white mt-1">{{formatInt (index .Data "Stats").TotalOutputTokensToday}}</p>
                    </div>
                    <div class="w-12 h-12 bg-purple-500/20 rounded-xl flex items-center justify-center">
                        <svg class="w-6 h-6 text-purple-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/>
                        </svg>
                    </div>
                </div>
            </div>
            
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-400 text-sm font-medium">Active Clients</p>
                        <p class="text-3xl font-bold text-white mt-1"><span id="stat-active-clients">{{(index .Data "Stats").ActiveClients}}</span> <span class="text-lg text-gray-500">/ <span id="stat-total-clients">{{(index .Data "Stats").TotalClients}}</span></span></p>
                    </div>
                    <div class="w-12 h-12 bg-emerald-500/20 rounded-xl flex items-center justify-center">
                        <svg class="w-6 h-6 text-emerald-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z"/>
                        </svg>
                    </div>
                </div>
            </div>
        </div>

        <!-- Mini Charts Row -->
        <div id="miniChartsRow" class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
            <div class="bg-gray-800 rounded-2xl p-4 border border-gray-700">
                <p class="text-gray-400 text-xs mb-1">Input Tokens Trend</p>
                <canvas id="miniInputChart" height="60"></canvas>
            </div>
            <div class="bg-gray-800 rounded-2xl p-4 border border-gray-700">
                <p class="text-gray-400 text-xs mb-1">Output Tokens Trend</p>
                <canvas id="miniOutputChart" height="60"></canvas>
            </div>
            <div class="bg-gray-800 rounded-2xl p-4 border border-gray-700">
                <p class="text-gray-400 text-xs mb-1">Clients Trend</p>
                <canvas id="miniClientsChart" height="60"></canvas>
            </div>
        </div>
        
        <!-- Charts Row - Compact Model Usage -->
        <div class="grid grid-cols-1 lg:grid-cols-3 gap-6 mb-6">
            <div class="bg-gray-800 rounded-2xl p-4 border border-gray-700 lg:col-span-2">
                <h3 class="text-sm font-semibold text-white mb-3">Top Models (Today)</h3>
                <div id="modelUsageList" class="space-y-2">
                    <p class="text-gray-500 text-sm">Loading...</p>
                </div>
            </div>
        </div>
        
        <!-- Recent Requests -->
        <div class="bg-gray-800 rounded-2xl border border-gray-700 overflow-hidden">
            <div class="px-6 py-4 border-b border-gray-700 flex justify-between items-center">
                <h3 class="text-lg font-semibold text-white">Recent Requests</h3>
                <a href="/admin/stats" class="text-sm text-blue-400 hover:text-blue-300">View All</a>
            </div>
            <div class="overflow-x-auto">
                <table class="w-full">
                    <thead class="bg-gray-900/50">
                        <tr>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Time</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Client</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Model</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Status</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Tokens</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Runtime</th>
                        </tr>
                    </thead>
                    <tbody id="recent-logs" class="divide-y divide-gray-700">
                        {{range (index .Data "RecentLogs")}}
                        <tr class="hover:bg-gray-700/50 transition-colors">
                            <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">{{formatDate .CreatedAt}}</td>
                            <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-300 font-mono">{{.ClientID}}</td>
                            <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-300">{{.Model}}</td>
                            <td class="px-6 py-4 whitespace-nowrap">
                                <span class="px-2 py-1 text-xs font-medium rounded-full {{if ge .StatusCode 400}}bg-red-500/20 text-red-400{{else}}bg-green-500/20 text-green-400{{end}}">
                                    {{.StatusCode}}
                                </span>
                            </td>
                            <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">{{.InputTokens}} / {{.OutputTokens}}</td>
                            <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">{{formatDuration .LatencyMs}}</td>
                            <td class="px-6 py-4 whitespace-nowrap">
                                {{if .IsStreaming}}<span class="text-xs px-1.5 py-0.5 bg-purple-500/20 text-purple-400 rounded mr-1">stream</span>{{end}}
                                {{if .HasTools}}<span class="text-xs px-1.5 py-0.5 bg-orange-500/20 text-orange-400 rounded mr-1">tools</span>{{end}}
                                {{if .RequestBody}}<span class="text-xs px-1.5 py-0.5 bg-blue-500/20 text-blue-400 rounded">body</span>{{end}}
                        </tr>
                        {{else}}
                        <tr>
                            <td colspan="6" class="px-6 py-8 text-center text-gray-500">No requests yet</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>

        <!-- Request Details Modal -->
        <div id="requestModal" class="fixed inset-0 bg-black/70 backdrop-blur-sm z-50 hidden flex items-center justify-center">
            <div class="bg-gray-800 rounded-2xl border border-gray-700 w-full max-w-3xl max-h-[80vh] flex flex-col">
                <div class="flex items-center justify-between px-6 py-4 border-b border-gray-700">
                    <h3 class="text-lg font-semibold text-white">Request Details</h3>
                    <button onclick="closeRequestModal()" class="text-gray-400 hover:text-white">
                        <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
                    </button>
                </div>
                <div class="flex-1 overflow-auto p-6">
                    <pre id="requestBodyContent" class="text-sm text-gray-300 font-mono whitespace-pre-wrap break-all"></pre>
                </div>
            </div>
        </div>

        <script>
        var chartColors = ['#3B82F6','#10B981','#8B5CF6','#F59E0B','#EF4444','#EC4899','#06B6D4','#F97316','#84CC16','#E879F9'];

        function formatDuration(ms) {
            if (ms < 1000) return ms + 'ms';
            if (ms < 60000) return (ms / 1000).toFixed(1) + 's';
            var mins = Math.floor(ms / 60000);
            var secs = Math.floor((ms % 60000) / 1000);
            return mins + 'm ' + secs + 's';
        }

        function initChart(usage) {
            var container = document.getElementById('modelUsageList');
            var labels = Object.keys(usage);
            var data = Object.values(usage);
            if (labels.length === 0) {
                container.innerHTML = '<p class="text-gray-500 text-sm">No usage data yet</p>';
                return;
            }
            var total = data.reduce(function(a, b) { return a + b; }, 0);
            var html = '';
            var sorted = labels.map(function(label, i) {
                return { label: label, count: data[i], color: chartColors[i % chartColors.length] };
            }).sort(function(a, b) { return b.count - a.count; }).slice(0, 5);
            sorted.forEach(function(item) {
                var pct = total > 0 ? Math.round(item.count / total * 100) : 0;
                html += '<div class="flex items-center gap-3">' +
                    '<div class="w-24 text-xs text-gray-400 truncate font-mono" title="' + item.label + '">' + item.label + '</div>' +
                    '<div class="flex-1 h-2 bg-gray-700 rounded-full overflow-hidden">' +
                        '<div class="h-full rounded-full" style="width: ' + pct + '%; background-color: ' + item.color + '"></div>' +
                    '</div>' +
                    '<div class="w-16 text-xs text-gray-300 text-right font-mono">' + item.count + ' (' + pct + '%)</div>' +
                '</div>';
            });
            container.innerHTML = html;
        }

        function updateChart(usage) {
            initChart(usage);
        }

        function updateStats(stats) {
            document.getElementById('stat-requests').textContent = stats.total_requests_today.toLocaleString();
            document.getElementById('stat-input-tokens').textContent = (stats.total_input_tokens_today / 1000).toFixed(1) + 'k';
            document.getElementById('stat-output-tokens').textContent = (stats.total_output_tokens_today / 1000).toFixed(1) + 'k';
            document.getElementById('stat-active-clients').textContent = stats.active_clients;
        }

        function updateRecentLogs(logs) {
            var tbody = document.getElementById('recent-logs');
            if (!logs || logs.length === 0) {
                tbody.innerHTML = '<tr><td colspan="6" class="px-6 py-8 text-center text-gray-500">No requests yet</td></tr>';
                return;
            }
            var html = '';
            logs.forEach(function(l) {
                var statusClass = l.status_code >= 400 ? 'bg-red-500/20 text-red-400' : 'bg-green-500/20 text-green-400';
                html += '<tr class="hover:bg-gray-700/50 transition-colors">';
                html += '<td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">' + l.created_at + '</td>';
                html += '<td class="px-6 py-4 whitespace-nowrap text-sm text-gray-300 font-mono">' + l.client_id + '</td>';
                html += '<td class="px-6 py-4 whitespace-nowrap text-sm text-gray-300">' + l.model + '</td>';
                html += '<td class="px-6 py-4 whitespace-nowrap"><span class="px-2 py-1 text-xs font-medium rounded-full ' + statusClass + '">' + l.status_code + '</span></td>';
                html += '<td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">' + l.input_tokens + ' / ' + l.output_tokens + '</td>';
                html += '<td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">' + formatDuration(l.latency_ms) + '</td>';
                html += '<td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">';
                if (l.is_streaming) html += '<span class="text-xs px-1.5 py-0.5 bg-purple-500/20 text-purple-400 rounded mr-1">stream</span>';
                if (l.has_tools) html += '<span class="text-xs px-1.5 py-0.5 bg-orange-500/20 text-orange-400 rounded mr-1">tools</span>';
                if (l.request_body) html += '<span class="text-xs px-1.5 py-0.5 bg-blue-500/20 text-blue-400 rounded">body</span>';
                html += '</td>';
                html += '</tr>';
            });
            tbody.innerHTML = html;
        }

        // WebSocket connection for real-time updates
        function connectWS() {
            var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
            var ws = new WebSocket(proto + '//' + location.host + '/admin/ws');

            ws.onmessage = function(event) {
                try {
                    var msg = JSON.parse(event.data);
                    if (msg.type === 'stats_update') {
                        updateStats(msg.stats);
                        updateRecentLogs(msg.recent_logs);
                        updateChart(msg.model_usage);
                    }
                } catch (e) {
                    console.error('WS parse error:', e);
                }
            };

            ws.onclose = function() {
                // Reconnect after 3 seconds
                setTimeout(connectWS, 3000);
            };

            ws.onerror = function() {
                ws.close();
            };
        }

        // Initialize chart with server-rendered data, then connect WS
        document.addEventListener('DOMContentLoaded', function() {
            initChart({{(index .Data "ModelUsage")}});
            connectWS();
        });
    </script>
</body>
</html>
{{end}}

{{define "clients.html"}}
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Clients - AI Gateway</title>
    <link rel="stylesheet" href="/static/style.css">
    <style>body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; } .hidden { display: none; }</style>
    <script>
        document.addEventListener('DOMContentLoaded', function() {
            window.showModal = function(id) { var el = document.getElementById(id); if(el) el.classList.remove('hidden'); };
            window.hideModal = function(id) { var el = document.getElementById(id); if(el) el.classList.add('hidden'); };
        });
    </script>
</head>
<body class="bg-gray-900 min-h-screen">
    <nav class="bg-gray-800/80 backdrop-blur-md border-b border-gray-700 sticky top-0 z-50">
        <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center space-x-3">
                    <div class="w-8 h-8 bg-gradient-to-br from-blue-500 to-blue-700 rounded-lg flex items-center justify-center">
                        <svg class="w-5 h-5 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>
                        </svg>
                    </div>
                    <span class="text-xl font-bold text-white">AI Gateway</span>
                </div>
                <div class="flex items-center space-x-1">
                    <a href="/admin/dashboard" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Dashboard</a>
                    <a href="/admin/clients" class="px-3 py-2 rounded-lg text-sm font-medium text-white bg-gray-700">Clients</a>
                    <a href="/admin/stats" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Stats</a>
                    <a href="https://github.com/DatanoiseTV/aigateway" target="_blank" class="px-3 py-2 rounded-lg text-gray-300 hover:text-white hover:bg-gray-700">
                        <svg class="w-5 h-5" fill="currentColor" viewBox="0 0 24 24">
                            <path fill-rule="evenodd" clip-rule="evenodd" d="M12 2C6.477 2 2 6.477 2 12c0 4.42 2.865 8.17 6.839 9.49.5.092.682-.217.682-.482 0-.237-.008-.866-.013-1.7-2.782.604-3.369-1.34-3.369-1.34-.454-1.156-1.11-1.464-1.11-1.464-.908-.62.069-.608.069-.608 1.003.07 1.531 1.03 1.531 1.03.892 1.529 2.341 1.087 2.91.831.092-.646.35-1.086.636-1.336-2.22-.253-4.555-1.11-4.555-4.943 0-1.091.39-1.984 1.029-2.683-.103-.253-.446-1.27.098-2.647 0 0 .84-.269 2.75 1.025A9.578 9.578 0 0112 6.836c.85.004 1.705.114 2.504.336 1.909-1.294 2.747-1.025 2.747-1.025.546 1.377.203 2.394.1 2.647.64.699 1.028 1.592 1.028 2.683 0 3.842-2.339 4.687-4.566 4.935.359.309.678.919.678 1.852 0 1.336-.012 2.415-.012 2.743 0 .267.18.578.688.48C19.138 20.167 22 16.418 22 12c0-5.523-4.477-10-10-10z"/>
                        </svg>
                    </a>
                    <form method="POST" action="/admin/logout" class="ml-2">
                        <button type="submit" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">
                            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"/>
                            </svg>
                        </button>
                    </form>
                </div>
            </div>
        </div>
    </nav>

    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <div class="flex justify-between items-center mb-8">
            <div>
                <h1 class="text-3xl font-bold text-white">Clients</h1>
                <p class="text-gray-400 mt-1">Manage API clients and their quotas</p>
            </div>
            <button onclick="showModal('createModal')" 
                class="bg-gradient-to-r from-blue-600 to-blue-700 text-white px-5 py-2.5 rounded-xl font-medium hover:from-blue-700 hover:to-blue-800 transition-all flex items-center space-x-2">
                <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/>
                </svg>
                <span>New Client</span>
            </button>
        </div>

        <div class="bg-gray-800 rounded-2xl border border-gray-700 overflow-hidden">
            <table class="w-full">
                <thead class="bg-gray-900/50">
                    <tr>
                        <th class="px-6 py-4 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Client</th>
                        <th class="px-6 py-4 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Status</th>
                        <th class="px-6 py-4 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Requests</th>
                        <th class="px-6 py-4 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Input Tokens</th>
                        <th class="px-6 py-4 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Output Tokens</th>
                        <th class="px-6 py-4 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Created</th>
                        <th class="px-6 py-4 text-right text-xs font-medium text-gray-400 uppercase tracking-wider">Actions</th>
                    </tr>
                </thead>
                <tbody class="divide-y divide-gray-700">
                    {{$root := .}}
                    {{range .Data.Clients}}
                    <tr class="hover:bg-gray-700/50 transition-colors">
                        <td class="px-6 py-4">
                            <a href="/admin/clients/{{.ID}}" class="flex items-center space-x-3">
                                <div class="w-10 h-10 bg-gradient-to-br from-blue-500/20 to-purple-500/20 rounded-xl flex items-center justify-center">
                                    <span class="text-blue-400 font-semibold">{{slice .Name 0 1}}</span>
                                </div>
                                <div>
                                    <div class="text-white font-medium">{{.Name}}</div>
                                    <div class="text-gray-500 text-sm">{{.Description}}</div>
                                </div>
                            </a>
                        </td>
                        <td class="px-6 py-4">
                            <form method="POST" action="/admin/clients/{{.ID}}/toggle">
                                <button type="submit" class="px-3 py-1 text-xs font-medium rounded-full {{if .IsActive}}bg-green-500/20 text-green-400{{else}}bg-red-500/20 text-red-400{{end}} hover:opacity-80 transition-opacity">
                                    {{if .IsActive}}Active{{else}}Disabled{{end}}
                                </button>
                            </form>
                        </td>
                        <td class="px-6 py-4">
                            <div class="flex items-center space-x-2">
                                <div class="w-24 bg-gray-700 rounded-full h-2">
                                    <div class="bg-blue-500 h-2 rounded-full" style="width: {{with (index $root.Data.ClientStats .ID)}}{{percentUsed .RequestsToday .RequestsLimit}}{{else}}0{{end}}%"></div>
                                </div>
                                <span class="text-gray-400 text-sm">
                                    {{with (index $root.Data.ClientStats .ID)}}{{.RequestsToday}}{{else}}0{{end}} / {{.QuotaRequestsDay}}
                                </span>
                            </div>
                        </td>
                        <td class="px-6 py-4">
                            <div class="flex items-center space-x-2">
                                <div class="w-24 bg-gray-700 rounded-full h-2">
                                    <div class="bg-green-500 h-2 rounded-full" style="width: {{with (index $root.Data.ClientStats .ID)}}{{percentUsed .InputTokensToday .InputTokensLimit}}{{else}}0{{end}}%"></div>
                                </div>
                                <span class="text-gray-400 text-sm">
                                    {{with (index $root.Data.ClientStats .ID)}}{{.InputTokensToday}}{{else}}0{{end}} / {{.QuotaInputTokensDay}}
                                </span>
                            </div>
                        </td>
                        <td class="px-6 py-4">
                            <div class="flex items-center space-x-2">
                                <div class="w-24 bg-gray-700 rounded-full h-2">
                                    <div class="bg-purple-500 h-2 rounded-full" style="width: {{with (index $root.Data.ClientStats .ID)}}{{percentUsed .OutputTokensToday .OutputTokensLimit}}{{else}}0{{end}}%"></div>
                                </div>
                                <span class="text-gray-400 text-sm">
                                    {{with (index $root.Data.ClientStats .ID)}}{{.OutputTokensToday}}{{else}}0{{end}} / {{.QuotaOutputTokensDay}}
                                </span>
                            </div>
                        </td>
                        <td class="px-6 py-4 text-gray-400 text-sm">{{formatDate .CreatedAt}}</td>
                        <td class="px-6 py-4 text-right">
                            <a href="/admin/clients/{{.ID}}" class="text-blue-400 hover:text-blue-300 font-medium">Manage</a>
                        </td>
                    </tr>
                    {{else}}
                    <tr>
                        <td colspan="7" class="px-6 py-12 text-center">
                            <div class="flex flex-col items-center">
                                <svg class="w-12 h-12 text-gray-600 mb-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z"/>
                                </svg>
                                <p class="text-gray-500">No clients yet</p>
                                <button onclick="showModal('createModal')" class="mt-2 text-blue-400 hover:text-blue-300 font-medium">Create your first client</button>
                            </div>
                        </td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>
    </div>
    
    <!-- Create Modal -->
    <div id="createModal" class="hidden fixed inset-0 bg-black/70 backdrop-blur-sm flex items-start justify-center z-50 p-4 overflow-y-auto">
        <div class="bg-gray-800 border border-gray-700 rounded-2xl w-full max-w-md p-4 my-4">
            <div class="flex justify-between items-center mb-4">
                <h2 class="text-lg font-bold text-white">New Client</h2>
                <button onclick="hideModal('createModal')" class="text-gray-400 hover:text-white">
                    <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
                    </svg>
                </button>
            </div>
            <form method="POST" action="/admin/clients">
                <div class="space-y-3">
                    <div>
                        <label class="block text-gray-400 text-xs font-medium my-2">Name</label>
                        <input type="text" name="name" required placeholder="My App" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <div class="grid grid-cols-2 gap-2">
                        <div>
                            <label class="block text-gray-400 text-xs font-medium my-2">API Key</label>
                            <select name="key_type" onchange="toggleCustomPrefix(this)" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                                <option value="gemini">gm_</option>
                                <option value="openai">sk-</option>
                                <option value="anthropic">sk-ant-</option>
                                <option value="custom">Custom</option>
                            </select>
                        </div>
                        <div id="customPrefixDiv" class="hidden">
                            <label class="block text-gray-400 text-xs font-medium my-2">Prefix</label>
                            <input type="text" name="key_prefix" placeholder="myapp_" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                    </div>
                    <script>function toggleCustomPrefix(el) { var div = document.getElementById('customPrefixDiv'); div.className = el.value === 'custom' ? '' : 'hidden'; }</script>
                    <div class="grid grid-cols-2 gap-2">
                        <div>
                            <label class="block text-gray-400 text-xs font-medium my-2">Backend</label>
                            <select name="backend" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                                {{range (index .Data "Providers")}}<option value="{{.}}" {{if eq . "gemini"}}selected{{end}}>{{.}}</option>{{end}}
                            </select>
                        </div>
                        <div>
                            <label class="block text-gray-400 text-xs font-medium my-2">Model</label>
                            <input type="text" name="backend_default_model" placeholder="optional" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                    </div>
                    <div>
                        <label class="block text-gray-400 text-xs font-medium my-2">API Key <span class="text-gray-500">(optional)</span></label>
                        <input type="password" name="backend_api_key" placeholder="Per-client upstream key" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <details class="text-xs">
                        <summary class="text-gray-400 cursor-pointer hover:text-white py-1">Advanced options</summary>
                        <div class="space-y-2 pt-2">
                            <div>
                                <label class="block text-gray-500 text-xs my-2">Base URL</label>
                                <input type="text" name="backend_base_url" placeholder="http://localhost:11434" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                            </div>
                            <div>
                                <label class="block text-gray-500 text-xs my-2">Description</label>
                                <textarea name="description" rows="1" placeholder="Optional" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"></textarea>
                            </div>
                            <div>
                                <label class="block text-gray-500 text-xs my-2">System Prompt</label>
                                <textarea name="system_prompt" rows="2" placeholder="Optional" class="w-full px-3 py-2 bg-gray-900 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"></textarea>
                            </div>
                        </div>
                    </details>
                </div>
                <div class="flex space-x-2 mt-4">
                    <button type="button" onclick="hideModal('createModal')" class="flex-1 px-3 py-2 bg-gray-700 text-white text-sm rounded-lg hover:bg-gray-600">Cancel</button>
                    <button type="submit" class="flex-1 px-3 py-2 bg-blue-600 text-white text-sm rounded-lg hover:bg-blue-700">Create</button>
                </div>
            </form>
        </div>
    </div>
</body>
</html>
{{end}}

{{define "client_detail.html"}}
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{.Title}} - AI Gateway</title>
    <link rel="stylesheet" href="/static/style.css">
    <script src="/static/chart.js"></script>
    <style>body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; }</style>
</head>
<body class="bg-gray-900 min-h-screen">
    <nav class="bg-gray-800/80 backdrop-blur-md border-b border-gray-700 sticky top-0 z-50">
        <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center space-x-3">
                    <a href="/admin/clients" class="text-gray-400 hover:text-white">
                        <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/>
                        </svg>
                    </a>
                    <div class="w-8 h-8 bg-gradient-to-br from-blue-500 to-blue-700 rounded-lg flex items-center justify-center">
                        <span class="text-white font-semibold text-sm">{{slice (index .Data "Client").Name 0 1}}</span>
                    </div>
                    <span class="text-xl font-bold text-white">{{(index .Data "Client").Name}}</span>
                    {{if (index .Data "Client").IsActive}}
                    <span class="px-2 py-0.5 text-xs font-medium bg-green-500/20 text-green-400 rounded-full">Active</span>
                    {{else}}
                    <span class="px-2 py-0.5 text-xs font-medium bg-red-500/20 text-red-400 rounded-full">Disabled</span>
                    {{end}}
                </div>
                <div class="flex items-center space-x-1">
                    <a href="/admin/dashboard" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Dashboard</a>
                    <a href="/admin/clients" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Clients</a>
                    <a href="/admin/stats" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Stats</a>
                    <a href="https://github.com/DatanoiseTV/aigateway" target="_blank" class="px-3 py-2 rounded-lg text-gray-300 hover:text-white hover:bg-gray-700">
                        <svg class="w-5 h-5" fill="currentColor" viewBox="0 0 24 24">
                            <path fill-rule="evenodd" clip-rule="evenodd" d="M12 2C6.477 2 2 6.477 2 12c0 4.42 2.865 8.17 6.839 9.49.5.092.682-.217.682-.482 0-.237-.008-.866-.013-1.7-2.782.604-3.369-1.34-3.369-1.34-.454-1.156-1.11-1.464-1.11-1.464-.908-.62.069-.608.069-.608 1.003.07 1.531 1.03 1.531 1.03.892 1.529 2.341 1.087 2.91.831.092-.646.35-1.086.636-1.336-2.22-.253-4.555-1.11-4.555-4.943 0-1.091.39-1.984 1.029-2.683-.103-.253-.446-1.27.098-2.647 0 0 .84-.269 2.75 1.025A9.578 9.578 0 0112 6.836c.85.004 1.705.114 2.504.336 1.909-1.294 2.747-1.025 2.747-1.025.546 1.377.203 2.394.1 2.647.64.699 1.028 1.592 1.028 2.683 0 3.842-2.339 4.687-4.566 4.935.359.309.678.919.678 1.852 0 1.336-.012 2.415-.012 2.743 0 .267.18.578.688.48C19.138 20.167 22 16.418 22 12c0-5.523-4.477-10-10-10z"/>
                        </svg>
                    </a>
                    <form method="POST" action="/admin/logout" class="ml-2">
                        <button type="submit" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">
                            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"/>
                            </svg>
                        </button>
                    </form>
                </div>
            </div>
        </div>
    </nav>

    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <script>
        var clientID = "{{(index .Data "Client").ID}}";
        
        function connectWS() {
            var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
            var ws = new WebSocket(proto + '//' + location.host + '/admin/ws');

            ws.onmessage = function(event) {
                try {
                    var msg = JSON.parse(event.data);
                    if (msg.type === 'stats_update') {
                        // Check if this update is for our client
                        if (msg.client_stats && msg.client_stats[clientID]) {
                            var s = msg.client_stats[clientID];
                            document.getElementById('client-requests').textContent = s.requests_today;
                            document.getElementById('client-input').textContent = s.input_tokens.toLocaleString();
                            document.getElementById('client-output').textContent = s.output_tokens.toLocaleString();
                        }
                    }
                } catch (e) {
                    console.error('WS parse error:', e);
                }
            };

            ws.onclose = function() {
                setTimeout(connectWS, 3000);
            };

            ws.onerror = function() {
                ws.close();
            };
        }
        
        // Also poll as fallback
        (function pollClientStats() {
            fetch('/admin/stats/api')
                .then(function(res) { return res.json(); })
                .then(function(data) {
                    // Could add client-specific stats here if available
                })
                .catch(function() {});
            setTimeout(pollClientStats, 3000);
        })();
        
        connectWS();
        </script>
        
        <!-- Stats Cards -->
        <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between mb-4">
                    <h3 class="text-gray-400 text-sm font-medium">Requests Today</h3>
                    <svg class="w-5 h-5 text-blue-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/>
                    </svg>
                </div>
                <p id="client-requests" class="text-3xl font-bold text-white">{{(index .Data "Stats").RequestsToday}}</p>
                <div class="mt-2 bg-gray-700 rounded-full h-2">
                    <div class="bg-blue-500 h-2 rounded-full transition-all" style="width: {{percentUsed (index .Data "Stats").RequestsToday (index .Data "Stats").RequestsLimit}}%"></div>
                </div>
                <p class="text-gray-500 text-sm mt-1">{{(index .Data "Stats").RequestsLimit}} daily limit</p>
            </div>
            
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between mb-4">
                    <h3 class="text-gray-400 text-sm font-medium">Input Tokens</h3>
                    <svg class="w-5 h-5 text-green-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/>
                    </svg>
                </div>
                <p id="client-input" class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").InputTokensToday}}</p>
                <div class="mt-2 bg-gray-700 rounded-full h-2">
                    <div class="bg-green-500 h-2 rounded-full transition-all" style="width: {{percentUsed (index .Data "Stats").InputTokensToday (index .Data "Stats").InputTokensLimit}}%"></div>
                </div>
                <p class="text-gray-500 text-sm mt-1">{{formatInt (index .Data "Stats").InputTokensLimit}} daily limit</p>
            </div>
            
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between mb-4">
                    <h3 class="text-gray-400 text-sm font-medium">Output Tokens</h3>
                    <svg class="w-5 h-5 text-purple-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/>
                    </svg>
                </div>
                <p id="client-output" class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").OutputTokensToday}}</p>
                <div class="mt-2 bg-gray-700 rounded-full h-2">
                    <div class="bg-purple-500 h-2 rounded-full transition-all" style="width: {{percentUsed (index .Data "Stats").OutputTokensToday (index .Data "Stats").OutputTokensLimit}}%"></div>
                </div>
                <p class="text-gray-500 text-sm mt-1">{{formatInt (index .Data "Stats").OutputTokensLimit}} daily limit</p>
            </div>
        </div>

        <div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-8">
            <!-- Settings Form -->
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <h3 class="text-lg font-semibold text-white mb-6">Client Settings</h3>
                <form method="POST" action="/admin/clients/{{(index .Data "Client").ID}}/update">
                    <div class="grid grid-cols-2 gap-4 mb-4">
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Rate (req/min)</label>
                            <input type="number" name="rate_limit_minute" value="{{(index .Data "Client").RateLimitMinute}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Rate (req/hour)</label>
                            <input type="number" name="rate_limit_hour" value="{{(index .Data "Client").RateLimitHour}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Rate (req/day)</label>
                            <input type="number" name="rate_limit_day" value="{{(index .Data "Client").RateLimitDay}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Quota (requests/day)</label>
                            <input type="number" name="quota_requests" value="{{(index .Data "Client").QuotaRequestsDay}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Quota (input tokens)</label>
                            <input type="number" name="quota_input_tokens" value="{{(index .Data "Client").QuotaInputTokensDay}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Quota (output tokens/day)</label>
                            <input type="number" name="quota_output_tokens" value="{{(index .Data "Client").QuotaOutputTokensDay}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Max input tokens/request</label>
                            <input type="number" name="max_input_tokens" value="{{(index .Data "Client").MaxInputTokens}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                            <p class="text-gray-500 text-xs mt-1">0 = unlimited</p>
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Max output tokens/request</label>
                            <input type="number" name="max_output_tokens" value="{{(index .Data "Client").MaxOutputTokens}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                            <p class="text-gray-500 text-xs mt-1">0 = unlimited</p>
                        </div>
                    </div>
                    <div class="mb-6">
                        <label class="block text-gray-400 text-sm font-medium mb-2">Client Name</label>
                        <input type="text" name="name" value="{{(index .Data "Client").Name}}" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <div class="mb-6">
                        <label class="block text-gray-400 text-sm font-medium mb-2">Description</label>
                        <textarea name="description" rows="2" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">{{(index .Data "Client").Description}}</textarea>
                    </div>
                    <div class="grid grid-cols-2 gap-4 mb-6">
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Backend Provider</label>
                            <select name="backend" id="backendSelect" onchange="updateBackendFields()" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                                {{range (index .Data "Providers")}}<option value="{{.}}" {{if eq . (index $.Data "Client").Backend}}selected{{end}}>{{.}}</option>{{end}}
                            </select>
                        </div>
                        <div>
                            <label class="block text-gray-400 text-sm font-medium mb-2">Default Model</label>
                            <div class="flex space-x-2">
                                <input type="text" name="backend_default_model" value="{{(index .Data "Client").BackendDefaultModel}}" placeholder="e.g. gemini-2.0-flash-lite-001" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                                <button type="button" onclick="fetchModels()" class="px-3 py-2 bg-purple-600 hover:bg-purple-700 text-white rounded-lg text-sm" title="Fetch available models from backend">
                                    <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/>
                                    </svg>
                                </button>
                            </div>
                        </div>
                    </div>
                    <div class="mb-6">
                        <label class="block text-gray-400 text-sm font-medium mb-2">Backend API Key</label>
                        <input type="password" name="backend_api_key" value="{{(index .Data "Client").BackendAPIKey}}" placeholder="Leave empty to use global config" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                        <p class="text-gray-500 text-xs mt-1">Per-client API key. If empty, uses global provider config.</p>
                    </div>
                    <div class="mb-6">
                        <label class="block text-gray-400 text-sm font-medium mb-2">Base URL Override</label>
                        <div class="flex space-x-2">
                            <input type="text" name="backend_base_url" value="{{(index .Data "Client").BackendBaseURL}}" placeholder="Leave empty for default" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                            <button type="button" onclick="testConnection()" class="px-4 py-2 bg-green-600 hover:bg-green-700 text-white rounded-lg text-sm flex items-center space-x-1" title="Test connection to backend">
                                <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>
                                </svg>
                                <span>Test</span>
                            </button>
                        </div>
                        <p class="text-gray-500 text-xs mt-1">For Ollama, LM Studio, Azure, or custom endpoints</p>
                    </div>
                    <div class="mb-6">
                        <label class="block text-gray-400 text-sm font-medium mb-2">System Prompt</label>
                        <textarea name="system_prompt" rows="3" placeholder="Injected as system message on every request from this client" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">{{(index .Data "Client").SystemPrompt}}</textarea>
                        <p class="text-gray-500 text-xs mt-1">Prepended before the user's messages. Leave empty to disable.</p>
                    </div>
                    <div class="mb-6">
                        <label class="block text-gray-400 text-sm font-medium mb-2">Tool Mode</label>
                        <select name="tool_mode" class="w-full px-4 py-2 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                            <option value="pass-through" {{if or (eq (index .Data "Client").ToolMode "pass-through" (index .Data "Client").ToolMode "")}}selected{{end}}>Pass-through (forward to client)</option>
                            <option value="gateway" {{if eq (index .Data "Client").ToolMode "gateway"}}selected{{end}}>Gateway (execute tools internally)</option>
                        </select>
                        <p class="text-gray-500 text-xs mt-1">Pass-through forwards tool_calls to the client (opencode) for execution.</p>
                    </div>

                    <!-- Model Whitelist -->
                    <div class="mb-6">
                        <label class="block text-gray-400 text-sm font-medium mb-2">Allowed Models (Whitelist)</label>
                        <div id="modelsList" class="bg-gray-900 border border-gray-600 rounded-lg p-3 max-h-48 overflow-y-auto">
                            <p class="text-gray-500 text-sm">Click "Fetch Models" to load available models from your backend, then select which models this client can use.</p>
                        </div>
                        <div id="selectedModels" class="mt-2 flex flex-wrap gap-2"></div>
                        <input type="hidden" name="models_list" id="modelsInput">
                        <p class="text-gray-500 text-xs mt-1">Leave empty to allow all models. Click "Fetch Models" button above to discover available models.</p>
                    </div>

                    <div class="flex items-center justify-between">
                        <label class="flex items-center text-gray-300">
                            <input type="checkbox" name="is_active" {{if (index .Data "Client").IsActive}}checked{{end}} class="w-5 h-5 rounded bg-gray-900 border-gray-600 text-blue-600 focus:ring-blue-500">
                            <span class="ml-2">Active</span>
                        </label>
                        <button type="submit" class="bg-blue-600 text-white px-6 py-2 rounded-lg hover:bg-blue-700 transition-colors">Save Changes</button>
                    </div>
                </form>
            </div>

            <!-- Danger Zone -->
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <h3 class="text-lg font-semibold text-white mb-6">Danger Zone</h3>
                
                <div class="space-y-4">
                    <div class="p-4 bg-gray-900/50 rounded-xl">
                        <div class="flex items-center justify-between">
                            <div>
                                <p class="text-white font-medium">Regenerate API Key</p>
                                <p class="text-gray-500 text-sm">Invalidates the current key and generates a new one</p>
                            </div>
                            <form method="POST" action="/admin/clients/{{(index .Data "Client").ID}}/regenerate" class="flex items-center space-x-2">
                                <select name="key_type" onchange="toggleRegenPrefix(this)" class="px-3 py-2 bg-gray-800 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-yellow-500">
                                    <option value="gemini">gm_</option>
                                    <option value="openai">sk-</option>
                                    <option value="anthropic">sk-ant-</option>
                                    <option value="custom">Custom</option>
                                </select>
                                <input type="text" name="key_prefix" placeholder="prefix_" class="hidden px-2 py-2 bg-gray-800 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-yellow-500 w-24">
                                <button type="submit" class="px-4 py-2 bg-yellow-600/20 text-yellow-400 border border-yellow-600/50 rounded-lg hover:bg-yellow-600/30 transition-colors">Regenerate</button>
                            </form>
                        </div>
                    </div>
                    <script>function toggleRegenPrefix(el) { var input = el.nextElementSibling; input.className = el.value === 'custom' ? 'px-2 py-2 bg-gray-800 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-yellow-500 w-24' : 'hidden px-2 py-2 bg-gray-800 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-yellow-500 w-24'; }</script>
                    
                    <div class="p-4 bg-red-500/10 rounded-xl border border-red-500/30">
                        <div class="flex items-center justify-between">
                            <div>
                                <p class="text-white font-medium">Delete Client</p>
                                <p class="text-gray-500 text-sm">Permanently delete this client and all associated data</p>
                            </div>
                            <form method="POST" action="/admin/clients/{{(index .Data "Client").ID}}/delete" onsubmit="return confirm('Are you sure? This cannot be undone.')">
                                <button type="submit" class="px-4 py-2 bg-red-600/20 text-red-400 border border-red-600/50 rounded-lg hover:bg-red-600/30 transition-colors">Delete</button>
                            </form>
                        </div>
                    </div>
                </div>
            </div>
        </div>

        <!-- Request Logs -->
        <div class="bg-gray-800 rounded-2xl border border-gray-700 overflow-hidden">
            <div class="px-6 py-4 border-b border-gray-700">
                <h3 class="text-lg font-semibold text-white">Request History</h3>
            </div>
            <div class="overflow-x-auto">
                <table class="w-full">
                    <thead class="bg-gray-900/50">
                        <tr>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Time</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Model</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Status</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Latency</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Input</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Output</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Error</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-gray-700">
                        {{range (index .Data "RecentLogs")}}
                        <tr class="hover:bg-gray-700/50">
                            <td class="px-6 py-4 text-sm text-gray-400">{{formatDate .CreatedAt}}</td>
                            <td class="px-6 py-4 text-sm text-white">{{.Model}}</td>
                            <td class="px-6 py-4">
                                <span class="px-2 py-1 text-xs font-medium rounded-full {{if ge .StatusCode 400}}bg-red-500/20 text-red-400{{else}}bg-green-500/20 text-green-400{{end}}">
                                    {{.StatusCode}}
                                </span>
                            </td>
                            <td class="px-6 py-4 text-sm text-gray-400">{{formatDuration .LatencyMs}}</td>
                            <td class="px-6 py-4 text-sm text-gray-400">{{.InputTokens}}</td>
                            <td class="px-6 py-4 text-sm text-gray-400">{{.OutputTokens}}</td>
                            <td class="px-6 py-4 text-sm text-red-400 max-w-xs truncate">{{.ErrorMessage}}</td>
                        </tr>
                        {{else}}
                        <tr>
                            <td colspan="7" class="px-6 py-8 text-center text-gray-500">No requests yet</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>
    </div>

    <!-- Toast Notification -->
    <div id="toast" class="fixed bottom-4 right-4 px-6 py-3 rounded-lg text-white font-medium hidden transition-opacity duration-300"></div>

    <script>
        var clientID = "{{(index .Data "Client").ID}}";
        var currentModels = {{toJson (index .Data "Client").BackendModels}};
        if (!Array.isArray(currentModels)) currentModels = [];

        function showToast(message, isSuccess) {
            var toast = document.getElementById('toast');
            toast.textContent = message;
            toast.className = 'fixed bottom-4 right-4 px-6 py-3 rounded-lg text-white font-medium transition-opacity duration-300 ' + (isSuccess ? 'bg-green-600' : 'bg-red-600');
            toast.classList.remove('hidden');
            setTimeout(function() {
                toast.classList.add('hidden');
            }, 3000);
        }

        function testConnection() {
            var btn = event.target.closest('button');
            btn.disabled = true;
            btn.innerHTML = '<svg class="w-4 h-4 animate-spin" fill="none" stroke="currentColor" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>';

            fetch('/admin/clients/' + clientID + '/test')
                .then(function(res) { return res.json(); })
                .then(function(data) {
                    showToast(data.message, data.success);
                })
                .catch(function(err) {
                    showToast('Error: ' + err.message, false);
                })
                .finally(function() {
                    btn.disabled = false;
                    btn.innerHTML = '<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg><span class="ml-1">Test</span>';
                });
        }

        function fetchModels() {
            var btn = event.target.closest('button');
            btn.disabled = true;
            btn.innerHTML = '<svg class="w-5 h-5 animate-spin" fill="none" stroke="currentColor" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm291A7.962 7.2 5.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>';

            fetch('/admin/clients/' + clientID + '/fetch-models')
                .then(function(res) { return res.json(); })
                .then(function(data) {
                    if (data.success) {
                        showToast('Loaded ' + data.models.length + ' models', true);
                        renderModelsList(data.models);
                        currentModels = data.models;
                    } else {
                        showToast(data.error || 'Failed to fetch models', false);
                    }
                })
                .catch(function(err) {
                    showToast('Error: ' + err.message, false);
                })
                .finally(function() {
                    btn.disabled = false;
                    btn.innerHTML = '<svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>';
                });
        }

        function renderModelsList(models) {
            var container = document.getElementById('modelsList');
            if (!models || models.length === 0) {
                container.innerHTML = '<p class="text-gray-500 text-sm">No models found</p>';
                return;
            }
            var selected = currentModels || [];
            var html = '<div class="grid grid-cols-2 gap-2">';
            models.forEach(function(m) {
                var isChecked = selected.indexOf(m) !== -1 ? 'checked' : '';
                html += '<label class="flex items-center space-x-2 cursor-pointer hover:bg-gray-800 p-1 rounded">';
                html += '<input type="checkbox" value="' + m + '" ' + isChecked + ' onchange="updateSelectedModels()" class="rounded bg-gray-700 border-gray-600 text-blue-600">';
                html += '<span class="text-gray-300 text-sm truncate">' + m + '</span>';
                html += '</label>';
            });
            html += '</div>';
            container.innerHTML = html;
            updateSelectedModels();
        }

        function updateSelectedModels() {
            var checkboxes = document.querySelectorAll('#modelsList input[type="checkbox"]:checked');
            var selected = Array.from(checkboxes).map(function(cb) { return cb.value; });
            document.getElementById('modelsInput').value = JSON.stringify(selected);
            
            var container = document.getElementById('selectedModels');
            if (selected.length === 0) {
                container.innerHTML = '<span class="text-gray-500 text-xs">All models allowed</span>';
            } else {
                container.innerHTML = selected.map(function(m) {
                    return '<span class="px-2 py-1 bg-blue-600/20 text-blue-400 text-xs rounded-full">' + m + '</span>';
                }).join('');
            }
        }

        if (currentModels && currentModels.length > 0) {
            renderModelsList(currentModels);
        }
    </script>
</body>
</html>
{{end}}

{{define "client_created.html"}}
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{.Title}} - AI Gateway</title>
    <link rel="stylesheet" href="/static/style.css">
    <style>body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; }</style>
</head>
<body class="bg-gray-900 min-h-screen flex items-center justify-center p-4">
    <div class="w-full max-w-lg">
        <div class="bg-gray-800 border border-gray-700 rounded-2xl p-8 text-center">
            <div class="w-16 h-16 bg-green-500/20 rounded-full flex items-center justify-center mx-auto mb-6">
                <svg class="w-8 h-8 text-green-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
                </svg>
            </div>
            
            <h1 class="text-2xl font-bold text-white mb-2">
                {{if (index .Data "Regen")}}API Key Regenerated{{else}}Client Created{{end}}
            </h1>
            <p class="text-gray-400 mb-6">{{(index .Data "Client").Name}}</p>
            
            <div class="bg-amber-500/10 border border-amber-500/50 rounded-xl p-4 mb-6">
                <div class="flex items-start space-x-3">
                    <svg class="w-5 h-5 text-amber-500 flex-shrink-0 mt-0.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
                    </svg>
                    <div class="text-left">
                        <p class="text-amber-400 font-medium text-sm">Save this API key now!</p>
                        <p class="text-amber-300/70 text-xs">It will not be shown again</p>
                    </div>
                </div>
            </div>
            
            <div class="bg-gray-900 rounded-xl p-4 mb-6">
                <code class="text-green-400 break-all text-sm font-mono">{{(index .Data "APIKey")}}</code>
            </div>
            
            <button onclick="navigator.clipboard.writeText('{{(index .Data "APIKey")}}')" class="mb-6 text-blue-400 hover:text-blue-300 text-sm font-medium">
                Copy to clipboard
            </button>
            
            <div class="flex space-x-3">
                <a href="/admin/clients/{{(index .Data "Client").ID}}" class="flex-1 px-4 py-3 bg-gray-700 text-white rounded-xl hover:bg-gray-600 transition-colors">
                    View Client
                </a>
                <a href="/admin/clients" class="flex-1 px-4 py-3 bg-blue-600 text-white rounded-xl hover:bg-blue-700 transition-colors">
                    All Clients
                </a>
            </div>
        </div>
    </div>
</body>
</html>
{{end}}

{{define "settings.html"}}
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Settings - AI Gateway</title>
    <link rel="stylesheet" href="/static/style.css">
    <style>body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; }</style>
</head>
<body class="bg-gray-900 min-h-screen">
    <nav class="bg-gray-800/80 backdrop-blur-md border-b border-gray-700 sticky top-0 z-50">
        <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center space-x-3">
                    <div class="w-8 h-8 bg-gradient-to-br from-blue-500 to-blue-700 rounded-lg flex items-center justify-center">
                        <svg class="w-5 h-5 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>
                        </svg>
                    </div>
                    <span class="text-xl font-bold text-white">AI Gateway</span>
                </div>
                <div class="flex items-center space-x-1">
                    <a href="/admin/dashboard" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Dashboard</a>
                    <a href="/admin/clients" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Clients</a>
                    <a href="/admin/stats" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Stats</a>
                    <form method="POST" action="/admin/logout" class="ml-2">
                        <button type="submit" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">
                            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"/>
                            </svg>
                        </button>
                    </form>
                </div>
            </div>
        </div>
    </nav>

    <div class="max-w-3xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <div class="bg-amber-500/10 border border-amber-500/50 rounded-xl p-4 mb-6">
            <div class="flex items-start space-x-3">
                <svg class="w-5 h-5 text-amber-500 flex-shrink-0 mt-0.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>
                </svg>
                <div class="text-amber-400">
                    <p class="font-medium text-sm">Provider Configuration Moved</p>
                    <p class="text-amber-300/70 text-xs mt-1">API keys and provider settings are now configured per-client. Go to Clients to manage individual client credentials.</p>
                </div>
            </div>
        </div>

        <!-- Server Info -->
        <div class="bg-gray-800 rounded-2xl border border-gray-700 p-6 mb-6">
            <h3 class="text-lg font-semibold text-white mb-6 flex items-center">
                <svg class="w-5 h-5 text-purple-500 mr-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2m-2-4h.01M17 16h.01"/>
                </svg>
                Server Information
            </h3>
            
            <div class="grid grid-cols-2 gap-4">
                <div class="bg-gray-900/50 rounded-xl p-4">
                    <p class="text-gray-500 text-xs uppercase tracking-wide">Port</p>
                    <p class="text-white font-medium">{{(index .Data "Config").Server.Port}}</p>
                </div>
                <div class="bg-gray-900/50 rounded-xl p-4">
                    <p class="text-gray-500 text-xs uppercase tracking-wide">Host</p>
                    <p class="text-white font-medium">{{(index .Data "Config").Server.Host}}</p>
                </div>
                <div class="bg-gray-900/50 rounded-xl p-4">
                    <p class="text-gray-500 text-xs uppercase tracking-wide">Default Rate (min)</p>
                    <p class="text-white font-medium">{{(index .Data "Config").Defaults.RateLimit.RequestsPerMinute}}</p>
                </div>
                <div class="bg-gray-900/50 rounded-xl p-4">
                    <p class="text-gray-500 text-xs uppercase tracking-wide">Default Daily Quota</p>
                    <p class="text-white font-medium">{{(index .Data "Config").Defaults.Quota.MaxRequestsPerDay}} requests</p>
                </div>
            </div>
        </div>
    </div>
</body>
</html>
{{end}}

{{define "stats.html"}}
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Statistics - AI Gateway</title>
    <link rel="stylesheet" href="/static/style.css">
    <script src="/static/chart.js"></script>
    <style>body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; }</style>
    <script>window.chartColors = ['#3B82F6','#10B981','#8B5CF6','#F59E0B','#EF4444','#EC4899','#06B6D4','#F97316','#84CC16','#E879F9'];</script>
</head>
<body class="bg-gray-900 min-h-screen">
    <nav class="bg-gray-800/80 backdrop-blur-md border-b border-gray-700 sticky top-0 z-50">
        <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center space-x-3">
                    <div class="w-8 h-8 bg-gradient-to-br from-blue-500 to-blue-700 rounded-lg flex items-center justify-center">
                        <svg class="w-5 h-5 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>
                        </svg>
                    </div>
                    <span class="text-xl font-bold text-white">AI Gateway</span>
                </div>
                <div class="flex items-center space-x-1">
                    <a href="/admin/dashboard" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Dashboard</a>
                    <a href="/admin/clients" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Clients</a>
                    <a href="/admin/stats" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Stats</a>
                    <a href="https://github.com/DatanoiseTV/aigateway" target="_blank" class="px-3 py-2 rounded-lg text-gray-300 hover:text-white hover:bg-gray-700">
                        <svg class="w-5 h-5" fill="currentColor" viewBox="0 0 24 24">
                            <path fill-rule="evenodd" clip-rule="evenodd" d="M12 2C6.477 2 2 6.477 2 12c0 4.42 2.865 8.17 6.839 9.49.5.092.682-.217.682-.482 0-.237-.008-.866-.013-1.7-2.782.604-3.369-1.34-3.369-1.34-.454-1.156-1.11-1.464-1.11-1.464-.908-.62.069-.608.069-.608 1.003.07 1.531 1.03 1.531 1.03.892 1.529 2.341 1.087 2.91.831.092-.646.35-1.086.636-1.336-2.22-.253-4.555-1.11-4.555-4.943 0-1.091.39-1.984 1.029-2.683-.103-.253-.446-1.27.098-2.647 0 0 .84-.269 2.75 1.025A9.578 9.578 0 0112 6.836c.85.004 1.705.114 2.504.336 1.909-1.294 2.747-1.025 2.747-1.025.546 1.377.203 2.394.1 2.647.64.699 1.028 1.592 1.028 2.683 0 3.842-2.339 4.687-4.566 4.935.359.309.678.919.678 1.852 0 1.336-.012 2.415-.012 2.743 0 .267.18.578.688.48C19.138 20.167 22 16.418 22 12c0-5.523-4.477-10-10-10z"/>
                        </svg>
                    </a>
                    <form method="POST" action="/admin/logout" class="ml-2">
                        <button type="submit" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">
                            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"/>
                            </svg>
                        </button>
                            </svg>
                        </button>
                    </form>
                </div>
            </div>
        </div>
    </nav>

    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <h1 class="text-2xl font-bold text-white mb-8">Statistics</h1>

        <!-- Overview Cards -->
        <div class="grid grid-cols-1 md:grid-cols-4 gap-6 mb-8">
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <p class="text-gray-400 text-sm font-medium">Requests Today</p>
                <p class="text-3xl font-bold text-white mt-2">{{(index .Data "Stats").TotalRequestsToday}}</p>
            </div>
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <p class="text-gray-400 text-sm font-medium">Input Tokens Today</p>
                <p class="text-3xl font-bold text-white mt-2">{{formatInt (index .Data "Stats").TotalInputTokensToday}}</p>
            </div>
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <p class="text-gray-400 text-sm font-medium">Output Tokens Today</p>
                <p class="text-3xl font-bold text-white mt-2">{{formatInt (index .Data "Stats").TotalOutputTokensToday}}</p>
            </div>
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <p class="text-gray-400 text-sm font-medium">Error Rate</p>
                <p class="text-3xl font-bold text-white mt-2">{{printf "%.1f" (index .Data "Stats").ErrorRate}}%</p>
            </div>
        </div>

        <!-- Charts Row -->
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-8">
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <h3 class="text-lg font-semibold text-white mb-4">Requests (Last 7 Days)</h3>
                <canvas id="requestsChart" height="200"></canvas>
            </div>
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <h3 class="text-lg font-semibold text-white mb-4">Tokens (Last 7 Days)</h3>
                <canvas id="tokensChart" height="200"></canvas>
            </div>
        </div>

        <!-- Hourly Chart -->
        <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700 mb-8">
            <h3 class="text-lg font-semibold text-white mb-4">Last 24 Hours</h3>
            <canvas id="hourlyChart" height="100"></canvas>
        </div>

        <!-- Model Stats -->
        <div class="bg-gray-800 rounded-2xl border border-gray-700 overflow-hidden mb-8">
            <div class="px-6 py-4 border-b border-gray-700">
                <h3 class="text-lg font-semibold text-white">Model Statistics (7 days)</h3>
            </div>
            <div class="overflow-x-auto">
                <table class="w-full">
                    <thead class="bg-gray-900/50">
                        <tr>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Model</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Requests</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Tokens</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Avg Runtime</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Success Rate</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-gray-700">
                        {{range (index .Data "ModelStats")}}
                        <tr class="hover:bg-gray-700/50">
                            <td class="px-6 py-4 text-sm text-white">{{.Model}}</td>
                            <td class="px-6 py-4 text-sm text-gray-300">{{formatInt .TotalRequests}}</td>
                            <td class="px-6 py-4 text-sm text-gray-300">{{formatInt .TotalTokens}}</td>
                            <td class="px-6 py-4 text-sm text-gray-300">{{formatDuration (printf "%.0f" .AvgLatencyMs | int)}}</td>
                            <td class="px-6 py-4 text-sm">
                                <span class="px-2 py-1 text-xs font-medium rounded-full {{if ge .SuccessRate 95.0}}bg-green-500/20 text-green-400{{else if ge .SuccessRate 80.0}}bg-yellow-500/20 text-yellow-400{{else}}bg-red-500/20 text-red-400{{end}}">
                                    {{printf "%.1f" .SuccessRate}}%
                                </span>
                            </td>
                        </tr>
                        {{else}}
                        <tr>
                            <td colspan="5" class="px-6 py-8 text-center text-gray-500">No model data yet</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>

        <!-- Client Stats -->
        <div class="bg-gray-800 rounded-2xl border border-gray-700 overflow-hidden">
            <div class="px-6 py-4 border-b border-gray-700">
                <h3 class="text-lg font-semibold text-white">Client Statistics (7 days)</h3>
            </div>
            <div class="overflow-x-auto">
                <table class="w-full">
                    <thead class="bg-gray-900/50">
                        <tr>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Client</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Requests</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Tokens</th>
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase">Success Rate</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-gray-700">
                        {{range (index .Data "ClientStats")}}
                        <tr class="hover:bg-gray-700/50">
                            <td class="px-6 py-4 text-sm text-white">{{.ClientName}}</td>
                            <td class="px-6 py-4 text-sm text-gray-300">{{formatInt .TotalRequests}}</td>
                            <td class="px-6 py-4 text-sm text-gray-300">{{formatInt .TotalTokens}}</td>
                            <td class="px-6 py-4 text-sm">
                                <span class="px-2 py-1 text-xs font-medium rounded-full {{if ge .SuccessRate 95.0}}bg-green-500/20 text-green-400{{else if ge .SuccessRate 80.0}}bg-yellow-500/20 text-yellow-400{{else}}bg-red-500/20 text-red-400{{end}}">
                                    {{printf "%.1f" .SuccessRate}}%
                                </span>
                            </td>
                        </tr>
                        {{else}}
                        <tr>
                            <td colspan="4" class="px-6 py-8 text-center text-gray-500">No client data yet</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>
    </div>

    <script>
        document.addEventListener('DOMContentLoaded', function() {
        // Historical 7 days chart
        var histData = {{toJson (index .Data "Historical7")}};
        if (!Array.isArray(histData)) histData = [];
        const requestsCtx = document.getElementById('requestsChart').getContext('2d');
        if (histData && histData.length > 0) {
            new Chart(requestsCtx, {
                type: 'line',
                data: {
                    labels: histData.map(d => new Date(d.date).toLocaleDateString()),
                    datasets: [{
                        label: 'Requests',
                        data: histData.map(d => d.total_requests),
                        borderColor: '#3B82F6',
                        backgroundColor: 'rgba(59, 130, 246, 0.1)',
                        fill: true,
                        tension: 0.4
                    }]
                },
                options: { responsive: true, plugins: { legend: { display: false } }, scales: { x: { ticks: { color: '#9CA3AF' }, grid: { color: '#374151' } }, y: { ticks: { color: '#9CA3AF' }, grid: { color: '#374151' } } } }
            });
        } else {
            requestsCtx.font = '14px Inter';
            requestsCtx.fillStyle = '#6B7280';
            requestsCtx.fillText('No data yet', requestsCtx.canvas.width / 2 - 40, requestsCtx.canvas.height / 2);
        }

        const tokensCtx = document.getElementById('tokensChart').getContext('2d');
        if (histData && histData.length > 0) {
            new Chart(tokensCtx, {
                type: 'line',
                data: {
                    labels: histData.map(d => new Date(d.date).toLocaleDateString()),
                    datasets: [
                        { label: 'Input', data: histData.map(d => d.total_input_tokens), borderColor: '#10B981', tension: 0.4 },
                        { label: 'Output', data: histData.map(d => d.total_output_tokens), borderColor: '#8B5CF6', tension: 0.4 }
                    ]
                },
                options: { responsive: true, plugins: { legend: { labels: { color: '#9CA3AF' } } }, scales: { x: { ticks: { color: '#9CA3AF' }, grid: { color: '#374151' } }, y: { ticks: { color: '#9CA3AF' }, grid: { color: '#374151' } } } }
            });
        } else {
            tokensCtx.font = '14px Inter';
            tokensCtx.fillStyle = '#6B7280';
            tokensCtx.fillText('No data yet', tokensCtx.canvas.width / 2 - 40, tokensCtx.canvas.height / 2);
        }

        // Hourly chart
        var hourlyData = {{toJson (index .Data "Hourly24")}};
        if (!Array.isArray(hourlyData)) hourlyData = [];
        const hourlyCtx = document.getElementById('hourlyChart').getContext('2d');
        if (hourlyData && hourlyData.length > 0) {
            new Chart(hourlyCtx, {
                type: 'bar',
                data: {
                    labels: hourlyData.map(d => new Date(d.hour).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})),
                    datasets: [
                        { label: 'Requests', data: hourlyData.map(d => d.total_requests), backgroundColor: '#3B82F6' },
                        { label: 'Errors', data: hourlyData.map(d => d.error_count), backgroundColor: '#EF4444' }
                    ]
                },
                options: { responsive: true, plugins: { legend: { labels: { color: '#9CA3AF' } } }, scales: { x: { stacked: true, ticks: { color: '#9CA3AF' }, grid: { color: '#374151' } }, y: { stacked: true, ticks: { color: '#9CA3AF' }, grid: { color: '#374151' } } } }
            });
        } else {
            hourlyCtx.font = '14px Inter';
            hourlyCtx.fillStyle = '#6B7280';
            hourlyCtx.fillText('No data yet', hourlyCtx.canvas.width / 2 - 40, hourlyCtx.canvas.height / 2);
        }
        });
    </script>
</body>
</html>
{{end}}
`)
