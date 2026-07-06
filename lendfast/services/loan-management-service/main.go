package main


import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
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
			Name: "lendfast_loan_mgmt_requests_total",
			Help: "Total requests to Loan Management Service",
		},
		[]string{"method", "endpoint", "status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lendfast_loan_mgmt_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)
	loansCreated = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_loans_created_total",
			Help: "Total loans created",
		},
	)
	totalDisbursed = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "lendfast_total_disbursed_amount",
			Help: "Total amount disbursed",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(loansCreated)
	prometheus.MustRegister(totalDisbursed)
}

// ─── Models ──────────────────────────────────────────────────────────────────

type Loan struct {
	ID                 int       `json:"id"`
	LoanID             string    `json:"loan_id"`
	ApplicationID      string    `json:"application_id"`
	BorrowerID         string    `json:"borrower_id"`
	LoanType           string    `json:"loan_type"`
	LoanAmount         float64   `json:"loan_amount"`
	InterestRate       float64   `json:"interest_rate"`
	TermMonths         int       `json:"term_months"`
	MonthlyPayment     float64   `json:"monthly_payment"`
	OutstandingBalance float64   `json:"outstanding_balance"`
	TotalPaid          float64   `json:"total_paid"`
	TotalInterestPaid  float64   `json:"total_interest_paid"`
	Status             string    `json:"status"`
	DisbursedAt        time.Time `json:"disbursed_at"`
	MaturityDate       time.Time `json:"maturity_date"`
	NextPaymentDue     time.Time `json:"next_payment_due"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type RepaymentScheduleItem struct {
	ID                int        `json:"id"`
	ScheduleID        string     `json:"schedule_id"`
	LoanID            string     `json:"loan_id"`
	InstallmentNumber int        `json:"installment_number"`
	DueDate           time.Time  `json:"due_date"`
	PrincipalAmount   float64    `json:"principal_amount"`
	InterestAmount    float64    `json:"interest_amount"`
	TotalAmount       float64    `json:"total_amount"`
	Status            string     `json:"status"`
	PaidDate          *time.Time `json:"paid_date,omitempty"`
	PaidAmount        float64    `json:"paid_amount"`
}

type CreateLoanRequest struct {
	ApplicationID string  `json:"application_id" binding:"required"`
	BorrowerID    string  `json:"borrower_id" binding:"required"`
	LoanType      string  `json:"loan_type" binding:"required"`
	LoanAmount    float64 `json:"loan_amount" binding:"required"`
	InterestRate  float64 `json:"interest_rate" binding:"required"`
	TermMonths    int     `json:"term_months" binding:"required"`
}

type RefinanceRequest struct {
	NewInterestRate float64 `json:"new_interest_rate" binding:"required"`
	NewTermMonths   int     `json:"new_term_months" binding:"required"`
}

// ─── Main ────────────────────────────────────────────────────────────────────

func main() {
	log.Println("🚀 LendFast Loan Management Service starting...")

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
			rabbitCh.QueueDeclare("loan_events", true, false, false, false, nil)
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
		c.JSON(http.StatusOK, gin.H{"message": "pong", "service": "loan-management-service"})
	})

	// API routes
	v1 := router.Group("/api/v1")
	{
		v1.POST("/loans", createLoan)
		v1.GET("/loans", listLoans)
		v1.GET("/loans/:id", getLoan)
		v1.GET("/loans/:id/schedule", getRepaymentSchedule)
		v1.GET("/loans/:id/balance", getLoanBalance)
		v1.PUT("/loans/:id/refinance", refinanceLoan)
		v1.GET("/loans/borrower/:borrower_id", getLoansByBorrower)
		v1.GET("/loans/:id/summary", getLoanSummary)
	}

	// Start server
	port := getEnv("PORT", "8304")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go
func() {
		log.Printf("🚀 Loan Management Service running on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down Loan Management Service...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("Loan Management Service exited")
}

// ─── EMI Calculation ─────────────────────────────────────────────────────────

// calculateEMI: EMI = P * r * (1+r)^n / ((1+r)^n - 1)
func calculateEMI(principal, annualRate float64, termMonths int) float64 {
	if annualRate == 0 {
		return principal / float64(termMonths)
	}
	r := annualRate / 100.0 / 12.0 // monthly rate
	n := float64(termMonths)
	pow := math.Pow(1+r, n)
	emi := principal * r * pow / (pow - 1)
	return math.Round(emi*100) / 100
}

// ─── Database Initialization ─────────────────────────────────────────────────

func initializeTables() {
	query := `
	CREATE TABLE IF NOT EXISTS loans (
		id SERIAL PRIMARY KEY,
		loan_id
varCHAR(50) UNIQUE NOT NULL,
		application_id
varCHAR(50) NOT NULL,
		borrower_id
varCHAR(50) NOT NULL,
		loan_type
varCHAR(30) NOT NULL DEFAULT 'personal',
		loan_amount DECIMAL(15,2) NOT NULL,
		interest_rate DECIMAL(5,2) NOT NULL,
		term_months INT NOT NULL,
		monthly_payment DECIMAL(15,2) NOT NULL,
		outstanding_balance DECIMAL(15,2) NOT NULL,
		total_paid DECIMAL(15,2) DEFAULT 0,
		total_interest_paid DECIMAL(15,2) DEFAULT 0,
		status
varCHAR(30) DEFAULT 'active',
		disbursed_at TIMESTAMP DEFAULT NOW(),
		maturity_date TIMESTAMP,
		next_payment_due TIMESTAMP,
		created_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_loans_loan_id ON loans(loan_id);
	CREATE INDEX IF NOT EXISTS idx_loans_application_id ON loans(application_id);
	CREATE INDEX IF NOT EXISTS idx_loans_borrower_id ON loans(borrower_id);
	CREATE INDEX IF NOT EXISTS idx_loans_status ON loans(status);

	CREATE TABLE IF NOT EXISTS repayment_schedule (
		id SERIAL PRIMARY KEY,
		schedule_id
varCHAR(50) UNIQUE NOT NULL,
		loan_id
varCHAR(50) NOT NULL,
		installment_number INT NOT NULL,
		due_date DATE NOT NULL,
		principal_amount DECIMAL(15,2) NOT NULL,
		interest_amount DECIMAL(15,2) NOT NULL,
		total_amount DECIMAL(15,2) NOT NULL,
		status
varCHAR(20) DEFAULT 'pending',
		paid_date DATE,
		paid_amount DECIMAL(15,2) DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_repayment_loan_id ON repayment_schedule(loan_id);
	CREATE INDEX IF NOT EXISTS idx_repayment_status ON repayment_schedule(status);
	CREATE INDEX IF NOT EXISTS idx_repayment_due_date ON repayment_schedule(due_date);
	`

	if db != nil {
		if _, err := db.Exec(query); err != nil {
			log.Println("⚠ Table creation error:", err)
		} else {
			log.Println("✓ Loans & Repayment Schedule tables ready")
			seedLoans()
		}
	}
}

func seedLoans() {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM loans").Scan(&count)
	if count > 0 {
		return
	}

	loans := []struct {
		appID, borrowerID, loanType string
		amount, rate                float64
		term                        int
		status                      string
	}{
		{"APP-seed0001", "BRW-seed0001", "personal", 25000.00, 7.5, 36, "active"},
		{"APP-seed0005", "BRW-seed0005", "personal", 15000.00, 6.5, 24, "active"},
		{"APP-seed0003", "BRW-seed0003", "mortgage", 250000.00, 5.5, 360, "active"},
	}

	for _, l := range loans {
		loanID := "LN-" + uuid.New().String()[:8]
		emi := calculateEMI(l.amount, l.rate, l.term)
		disbursed := time.Now().AddDate(0, -6, 0) // 6 months ago
		maturity := disbursed.AddDate(0, l.term, 0)
		nextPayment := time.Now().AddDate(0, 1, 0)

		db.Exec(`INSERT INTO loans (loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, total_paid, total_interest_paid, status, disbursed_at, maturity_date, next_payment_due)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			loanID, l.appID, l.borrowerID, l.loanType, l.amount, l.rate, l.term, emi, l.amount*0.85, emi*6, emi*6-l.amount*0.15, l.status, disbursed, maturity, nextPayment)

		// Generate repayment schedule
		generateSchedule(loanID, l.amount, l.rate, l.term, disbursed)
	}
	log.Println("✓ Seed loans inserted")
}

