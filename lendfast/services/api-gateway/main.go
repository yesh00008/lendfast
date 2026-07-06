package main


import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─── Global
variables ────────────────────────────────────────────────────────

var (
	db          *sql.DB
	redisClient *redis.Client
	ctx         = context.Background()

	// Downstream service URLs
	borrowerServiceURL       string
	applicationServiceURL    string
	loanManagementServiceURL string
	paymentServiceURL        string
	collectionsServiceURL    string
	reportingServiceURL      string

	// Prometheus metrics
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lendfast_gateway_requests_total",
			Help: "Total number of requests to LendFast API Gateway",
		},
		[]string{"method", "endpoint", "status"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lendfast_gateway_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)

	activeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "lendfast_gateway_active_connections",
			Help: "Number of active connections",
		},
	)

	rateLimitHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lendfast_gateway_rate_limit_hits_total",
			Help: "Total rate limit hits",
		},
		[]string{"client_ip"},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(activeConnections)
	prometheus.MustRegister(rateLimitHits)
}

// ─── Main ────────────────────────────────────────────────────────────────────

func main() {
	log.Println("🚀 LendFast API Gateway starting...")

	// Initialize downstream service URLs
	borrowerServiceURL = getEnv("BORROWER_SERVICE_URL", "http://localhost:8301")
	applicationServiceURL = getEnv("APPLICATION_SERVICE_URL", "http://localhost:8302")
	loanManagementServiceURL = getEnv("LOAN_MANAGEMENT_SERVICE_URL", "http://localhost:8304")
	paymentServiceURL = getEnv("PAYMENT_SERVICE_URL", "http://localhost:8305")
	collectionsServiceURL = getEnv("COLLECTIONS_SERVICE_URL", "http://localhost:8306")
	reportingServiceURL = getEnv("REPORTING_SERVICE_URL", "http://localhost:8307")

	// Database connection
	var err error
	dbURL := getEnv("DATABASE_URL", "postgres://fintech:fintech123@payflow-postgres:5432/lendfast?sslmode=disable")
	db, err = sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err = db.Ping(); err != nil {
		log.Println("⚠ Database not available:", err)
	} else {
		log.Println("✓ Database connected")
	}

	// Redis connection
	redisClient = redis.NewClient(&redis.Options{
		Addr:     getEnv("REDIS_URL", "payflow-redis:6379"),
		Password: "",
		DB:       3,
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Println("⚠ Redis not available:", err)
	} else {
		log.Println("✓ Redis connected")
	}

	// Initialize API key table
	initializeGatewayTables()

	// Setup Gin router - with request logging
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// Global middleware
	router.Use(corsMiddleware())
	router.Use(requestIDMiddleware())
	router.Use(securityHeadersMiddleware())
	router.Use(metricsMiddleware())
	router.Use(rateLimitMiddleware(200))

	// Health, metrics & ping endpoints
	router.GET("/health", healthCheck)
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))
	router.GET("/api/v1/ping",
func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong", "service": "lendfast-api-gateway", "time": time.Now().Format(time.RFC3339)})
	})

	// Public routes (no auth)
	router.POST("/v1/auth/login", handleLogin)
	router.POST("/v1/auth/register", proxyTo(borrowerServiceURL))

	// Authenticated routes
	auth := router.Group("/v1")
	auth.Use(authMiddleware())
	{
		// Borrower routes
		auth.POST("/borrowers", proxyTo(borrowerServiceURL))
		auth.GET("/borrowers", proxyTo(borrowerServiceURL))
		auth.GET("/borrowers/:id", proxyTo(borrowerServiceURL))
		auth.PUT("/borrowers/:id", proxyTo(borrowerServiceURL))
		auth.DELETE("/borrowers/:id", proxyTo(borrowerServiceURL))
		auth.GET("/borrowers/search", proxyTo(borrowerServiceURL))

		// Loan application routes
		auth.POST("/applications", proxyTo(applicationServiceURL))
		auth.GET("/applications", proxyTo(applicationServiceURL))
		auth.GET("/applications/:id", proxyTo(applicationServiceURL))
		auth.PUT("/applications/:id", proxyTo(applicationServiceURL))
		auth.PUT("/applications/:id/status", proxyTo(applicationServiceURL))
		auth.POST("/applications/:id/documents", proxyTo(applicationServiceURL))
		auth.GET("/applications/:id/documents", proxyTo(applicationServiceURL))

		// Loan management routes
		auth.POST("/loans", proxyTo(loanManagementServiceURL))
		auth.GET("/loans", proxyTo(loanManagementServiceURL))
		auth.GET("/loans/:id", proxyTo(loanManagementServiceURL))
		auth.GET("/loans/:id/schedule", proxyTo(loanManagementServiceURL))
		auth.PUT("/loans/:id/refinance", proxyTo(loanManagementServiceURL))
		auth.GET("/loans/borrower/:borrower_id", proxyTo(loanManagementServiceURL))

		// Payment routes (stricter rate limit)
		auth.POST("/payments", rateLimitMiddleware(50), proxyTo(paymentServiceURL))
		auth.GET("/payments", proxyTo(paymentServiceURL))
		auth.GET("/payments/:id", proxyTo(paymentServiceURL))
		auth.GET("/payments/loan/:loan_id", proxyTo(paymentServiceURL))
		auth.POST("/autopay", proxyTo(paymentServiceURL))
		auth.GET("/autopay/loan/:loan_id", proxyTo(paymentServiceURL))
		auth.PUT("/autopay/:id", proxyTo(paymentServiceURL))
		auth.DELETE("/autopay/:id", proxyTo(paymentServiceURL))

		// Collections routes
		auth.GET("/delinquencies", proxyTo(collectionsServiceURL))
		auth.GET("/delinquencies/:id", proxyTo(collectionsServiceURL))
		auth.PUT("/delinquencies/:id/escalate", proxyTo(collectionsServiceURL))
		auth.POST("/delinquencies/:id/contact", proxyTo(collectionsServiceURL))

		// Reporting routes
		auth.GET("/reports/portfolio", proxyTo(reportingServiceURL))
		auth.GET("/reports/delinquency", proxyTo(reportingServiceURL))
		auth.GET("/reports/revenue", proxyTo(reportingServiceURL))
		auth.GET("/reports/dashboard", proxyTo(reportingServiceURL))
	}

	// Start server
	port := getEnv("PORT", "8300")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go
