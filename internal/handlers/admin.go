package handlers

import (
	"encoding/gob"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"ai-gateway/internal/config"
	"ai-gateway/internal/models"
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
		r.Get("/admin/settings", h.ShowSettings)
		r.Post("/admin/settings", h.UpdateSettings)
		r.Get("/admin/stats/api", h.GetAPISTats)
		r.Get("/admin/ws", h.HandleDashboardWS)
		r.Get("/admin/api/test-connection", h.TestConnection)
		r.Get("/admin/api/models", h.GetModels)
		r.Get("/admin/api/fetch-models", h.FetchModels)
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

	h.render(w, "dashboard.html", PageData{
		Title: "Dashboard",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Stats":      stats,
			"RecentLogs": recentLogs,
			"ModelUsage": modelUsage,
			"GeminiKey":  h.cfg.Gemini.APIKey != "",
			"Models":     h.cfg.Gemini.AllowedModels,
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

	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	client, apiKey, err := h.clientService.CreateClient(name, description, keyType, h.cfg)
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
		},
	})
}

func (h *AdminHandler) UpdateClient(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	r.ParseForm()
	name := r.Form.Get("name")
	description := r.Form.Get("description")
	isActive := r.Form.Get("is_active") == "on"
	rateLimitMinute := parseInt(r.Form.Get("rate_limit_minute"), 60)
	rateLimitHour := parseInt(r.Form.Get("rate_limit_hour"), 1000)
	rateLimitDay := parseInt(r.Form.Get("rate_limit_day"), 10000)
	quotaInputTokens := parseInt(r.Form.Get("quota_input_tokens"), 1000000)
	quotaOutputTokens := parseInt(r.Form.Get("quota_output_tokens"), 500000)
	quotaRequests := parseInt(r.Form.Get("quota_requests"), 1000)
	maxInputTokens := parseInt(r.Form.Get("max_input_tokens"), 1000000)
	maxOutputTokens := parseInt(r.Form.Get("max_output_tokens"), 8192)

	client, err := h.clientService.GetClientByID(id)
	if err != nil || client == nil {
		http.Error(w, "Client not found", http.StatusNotFound)
		return
	}

	client.Name = name
	client.Description = description
	client.IsActive = isActive
	client.RateLimitMinute = rateLimitMinute
	client.RateLimitHour = rateLimitHour
	client.RateLimitDay = rateLimitDay
	client.QuotaInputTokensDay = quotaInputTokens
	client.QuotaOutputTokensDay = quotaOutputTokens
	client.QuotaRequestsDay = quotaRequests
	client.MaxInputTokens = maxInputTokens
	client.MaxOutputTokens = maxOutputTokens

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

	apiKey, err := h.clientService.RegenerateAPIKey(id, keyType)
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

func (h *AdminHandler) ShowSettings(w http.ResponseWriter, r *http.Request) {
	h.render(w, "settings.html", PageData{
		Title: "Settings",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Config": h.cfg,
		},
	})
}

func (h *AdminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	h.cfg.Gemini.APIKey = r.Form.Get("gemini_api_key")
	h.cfg.Gemini.DefaultModel = r.Form.Get("default_model")

	allowedModels := r.Form["allowed_models"]
	if len(allowedModels) > 0 {
		h.cfg.Gemini.AllowedModels = allowedModels
	}

	config.Save(h.cfg)

	http.Redirect(w, r, "/admin/settings?success=true", http.StatusFound)
}