func generateSchedule(loanID string, principal, annualRate float64, termMonths int, startDate time.Time) {
	r := annualRate / 100.0 / 12.0
	emi := calculateEMI(principal, annualRate, termMonths)
	balance := principal

	for i := 1; i <= termMonths; i++ {
		interestAmount := math.Round(balance*r*100) / 100
		principalAmount := math.Round((emi-interestAmount)*100) / 100
		if i == termMonths {
			principalAmount = math.Round(balance*100) / 100
			interestAmount = math.Round((emi-principalAmount)*100) / 100
		}
		totalAmount := principalAmount + interestAmount

		dueDate := startDate.AddDate(0, i, 0)
		scheduleID := "SCH-" + uuid.New().String()[:8]

		status := "pending"
		if i <= 6 {
// First 6 installments already paid for seed data
			status = "paid"
		}

		db.Exec(`INSERT INTO repayment_schedule (schedule_id, loan_id, installment_number, due_date, principal_amount, interest_amount, total_amount, status, paid_amount)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			scheduleID, loanID, i, dueDate, principalAmount, interestAmount, totalAmount, status,
func() float64 {
				if status == "paid" {
					return totalAmount
				}
				return 0
			}())

		balance -= principalAmount
		if balance < 0 {
			balance = 0
		}
	}
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func createLoan(c *gin.Context) {
	var req CreateLoanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Validate loan type
	validTypes := map[string]bool{"personal": true, "auto": true, "mortgage": true, "business": true}
	if !validTypes[req.LoanType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid loan type"})
		return
	}

	// Validate interest rate
	if req.InterestRate < 0.5 || req.InterestRate > 30 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Interest rate must be between 0.5% and 30%"})
		return
	}

	// Validate term
	if req.TermMonths < 6 || req.TermMonths > 360 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Term must be between 6 and 360 months"})
		return
	}

	// Check for duplicate application ID
	var exists bool
	db.QueryRow("SELECT EXISTS(SELECT 1 FROM loans WHERE application_id = $1)", req.ApplicationID).Scan(&exists)
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "Loan already exists for this application"})
		return
	}

	loanID := "LN-" + uuid.New().String()[:8]
	emi := calculateEMI(req.LoanAmount, req.InterestRate, req.TermMonths)
	disbursedAt := time.Now()
	maturityDate := disbursedAt.AddDate(0, req.TermMonths, 0)
	nextPaymentDue := disbursedAt.AddDate(0, 1, 0)

	var loan Loan
	err := db.QueryRow(`
		INSERT INTO loans (loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, status, disbursed_at, maturity_date, next_payment_due)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', $10, $11, $12)
		RETURNING id, loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, total_paid, total_interest_paid, status, disbursed_at, maturity_date, next_payment_due, created_at, updated_at`,
		loanID, req.ApplicationID, req.BorrowerID, req.LoanType, req.LoanAmount, req.InterestRate, req.TermMonths, emi, req.LoanAmount, disbursedAt, maturityDate, nextPaymentDue,
	).Scan(&loan.ID, &loan.LoanID, &loan.ApplicationID, &loan.BorrowerID, &loan.LoanType,
		&loan.LoanAmount, &loan.InterestRate, &loan.TermMonths, &loan.MonthlyPayment,
		&loan.OutstandingBalance, &loan.TotalPaid, &loan.TotalInterestPaid, &loan.Status,
		&loan.DisbursedAt, &loan.MaturityDate, &loan.NextPaymentDue, &loan.CreatedAt, &loan.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create loan", "details": err.Error()})
		return
	}

	// Generate repayment schedule
	generateSchedule(loanID, req.LoanAmount, req.InterestRate, req.TermMonths, disbursedAt)

	cacheLoan(loan)
	publishEvent("loan.created", loan)
	loansCreated.Inc()
	totalDisbursed.Add(req.LoanAmount)

	c.JSON(http.StatusCreated, gin.H{"status": "success", "data": loan})
}

func getLoan(c *gin.Context) {
	loanID := c.Param("id")

	// Try cache
	cached, err := redisClient.Get(ctx, "loan:"+loanID).Result()
	if err == nil {
		var loan Loan
		if json.Unmarshal([]byte(cached), &loan) == nil {
			c.JSON(http.StatusOK, gin.H{"status": "success", "data": loan, "source": "cache"})
			return
		}
	}

	var loan Loan
	err = db.QueryRow(`
		SELECT id, loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, total_paid, total_interest_paid, status, disbursed_at, maturity_date, next_payment_due, created_at, updated_at
		FROM loans WHERE loan_id = $1`, loanID,
	).Scan(&loan.ID, &loan.LoanID, &loan.ApplicationID, &loan.BorrowerID, &loan.LoanType,
		&loan.LoanAmount, &loan.InterestRate, &loan.TermMonths, &loan.MonthlyPayment,
		&loan.OutstandingBalance, &loan.TotalPaid, &loan.TotalInterestPaid, &loan.Status,
		&loan.DisbursedAt, &loan.MaturityDate, &loan.NextPaymentDue, &loan.CreatedAt, &loan.UpdatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Loan not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error", "details": err.Error()})
		return
	}

	cacheLoan(loan)
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": loan})
}

func listLoans(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	loanType := c.DefaultQuery("loan_type", "")
	limit := c.DefaultQuery("limit", "50")
	offset := c.DefaultQuery("offset", "0")

	query := `SELECT id, loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, total_paid, total_interest_paid, status, disbursed_at, maturity_date, next_payment_due, created_at, updated_at FROM loans WHERE 1=1`
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

	loans := []Loan{}
	for rows.Next() {
		var l Loan
		if err := rows.Scan(&l.ID, &l.LoanID, &l.ApplicationID, &l.BorrowerID, &l.LoanType,
			&l.LoanAmount, &l.InterestRate, &l.TermMonths, &l.MonthlyPayment,
			&l.OutstandingBalance, &l.TotalPaid, &l.TotalInterestPaid, &l.Status,
			&l.DisbursedAt, &l.MaturityDate, &l.NextPaymentDue, &l.CreatedAt, &l.UpdatedAt); err != nil {
			continue
		}
		loans = append(loans, l)
	}

	var total int
	db.QueryRow("SELECT COUNT(*) FROM loans").Scan(&total)

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": loans, "total": total})
}

func getRepaymentSchedule(c *gin.Context) {
	loanID := c.Param("id")

	// Verify loan exists
	var exists bool
	db.QueryRow("SELECT EXISTS(SELECT 1 FROM loans WHERE loan_id = $1)", loanID).Scan(&exists)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Loan not found"})
		return
	}

	rows, err := db.Query(`
		SELECT id, schedule_id, loan_id, installment_number, due_date, principal_amount, interest_amount, total_amount, status, paid_date, paid_amount
		FROM repayment_schedule WHERE loan_id = $1 ORDER BY installment_number ASC`, loanID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	schedule := []RepaymentScheduleItem{}
	var totalPrincipal, totalInterest, totalAmount float64
	for rows.Next() {
		var s RepaymentScheduleItem
		if err := rows.Scan(&s.ID, &s.ScheduleID, &s.LoanID, &s.InstallmentNumber, &s.DueDate,
			&s.PrincipalAmount, &s.InterestAmount, &s.TotalAmount, &s.Status, &s.PaidDate, &s.PaidAmount); err != nil {
			continue
		}
		totalPrincipal += s.PrincipalAmount
		totalInterest += s.InterestAmount
		totalAmount += s.TotalAmount
		schedule = append(schedule, s)
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   schedule,
		"summary": gin.H{
			"total_installments": len(schedule),
			"total_principal":    fmt.Sprintf("%.2f", totalPrincipal),
			"total_interest":     fmt.Sprintf("%.2f", totalInterest),
			"total_amount":       fmt.Sprintf("%.2f", totalAmount),
		},
	})
}

func getLoanBalance(c *gin.Context) {
	loanID := c.Param("id")

	var loan Loan
	err := db.QueryRow(`
		SELECT loan_id, loan_amount, outstanding_balance, total_paid, total_interest_paid, monthly_payment, next_payment_due, status
		FROM loans WHERE loan_id = $1`, loanID,
	).Scan(&loan.LoanID, &loan.LoanAmount, &loan.OutstandingBalance, &loan.TotalPaid,
		&loan.TotalInterestPaid, &loan.MonthlyPayment, &loan.NextPaymentDue, &loan.Status)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Loan not found"})
		return
	}

	// Count remaining installments
	var remaining int
	db.QueryRow("SELECT COUNT(*) FROM repayment_schedule WHERE loan_id = $1 AND status = 'pending'", loanID).Scan(&remaining)

	// Count overdue installments
	var overdue int
	db.QueryRow("SELECT COUNT(*) FROM repayment_schedule WHERE loan_id = $1 AND status = 'pending' AND due_date < NOW()", loanID).Scan(&overdue)

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"loan_id":                loanID,
			"loan_amount":            loan.LoanAmount,
			"outstanding_balance":    loan.OutstandingBalance,
			"total_paid":             loan.TotalPaid,
			"total_interest_paid":    loan.TotalInterestPaid,
			"monthly_payment":        loan.MonthlyPayment,
			"remaining_installments": remaining,
			"overdue_installments":   overdue,
			"next_payment_due":       loan.NextPaymentDue.Format("2006-01-02"),
			"loan_status":            loan.Status,
			"payoff_amount":          fmt.Sprintf("%.2f", loan.OutstandingBalance*1.005), // 0.5% early payoff fee
		},
	})
}

func refinanceLoan(c *gin.Context) {
	loanID := c.Param("id")
	var req RefinanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Validate
	if req.NewInterestRate < 0.5 || req.NewInterestRate > 30 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Interest rate must be between 0.5% and 30%"})
		return
	}
	if req.NewTermMonths < 6 || req.NewTermMonths > 360 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Term must be between 6 and 360 months"})
		return
	}

	// Get current loan
	var currentBalance float64
	var currentStatus string
	err := db.QueryRow("SELECT outstanding_balance, status FROM loans WHERE loan_id = $1", loanID).Scan(&currentBalance, &currentStatus)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Loan not found"})
		return
	}
	if currentStatus != "active" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only refinance active loans"})
		return
	}

	// Calculate new EMI on remaining balance
	newEMI := calculateEMI(currentBalance, req.NewInterestRate, req.NewTermMonths)
	newMaturity := time.Now().AddDate(0, req.NewTermMonths, 0)
	nextPayment := time.Now().AddDate(0, 1, 0)

	// Mark old loan as refinanced
	db.Exec("UPDATE loans SET status = 'refinanced', updated_at = NOW() WHERE loan_id = $1", loanID)

	// Delete old pending schedule
	db.Exec("DELETE FROM repayment_schedule WHERE loan_id = $1 AND status = 'pending'", loanID)

	// Create new loan
	newLoanID := "LN-" + uuid.New().String()[:8]
	var loan Loan
	err = db.QueryRow(`
		INSERT INTO loans (loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, status, disbursed_at, maturity_date, next_payment_due)
		SELECT $1, application_id, borrower_id, loan_type, $2, $3, $4, $5, $2, 'active', NOW(), $6, $7
		FROM loans WHERE loan_id = $8
		RETURNING id, loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, total_paid, total_interest_paid, status, disbursed_at, maturity_date, next_payment_due, created_at, updated_at`,
		newLoanID, currentBalance, req.NewInterestRate, req.NewTermMonths, newEMI, newMaturity, nextPayment, loanID,
	).Scan(&loan.ID, &loan.LoanID, &loan.ApplicationID, &loan.BorrowerID, &loan.LoanType,
		&loan.LoanAmount, &loan.InterestRate, &loan.TermMonths, &loan.MonthlyPayment,
		&loan.OutstandingBalance, &loan.TotalPaid, &loan.TotalInterestPaid, &loan.Status,
		&loan.DisbursedAt, &loan.MaturityDate, &loan.NextPaymentDue, &loan.CreatedAt, &loan.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to refinance loan", "details": err.Error()})
		return
	}

	// Generate new schedule
	generateSchedule(newLoanID, currentBalance, req.NewInterestRate, req.NewTermMonths, time.Now())

	redisClient.Del(ctx, "loan:"+loanID)
	cacheLoan(loan)
	publishEvent("loan.refinanced", gin.H{"old_loan_id": loanID, "new_loan": loan})

	c.JSON(http.StatusOK, gin.H{
		"status":      "success",
		"data":        loan,
		"old_loan_id": loanID,
		"new_loan_id": newLoanID,
		"savings":     gin.H{"message": "Refinance complete. New schedule generated."},
	})
}

func getLoansByBorrower(c *gin.Context) {
	borrowerID := c.Param("borrower_id")

	rows, err := db.Query(`
		SELECT id, loan_id, application_id, borrower_id, loan_type, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, total_paid, status, created_at
		FROM loans WHERE borrower_id = $1 ORDER BY created_at DESC`, borrowerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	loans := []Loan{}
	var totalOutstanding float64
	for rows.Next() {
		var l Loan
		if err := rows.Scan(&l.ID, &l.LoanID, &l.ApplicationID, &l.BorrowerID, &l.LoanType,
			&l.LoanAmount, &l.InterestRate, &l.TermMonths, &l.MonthlyPayment,
			&l.OutstandingBalance, &l.TotalPaid, &l.Status, &l.CreatedAt); err != nil {
			continue
		}
		if l.Status == "active" {
			totalOutstanding += l.OutstandingBalance
		}
		loans = append(loans, l)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "success",
		"data":              loans,
		"count":             len(loans),
		"total_outstanding": fmt.Sprintf("%.2f", totalOutstanding),
	})
}

func getLoanSummary(c *gin.Context) {
	loanID := c.Param("id")

	var loan Loan
	err := db.QueryRow(`
		SELECT loan_id, loan_amount, interest_rate, term_months, monthly_payment, outstanding_balance, total_paid, total_interest_paid, status, disbursed_at, maturity_date
		FROM loans WHERE loan_id = $1`, loanID,
	).Scan(&loan.LoanID, &loan.LoanAmount, &loan.InterestRate, &loan.TermMonths,
		&loan.MonthlyPayment, &loan.OutstandingBalance, &loan.TotalPaid,
		&loan.TotalInterestPaid, &loan.Status, &loan.DisbursedAt, &loan.MaturityDate)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Loan not found"})
		return
	}

	// Compute total cost of loan
	totalCost := loan.MonthlyPayment * float64(loan.TermMonths)
	totalInterest := totalCost - loan.LoanAmount
	completionPct := (loan.TotalPaid / totalCost) * 100

	var paidInstallments, pendingInstallments, overdueInstallments int
	db.QueryRow("SELECT COUNT(*) FROM repayment_schedule WHERE loan_id = $1 AND status = 'paid'", loanID).Scan(&paidInstallments)
	db.QueryRow("SELECT COUNT(*) FROM repayment_schedule WHERE loan_id = $1 AND status = 'pending'", loanID).Scan(&pendingInstallments)
	db.QueryRow("SELECT COUNT(*) FROM repayment_schedule WHERE loan_id = $1 AND status = 'pending' AND due_date < NOW()", loanID).Scan(&overdueInstallments)

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"loan_id":              loan.LoanID,
			"principal":            loan.LoanAmount,
			"interest_rate":        loan.InterestRate,
			"term_months":          loan.TermMonths,
			"monthly_payment":      loan.MonthlyPayment,
			"total_cost":           fmt.Sprintf("%.2f", totalCost),
			"total_interest":       fmt.Sprintf("%.2f", totalInterest),
			"outstanding_balance":  loan.OutstandingBalance,
			"total_paid":           loan.TotalPaid,
			"completion_pct":       fmt.Sprintf("%.1f%%", completionPct),
			"paid_installments":    paidInstallments,
			"pending_installments": pendingInstallments,
			"overdue_installments": overdueInstallments,
			"disbursed_at":         loan.DisbursedAt.Format("2006-01-02"),
			"maturity_date":        loan.MaturityDate.Format("2006-01-02"),
			"loan_status":          loan.Status,
		},
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func cacheLoan(loan Loan) {
	data, _ := json.Marshal(loan)
	redisClient.Set(ctx, "loan:"+loan.LoanID, data, 5*time.Minute)
}

func publishEvent(eventType string, data interface{}) {
	if rabbitCh == nil {
		return
	}
	body, _ := json.Marshal(gin.H{
		"event_type": eventType,
		"data":       data,
		"timestamp":  time.Now(),
		"service":    "loan-management-service",
	})
	rabbitCh.Publish("", "loan_events", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
}

func healthCheck(c *gin.Context) {
	health := gin.H{
		"status":  "UP",
		"service": "lendfast-loan-management-service",
		"port":    "8304",
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
