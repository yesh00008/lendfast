package main


import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/streadway/amqp"
)

// ─── Global
variables ────────────────────────────────────────────────────────

var (
	db          *sql.DB
	redisClient *redis.Client
	rabbitConn  *amqp.Connection
	rabbitCh    *amqp.Channel
	ctx         = context.Background()

	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lendfast_application_requests_total",
			Help: "Total requests to Application Service",
		},
		[]string{"method", "endpoint", "status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lendfast_application_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)
	applicationsSubmitted = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_applications_submitted_total",
			Help: "Total loan applications submitted",
		},
	)
	applicationsApproved = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_applications_approved_total",
			Help: "Total loan applications approved",
		},
	)
	applicationsRejected = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_applications_rejected_total",
			Help: "Total loan applications rejected",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(applicationsSubmitted)
	prometheus.MustRegister(applicationsApproved)
	prometheus.MustRegister(applicationsRejected)
}

// ─── Models ──────────────────────────────────────────────────────────────────

type Application struct {
	ID                  int       `json:"id"`
	ApplicationID       string    `json:"application_id"`
	BorrowerID          string    `json:"borrower_id"`
	LoanType            string    `json:"loan_type"`
	LoanPurpose         string    `json:"loan_purpose"`
	RequestedAmount     float64   `json:"requested_amount"`
	RequestedTermMonths int       `json:"requested_term_months"`
	EmploymentVerified  bool      `json:"employment_verified"`
	IncomeVerified      bool      `json:"income_verified"`
	Status              string    `json:"status"`
	CreditScore         int       `json:"credit_score"`
	DebtToIncomeRatio   float64   `json:"debt_to_income_ratio"`
	Notes               string    `json:"notes"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type Document struct {
	ID            int       `json:"id"`
	DocumentID    string    `json:"document_id"`
	ApplicationID string    `json:"application_id"`
	DocumentType  string    `json:"document_type"`
	FileName      string    `json:"file_name"`
	FileSize      int64     `json:"file_size"`
	Status        string    `json:"status"`
	UploadedAt    time.Time `json:"uploaded_at"`
}

type CreateApplicationRequest struct {
	BorrowerID          string  `json:"borrower_id" binding:"required"`
	LoanType            string  `json:"loan_type" binding:"required"`
	LoanPurpose         string  `json:"loan_purpose" binding:"required"`
	RequestedAmount     float64 `json:"requested_amount" binding:"required"`
	RequestedTermMonths int     `json:"requested_term_months" binding:"required"`
	CreditScore         int     `json:"credit_score"`
	DebtToIncomeRatio   float64 `json:"debt_to_income_ratio"`
}

type UpdateStatusRequest struct {
	Status string `json:"status" binding:"required"`
	Notes  string `json:"notes"`
}

type UploadDocumentRequest struct {
	DocumentType string `json:"document_type" binding:"required"`
	FileName     string `json:"file_name" binding:"required"`
	FileSize     int64  `json:"file_size" binding:"required"`
}

// Valid status transitions
var validTransitions = map[string][]string{
	"draft":        {"submitted"},
	"submitted":    {"under_review"},
	"under_review": {"approved", "rejected"},
	"approved":     {"disbursed"},
	"rejected":     {},
	"disbursed":    {},
}

// ─── Main ────────────────────────────────────────────────────────────────────

func main() {
	log.Println("🚀 LendFast Application Service starting...")

	// Database
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

	// Redis
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

	// RabbitMQ
	rabbitURL := getEnv("RABBITMQ_URL", "amqp://fintech:rabbit123@payflow-rabbitmq:5672/")
	rabbitConn, err = amqp.Dial(rabbitURL)
	if err != nil {
		log.Println("⚠ RabbitMQ not available:", err)
	} else {
		rabbitCh, err = rabbitConn.Channel()
		if err != nil {
			log.Println("⚠ RabbitMQ channel error:", err)
		} else {
			log.Println("✓ RabbitMQ connected")
			defer rabbitConn.Close()
			defer rabbitCh.Close()
			rabbitCh.QueueDeclare("application_events", true, false, false, false, nil)
		}
	}

	// Initialize tables
	initializeTables()

	// Gin - with request logging
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()
	router.Use(prometheusMiddleware())

	// Health & metrics
	router.GET("/health", healthCheck)
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))
	router.GET("/api/v1/ping",
func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong", "service": "application-service"})
	})

	// API routes
	v1 := router.Group("/api/v1")
	{
		v1.POST("/applications", createApplication)
		v1.GET("/applications", listApplications)
		v1.GET("/applications/:id", getApplication)
		v1.PUT("/applications/:id", updateApplication)
		v1.PUT("/applications/:id/status", updateApplicationStatus)
		v1.POST("/applications/:id/documents", uploadDocument)
		v1.GET("/applications/:id/documents", listDocuments)
		v1.GET("/applications/borrower/:borrower_id", getApplicationsByBorrower)
		v1.GET("/applications/:id/eligibility", checkEligibility)
	}

	// Start server
	port := getEnv("PORT", "8302")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go
func() {
		log.Printf("🚀 Application Service running on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down Application Service...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("Application Service exited")
}

// ─── Database Initialization ─────────────────────────────────────────────────

func initializeTables() {
	query := `
	CREATE TABLE IF NOT EXISTS applications (
		id SERIAL PRIMARY KEY,
		application_id
varCHAR(50) UNIQUE NOT NULL,
		borrower_id
varCHAR(50) NOT NULL,
		loan_type
varCHAR(30) NOT NULL DEFAULT 'personal',
		loan_purpose TEXT DEFAULT '',
		requested_amount DECIMAL(15,2) NOT NULL,
		requested_term_months INT NOT NULL,
		employment_verified BOOLEAN DEFAULT FALSE,
		income_verified BOOLEAN DEFAULT FALSE,
		status
varCHAR(30) DEFAULT 'draft',
		credit_score INT DEFAULT 0,
		debt_to_income_ratio DECIMAL(5,2) DEFAULT 0,
		notes TEXT DEFAULT '',
		created_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_applications_application_id ON applications(application_id);
	CREATE INDEX IF NOT EXISTS idx_applications_borrower_id ON applications(borrower_id);
	CREATE INDEX IF NOT EXISTS idx_applications_status ON applications(status);
	CREATE INDEX IF NOT EXISTS idx_applications_loan_type ON applications(loan_type);

	CREATE TABLE IF NOT EXISTS documents (
		id SERIAL PRIMARY KEY,
		document_id
varCHAR(50) UNIQUE NOT NULL,
		application_id
varCHAR(50) NOT NULL,
		document_type
varCHAR(50) NOT NULL,
		file_name
varCHAR(255) NOT NULL,
		file_size BIGINT DEFAULT 0,
		status
varCHAR(20) DEFAULT 'uploaded',
		uploaded_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_documents_application_id ON documents(application_id);
	CREATE INDEX IF NOT EXISTS idx_documents_document_id ON documents(document_id);
	`

	if db != nil {
		if _, err := db.Exec(query); err != nil {
			log.Println("⚠ Table creation error:", err)
		} else {
			log.Println("✓ Applications & Documents tables ready")
			seedApplications()
		}
	}
}

func seedApplications() {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM applications").Scan(&count)
	if count > 0 {
		return
	}

	apps := []struct {
		borrowerID string
		loanType   string
		purpose    string
		amount     float64
		term       int
		score      int
		dti        float64
		status     string
	}{
		{"BRW-seed0001", "personal", "Debt consolidation", 25000.00, 36, 740, 28.5, "approved"},
		{"BRW-seed0002", "auto", "New car purchase", 35000.00, 60, 680, 35.2, "under_review"},
		{"BRW-seed0003", "mortgage", "First home purchase", 250000.00, 360, 790, 22.0, "submitted"},
		{"BRW-seed0004", "business", "Equipment purchase", 75000.00, 48, 620, 42.1, "rejected"},
		{"BRW-seed0005", "personal", "Home improvement", 15000.00, 24, 710, 31.0, "disbursed"},
	}

	for _, a := range apps {
		appID := "APP-" + uuid.New().String()[:8]
		db.Exec(`INSERT INTO applications (application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, credit_score, debt_to_income_ratio, status)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			appID, a.borrowerID, a.loanType, a.purpose, a.amount, a.term, a.score, a.dti, a.status)
	}
	log.Println("✓ Seed applications inserted")
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func createApplication(c *gin.Context) {
	var req CreateApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Validate loan type
	validTypes := map[string]bool{"personal": true, "auto": true, "mortgage": true, "business": true}
	if !validTypes[req.LoanType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid loan type. Must be: personal, auto, mortgage, business"})
		return
	}

	// Validate amount ranges by loan type
	minMax := map[string][2]float64{
		"personal": {1000, 100000},
		"auto":     {5000, 150000},
		"mortgage": {50000, 2000000},
		"business": {10000, 500000},
	}
	limits := minMax[req.LoanType]
	if req.RequestedAmount < limits[0] || req.RequestedAmount > limits[1] {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Amount for %s loan must be between $%.0f and $%.0f", req.LoanType, limits[0], limits[1]),
		})
		return
	}

	// Validate term
	if req.RequestedTermMonths < 6 || req.RequestedTermMonths > 360 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Term must be between 6 and 360 months"})
		return
	}

	// Validate DTI ratio
	if req.DebtToIncomeRatio < 0 || req.DebtToIncomeRatio > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Debt-to-income ratio must be between 0 and 100"})
		return
	}

	applicationID := "APP-" + uuid.New().String()[:8]

	var app Application
	err := db.QueryRow(`
		INSERT INTO applications (application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, credit_score, debt_to_income_ratio, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'draft')
		RETURNING id, application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, employment_verified, income_verified, status, credit_score, debt_to_income_ratio, notes, created_at, updated_at`,
		applicationID, req.BorrowerID, req.LoanType, req.LoanPurpose, req.RequestedAmount, req.RequestedTermMonths, req.CreditScore, req.DebtToIncomeRatio,
	).Scan(&app.ID, &app.ApplicationID, &app.BorrowerID, &app.LoanType, &app.LoanPurpose,
		&app.RequestedAmount, &app.RequestedTermMonths, &app.EmploymentVerified, &app.IncomeVerified,
		&app.Status, &app.CreditScore, &app.DebtToIncomeRatio, &app.Notes, &app.CreatedAt, &app.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create application", "details": err.Error()})
		return
	}

	cacheApplication(app)
	publishEvent("application.submitted", app)
	applicationsSubmitted.Inc()

	c.JSON(http.StatusCreated, gin.H{"status": "success", "data": app})
}

