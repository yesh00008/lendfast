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
			Name: "lendfast_collections_requests_total",
			Help: "Total requests to Collections Service",
		},
		[]string{"method", "endpoint", "status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lendfast_collections_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)
	delinquenciesCreated = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_delinquencies_created_total",
			Help: "Total delinquencies created",
		},
	)
	escalationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lendfast_escalations_total",
			Help: "Total escalations by level",
		},
		[]string{"level"},
	)
	contactsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_contacts_total",
			Help: "Total collection contacts made",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(delinquenciesCreated)
	prometheus.MustRegister(escalationsTotal)
	prometheus.MustRegister(contactsTotal)
}

// ─── Models ──────────────────────────────────────────────────────────────────

type Delinquency struct {
	ID              int        `json:"id"`
	DelinquencyID   string     `json:"delinquency_id"`
	LoanID          string     `json:"loan_id"`
	BorrowerID      string     `json:"borrower_id"`
	DaysPastDue     int        `json:"days_past_due"`
	AmountOverdue   float64    `json:"amount_overdue"`
	EscalationLevel string     `json:"escalation_level"`
	LastContactDate *time.Time `json:"last_contact_date,omitempty"`
	NextActionDate  *time.Time `json:"next_action_date,omitempty"`
	Status          string     `json:"status"`
	Notes           string     `json:"notes"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type EscalateRequest struct {
	Level string `json:"level" binding:"required"`
	Notes string `json:"notes"`
}

type ContactRequest struct {
	ContactMethod string `json:"contact_method" binding:"required"`
	Notes         string `json:"notes" binding:"required"`
	Outcome       string `json:"outcome" binding:"required"`
}

type ContactLog struct {
	ID            int       `json:"id"`
	ContactID     string    `json:"contact_id"`
	DelinquencyID string    `json:"delinquency_id"`
	ContactMethod string    `json:"contact_method"`
	Notes         string    `json:"notes"`
	Outcome       string    `json:"outcome"`
	ContactedAt   time.Time `json:"contacted_at"`
}

// ─── Main ────────────────────────────────────────────────────────────────────

func main() {
	log.Println("🚀 LendFast Collections Service starting...")

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
			rabbitCh.QueueDeclare("collections_events", true, false, false, false, nil)
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
		c.JSON(http.StatusOK, gin.H{"message": "pong", "service": "collections-service"})
	})

	// API routes
	v1 := router.Group("/api/v1")
	{
		v1.GET("/delinquencies", listDelinquencies)
		v1.GET("/delinquencies/:id", getDelinquency)
		v1.POST("/delinquencies", createDelinquency)
		v1.PUT("/delinquencies/:id/escalate", escalateDelinquency)
		v1.POST("/delinquencies/:id/contact", logContact)
		v1.GET("/delinquencies/:id/contacts", getContactHistory)
		v1.PUT("/delinquencies/:id/resolve", resolveDelinquency)
		v1.GET("/delinquencies/loan/:loan_id", getByLoan)
		v1.GET("/delinquencies/borrower/:borrower_id", getByBorrower)
		v1.GET("/delinquencies/scan", scanOverdueLoans)
		v1.GET("/delinquencies/summary", delinquencySummary)
	}

	// Start server
	port := getEnv("PORT", "8306")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go
func() {
		log.Printf("🚀 Collections Service running on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down Collections Service...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("Collections Service exited")
}

// ─── Database Initialization ─────────────────────────────────────────────────

func initializeTables() {
	query := `
	CREATE TABLE IF NOT EXISTS delinquencies (
		id SERIAL PRIMARY KEY,
		delinquency_id
varCHAR(50) UNIQUE NOT NULL,
		loan_id
varCHAR(50) NOT NULL,
		borrower_id
varCHAR(50) NOT NULL,
		days_past_due INT DEFAULT 0,
		amount_overdue DECIMAL(15,2) DEFAULT 0,
		escalation_level
varCHAR(30) DEFAULT 'notice',
		last_contact_date TIMESTAMP,
		next_action_date TIMESTAMP,
		status
varCHAR(30) DEFAULT 'open',
		notes TEXT DEFAULT '',
		created_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_delinquencies_delinquency_id ON delinquencies(delinquency_id);
	CREATE INDEX IF NOT EXISTS idx_delinquencies_loan_id ON delinquencies(loan_id);
	CREATE INDEX IF NOT EXISTS idx_delinquencies_borrower_id ON delinquencies(borrower_id);
	CREATE INDEX IF NOT EXISTS idx_delinquencies_status ON delinquencies(status);
	CREATE INDEX IF NOT EXISTS idx_delinquencies_escalation ON delinquencies(escalation_level);

	CREATE TABLE IF NOT EXISTS contact_log (
		id SERIAL PRIMARY KEY,
		contact_id
varCHAR(50) UNIQUE NOT NULL,
		delinquency_id
varCHAR(50) NOT NULL,
		contact_method
varCHAR(30) NOT NULL,
		notes TEXT DEFAULT '',
		outcome
varCHAR(50) DEFAULT '',
		contacted_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_contact_log_delinquency ON contact_log(delinquency_id);
	`

	if db != nil {
		if _, err := db.Exec(query); err != nil {
			log.Println("⚠ Table creation error:", err)
		} else {
			log.Println("✓ Delinquencies & Contact Log tables ready")
			seedDelinquencies()
		}
	}
}

func seedDelinquencies() {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM delinquencies").Scan(&count)
	if count > 0 {
		return
	}

	delinquencies := []struct {
		loanID, borrowerID, level, status string
		days                              int
		amount                            float64
	}{
		{"LN-seed0001", "BRW-seed0001", "notice", "open", 15, 776.72},
		{"LN-seed0002", "BRW-seed0005", "warning", "open", 35, 1302.66},
		{"LN-seed0003", "BRW-seed0003", "collections", "open", 60, 4258.41},
	}

	for _, d := range delinquencies {
		delID := "DEL-" + uuid.New().String()[:8]
		nextAction := time.Now().AddDate(0, 0, 7)
		db.Exec(`INSERT INTO delinquencies (delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, next_action_date, status)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			delID, d.loanID, d.borrowerID, d.days, d.amount, d.level, nextAction, d.status)
	}
	log.Println("✓ Seed delinquencies inserted")
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func createDelinquency(c *gin.Context) {
	var req struct {
		LoanID        string  `json:"loan_id" binding:"required"`
		BorrowerID    string  `json:"borrower_id" binding:"required"`
		DaysPastDue   int     `json:"days_past_due" binding:"required"`
		AmountOverdue float64 `json:"amount_overdue" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Determine initial escalation level
	level := "notice"
	if req.DaysPastDue > 90 {
		level = "legal"
	} else if req.DaysPastDue > 60 {
		level = "collections"
	} else if req.DaysPastDue > 30 {
		level = "warning"
	}

	// Check for existing open delinquency for this loan
	var existingID string
	err := db.QueryRow("SELECT delinquency_id FROM delinquencies WHERE loan_id = $1 AND status = 'open'", req.LoanID).Scan(&existingID)
	if err == nil {
		// Update existing
		db.Exec("UPDATE delinquencies SET days_past_due = $1, amount_overdue = $2, escalation_level = $3, updated_at = NOW() WHERE delinquency_id = $4",
			req.DaysPastDue, req.AmountOverdue, level, existingID)
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Existing delinquency updated", "delinquency_id": existingID})
		return
	}

	delID := "DEL-" + uuid.New().String()[:8]
	nextAction := time.Now().AddDate(0, 0, 7)

	var del Delinquency
	err = db.QueryRow(`
		INSERT INTO delinquencies (delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, next_action_date, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'open')
		RETURNING id, delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, last_contact_date, next_action_date, status, notes, created_at, updated_at`,
		delID, req.LoanID, req.BorrowerID, req.DaysPastDue, req.AmountOverdue, level, nextAction,
	).Scan(&del.ID, &del.DelinquencyID, &del.LoanID, &del.BorrowerID, &del.DaysPastDue,
		&del.AmountOverdue, &del.EscalationLevel, &del.LastContactDate, &del.NextActionDate,
		&del.Status, &del.Notes, &del.CreatedAt, &del.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create delinquency", "details": err.Error()})
		return
	}

	publishEvent("delinquency.created", del)
	delinquenciesCreated.Inc()

	c.JSON(http.StatusCreated, gin.H{"status": "success", "data": del})
}

func getDelinquency(c *gin.Context) {
	delID := c.Param("id")

	var del Delinquency
	err := db.QueryRow(`
		SELECT id, delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, last_contact_date, next_action_date, status, notes, created_at, updated_at
		FROM delinquencies WHERE delinquency_id = $1`, delID,
	).Scan(&del.ID, &del.DelinquencyID, &del.LoanID, &del.BorrowerID, &del.DaysPastDue,
		&del.AmountOverdue, &del.EscalationLevel, &del.LastContactDate, &del.NextActionDate,
		&del.Status, &del.Notes, &del.CreatedAt, &del.UpdatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delinquency not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": del})
}

func listDelinquencies(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	level := c.DefaultQuery("escalation_level", "")
	limit := c.DefaultQuery("limit", "50")
	offset := c.DefaultQuery("offset", "0")

	query := `SELECT id, delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, last_contact_date, next_action_date, status, notes, created_at, updated_at FROM delinquencies WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	if level != "" {
		query += fmt.Sprintf(" AND escalation_level = $%d", argIdx)
		args = append(args, level)
		argIdx++
	}

	query += fmt.Sprintf(" ORDER BY days_past_due DESC, amount_overdue DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	delinquencies := []Delinquency{}
	for rows.Next() {
		var d Delinquency
		if err := rows.Scan(&d.ID, &d.DelinquencyID, &d.LoanID, &d.BorrowerID, &d.DaysPastDue,
			&d.AmountOverdue, &d.EscalationLevel, &d.LastContactDate, &d.NextActionDate,
			&d.Status, &d.Notes, &d.CreatedAt, &d.UpdatedAt); err != nil {
			continue
		}
		delinquencies = append(delinquencies, d)
	}

	var total int
	db.QueryRow("SELECT COUNT(*) FROM delinquencies").Scan(&total)

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": delinquencies, "total": total})
}

func escalateDelinquency(c *gin.Context) {
	delID := c.Param("id")
	var req EscalateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Validate escalation level
	validLevels := map[string]int{"notice": 1, "warning": 2, "collections": 3, "legal": 4}
	newLevel, valid := validLevels[req.Level]
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid escalation level. Must be: notice, warning, collections, legal"})
		return
	}

	// Get current level
	var currentLevel string
	var currentStatus string
	err := db.QueryRow("SELECT escalation_level, status FROM delinquencies WHERE delinquency_id = $1", delID).Scan(&currentLevel, &currentStatus)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delinquency not found"})
		return
	}

	if currentStatus != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only escalate open delinquencies"})
		return
	}

	currentLevelNum := validLevels[currentLevel]
	if newLevel <= currentLevelNum {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":         "Can only escalate to a higher level",
			"current_level": currentLevel,
			"requested":     req.Level,
		})
		return
	}

	// Calculate next action date based on level
	nextActionDays := map[string]int{"notice": 14, "warning": 7, "collections": 3, "legal": 1}
	nextAction := time.Now().AddDate(0, 0, nextActionDays[req.Level])

	notes := req.Notes
	if notes == "" {
		notes = fmt.Sprintf("Escalated from %s to %s", currentLevel, req.Level)
	}

	var del Delinquency
	err = db.QueryRow(`
		UPDATE delinquencies SET escalation_level = $1, next_action_date = $2, notes = $3, updated_at = NOW()
		WHERE delinquency_id = $4
		RETURNING id, delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, last_contact_date, next_action_date, status, notes, created_at, updated_at`,
		req.Level, nextAction, notes, delID,
	).Scan(&del.ID, &del.DelinquencyID, &del.LoanID, &del.BorrowerID, &del.DaysPastDue,
		&del.AmountOverdue, &del.EscalationLevel, &del.LastContactDate, &del.NextActionDate,
		&del.Status, &del.Notes, &del.CreatedAt, &del.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to escalate", "details": err.Error()})
		return
	}

	// If escalated to legal, mark loan as defaulted
	if req.Level == "legal" {
		db.Exec("UPDATE loans SET status = 'defaulted', updated_at = NOW() WHERE loan_id = $1", del.LoanID)
	}

	publishEvent("delinquency.escalated", gin.H{"delinquency": del, "from": currentLevel, "to": req.Level})
	escalationsTotal.WithLabelValues(req.Level).Inc()

	c.JSON(http.StatusOK, gin.H{
		"status":         "success",
		"data":           del,
		"escalated_from": currentLevel,
		"escalated_to":   req.Level,
	})
}

