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
	"regexp"
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
			Name: "lendfast_borrower_requests_total",
			Help: "Total requests to Borrower Service",
		},
		[]string{"method", "endpoint", "status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lendfast_borrower_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)
	borrowersCreated = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_borrowers_created_total",
			Help: "Total borrowers created",
		},
	)
	borrowersUpdated = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_borrowers_updated_total",
			Help: "Total borrowers updated",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(borrowersCreated)
	prometheus.MustRegister(borrowersUpdated)
}

// ─── Models ──────────────────────────────────────────────────────────────────

type Borrower struct {
	ID               int       `json:"id"`
	BorrowerID       string    `json:"borrower_id"`
	Email            string    `json:"email"`
	FullName         string    `json:"full_name"`
	Phone            string    `json:"phone"`
	DateOfBirth      string    `json:"date_of_birth"`
	SSNHash          string    `json:"ssn_hash,omitempty"`
	EmploymentStatus string    `json:"employment_status"`
	EmployerName     string    `json:"employer_name"`
	AnnualIncome     float64   `json:"annual_income"`
	CreditScore      int       `json:"credit_score"`
	Address          string    `json:"address"`
	City             string    `json:"city"`
	State            string    `json:"state"`
	ZipCode          string    `json:"zip_code"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CreateBorrowerRequest struct {
	Email            string  `json:"email" binding:"required"`
	FullName         string  `json:"full_name" binding:"required"`
	Phone            string  `json:"phone" binding:"required"`
	DateOfBirth      string  `json:"date_of_birth" binding:"required"`
	SSN              string  `json:"ssn" binding:"required"`
	EmploymentStatus string  `json:"employment_status" binding:"required"`
	EmployerName     string  `json:"employer_name"`
	AnnualIncome     float64 `json:"annual_income" binding:"required"`
	CreditScore      int     `json:"credit_score"`
	Address          string  `json:"address" binding:"required"`
	City             string  `json:"city" binding:"required"`
	State            string  `json:"state" binding:"required"`
	ZipCode          string  `json:"zip_code" binding:"required"`
}

type UpdateBorrowerRequest struct {
	FullName         string  `json:"full_name"`
	Phone            string  `json:"phone"`
	EmploymentStatus string  `json:"employment_status"`
	EmployerName     string  `json:"employer_name"`
	AnnualIncome     float64 `json:"annual_income"`
	CreditScore      int     `json:"credit_score"`
	Address          string  `json:"address"`
	City             string  `json:"city"`
	State            string  `json:"state"`
	ZipCode          string  `json:"zip_code"`
}

// ─── Main ────────────────────────────────────────────────────────────────────

func main() {
	log.Println("🚀 LendFast Borrower Service starting...")

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
			rabbitCh.QueueDeclare("borrower_events", true, false, false, false, nil)
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
		c.JSON(http.StatusOK, gin.H{"message": "pong", "service": "borrower-service"})
	})

	// API routes
	v1 := router.Group("/api/v1")
	{
		v1.POST("/borrowers", createBorrower)
		v1.GET("/borrowers", listBorrowers)
		v1.GET("/borrowers/:id", getBorrower)
		v1.PUT("/borrowers/:id", updateBorrower)
		v1.DELETE("/borrowers/:id", deactivateBorrower)
		v1.GET("/borrowers/search", searchBorrowers)
		v1.GET("/borrowers/:id/summary", getBorrowerSummary)
	}

	// Start server
	port := getEnv("PORT", "8301")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go
func() {
		log.Printf("🚀 Borrower Service running on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down Borrower Service...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("Borrower Service exited")
}

// ─── Database Initialization ─────────────────────────────────────────────────

func initializeTables() {
	query := `
	CREATE TABLE IF NOT EXISTS borrowers (
		id SERIAL PRIMARY KEY,
		borrower_id
varCHAR(50) UNIQUE NOT NULL,
		email
varCHAR(255) UNIQUE NOT NULL,
		full_name
varCHAR(255) NOT NULL,
		phone
varCHAR(50) DEFAULT '',
		date_of_birth DATE,
		ssn_hash
varCHAR(255) DEFAULT '',
		employment_status
varCHAR(50) DEFAULT 'unknown',
		employer_name
varCHAR(255) DEFAULT '',
		annual_income DECIMAL(15,2) DEFAULT 0,
		credit_score INT DEFAULT 0,
		address TEXT DEFAULT '',
		city
varCHAR(100) DEFAULT '',
		state
varCHAR(50) DEFAULT '',
		zip_code
varCHAR(20) DEFAULT '',
		status
varCHAR(20) DEFAULT 'active',
		created_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_borrowers_borrower_id ON borrowers(borrower_id);
	CREATE INDEX IF NOT EXISTS idx_borrowers_email ON borrowers(email);
	CREATE INDEX IF NOT EXISTS idx_borrowers_status ON borrowers(status);
	CREATE INDEX IF NOT EXISTS idx_borrowers_credit_score ON borrowers(credit_score);
	`

	if db != nil {
		if _, err := db.Exec(query); err != nil {
			log.Println("⚠ Table creation error:", err)
		} else {
			log.Println("✓ Borrowers table ready")
			seedBorrowers()
		}
	}
}

func seedBorrowers() {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM borrowers").Scan(&count)
	if count > 0 {
		return
	}

	borrowers := []struct {
		email, fullName, phone, dob, employment, employer string
		income                                            float64
		score                                             int
		address, city, state, zip                         string
	}{
		{"john.doe@email.com", "John Doe", "+1-555-0201", "1985-03-15", "employed", "TechCorp Inc", 95000.00, 740, "123 Main St", "New York", "NY", "10001"},
		{"jane.smith@email.com", "Jane Smith", "+1-555-0202", "1990-07-22", "employed", "FinServ LLC", 78000.00, 680, "456 Oak Ave", "Los Angeles", "CA", "90001"},
		{"bob.wilson@email.com", "Bob Wilson", "+1-555-0203", "1978-11-08", "self_employed", "Wilson Consulting", 120000.00, 790, "789 Pine Dr", "Chicago", "IL", "60601"},
		{"alice.johnson@email.com", "Alice Johnson", "+1-555-0204", "1992-01-30", "employed", "HealthCare Plus", 65000.00, 620, "321 Elm St", "Houston", "TX", "77001"},
		{"charlie.brown@email.com", "Charlie Brown", "+1-555-0205", "1988-06-12", "employed", "Build Co", 85000.00, 710, "654 Maple Ln", "Phoenix", "AZ", "85001"},
	}

	for _, b := range borrowers {
		borrowerID := "BRW-" + uuid.New().String()[:8]
		ssnHash := fmt.Sprintf("hash_%s_%d", b.email, time.Now().UnixNano())
		db.Exec(`INSERT INTO borrowers (borrower_id, email, full_name, phone, date_of_birth, ssn_hash, employment_status, employer_name, annual_income, credit_score, address, city, state, zip_code, status)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'active')`,
			borrowerID, b.email, b.fullName, b.phone, b.dob, ssnHash, b.employment, b.employer, b.income, b.score, b.address, b.city, b.state, b.zip)
	}
	log.Println("✓ Seed borrowers inserted")
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func createBorrower(c *gin.Context) {
	var req CreateBorrowerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Validate email
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(req.Email) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid email format"})
		return
	}

	// Validate employment status
	validEmployment := map[string]bool{"employed": true, "self_employed": true, "unemployed": true, "retired": true, "student": true}
	if !validEmployment[req.EmploymentStatus] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid employment status. Must be: employed, self_employed, unemployed, retired, student"})
		return
	}

	// Validate annual income
	if req.AnnualIncome < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Annual income must be non-negative"})
		return
	}

	// Validate credit score range
	if req.CreditScore < 0 || req.CreditScore > 850 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Credit score must be between 0 and 850"})
		return
	}

	// Check duplicate email
	var exists bool
	db.QueryRow("SELECT EXISTS(SELECT 1 FROM borrowers WHERE email = $1)", req.Email).Scan(&exists)
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "Email already registered"})
		return
	}

	borrowerID := "BRW-" + uuid.New().String()[:8]
	ssnHash := fmt.Sprintf("sha256_%s", uuid.New().String()) // In production, use real SHA-256

	var borrower Borrower
	err := db.QueryRow(`
		INSERT INTO borrowers (borrower_id, email, full_name, phone, date_of_birth, ssn_hash, employment_status, employer_name, annual_income, credit_score, address, city, state, zip_code, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'active')
		RETURNING id, borrower_id, email, full_name, phone, date_of_birth, ssn_hash, employment_status, employer_name, annual_income, credit_score, address, city, state, zip_code, status, created_at, updated_at`,
		borrowerID, req.Email, req.FullName, req.Phone, req.DateOfBirth, ssnHash, req.EmploymentStatus, req.EmployerName, req.AnnualIncome, req.CreditScore, req.Address, req.City, req.State, req.ZipCode,
	).Scan(&borrower.ID, &borrower.BorrowerID, &borrower.Email, &borrower.FullName, &borrower.Phone,
		&borrower.DateOfBirth, &borrower.SSNHash, &borrower.EmploymentStatus, &borrower.EmployerName,
		&borrower.AnnualIncome, &borrower.CreditScore, &borrower.Address, &borrower.City, &borrower.State,
		&borrower.ZipCode, &borrower.Status, &borrower.CreatedAt, &borrower.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create borrower", "details": err.Error()})
		return
	}

	borrower.SSNHash = "" // Don't return SSN hash
	cacheBorrower(borrower)
	publishEvent("borrower.created", borrower)
	borrowersCreated.Inc()

	c.JSON(http.StatusCreated, gin.H{"status": "success", "data": borrower})
}

func getBorrower(c *gin.Context) {
	borrowerID := c.Param("id")

	// Try cache first
	cached, err := redisClient.Get(ctx, "borrower:"+borrowerID).Result()
	if err == nil {
		var borrower Borrower
		if json.Unmarshal([]byte(cached), &borrower) == nil {
			c.JSON(http.StatusOK, gin.H{"status": "success", "data": borrower, "source": "cache"})
			return
		}
	}

	var borrower Borrower
	err = db.QueryRow(`
		SELECT id, borrower_id, email, full_name, phone, COALESCE(date_of_birth::text, ''), ssn_hash, employment_status, employer_name, annual_income, credit_score, address, city, state, zip_code, status, created_at, updated_at
		FROM borrowers WHERE borrower_id = $1`, borrowerID,
	).Scan(&borrower.ID, &borrower.BorrowerID, &borrower.Email, &borrower.FullName, &borrower.Phone,
		&borrower.DateOfBirth, &borrower.SSNHash, &borrower.EmploymentStatus, &borrower.EmployerName,
		&borrower.AnnualIncome, &borrower.CreditScore, &borrower.Address, &borrower.City, &borrower.State,
		&borrower.ZipCode, &borrower.Status, &borrower.CreatedAt, &borrower.UpdatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Borrower not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error", "details": err.Error()})
		return
	}

	borrower.SSNHash = "" // Don't expose SSN hash
	cacheBorrower(borrower)
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": borrower})
}

func listBorrowers(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	employment := c.DefaultQuery("employment_status", "")
	minScore := c.DefaultQuery("min_credit_score", "")
	limit := c.DefaultQuery("limit", "50")
	offset := c.DefaultQuery("offset", "0")

	query := `SELECT id, borrower_id, email, full_name, phone, COALESCE(date_of_birth::text, ''), employment_status, employer_name, annual_income, credit_score, address, city, state, zip_code, status, created_at, updated_at FROM borrowers WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	if employment != "" {
		query += fmt.Sprintf(" AND employment_status = $%d", argIdx)
		args = append(args, employment)
		argIdx++
	}
	if minScore != "" {
		query += fmt.Sprintf(" AND credit_score >= $%d", argIdx)
		args = append(args, minScore)
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

	borrowers := []Borrower{}
	for rows.Next() {
		var b Borrower
		if err := rows.Scan(&b.ID, &b.BorrowerID, &b.Email, &b.FullName, &b.Phone,
			&b.DateOfBirth, &b.EmploymentStatus, &b.EmployerName, &b.AnnualIncome,
			&b.CreditScore, &b.Address, &b.City, &b.State, &b.ZipCode,
			&b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
			continue
		}
		borrowers = append(borrowers, b)
	}

	var total int
	db.QueryRow("SELECT COUNT(*) FROM borrowers").Scan(&total)

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": borrowers, "total": total})
}

func updateBorrower(c *gin.Context) {
	borrowerID := c.Param("id")
	var req UpdateBorrowerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Build dynamic update
	sets := []string{}
	args := []interface{}{}
	argIdx := 1

	if req.FullName != "" {
		sets = append(sets, fmt.Sprintf("full_name = $%d", argIdx))
		args = append(args, req.FullName)
		argIdx++
	}
	if req.Phone != "" {
		sets = append(sets, fmt.Sprintf("phone = $%d", argIdx))
		args = append(args, req.Phone)
		argIdx++
	}
	if req.EmploymentStatus != "" {
		validEmployment := map[string]bool{"employed": true, "self_employed": true, "unemployed": true, "retired": true, "student": true}
		if !validEmployment[req.EmploymentStatus] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid employment status"})
			return
		}
		sets = append(sets, fmt.Sprintf("employment_status = $%d", argIdx))
		args = append(args, req.EmploymentStatus)
		argIdx++
	}
	if req.EmployerName != "" {
		sets = append(sets, fmt.Sprintf("employer_name = $%d", argIdx))
		args = append(args, req.EmployerName)
		argIdx++
	}
	if req.AnnualIncome > 0 {
		sets = append(sets, fmt.Sprintf("annual_income = $%d", argIdx))
		args = append(args, req.AnnualIncome)
		argIdx++
	}
	if req.CreditScore > 0 {
		if req.CreditScore > 850 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Credit score must be between 0 and 850"})
			return
		}
		sets = append(sets, fmt.Sprintf("credit_score = $%d", argIdx))
		args = append(args, req.CreditScore)
		argIdx++
	}
	if req.Address != "" {
		sets = append(sets, fmt.Sprintf("address = $%d", argIdx))
		args = append(args, req.Address)
		argIdx++
	}
	if req.City != "" {
		sets = append(sets, fmt.Sprintf("city = $%d", argIdx))
		args = append(args, req.City)
		argIdx++
	}
	if req.State != "" {
		sets = append(sets, fmt.Sprintf("state = $%d", argIdx))
		args = append(args, req.State)
		argIdx++
	}
	if req.ZipCode != "" {
		sets = append(sets, fmt.Sprintf("zip_code = $%d", argIdx))
		args = append(args, req.ZipCode)
		argIdx++
	}

	if len(sets) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	sets = append(sets, fmt.Sprintf("updated_at = $%d", argIdx))
	args = append(args, time.Now())
	argIdx++

	args = append(args, borrowerID)
	query := fmt.Sprintf(`UPDATE borrowers SET %s WHERE borrower_id = $%d
		RETURNING id, borrower_id, email, full_name, phone, COALESCE(date_of_birth::text, ''), employment_status, employer_name, annual_income, credit_score, address, city, state, zip_code, status, created_at, updated_at`,
		strings.Join(sets, ", "), argIdx)

	var borrower Borrower
	err := db.QueryRow(query, args...).Scan(
		&borrower.ID, &borrower.BorrowerID, &borrower.Email, &borrower.FullName, &borrower.Phone,
		&borrower.DateOfBirth, &borrower.EmploymentStatus, &borrower.EmployerName, &borrower.AnnualIncome,
		&borrower.CreditScore, &borrower.Address, &borrower.City, &borrower.State, &borrower.ZipCode,
		&borrower.Status, &borrower.CreatedAt, &borrower.UpdatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Borrower not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update borrower", "details": err.Error()})
		return
	}

	cacheBorrower(borrower)
	publishEvent("borrower.updated", borrower)
	borrowersUpdated.Inc()

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": borrower})
}

