package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"ai-gateway/internal/config"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

type SetupHandler struct {
	cfg       *config.Config
	setupMode bool
}

func NewSetupHandler(cfg *config.Config, setupMode bool) *SetupHandler {
	return &SetupHandler{cfg: cfg, setupMode: setupMode}
}

func (h *SetupHandler) IsSetupRequired() bool {
	return h.cfg.Admin.PasswordHash == "__SETUP_REQUIRED__" || h.setupMode
}

func (h *SetupHandler) RegisterRoutes(r chi.Router) {
	r.Get("/setup", h.ShowSetup)
	r.Post("/setup", h.HandleSetup)
}

func (h *SetupHandler) ShowSetup(w http.ResponseWriter, r *http.Request) {
	if !h.IsSetupRequired() {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, setupHTML, h.cfg.Server.Port)
}

func (h *SetupHandler) HandleSetup(w http.ResponseWriter, r *http.Request) {
	if !h.IsSetupRequired() {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	r.ParseForm()
	username := r.Form.Get("username")
	password := r.Form.Get("password")
	confirmPassword := r.Form.Get("confirm_password")

	if username == "" || password == "" {
		h.showError(w, "Username and password are required")
		return
	}

	if password != confirmPassword {
		h.showError(w, "Passwords do not match")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		h.showError(w, "Failed to hash password")
		return
	}

	h.cfg.Admin.Username = username
	h.cfg.Admin.PasswordHash = string(hash)

	if h.cfg.Admin.SessionSecret == "" {
		h.cfg.Admin.SessionSecret = generateRandomString(32)
	}

	h.cfg.Prometheus.Enabled = true
	h.cfg.Prometheus.Username = "prometheus"
	h.cfg.Prometheus.Password = generateRandomString(20)

	if err := config.SaveConfig(h.cfg, "config.yaml"); err != nil {
		h.showError(w, "Failed to save config: "+err.Error())
		return
	}

	http.Redirect(w, r, "/admin/login", http.StatusFound)
}

func (h *SetupHandler) showError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, setupHTML, h.cfg.Server.Port)
}

func generateRandomString(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return ""
	}
	return hex.EncodeToString(bytes)[:length]
}

var setupHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>AI Gateway - Setup</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <style>
        body { background: linear-gradient(135deg, #1e293b 0%, #0f172a 100%); min-height: 100vh; }
    </style>
</head>
<body class="flex items-center justify-center">
    <div class="w-full max-w-md">
        <div class="bg-gray-800 rounded-2xl p-8 shadow-2xl border border-gray-700">
            <div class="text-center mb-8">
                <h1 class="text-3xl font-bold text-white mb-2">AI Gateway</h1>
                <p class="text-gray-400">Setup Wizard</p>
            </div>

            <form method="POST" class="space-y-6">
                <div>
                    <label class="block text-gray-400 text-sm font-medium mb-2">Admin Username</label>
                    <input type="text" name="username" value="admin" required
                        class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                </div>

                <div>
                    <label class="block text-gray-400 text-sm font-medium mb-2">Password</label>
                    <input type="password" name="password" required
                        class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                </div>

                <div>
                    <label class="block text-gray-400 text-sm font-medium mb-2">Confirm Password</label>
                    <input type="password" name="confirm_password" required
                        class="w-full px-4 py-3 bg-gray-900 border border-gray-600 text-white rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500">
                </div>

                <div class="p-4 bg-gray-900/50 rounded-lg border border-gray-700">
                    <h3 class="text-white font-medium mb-2">Default Configuration</h3>
                    <ul class="text-gray-400 text-sm space-y-1">
                        <li>Prometheus metrics enabled</li>
                        <li>Username: prometheus</li>
                        <li>Password: auto-generated</li>
                        <li>Port: %d</li>
                    </ul>
                </div>

                <button type="submit"
                    class="w-full bg-blue-600 text-white py-3 rounded-lg hover:bg-blue-700 transition-colors font-medium">
                    Complete Setup
                </button>
            </form>
        </div>
    </div>
</body>
</html>`