func logContact(c *gin.Context) {
	delID := c.Param("id")
	var req ContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Validate contact method
	validMethods := map[string]bool{"phone": true, "email": true, "letter": true, "sms": true, "in_person": true}
	if !validMethods[req.ContactMethod] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid contact method. Must be: phone, email, letter, sms, in_person"})
		return
	}

	// Validate outcome
	validOutcomes := map[string]bool{"payment_promised": true, "no_answer": true, "refused": true, "payment_made": true, "negotiating": true, "disconnected": true}
	if !validOutcomes[req.Outcome] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid outcome. Must be: payment_promised, no_answer, refused, payment_made, negotiating, disconnected"})
		return
	}

	// Verify delinquency exists
	var exists bool
	db.QueryRow("SELECT EXISTS(SELECT 1 FROM delinquencies WHERE delinquency_id = $1)", delID).Scan(&exists)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delinquency not found"})
		return
	}

	contactID := "CON-" + uuid.New().String()[:8]
	var contact ContactLog
	err := db.QueryRow(`
		INSERT INTO contact_log (contact_id, delinquency_id, contact_method, notes, outcome)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, contact_id, delinquency_id, contact_method, notes, outcome, contacted_at`,
		contactID, delID, req.ContactMethod, req.Notes, req.Outcome,
	).Scan(&contact.ID, &contact.ContactID, &contact.DelinquencyID, &contact.ContactMethod,
		&contact.Notes, &contact.Outcome, &contact.ContactedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log contact", "details": err.Error()})
		return
	}

	// Update delinquency last contact date
	db.Exec("UPDATE delinquencies SET last_contact_date = NOW(), updated_at = NOW() WHERE delinquency_id = $1", delID)

	// If payment_made, resolve delinquency
	if req.Outcome == "payment_made" {
		db.Exec("UPDATE delinquencies SET status = 'resolved', updated_at = NOW() WHERE delinquency_id = $1", delID)
	}

	publishEvent("collection.contact", contact)
	contactsTotal.Inc()

	c.JSON(http.StatusCreated, gin.H{"status": "success", "data": contact})
}