func() {
		log.Printf("🚀 LendFast API Gateway running on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down LendFast API Gateway...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("LendFast API Gateway exited")
}

// ─── Database Initialization ─────────────────────────────────────────────────

func initializeGatewayTables() {
	query := `
	CREATE TABLE IF NOT EXISTS api_keys (
		id SERIAL PRIMARY KEY,
		key_id
varCHAR(100) UNIQUE NOT NULL,
		key_hash
varCHAR(255) NOT NULL,
		client_name
varCHAR(255) NOT NULL,
		scopes TEXT DEFAULT 'read',
		rate_limit INT DEFAULT 200,
		active BOOLEAN DEFAULT TRUE,
		created_at TIMESTAMP DEFAULT NOW(),
		expires_at TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_api_keys_key_id ON api_keys(key_id);

	CREATE TABLE IF NOT EXISTS gateway_audit_log (
		id SERIAL PRIMARY KEY,
		request_id
varCHAR(100),
		client_ip
varCHAR(50),
		method
varCHAR(10),
		path TEXT,
		status_code INT,
		latency_ms FLOAT,
		user_agent TEXT,
		created_at TIMESTAMP DEFAULT NOW()
	);
	CREATE INDEX IF NOT EXISTS idx_gateway_audit_created ON gateway_audit_log(created_at);
	`

	if db != nil {
		if _, err := db.Exec(query); err != nil {
			log.Println("⚠ Gateway table creation error:", err)
		} else {
			log.Println("✓ Gateway tables ready")
		}
	}
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func handleLogin(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// In production, validate credentials and issue a real JWT
	// For now, return a mock token
	token := fmt.Sprintf("lf-token-%d", time.Now().UnixNano())
	redisClient.Set(ctx, "lendfast:session:"+token, req.Email, 24*time.Hour)

	c.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"token":      token,
		"token_type": "Bearer",
		"expires_in": 86400,
	})
}