func (h *AdminHandler) GetAPISTats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.statsService.GetGlobalStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"total_requests":%d,"total_input_tokens":%d,"total_output_tokens":%d,"active_clients":%d,"error_rate":%.2f}`,
		stats.TotalRequestsToday, stats.TotalInputTokensToday, stats.TotalOutputTokensToday, stats.ActiveClients, stats.ErrorRate)
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
	models := h.cfg.Gemini.AllowedModels

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
		fmt.Println("Template error:", err)
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
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
    <style>
        body { font-family: 'Inter', sans-serif; }
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
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
    <style>body { font-family: 'Inter', sans-serif; }</style>
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
                    <a href="/admin/settings" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Settings</a>
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
        <!-- Status Banner -->
        {{if not (index .Data "GeminiKey")}}
        <div class="mb-6 bg-amber-500/10 border border-amber-500/50 rounded-xl p-4 flex items-center justify-between">
            <div class="flex items-center space-x-3">
                <svg class="w-5 h-5 text-amber-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
                </svg>
                <span class="text-amber-400 font-medium">Gemini API key not configured</span>
            </div>
            <a href="/admin/settings" class="text-sm text-amber-400 hover:text-amber-300 font-medium">Configure now →</a>
        </div>
        {{end}}

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

        <!-- Charts Row -->
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-8">
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <h3 class="text-lg font-semibold text-white mb-4">Model Usage</h3>
                <canvas id="modelChart" height="200"></canvas>
            </div>
            
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <h3 class="text-lg font-semibold text-white mb-4">System Status</h3>
                <div class="space-y-4">
                    <div class="flex items-center justify-between p-4 bg-gray-900/50 rounded-xl">
                        <div class="flex items-center space-x-3">
                            <div class="w-3 h-3 bg-green-500 rounded-full animate-pulse"></div>
                            <span class="text-gray-300">Server Status</span>
                        </div>
                        <span class="text-green-400 font-medium">Online</span>
                    </div>
                    <div class="flex items-center justify-between p-4 bg-gray-900/50 rounded-xl">
                        <div class="flex items-center space-x-3">
                            <div class="w-3 h-3 {{if (index .Data "GeminiKey")}}bg-green-500{{else}}bg-red-500{{end}} rounded-full"></div>
                            <span class="text-gray-300">Gemini API</span>
                        </div>
                        <span class="{{if (index .Data "GeminiKey")}}text-green-400{{else}}text-red-400{{end}} font-medium">{{if (index .Data "GeminiKey")}}Connected{{else}}Not Configured{{end}}</span>
                    </div>
                    <div class="flex items-center justify-between p-4 bg-gray-900/50 rounded-xl">
                        <div class="flex items-center space-x-3">
                            <div class="w-3 h-3 bg-green-500 rounded-full"></div>
                            <span class="text-gray-300">Database</span>
                        </div>
                        <span class="text-green-400 font-medium">Connected</span>
                    </div>
                </div>
            </div>
        </div>

        <!-- Recent Requests -->
        <div class="bg-gray-800 rounded-2xl border border-gray-700 overflow-hidden">
            <div class="px-6 py-4 border-b border-gray-700">
                <h3 class="text-lg font-semibold text-white">Recent Requests</h3>
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
                            <th class="px-6 py-3 text-left text-xs font-medium text-gray-400 uppercase tracking-wider">Latency</th>
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
                            <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">{{.LatencyMs}}ms</td>
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
    </div>
    
    <script>
        var chartColors = ['#3B82F6','#10B981','#8B5CF6','#F59E0B','#EF4444','#EC4899','#06B6D4','#F97316','#84CC16','#E879F9'];
        var modelChart = null;

        function initChart(usage) {
            var el = document.getElementById('modelChart');
            var labels = Object.keys(usage);
            var data = Object.values(usage);
            if (labels.length === 0) {
                el.parentElement.innerHTML = '<h3 class="text-lg font-semibold text-white mb-4">Model Usage</h3><canvas id="modelChart" height="200"></canvas><div id="chartEmpty" class="text-gray-500 text-center py-8">No usage data yet</div>';
                return;
            }
            modelChart = new Chart(el, {
                type: 'doughnut',
                data: {
                    labels: labels,
                    datasets: [{ data: data, backgroundColor: chartColors.slice(0, labels.length), borderWidth: 0 }]
                },
                options: {
                    responsive: true,
                    animation: { duration: 300 },
                    plugins: { legend: { position: 'right', labels: { color: '#9CA3AF' } } }
                }
            });
        }

        function updateChart(usage) {
            var labels = Object.keys(usage);
            var data = Object.values(usage);
            if (labels.length === 0) return;
            var empty = document.getElementById('chartEmpty');
            if (empty) empty.remove();
            if (!modelChart) {
                initChart(usage);
                return;
            }
            modelChart.data.labels = labels;
            modelChart.data.datasets[0].data = data;
            modelChart.data.datasets[0].backgroundColor = chartColors.slice(0, labels.length);
            modelChart.update();
        }

        function updateStats(stats) {
            document.getElementById('stat-requests').textContent = stats.total_requests_today;
            document.getElementById('stat-input-tokens').textContent = stats.total_input_tokens_today;
            document.getElementById('stat-output-tokens').textContent = stats.total_output_tokens_today;
            document.getElementById('stat-active-clients').textContent = stats.active_clients;
            document.getElementById('stat-total-clients').textContent = stats.total_clients;
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
                html += '<td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400">' + l.latency_ms + 'ms</td>';
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
        initChart({{(index .Data "ModelUsage")}});
        connectWS();
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
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
    <style>body { font-family: 'Inter', sans-serif; } .hidden { display: none; }</style>
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
                    <a href="/admin/settings" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Settings</a>
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
    <div id="createModal" class="hidden fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center justify-center z-50 p-4">
        <div class="bg-gray-800 border border-gray-700 rounded-2xl w-full max-w-md p-6">
            <div class="flex justify-between items-center mb-6">
                <h2 class="text-xl font-bold text-white">Create New Client</h2>
                <button onclick="hideModal('createModal')" class="text-gray-400 hover:text-white">
                    <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
                    </svg>
                </button>
            </div>
            <form method="POST" action="/admin/clients">
                <div class="mb-4">
                    <label class="block text-gray-300 text-sm font-medium mb-2">Name</label>
                    <input type="text" name="name" required placeholder="My App" class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500">
                </div>
                <div class="mb-4">
                    <label class="block text-gray-300 text-sm font-medium mb-2">API Key Type</label>
                    <select name="key_type" class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500">
                        <option value="gemini">gm_ (Gemini style)</option>
                        <option value="openai">sk- (OpenAI style)</option>
                        <option value="anthropic">sk-ant- (Anthropic style)</option>
                    </select>
                </div>
                <div class="mb-6">
                    <label class="block text-gray-300 text-sm font-medium mb-2">Description</label>
                    <textarea name="description" placeholder="Optional description" rows="2" class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500"></textarea>
                </div>
                <div class="flex space-x-3">
                    <button type="button" onclick="hideModal('createModal')" class="flex-1 px-4 py-3 bg-gray-700 text-white rounded-xl hover:bg-gray-600 transition-colors">Cancel</button>
                    <button type="submit" class="flex-1 px-4 py-3 bg-blue-600 text-white rounded-xl hover:bg-blue-700 transition-colors">Create Client</button>
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
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
    <style>body { font-family: 'Inter', sans-serif; }</style>
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
                    <a href="/admin/settings" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Settings</a>
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
        <!-- Stats Cards -->
        <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
            <div class="bg-gray-800 rounded-2xl p-6 border border-gray-700">
                <div class="flex items-center justify-between mb-4">
                    <h3 class="text-gray-400 text-sm font-medium">Requests Today</h3>
                    <svg class="w-5 h-5 text-blue-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/>
                    </svg>
                </div>
                <p class="text-3xl font-bold text-white">{{(index .Data "Stats").RequestsToday}}</p>
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
                <p class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").InputTokensToday}}</p>
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
                <p class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").OutputTokensToday}}</p>
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
                                <select name="key_type" class="px-3 py-2 bg-gray-800 border border-gray-600 text-white text-sm rounded-lg focus:outline-none focus:ring-2 focus:ring-yellow-500">
                                    <option value="gemini">gm_</option>
                                    <option value="openai">sk-</option>
                                    <option value="anthropic">sk-ant-</option>
                                </select>
                                <button type="submit" class="px-4 py-2 bg-yellow-600/20 text-yellow-400 border border-yellow-600/50 rounded-lg hover:bg-yellow-600/30 transition-colors">Regenerate</button>
                            </form>
                        </div>
                    </div>
                    
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
                            <td class="px-6 py-4 text-sm text-gray-400">{{.LatencyMs}}ms</td>
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
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
    <style>body { font-family: 'Inter', sans-serif; }</style>
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
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
    <style>body { font-family: 'Inter', sans-serif; } .hidden { display: none; }</style>
    <script>
        async function testConnection() {
            const btn = document.getElementById('testBtn');
            const result = document.getElementById('testResult');
            btn.disabled = true;
            btn.textContent = 'Testing...';
            try {
                const res = await fetch('/admin/api/test-connection');
                const data = await res.json();
                result.textContent = data.message;
                result.className = data.success ? 'text-green-400 mt-2' : 'text-red-400 mt-2';
                result.classList.remove('hidden');
            } catch (e) {
                result.textContent = 'Error: ' + e.message;
                result.className = 'text-red-400 mt-2';
                result.classList.remove('hidden');
            }
            btn.disabled = false;
            btn.textContent = 'Test Connection';
        }
        
        async function fetchModels() {
            const btn = document.getElementById('fetchBtn');
            const list = document.getElementById('modelList');
            btn.disabled = true;
            btn.textContent = 'Fetching...';
            try {
                const res = await fetch('/admin/api/fetch-models');
                const data = await res.json();
                if (data.success) {
                    list.innerHTML = '';
                    data.models.forEach(m => {
                        const label = document.createElement('label');
                        label.className = 'flex items-center space-x-2 text-gray-300 text-sm';
                        const checkbox = document.createElement('input');
                        checkbox.type = 'checkbox';
                        checkbox.name = 'allowed_models';
                        checkbox.value = m;
                        checkbox.className = 'rounded bg-gray-900 border-gray-600 text-blue-600';
                        const span = document.createElement('span');
                        span.textContent = m;
                        label.appendChild(checkbox);
                        label.appendChild(span);
                        list.appendChild(label);
                    });
                    document.getElementById('modelSection').classList.remove('hidden');
                } else {
                    alert('Error: ' + data.error);
                }
            } catch (e) {
                alert('Error: ' + e.message);
            }
            btn.disabled = false;
            btn.textContent = 'Fetch Available Models';
        }

        function selectAllModels() {
            document.querySelectorAll('#modelList input[type="checkbox"]').forEach(cb => { cb.checked = true; });
        }

        function deselectAllModels() {
            document.querySelectorAll('#modelList input[type="checkbox"]').forEach(cb => { cb.checked = false; });
        }
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
                    <a href="/admin/clients" class="px-3 py-2 rounded-lg text-sm font-medium text-gray-300 hover:text-white hover:bg-gray-700">Clients</a>
                    <a href="/admin/settings" class="px-3 py-2 rounded-lg text-sm font-medium text-white bg-gray-700">Settings</a>
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
        {{if .Data.Config}}
        <form method="POST" action="/admin/settings">
            <!-- Gemini API Settings -->
            <div class="bg-gray-800 rounded-2xl border border-gray-700 p-6 mb-6">
                <h3 class="text-lg font-semibold text-white mb-6 flex items-center">
                    <svg class="w-5 h-5 text-blue-500 mr-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z"/>
                    </svg>
                    Gemini API Configuration
                </h3>
                
                <div class="space-y-4">
                    <div>
                        <label class="block text-gray-300 text-sm font-medium mb-2">API Key</label>
                        <input type="password" name="gemini_api_key" value="{{(index .Data "Config").Gemini.APIKey}}" placeholder="Enter your Gemini API key"
                            class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500">
                        <p class="text-gray-500 text-xs mt-1">Get your API key from <a href="https://aistudio.google.com/app/apikey" target="_blank" class="text-blue-400 hover:text-blue-300">Google AI Studio</a></p>
                    </div>
                    
                    <div>
                        <label class="block text-gray-300 text-sm font-medium mb-2">Default Model</label>
                        <input type="text" name="default_model" value="{{(index .Data "Config").Gemini.DefaultModel}}" placeholder="gemini-flash-lite-latest"
                            class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    
                    <div class="flex items-center space-x-4 pt-4">
                        <button type="button" id="testBtn" onclick="testConnection()" class="px-4 py-2 bg-green-600 hover:bg-green-700 text-white rounded-lg font-medium">
                            Test Connection
                        </button>
                        <button type="button" id="fetchBtn" onclick="fetchModels()" class="px-4 py-2 bg-purple-600 hover:bg-purple-700 text-white rounded-lg font-medium">
                            Fetch Available Models
                        </button>
                    </div>
                    <div id="testResult" class="hidden"></div>
                </div>
            </div>

            <!-- Allowed Models -->
            <div class="bg-gray-800 rounded-2xl border border-gray-700 p-6 mb-6">
                <div class="flex items-center justify-between mb-4">
                    <h3 class="text-lg font-semibold text-white flex items-center">
                        <svg class="w-5 h-5 text-blue-500 mr-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"/>
                        </svg>
                        Allowed Models
                    </h3>
                    <div class="flex items-center space-x-2">
                        <button type="button" onclick="selectAllModels()" class="px-3 py-1.5 text-xs font-medium bg-blue-600/20 text-blue-400 border border-blue-600/50 rounded-lg hover:bg-blue-600/30 transition-colors">
                            Allow All
                        </button>
                        <button type="button" onclick="deselectAllModels()" class="px-3 py-1.5 text-xs font-medium bg-gray-600/20 text-gray-400 border border-gray-600/50 rounded-lg hover:bg-gray-600/30 transition-colors">
                            Clear All
                        </button>
                    </div>
                </div>
                <div id="modelSection" class="{{if (index .Data "Config").Gemini.AllowedModels}}{{else}}hidden{{end}} mb-4">
                    <div id="modelList" class="grid grid-cols-2 gap-2 max-h-48 overflow-y-auto">
                        {{range (index .Data "Config").Gemini.AllowedModels}}
                        <label class="flex items-center space-x-2 text-gray-300 text-sm">
                            <input type="checkbox" name="allowed_models" value="{{.}}" checked class="rounded bg-gray-900 border-gray-600 text-blue-600">
                            <span>{{.}}</span>
                        </label>
                        {{end}}
                    </div>
                </div>
                <p class="text-gray-500 text-xs">Click "Fetch Available Models" to get the list from Google's API</p>
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

            <button type="submit" class="w-full bg-gradient-to-r from-blue-600 to-blue-700 text-white font-semibold py-3 px-4 rounded-xl hover:from-blue-700 hover:to-blue-800 transition-all">
                Save Settings
            </button>
        </form>
        {{end}}
    </div>
</body>
</html>
{{end}}
`)