func getContactHistory(c *gin.Context) {
	delID := c.Param("id")

	rows, err := db.Query(`
		SELECT id, contact_id, delinquency_id, contact_method, notes, outcome, contacted_at
		FROM contact_log WHERE delinquency_id = $1 ORDER BY contacted_at DESC`, delID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	contacts := []ContactLog{}
	for rows.Next() {
		var cl ContactLog
		if err := rows.Scan(&cl.ID, &cl.ContactID, &cl.DelinquencyID, &cl.ContactMethod,
			&cl.Notes, &cl.Outcome, &cl.ContactedAt); err != nil {
			continue
		}
		contacts = append(contacts, cl)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": contacts, "count": len(contacts)})
}

func resolveDelinquency(c *gin.Context) {
	delID := c.Param("id")
	var req struct {
		Notes string `json:"notes"`
	}
	c.ShouldBindJSON(&req)

	notes := req.Notes
	if notes == "" {
		notes = "Resolved - payment received"
	}

	result, err := db.Exec("UPDATE delinquencies SET status = 'resolved', notes = $1, updated_at = NOW() WHERE delinquency_id = $2 AND status = 'open'",
		notes, delID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve"})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Delinquency not found or already resolved"})
		return
	}

	publishEvent("delinquency.resolved", gin.H{"delinquency_id": delID})

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Delinquency resolved"})
}

