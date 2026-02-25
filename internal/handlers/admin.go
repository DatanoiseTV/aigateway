package handlers

import (
	"encoding/gob"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"gemini-proxy/internal/config"
	"gemini-proxy/internal/models"
	"gemini-proxy/internal/services"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/crypto/bcrypt"
)

type AdminHandler struct {
	cfg           *config.Config
	clientService *services.ClientService
	statsService  *services.StatsService
	templates     *template.Template
}

type PageData struct {
	Title     string
	User      string
	Data      interface{}
	CSRFToken string
}

func NewAdminHandler(cfg *config.Config, clientService *services.ClientService, statsService *services.StatsService) (*AdminHandler, error) {
	tmpl := template.New("admin").Funcs(template.FuncMap{
		"formatDate": formatDate,
		"formatInt":  formatInt,
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
		r.Get("/admin/stats", h.GetStats)
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
	stats, err := h.statsService.GetGlobalStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	recentLogs, _ := h.statsService.GetRecentRequests("", 10)
	modelUsage, _ := h.statsService.GetModelUsage()

	h.render(w, "dashboard.html", PageData{
		Title: "Dashboard",
		User:  h.cfg.Admin.Username,
		Data: map[string]interface{}{
			"Stats":      stats,
			"RecentLogs": recentLogs,
			"ModelUsage": modelUsage,
		},
	})
}

func (h *AdminHandler) ListClients(w http.ResponseWriter, r *http.Request) {
	clients, err := h.clientService.GetAllClients()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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

	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	client, apiKey, err := h.clientService.CreateClient(name, description, h.cfg)
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
	recentLogs, _ := h.statsService.GetRecentRequests(id, 20)

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

	err = h.clientService.UpdateClient(client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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

	apiKey, err := h.clientService.RegenerateAPIKey(id)
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

func (h *AdminHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.statsService.GetGlobalStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"total_requests":%d,"total_input_tokens":%d,"total_output_tokens":%d,"active_clients":%d,"error_rate":%.2f}`,
		stats.TotalRequestsToday, stats.TotalInputTokensToday, stats.TotalOutputTokensToday, stats.ActiveClients, stats.ErrorRate)
}

func (h *AdminHandler) render(w http.ResponseWriter, name string, data PageData) {
	err := h.templates.ExecuteTemplate(w, name, data)
	if err != nil {
		fmt.Println("Template error:", err)
	}
}

func formatDate(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	return fmt.Sprintf("%d", n)
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
    <title>{{.Title}} - Gemini Proxy</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-gray-900 min-h-screen flex items-center justify-center">
    <div class="bg-gray-800 p-8 rounded-lg shadow-xl w-96">
        <h1 class="text-2xl font-bold text-white mb-6 text-center">Admin Login</h1>
        <form method="POST" action="/admin/login">
            <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
            <div class="mb-4">
                <label class="block text-gray-300 text-sm font-bold mb-2">Username</label>
                <input type="text" name="username" class="w-full px-3 py-2 bg-gray-700 text-white rounded focus:outline-none focus:ring-2 focus:ring-blue-500">
            </div>
            <div class="mb-6">
                <label class="block text-gray-300 text-sm font-bold mb-2">Password</label>
                <input type="password" name="password" class="w-full px-3 py-2 bg-gray-700 text-white rounded focus:outline-none focus:ring-2 focus:ring-blue-500">
            </div>
            <button type="submit" class="w-full bg-blue-600 text-white font-bold py-2 px-4 rounded hover:bg-blue-700">Login</button>
        </form>
    </div>
</body>
</html>
{{end}}

{{define "dashboard.html"}}
<!DOCTYPE html>
<html>
<head>
    <title>{{.Title}} - Gemini Proxy</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
</head>
<body class="bg-gray-900 min-h-screen">
    <nav class="bg-gray-800 border-b border-gray-700">
        <div class="max-w-7xl mx-auto px-4">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center">
                    <span class="text-xl font-bold text-white">Gemini Proxy</span>
                </div>
                <div class="flex items-center space-x-4">
                    <a href="/admin/dashboard" class="text-gray-300 hover:text-white">Dashboard</a>
                    <a href="/admin/clients" class="text-gray-300 hover:text-white">Clients</a>
                    <form method="POST" action="/admin/logout" class="inline">
                        <button type="submit" class="text-gray-300 hover:text-white">Logout</button>
                    </form>
                </div>
            </div>
        </div>
    </nav>
    
    <div class="max-w-7xl mx-auto px-4 py-8">
        <h1 class="text-3xl font-bold text-white mb-8">Dashboard</h1>
        
        <div class="grid grid-cols-1 md:grid-cols-4 gap-6 mb-8">
            <div class="bg-gray-800 rounded-lg p-6">
                <div class="text-gray-400 text-sm">Requests Today</div>
                <div class="text-3xl font-bold text-white">{{(index .Data "Stats").TotalRequestsToday}}</div>
            </div>
            <div class="bg-gray-800 rounded-lg p-6">
                <div class="text-gray-400 text-sm">Input Tokens</div>
                <div class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").TotalInputTokensToday}}</div>
            </div>
            <div class="bg-gray-800 rounded-lg p-6">
                <div class="text-gray-400 text-sm">Output Tokens</div>
                <div class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").TotalOutputTokensToday}}</div>
            </div>
            <div class="bg-gray-800 rounded-lg p-6">
                <div class="text-gray-400 text-sm">Active Clients</div>
                <div class="text-3xl font-bold text-white">{{(index .Data "Stats").ActiveClients}}</div>
            </div>
        </div>
        
        <div class="bg-gray-800 rounded-lg p-6 mb-8">
            <h2 class="text-xl font-bold text-white mb-4">Model Usage Today</h2>
            <canvas id="modelChart" height="100"></canvas>
        </div>
        
        <div class="bg-gray-800 rounded-lg p-6">
            <h2 class="text-xl font-bold text-white mb-4">Recent Requests</h2>
            <div class="overflow-x-auto">
                <table class="w-full text-left">
                    <thead>
                        <tr class="text-gray-400 border-b border-gray-700">
                            <th class="py-3">Time</th>
                            <th class="py-3">Client</th>
                            <th class="py-3">Model</th>
                            <th class="py-3">Status</th>
                            <th class="py-3">Tokens</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range (index .Data "RecentLogs")}}
                        <tr class="border-b border-gray-700 text-gray-300">
                            <td class="py-3">{{formatDate .CreatedAt}}</td>
                            <td class="py-3">{{.ClientID}}</td>
                            <td class="py-3">{{.Model}}</td>
                            <td class="py-3 {{if ge .StatusCode 400}}text-red-400{{else}}text-green-400{{end}}">{{.StatusCode}}</td>
                            <td class="py-3">{{.InputTokens}} / {{.OutputTokens}}</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>
    </div>
    
    <script>
        const modelUsage = {{(index .Data "ModelUsage")}};
        const labels = Object.keys(modelUsage);
        const data = Object.values(modelUsage);
        new Chart(document.getElementById('modelChart'), {
            type: 'bar',
            data: { labels: labels, datasets: [{ label: 'Requests', data: data, backgroundColor: '#3B82F6' }] },
            options: { responsive: true, scales: { y: { beginAtZero: true, ticks: { color: '#9CA3AF' } }, x: { ticks: { color: '#9CA3AF' } } } }
        });
    </script>
</body>
</html>
{{end}}

{{define "clients.html"}}
<!DOCTYPE html>
<html>
<head>
    <title>{{.Title}} - Gemini Proxy</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-gray-900 min-h-screen">
    <nav class="bg-gray-800 border-b border-gray-700">
        <div class="max-w-7xl mx-auto px-4">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center">
                    <span class="text-xl font-bold text-white">Gemini Proxy</span>
                </div>
                <div class="flex items-center space-x-4">
                    <a href="/admin/dashboard" class="text-gray-300 hover:text-white">Dashboard</a>
                    <a href="/admin/clients" class="text-gray-300 hover:text-white">Clients</a>
                    <form method="POST" action="/admin/logout" class="inline">
                        <button type="submit" class="text-gray-300 hover:text-white">Logout</button>
                    </form>
                </div>
            </div>
        </div>
    </nav>
    
    <div class="max-w-7xl mx-auto px-4 py-8">
        <div class="flex justify-between items-center mb-8">
            <h1 class="text-3xl font-bold text-white">Clients</h1>
            <button onclick="document.getElementById('createModal').classList.remove('hidden')" class="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">New Client</button>
        </div>
        
        <div class="bg-gray-800 rounded-lg overflow-hidden">
            <table class="w-full text-left">
                <thead class="bg-gray-700">
                    <tr>
                        <th class="px-6 py-3 text-gray-300">Name</th>
                        <th class="px-6 py-3 text-gray-300">Status</th>
                        <th class="px-6 py-3 text-gray-300">Requests Today</th>
                        <th class="px-6 py-3 text-gray-300">Input Tokens</th>
                        <th class="px-6 py-3 text-gray-300">Output Tokens</th>
                        <th class="px-6 py-3 text-gray-300">Created</th>
                        <th class="px-6 py-3 text-gray-300">Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {{range .Data.Clients}}
                    <tr class="border-b border-gray-700">
                        <td class="px-6 py-4 text-white">
                            <a href="/admin/clients/{{.ID}}" class="hover:text-blue-400">{{.Name}}</a>
                        </td>
                        <td class="px-6 py-4">
                            {{if .IsActive}}
                            <span class="text-green-400">Active</span>
                            {{else}}
                            <span class="text-red-400">Disabled</span>
                            {{end}}
                        </td>
                        <td class="px-6 py-4 text-gray-300">
                            {{with (index (index .Data "ClientStats") .ID)}}
                            {{.RequestsToday}} / {{.RequestsLimit}}
                            {{else}}0 / {{.QuotaRequestsDay}}{{end}}
                        </td>
                        <td class="px-6 py-4 text-gray-300">
                            {{with (index (index .Data "ClientStats") .ID)}}
                            {{.InputTokensToday}} / {{.InputTokensLimit}}
                            {{else}}0 / {{.QuotaInputTokensDay}}{{end}}
                        </td>
                        <td class="px-6 py-4 text-gray-300">
                            {{with (index (index .Data "ClientStats") .ID)}}
                            {{.OutputTokensToday}} / {{.OutputTokensLimit}}
                            {{else}}0 / {{.QuotaOutputTokensDay}}{{end}}
                        </td>
                        <td class="px-6 py-4 text-gray-300">{{formatDate .CreatedAt}}</td>
                        <td class="px-6 py-4">
                            <a href="/admin/clients/{{.ID}}" class="text-blue-400 hover:text-blue-300">View</a>
                        </td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>
    </div>
    
    <div id="createModal" class="hidden fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
        <div class="bg-gray-800 p-6 rounded-lg w-96">
            <h2 class="text-xl font-bold text-white mb-4">Create New Client</h2>
            <form method="POST" action="/admin/clients">
                <div class="mb-4">
                    <label class="block text-gray-300 text-sm font-bold mb-2">Name</label>
                    <input type="text" name="name" class="w-full px-3 py-2 bg-gray-700 text-white rounded">
                </div>
                <div class="mb-4">
                    <label class="block text-gray-300 text-sm font-bold mb-2">Description</label>
                    <textarea name="description" class="w-full px-3 py-2 bg-gray-700 text-white rounded"></textarea>
                </div>
                <div class="flex justify-end space-x-2">
                    <button type="button" onclick="document.getElementById('createModal').classList.add('hidden')" class="px-4 py-2 text-gray-300 hover:text-white">Cancel</button>
                    <button type="submit" class="px-4 py-2 bg-blue-600 text-white rounded hover:bg-blue-700">Create</button>
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
    <title>{{.Title}} - Gemini Proxy</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-gray-900 min-h-screen">
    <nav class="bg-gray-800 border-b border-gray-700">
        <div class="max-w-7xl mx-auto px-4">
            <div class="flex items-center justify-between h-16">
                <div class="flex items-center">
                    <span class="text-xl font-bold text-white">Gemini Proxy</span>
                </div>
                <div class="flex items-center space-x-4">
                    <a href="/admin/dashboard" class="text-gray-300 hover:text-white">Dashboard</a>
                    <a href="/admin/clients" class="text-gray-300 hover:text-white">Clients</a>
                    <form method="POST" action="/admin/logout" class="inline">
                        <button type="submit" class="text-gray-300 hover:text-white">Logout</button>
                    </form>
                </div>
            </div>
        </div>
    </nav>
    
    <div class="max-w-7xl mx-auto px-4 py-8">
        <div class="flex justify-between items-center mb-8">
            <h1 class="text-3xl font-bold text-white">{{(index .Data "Client").Name}}</h1>
            <div class="space-x-2">
                <form method="POST" action="/admin/clients/{{(index .Data "Client").ID}}/regenerate" class="inline">
                    <button type="submit" class="bg-yellow-600 text-white px-4 py-2 rounded hover:bg-yellow-700">Regenerate Key</button>
                </form>
                <form method="POST" action="/admin/clients/{{(index .Data "Client").ID}}/delete" class="inline" onsubmit="return confirm('Are you sure?')">
                    <button type="submit" class="bg-red-600 text-white px-4 py-2 rounded hover:bg-red-700">Delete</button>
                </form>
            </div>
        </div>
        
        <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
            <div class="bg-gray-800 rounded-lg p-6">
                <div class="text-gray-400 text-sm">Requests Today</div>
                <div class="text-3xl font-bold text-white">{{(index .Data "Stats").RequestsToday}} / {{(index .Data "Stats").RequestsLimit}}</div>
            </div>
            <div class="bg-gray-800 rounded-lg p-6">
                <div class="text-gray-400 text-sm">Input Tokens</div>
                <div class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").InputTokensToday}} / {{(index .Data "Stats").InputTokensLimit}}</div>
            </div>
            <div class="bg-gray-800 rounded-lg p-6">
                <div class="text-gray-400 text-sm">Output Tokens</div>
                <div class="text-3xl font-bold text-white">{{formatInt (index .Data "Stats").OutputTokensToday}} / {{(index .Data "Stats").OutputTokensLimit}}</div>
            </div>
        </div>
        
        <div class="bg-gray-800 rounded-lg p-6 mb-8">
            <h2 class="text-xl font-bold text-white mb-4">Edit Client</h2>
            <form method="POST" action="/admin/clients/{{(index .Data "Client").ID}}/update">
                <div class="grid grid-cols-2 gap-4 mb-4">
                    <div>
                        <label class="block text-gray-300 text-sm font-bold mb-2">Rate Limit (min)</label>
                        <input type="number" name="rate_limit_minute" value="{{(index .Data "Client").RateLimitMinute}}" class="w-full px-3 py-2 bg-gray-700 text-white rounded">
                    </div>
                    <div>
                        <label class="block text-gray-300 text-sm font-bold mb-2">Rate Limit (hour)</label>
                        <input type="number" name="rate_limit_hour" value="{{(index .Data "Client").RateLimitHour}}" class="w-full px-3 py-2 bg-gray-700 text-white rounded">
                    </div>
                    <div>
                        <label class="block text-gray-300 text-sm font-bold mb-2">Rate Limit (day)</label>
                        <input type="number" name="rate_limit_day" value="{{(index .Data "Client").RateLimitDay}}" class="w-full px-3 py-2 bg-gray-700 text-white rounded">
                    </div>
                    <div>
                        <label class="block text-gray-300 text-sm font-bold mb-2">Quota Requests/Day</label>
                        <input type="number" name="quota_requests" value="{{(index .Data "Client").QuotaRequestsDay}}" class="w-full px-3 py-2 bg-gray-700 text-white rounded">
                    </div>
                    <div>
                        <label class="block text-gray-300 text-sm font-bold mb-2">Quota Input Tokens/Day</label>
                        <input type="number" name="quota_input_tokens" value="{{(index .Data "Client").QuotaInputTokensDay}}" class="w-full px-3 py-2 bg-gray-700 text-white rounded">
                    </div>
                    <div>
                        <label class="block text-gray-300 text-sm font-bold mb-2">Quota Output Tokens/Day</label>
                        <input type="number" name="quota_output_tokens" value="{{(index .Data "Client").QuotaOutputTokensDay}}" class="w-full px-3 py-2 bg-gray-700 text-white rounded">
                    </div>
                </div>
                <div class="mb-4">
                    <label class="flex items-center text-gray-300">
                        <input type="checkbox" name="is_active" {{if (index .Data "Client").IsActive}}checked{{end}} class="mr-2">
                        Active
                    </label>
                </div>
                <button type="submit" class="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">Update Client</button>
            </form>
        </div>
        
        <div class="bg-gray-800 rounded-lg p-6">
            <h2 class="text-xl font-bold text-white mb-4">Recent Requests</h2>
            <div class="overflow-x-auto">
                <table class="w-full text-left">
                    <thead>
                        <tr class="text-gray-400 border-b border-gray-700">
                            <th class="py-3">Time</th>
                            <th class="py-3">Model</th>
                            <th class="py-3">Status</th>
                            <th class="py-3">Latency</th>
                            <th class="py-3">Tokens</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range (index .Data "RecentLogs")}}
                        <tr class="border-b border-gray-700 text-gray-300">
                            <td class="py-3">{{formatDate .CreatedAt}}</td>
                            <td class="py-3">{{.Model}}</td>
                            <td class="py-3 {{if ge .StatusCode 400}}text-red-400{{else}}text-green-400{{end}}">{{.StatusCode}}</td>
                            <td class="py-3">{{.LatencyMs}}ms</td>
                            <td class="py-3">{{.InputTokens}} / {{.OutputTokens}}</td>
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
    <title>{{.Title}} - Gemini Proxy</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-gray-900 min-h-screen flex items-center justify-center">
    <div class="bg-gray-800 p-8 rounded-lg shadow-xl w-96">
        <h1 class="text-2xl font-bold text-white mb-6">{{if (index .Data "Regen")}}API Key Regenerated{{else}}Client Created{{end}}</h1>
        
        <div class="mb-4">
            <p class="text-gray-400 text-sm">Client: {{(index .Data "Client").Name}}</p>
        </div>
        
        <div class="mb-4">
            <p class="text-yellow-400 text-sm font-bold">Save this API key now - it will not be shown again!</p>
        </div>
        
        <div class="bg-gray-700 p-4 rounded mb-6">
            <code class="text-green-400 break-all">{{(index .Data "APIKey")}}</code>
        </div>
        
        <a href="/admin/clients" class="block text-center bg-blue-600 text-white font-bold py-2 px-4 rounded hover:bg-blue-700">Back to Clients</a>
    </div>
</body>
</html>
{{end}}
`)