func getApplication(c *gin.Context) {
	appID := c.Param("id")

	// Try cache
	cached, err := redisClient.Get(ctx, "application:"+appID).Result()
	if err == nil {
		var app Application
		if json.Unmarshal([]byte(cached), &app) == nil {
			c.JSON(http.StatusOK, gin.H{"status": "success", "data": app, "source": "cache"})
			return
		}
	}

	var app Application
	err = db.QueryRow(`
		SELECT id, application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, employment_verified, income_verified, status, credit_score, debt_to_income_ratio, notes, created_at, updated_at
		FROM applications WHERE application_id = $1`, appID,
	).Scan(&app.ID, &app.ApplicationID, &app.BorrowerID, &app.LoanType, &app.LoanPurpose,
		&app.RequestedAmount, &app.RequestedTermMonths, &app.EmploymentVerified, &app.IncomeVerified,
		&app.Status, &app.CreditScore, &app.DebtToIncomeRatio, &app.Notes, &app.CreatedAt, &app.UpdatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Application not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error", "details": err.Error()})
		return
	}

	cacheApplication(app)
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": app})
}

func listApplications(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	loanType := c.DefaultQuery("loan_type", "")
	limit := c.DefaultQuery("limit", "50")
	offset := c.DefaultQuery("offset", "0")

	query := `SELECT id, application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, employment_verified, income_verified, status, credit_score, debt_to_income_ratio, notes, created_at, updated_at FROM applications WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	if loanType != "" {
		query += fmt.Sprintf(" AND loan_type = $%d", argIdx)
		args = append(args, loanType)
		argIdx++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error", "details": err.Error()})
		return
	}
	defer rows.Close()

	applications := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.ApplicationID, &a.BorrowerID, &a.LoanType, &a.LoanPurpose,
			&a.RequestedAmount, &a.RequestedTermMonths, &a.EmploymentVerified, &a.IncomeVerified,
			&a.Status, &a.CreditScore, &a.DebtToIncomeRatio, &a.Notes, &a.CreatedAt, &a.UpdatedAt); err != nil {
			continue
		}
		applications = append(applications, a)
	}

	var total int
	db.QueryRow("SELECT COUNT(*) FROM applications").Scan(&total)

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": applications, "total": total})
}

func updateApplication(c *gin.Context) {
	appID := c.Param("id")
	var req CreateApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Only allow update if in draft status
	var currentStatus string
	err := db.QueryRow("SELECT status FROM applications WHERE application_id = $1", appID).Scan(&currentStatus)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Application not found"})
		return
	}
	if currentStatus != "draft" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only update applications in draft status"})
		return
	}

	var app Application
	err = db.QueryRow(`
		UPDATE applications SET loan_type = $1, loan_purpose = $2, requested_amount = $3, requested_term_months = $4, credit_score = $5, debt_to_income_ratio = $6, updated_at = NOW()
		WHERE application_id = $7
		RETURNING id, application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, employment_verified, income_verified, status, credit_score, debt_to_income_ratio, notes, created_at, updated_at`,
		req.LoanType, req.LoanPurpose, req.RequestedAmount, req.RequestedTermMonths, req.CreditScore, req.DebtToIncomeRatio, appID,
	).Scan(&app.ID, &app.ApplicationID, &app.BorrowerID, &app.LoanType, &app.LoanPurpose,
		&app.RequestedAmount, &app.RequestedTermMonths, &app.EmploymentVerified, &app.IncomeVerified,
		&app.Status, &app.CreditScore, &app.DebtToIncomeRatio, &app.Notes, &app.CreatedAt, &app.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update application", "details": err.Error()})
		return
	}

	cacheApplication(app)
	publishEvent("application.updated", app)

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": app})
}

func updateApplicationStatus(c *gin.Context) {
	appID := c.Param("id")
	var req UpdateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Get current status
	var currentStatus string
	err := db.QueryRow("SELECT status FROM applications WHERE application_id = $1", appID).Scan(&currentStatus)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Application not found"})
		return
	}

	// Validate transition
	allowed, exists := validTransitions[currentStatus]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown current status"})
		return
	}

	transitionAllowed := false
	for _, s := range allowed {
		if s == req.Status {
			transitionAllowed = true
			break
		}
	}
	if !transitionAllowed {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":            fmt.Sprintf("Cannot transition from '%s' to '%s'", currentStatus, req.Status),
			"allowed_statuses": allowed,
		})
		return
	}

	// Update status
	var app Application
	err = db.QueryRow(`
		UPDATE applications SET status = $1, notes = CASE WHEN $2 = '' THEN notes ELSE $2 END, updated_at = NOW()
		WHERE application_id = $3
		RETURNING id, application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, employment_verified, income_verified, status, credit_score, debt_to_income_ratio, notes, created_at, updated_at`,
		req.Status, req.Notes, appID,
	).Scan(&app.ID, &app.ApplicationID, &app.BorrowerID, &app.LoanType, &app.LoanPurpose,
		&app.RequestedAmount, &app.RequestedTermMonths, &app.EmploymentVerified, &app.IncomeVerified,
		&app.Status, &app.CreditScore, &app.DebtToIncomeRatio, &app.Notes, &app.CreatedAt, &app.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update status", "details": err.Error()})
		return
	}

	// Track metrics
	switch req.Status {
	case "approved":
		applicationsApproved.Inc()
	case "rejected":
		applicationsRejected.Inc()
	}

	cacheApplication(app)
	publishEvent("application.status."+req.Status, app)

	c.JSON(http.StatusOK, gin.H{
		"status":          "success",
		"data":            app,
		"previous_status": currentStatus,
		"new_status":      req.Status,
	})
}

func uploadDocument(c *gin.Context) {
	appID := c.Param("id")
	var req UploadDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Verify application exists
	var exists bool
	db.QueryRow("SELECT EXISTS(SELECT 1 FROM applications WHERE application_id = $1)", appID).Scan(&exists)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Application not found"})
		return
	}

	// Validate document type
	validDocTypes := map[string]bool{
		"id_proof": true, "income_proof": true, "employment_letter": true,
		"bank_statement": true, "tax_return": true, "property_deed": true,
		"insurance": true, "other": true,
	}
	if !validDocTypes[req.DocumentType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid document type"})
		return
	}

	// Validate file size (max 10MB)
	if req.FileSize > 10*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size must be under 10MB"})
		return
	}

	documentID := "DOC-" + uuid.New().String()[:8]
	var doc Document
	err := db.QueryRow(`
		INSERT INTO documents (document_id, application_id, document_type, file_name, file_size, status)
		VALUES ($1, $2, $3, $4, $5, 'uploaded')
		RETURNING id, document_id, application_id, document_type, file_name, file_size, status, uploaded_at`,
		documentID, appID, req.DocumentType, req.FileName, req.FileSize,
	).Scan(&doc.ID, &doc.DocumentID, &doc.ApplicationID, &doc.DocumentType, &doc.FileName, &doc.FileSize, &doc.Status, &doc.UploadedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload document", "details": err.Error()})
		return
	}

	publishEvent("document.uploaded", doc)

	c.JSON(http.StatusCreated, gin.H{"status": "success", "data": doc})
}

func listDocuments(c *gin.Context) {
	appID := c.Param("id")

	rows, err := db.Query(`
		SELECT id, document_id, application_id, document_type, file_name, file_size, status, uploaded_at
		FROM documents WHERE application_id = $1 ORDER BY uploaded_at DESC`, appID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	docs := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.DocumentID, &d.ApplicationID, &d.DocumentType, &d.FileName, &d.FileSize, &d.Status, &d.UploadedAt); err != nil {
			continue
		}
		docs = append(docs, d)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": docs, "count": len(docs)})
}

func getApplicationsByBorrower(c *gin.Context) {
	borrowerID := c.Param("borrower_id")

	rows, err := db.Query(`
		SELECT id, application_id, borrower_id, loan_type, loan_purpose, requested_amount, requested_term_months, status, credit_score, debt_to_income_ratio, created_at, updated_at
		FROM applications WHERE borrower_id = $1 ORDER BY created_at DESC`, borrowerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	apps := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.ApplicationID, &a.BorrowerID, &a.LoanType, &a.LoanPurpose,
			&a.RequestedAmount, &a.RequestedTermMonths, &a.Status, &a.CreditScore,
			&a.DebtToIncomeRatio, &a.CreatedAt, &a.UpdatedAt); err != nil {
			continue
		}
		apps = append(apps, a)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": apps, "count": len(apps)})
}

func checkEligibility(c *gin.Context) {
	appID := c.Param("id")

	var app Application
	err := db.QueryRow(`
		SELECT application_id, borrower_id, loan_type, requested_amount, requested_term_months, credit_score, debt_to_income_ratio
		FROM applications WHERE application_id = $1`, appID,
	).Scan(&app.ApplicationID, &app.BorrowerID, &app.LoanType, &app.RequestedAmount,
		&app.RequestedTermMonths, &app.CreditScore, &app.DebtToIncomeRatio)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Application not found"})
		return
	}

	eligible := true
	reasons := []string{}

	// Credit score check
	minScores := map[string]int{"personal": 620, "auto": 600, "mortgage": 680, "business": 650}
	if minScore, ok := minScores[app.LoanType]; ok && app.CreditScore < minScore {
		eligible = false
		reasons = append(reasons, fmt.Sprintf("Credit score %d below minimum %d for %s loan", app.CreditScore, minScore, app.LoanType))
	}

	// DTI check
	if app.DebtToIncomeRatio > 43 {
		eligible = false
		reasons = append(reasons, fmt.Sprintf("Debt-to-income ratio %.1f%% exceeds maximum 43%%", app.DebtToIncomeRatio))
	}

	// Estimate interest rate based on credit score
	var estimatedRate float64
	if app.CreditScore >= 760 {
		estimatedRate = 5.5
	} else if app.CreditScore >= 720 {
		estimatedRate = 6.5
	} else if app.CreditScore >= 680 {
		estimatedRate = 7.5
	} else if app.CreditScore >= 640 {
		estimatedRate = 9.0
	} else {
		estimatedRate = 12.0
	}

	// Calculate estimated monthly payment (EMI)
	monthlyRate := estimatedRate / 100 / 12
	n := float64(app.RequestedTermMonths)
	p := app.RequestedAmount
	var emi float64
	if monthlyRate > 0 {
		pow := 1.0
		for i := 0; i < app.RequestedTermMonths; i++ {
			pow *= (1 + monthlyRate)
		}
		emi = p * monthlyRate * pow / (pow - 1)
	} else {
		emi = p / n
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"application_id":            app.ApplicationID,
			"eligible":                  eligible,
			"reasons":                   reasons,
			"estimated_interest_rate":   estimatedRate,
			"estimated_monthly_payment": fmt.Sprintf("%.2f", emi),
			"total_estimated_cost":      fmt.Sprintf("%.2f", emi*n),
		},
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func cacheApplication(app Application) {
	data, _ := json.Marshal(app)
	redisClient.Set(ctx, "application:"+app.ApplicationID, data, 5*time.Minute)
}

func publishEvent(eventType string, data interface{}) {
	if rabbitCh == nil {
		return
	}
	body, _ := json.Marshal(gin.H{
		"event_type": eventType,
		"data":       data,
		"timestamp":  time.Now(),
		"service":    "application-service",
	})
	rabbitCh.Publish("", "application_events", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
}

func healthCheck(c *gin.Context) {
	health := gin.H{
		"status":  "UP",
		"service": "lendfast-application-service",
		"port":    "8302",
		"time":    time.Now().Format(time.RFC3339),
	}

	if db != nil {
		if err := db.Ping(); err != nil {
			health["database"] = "DOWN"
		} else {
			health["database"] = "UP"
		}
	}
	if err := redisClient.Ping(ctx).Err(); err != nil {
		health["redis"] = "DOWN"
	} else {
		health["redis"] = "UP"
	}
	if rabbitCh != nil {
		health["rabbitmq"] = "UP"
	} else {
		health["rabbitmq"] = "DOWN"
	}

	c.JSON(http.StatusOK, health)
}

func prometheusMiddleware() gin.HandlerFunc {
	return
func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start).Seconds()
		status := fmt.Sprintf("%d", c.Writer.Status())
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = c.Request.URL.Path
		}
		requestsTotal.WithLabelValues(c.Request.Method, endpoint, status).Inc()
		requestDuration.WithLabelValues(c.Request.Method, endpoint).Observe(duration)
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Suppress unused import warnings
var _ = strings.NewReader