func getByLoan(c *gin.Context) {
	loanID := c.Param("loan_id")

	rows, err := db.Query(`
		SELECT id, delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, status, created_at
		FROM delinquencies WHERE loan_id = $1 ORDER BY created_at DESC`, loanID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	delinquencies := []Delinquency{}
	for rows.Next() {
		var d Delinquency
		if err := rows.Scan(&d.ID, &d.DelinquencyID, &d.LoanID, &d.BorrowerID, &d.DaysPastDue,
			&d.AmountOverdue, &d.EscalationLevel, &d.Status, &d.CreatedAt); err != nil {
			continue
		}
		delinquencies = append(delinquencies, d)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": delinquencies, "count": len(delinquencies)})
}

func getByBorrower(c *gin.Context) {
	borrowerID := c.Param("borrower_id")

	rows, err := db.Query(`
		SELECT id, delinquency_id, loan_id, borrower_id, days_past_due, amount_overdue, escalation_level, status, created_at
		FROM delinquencies WHERE borrower_id = $1 ORDER BY created_at DESC`, borrowerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	delinquencies := []Delinquency{}
	for rows.Next() {
		var d Delinquency
		if err := rows.Scan(&d.ID, &d.DelinquencyID, &d.LoanID, &d.BorrowerID, &d.DaysPastDue,
			&d.AmountOverdue, &d.EscalationLevel, &d.Status, &d.CreatedAt); err != nil {
			continue
		}
		delinquencies = append(delinquencies, d)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": delinquencies, "count": len(delinquencies)})
}

func scanOverdueLoans(c *gin.Context) {
	// Scan all overdue repayment schedule items and create/update delinquencies
	rows, err := db.Query(`
		SELECT l.loan_id, l.borrower_id,
			COUNT(*) AS overdue_installments,
			SUM(rs.total_amount) AS total_overdue,
			MAX(CURRENT_DATE - rs.due_date) AS max_days_past_due
		FROM repayment_schedule rs
		JOIN loans l ON rs.loan_id = l.loan_id
		WHERE rs.status = 'pending' AND rs.due_date < CURRENT_DATE AND l.status = 'active'
		GROUP BY l.loan_id, l.borrower_id
		ORDER BY max_days_past_due DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error", "details": err.Error()})
		return
	}
	defer rows.Close()

	type OverdueLoan struct {
		LoanID              string  `json:"loan_id"`
		BorrowerID          string  `json:"borrower_id"`
		OverdueInstallments int     `json:"overdue_installments"`
		TotalOverdue        float64 `json:"total_overdue"`
		MaxDaysPastDue      int     `json:"max_days_past_due"`
	}

	results := []OverdueLoan{}
	for rows.Next() {
		var ol OverdueLoan
		if err := rows.Scan(&ol.LoanID, &ol.BorrowerID, &ol.OverdueInstallments, &ol.TotalOverdue, &ol.MaxDaysPastDue); err != nil {
			continue
		}
		results = append(results, ol)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":        "success",
		"overdue_loans": results,
		"count":         len(results),
		"scanned_at":    time.Now().Format(time.RFC3339),
	})
}

func delinquencySummary(c *gin.Context) {
	type LevelCount struct {
		Level string  `json:"level"`
		Count int     `json:"count"`
		Total float64 `json:"total_amount"`
	}

	rows, err := db.Query(`
		SELECT escalation_level, COUNT(*), COALESCE(SUM(amount_overdue), 0)
		FROM delinquencies WHERE status = 'open'
		GROUP BY escalation_level
		ORDER BY CASE escalation_level WHEN 'legal' THEN 4 WHEN 'collections' THEN 3 WHEN 'warning' THEN 2 ELSE 1 END DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	levels := []LevelCount{}
	var totalOpen int
	var totalAmount float64
	for rows.Next() {
		var lc LevelCount
		if err := rows.Scan(&lc.Level, &lc.Count, &lc.Total); err != nil {
			continue
		}
		totalOpen += lc.Count
		totalAmount += lc.Total
		levels = append(levels, lc)
	}

	var resolved int
	db.QueryRow("SELECT COUNT(*) FROM delinquencies WHERE status = 'resolved'").Scan(&resolved)

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"by_level":         levels,
			"total_open":       totalOpen,
			"total_resolved":   resolved,
			"total_amount_due": fmt.Sprintf("%.2f", totalAmount),
		},
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func publishEvent(eventType string, data interface{}) {
	if rabbitCh == nil {
		return
	}
	body, _ := json.Marshal(gin.H{
		"event_type": eventType,
		"data":       data,
		"timestamp":  time.Now(),
		"service":    "collections-service",
	})
	rabbitCh.Publish("", "collections_events", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
}

func healthCheck(c *gin.Context) {
	health := gin.H{
		"status":  "UP",
		"service": "lendfast-collections-service",
		"port":    "8306",
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