// proxyTo creates a reverse proxy handler to the target service
func proxyTo(targetURL string) gin.HandlerFunc {
	return
func(c *gin.Context) {
		target, err := url.Parse(targetURL)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "service unavailable"})
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler =
func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Proxy error to %s: %v", targetURL, err)
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(gin.H{"error": "service unavailable", "details": err.Error()})
		}

		// Forward request ID
		if reqID, exists := c.Get("request_id"); exists {
			c.Request.Header.Set("X-Request-ID", reqID.(string))
		}

		c.Request.Host = target.Host
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func healthCheck(c *gin.Context) {
	services := map[string]string{
		"borrower-service":        borrowerServiceURL,
		"application-service":     applicationServiceURL,
		"loan-management-service": loanManagementServiceURL,
		"payment-service":         paymentServiceURL,
		"collections-service":     collectionsServiceURL,
		"reporting-service":       reportingServiceURL,
	}

	health := gin.H{
		"status":  "UP",
		"service": "lendfast-api-gateway",
		"port":    "8300",
		"time":    time.Now().Format(time.RFC3339),
	}

	// Check database
	if db != nil {
		if err := db.Ping(); err != nil {
			health["database"] = "DOWN"
		} else {
			health["database"] = "UP"
		}
	}

	// Check Redis
	if err := redisClient.Ping(ctx).Err(); err != nil {
		health["redis"] = "DOWN"
	} else {
		health["redis"] = "UP"
	}

	// Check downstream services
	serviceHealth := make(map[string]string)
	client := &http.Client{Timeout: 2 * time.Second}
	for name, svcURL := range services {
		resp, err := client.Get(svcURL + "/health")
		if err != nil || resp.StatusCode != 200 {
			serviceHealth[name] = "DOWN"
		} else {
			serviceHealth[name] = "UP"
			resp.Body.Close()
		}
	}
	health["services"] = serviceHealth

	c.JSON(http.StatusOK, health)
}

// ─── Middleware ───────────────────────────────────────────────────────────────

func corsMiddleware() gin.HandlerFunc {
	return
func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Request-ID, Idempotency-Key")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func requestIDMiddleware() gin.HandlerFunc {
	return
func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = fmt.Sprintf("lf-%d", time.Now().UnixNano())
		}
		c.Header("X-Request-ID", requestID)
		c.Set("request_id", requestID)
		c.Next()

		// Audit log (async)
		go
func(rid, ip, method, path string, status int) {
			if db != nil {
				db.Exec(`INSERT INTO gateway_audit_log (request_id, client_ip, method, path, status_code, created_at) VALUES ($1, $2, $3, $4, $5, NOW())`,
					rid, ip, method, path, status)
			}
		}(requestID, c.ClientIP(), c.Request.Method, c.Request.URL.Path, c.Writer.Status())
	}
}

func securityHeadersMiddleware() gin.HandlerFunc {
	return
func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Content-Security-Policy", "default-src 'self'")
		c.Next()
	}
}

func metricsMiddleware() gin.HandlerFunc {
	return
func(c *gin.Context) {
		start := time.Now()
		activeConnections.Inc()

		c.Next()

		duration := time.Since(start).Seconds()
		status := fmt.Sprintf("%d", c.Writer.Status())
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = c.Request.URL.Path
		}

		requestsTotal.WithLabelValues(c.Request.Method, endpoint, status).Inc()
		requestDuration.WithLabelValues(c.Request.Method, endpoint).Observe(duration)
		activeConnections.Dec()
	}
}

func rateLimitMiddleware(maxRequests int) gin.HandlerFunc {
	return
func(c *gin.Context) {
		clientIP := c.ClientIP()
		key := fmt.Sprintf("lendfast:ratelimit:%s", clientIP)

		count, err := redisClient.Incr(ctx, key).Result()
		if err != nil {
			// Fail open if Redis is down
			c.Next()
			return
		}

		if count == 1 {
			redisClient.Expire(ctx, key, time.Minute)
		}

		if count > int64(maxRequests) {
			c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", maxRequests))
			c.Header("X-RateLimit-Remaining", "0")
			c.Header("Retry-After", "60")
			rateLimitHits.WithLabelValues(clientIP).Inc()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":   "rate limit exceeded",
				"message": fmt.Sprintf("Maximum %d requests per minute", maxRequests),
			})
			return
		}

		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", maxRequests))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", int64(maxRequests)-count))
		c.Next()
	}
}

func authMiddleware() gin.HandlerFunc {
	return
func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authorization header required"})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization format, expected: Bearer <token>"})
			return
		}

		token := parts[1]

		// Check token in Redis session store
		email, err := redisClient.Get(ctx, "lendfast:session:"+token).Result()
		if err != nil {
			// Fail open: accept token if Redis is down (downstream services also validate)
			log.Printf("Token validation skipped (Redis unavailable): %v", err)
			c.Set("token", token)
			c.Next()
			return
		}

		c.Set("token", token)
		c.Set("user_email", email)
		c.Next()
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Suppress unused import warnings
var _ = io.ReadAll
var _ = strings.NewReader