func deactivateBorrower(c *gin.Context) {
	borrowerID := c.Param("id")

	result, err := db.Exec("UPDATE borrowers SET status = 'inactive', updated_at = $1 WHERE borrower_id = $2 AND status = 'active'", time.Now(), borrowerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to deactivate borrower"})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Borrower not found or already inactive"})
		return
	}

	redisClient.Del(ctx, "borrower:"+borrowerID)
	publishEvent("borrower.deactivated", gin.H{"borrower_id": borrowerID})

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Borrower deactivated"})
}

func searchBorrowers(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Search query required"})
		return
	}

	searchPattern := "%" + strings.ToLower(query) + "%"
	rows, err := db.Query(`
		SELECT id, borrower_id, email, full_name, phone, COALESCE(date_of_birth::text, ''), employment_status, employer_name, annual_income, credit_score, city, state, status, created_at, updated_at
		FROM borrowers WHERE LOWER(email) LIKE $1 OR LOWER(full_name) LIKE $1 OR LOWER(city) LIKE $1
		ORDER BY created_at DESC LIMIT 20`, searchPattern)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Search failed"})
		return
	}
	defer rows.Close()

	borrowers := []Borrower{}
	for rows.Next() {
		var b Borrower
		if err := rows.Scan(&b.ID, &b.BorrowerID, &b.Email, &b.FullName, &b.Phone,
			&b.DateOfBirth, &b.EmploymentStatus, &b.EmployerName, &b.AnnualIncome,
			&b.CreditScore, &b.City, &b.State, &b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
			continue
		}
		borrowers = append(borrowers, b)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": borrowers, "count": len(borrowers)})
}

