package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-gateway/internal/config"
	"ai-gateway/internal/handlers"
	"ai-gateway/internal/logger"
	"ai-gateway/internal/middleware"
	"ai-gateway/internal/models"
	"ai-gateway/internal/providers"
	"ai-gateway/internal/services"
	"ai-gateway/internal/templates"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
	setupMode = flag.Bool("setup", false, "Run setup wizard")
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	port := flag.Int("port", 0, "Port to listen on (overrides config)")
	flag.Parse()

	printBanner()

	if err := logger.Init(false); err != nil {
		log.Printf("Failed to init logger, using silent: %v", err)
		logger.InitSilent()
	}
	defer logger.Sync()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	if err := os.MkdirAll("./logs", 0755); err != nil {
		log.Fatalf("Failed to create logs directory: %v", err)
	}

	db, err := initDatabase(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	if err := autoMigrate(db); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	clientService := services.NewClientService(db)
	geminiService := services.NewGeminiService(db, cfg)
	statsService := services.NewStatsService(db)
	toolService := services.NewToolService()

	// Build the multi-backend provider registry from config
	providerRegistry := providers.BuildRegistry(cfg)

	// Set up the real-time dashboard WebSocket hub
	dashboardHub := services.NewDashboardHub(statsService)
	geminiService.SetOnRequestLogged(dashboardHub.NotifyUpdate)

	router := chi.NewRouter()

	router.Use(middleware.Recovery)
	router.Use(middleware.SecurityHeaders)
	router.Use(middleware.MaxRequestSize(10 << 20))

	proxyHandler := handlers.NewProxyHandler(geminiService, statsService)
	openaiHandler := handlers.NewOpenAIHandler(geminiService, clientService, statsService, providerRegistry, toolService)

	rateLimiter := middleware.NewRateLimiter()
	authMiddleware := middleware.NewAuthMiddleware(clientService)

	router.Group(func(r chi.Router) {
		r.Use(authMiddleware.Handler)
		r.Use(rateLimiter.Middleware)
		proxyHandler.RegisterRoutes(r)
		openaiHandler.RegisterRoutes(r)
	})

	adminHandler, err := handlers.NewAdminHandler(cfg, clientService, statsService, geminiService, dashboardHub)
	if err != nil {
		log.Fatalf("Failed to initialize admin handler: %v", err)
	}

	// Setup wizard - runs if password is not set or -setup flag is provided
	setupHandler := handlers.NewSetupHandler(cfg, *setupMode)
	if setupHandler.IsSetupRequired() {
		setupHandler.RegisterRoutes(router)
		router.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/setup", http.StatusFound)
		})
		log.Printf("Setup wizard enabled at /setup")
	} else {
		router.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/dashboard", http.StatusFound)
		})
	}

	adminHandler.RegisterRoutes(router)

	// Prometheus metrics endpoint
	if cfg.Prometheus.Enabled {
		metricsHandler := handlers.NewMetricsHandler(statsService, cfg.Prometheus.Username, cfg.Prometheus.Password)
		metricsHandler.RegisterRoutes(router)
		log.Printf("Prometheus metrics enabled at /metrics (auth: %s)", cfg.Prometheus.Username)
	}

	router.Handle("/static/*", http.FileServer(http.FS(templates.Static)))

	router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	serverPort := cfg.Server.Port
	if *port > 0 {
		serverPort = *port
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, serverPort)
	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Server starting on %s", addr)
		if cfg.Server.HTTPS.Enabled && cfg.Server.HTTPS.CertFile != "" && cfg.Server.HTTPS.KeyFile != "" {
			log.Fatal(server.ListenAndServeTLS(cfg.Server.HTTPS.CertFile, cfg.Server.HTTPS.KeyFile))
		} else {
			log.Fatal(server.ListenAndServe())
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("Server exited")
}

func initDatabase(cfg *config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(cfg.Database.Path), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}

func autoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&models.Client{},
		&models.RequestLog{},
		&models.DailyUsage{},
	)
}

func printBanner() {
	fmt.Println("AI Gateway v" + version + " (" + commit + ")")
}