func getBorrowerSummary(c *gin.Context) {
	borrowerID := c.Param("id")

	var borrower Borrower
	err := db.QueryRow(`
		SELECT id, borrower_id, email, full_name, phone, employment_status, annual_income, credit_score, city, state, status, created_at
		FROM borrowers WHERE borrower_id = $1`, borrowerID,
	).Scan(&borrower.ID, &borrower.BorrowerID, &borrower.Email, &borrower.FullName, &borrower.Phone,
		&borrower.EmploymentStatus, &borrower.AnnualIncome, &borrower.CreditScore, &borrower.City,
		&borrower.State, &borrower.Status, &borrower.CreatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Borrower not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	// Determine eligibility tier based on credit score
	tier := "subprime"
	if borrower.CreditScore >= 740 {
		tier = "prime"
	} else if borrower.CreditScore >= 670 {
		tier = "near_prime"
	}

	// Estimate max loan amount based on income and credit
	maxLoan := borrower.AnnualIncome * 3
	if borrower.CreditScore >= 740 {
		maxLoan = borrower.AnnualIncome * 5
	} else if borrower.CreditScore >= 670 {
		maxLoan = borrower.AnnualIncome * 4
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"borrower":           borrower,
			"eligibility_tier":   tier,
			"estimated_max_loan": maxLoan,
			"debt_capacity":      borrower.AnnualIncome * 0.43, // 43% DTI max
		},
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func cacheBorrower(borrower Borrower) {
	data, _ := json.Marshal(borrower)
	redisClient.Set(ctx, "borrower:"+borrower.BorrowerID, data, 5*time.Minute)
}

func publishEvent(eventType string, data interface{}) {
	if rabbitCh == nil {
		return
	}
	body, _ := json.Marshal(gin.H{
		"event_type": eventType,
		"data":       data,
		"timestamp":  time.Now(),
		"service":    "borrower-service",
	})
	rabbitCh.Publish("", "borrower_events", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
}

func healthCheck(c *gin.Context) {
	health := gin.H{
		"status":  "UP",
		"service": "lendfast-borrower-service",
		"port":    "8301",
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
var _ = regexp.Compile
