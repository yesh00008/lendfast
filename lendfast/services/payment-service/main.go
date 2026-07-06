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
			Name: "lendfast_payment_requests_total",
			Help: "Total requests to Payment Service",
		},
		[]string{"method", "endpoint", "status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lendfast_payment_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)
	paymentsProcessed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "lendfast_payments_processed_total",
			Help: "Total payments processed",
		},
	)
	paymentAmountTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "lendfast_payment_amount_total",
			Help: "Total payment amount processed",
		},
	)
	lateFeeCollected = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "lendfast_late_fee_collected_total",
			Help: "Total late fees collected",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(paymentsProcessed)
	prometheus.MustRegister(paymentAmountTotal)
	prometheus.MustRegister(lateFeeCollected)
}

// ─── Models ──────────────────────────────────────────────────────────────────

type Payment struct {
	ID              int        `json:"id"`
	PaymentID       string     `json:"payment_id"`
	LoanID          string     `json:"loan_id"`
	BorrowerID      string     `json:"borrower_id"`
	Amount          float64    `json:"amount"`
	PrincipalAmount float64    `json:"principal_amount"`
	InterestAmount  float64    `json:"interest_amount"`
	LateFee         float64    `json:"late_fee"`
	PaymentMethod   string     `json:"payment_method"`
	Status          string     `json:"status"`
	PaymentDate     time.Time  `json:"payment_date"`
	ProcessedAt     *time.Time `json:"processed_at,omitempty"`
}

type Autopay struct {
	ID                int       `json:"id"`
	AutopayID         string    `json:"autopay_id"`
	LoanID            string    `json:"loan_id"`
	BorrowerID        string    `json:"borrower_id"`
	PaymentMethod     string    `json:"payment_method"`
	AccountNumberHash string    `json:"account_number_hash,omitempty"`
	Active            bool      `json:"active"`
	CreatedAt         time.Time `json:"created_at"`
}

type CreatePaymentRequest struct {
	LoanID        string  `json:"loan_id" binding:"required"`
	BorrowerID    string  `json:"borrower_id" binding:"required"`
	Amount        float64 `json:"amount" binding:"required"`
	PaymentMethod string  `json:"payment_method" binding:"required"`
}

type CreateAutopayRequest struct {
	LoanID        string `json:"loan_id" binding:"required"`
	BorrowerID    string `json:"borrower_id" binding:"required"`
	PaymentMethod string `json:"payment_method" binding:"required"`
	AccountNumber string `json:"account_number" binding:"required"`
}

// ─── Main ────────────────────────────────────────────────────────────────────

func main() {
	log.Println("🚀 LendFast Payment Service starting...")

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
			rabbitCh.QueueDeclare("payment_events", true, false, false, false, nil)
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
		c.JSON(http.StatusOK, gin.H{"message": "pong", "service": "payment-service"})
	})

	// API routes
	v1 := router.Group("/api/v1")
	{
		// Payment routes
		v1.POST("/payments", processPayment)
		v1.GET("/payments", listPayments)
		v1.GET("/payments/:id", getPayment)
		v1.GET("/payments/loan/:loan_id", getPaymentsByLoan)
		v1.GET("/payments/borrower/:borrower_id", getPaymentsByBorrower)
		v1.POST("/payments/:id/refund", refundPayment)

		// Autopay routes
		v1.POST("/autopay", setupAutopay)
		v1.GET("/autopay/loan/:loan_id", getAutopayByLoan)
		v1.PUT("/autopay/:id", updateAutopay)
		v1.DELETE("/autopay/:id", cancelAutopay)

		// Late fee routes
		v1.GET("/late-fees/loan/:loan_id", calculateLateFees)
	}

	// Start server
	port := getEnv("PORT", "8305")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go
func() {
		log.Printf("🚀 Payment Service running on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down Payment Service...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("Payment Service exited")
}

// ─── Database Initialization ─────────────────────────────────────────────────

func initializeTables() {
	query := `
	CREATE TABLE IF NOT EXISTS payments (
		id SERIAL PRIMARY KEY,
		payment_id
varCHAR(50) UNIQUE NOT NULL,
		loan_id
varCHAR(50) NOT NULL,
		borrower_id
varCHAR(50) NOT NULL,
		amount DECIMAL(15,2) NOT NULL,
		principal_amount DECIMAL(15,2) DEFAULT 0,
		interest_amount DECIMAL(15,2) DEFAULT 0,
		late_fee DECIMAL(15,2) DEFAULT 0,
		payment_method
varCHAR(20) NOT NULL DEFAULT 'ach',
		status
varCHAR(20) DEFAULT 'pending',
		payment_date TIMESTAMP DEFAULT NOW(),
		processed_at TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_payments_payment_id ON payments(payment_id);
	CREATE INDEX IF NOT EXISTS idx_payments_loan_id ON payments(loan_id);
	CREATE INDEX IF NOT EXISTS idx_payments_borrower_id ON payments(borrower_id);
	CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);

	CREATE TABLE IF NOT EXISTS autopay (
		id SERIAL PRIMARY KEY,
		autopay_id
varCHAR(50) UNIQUE NOT NULL,
		loan_id
varCHAR(50) NOT NULL,
		borrower_id
varCHAR(50) NOT NULL,
		payment_method
varCHAR(20) NOT NULL DEFAULT 'ach',
		account_number_hash
varCHAR(255) DEFAULT '',
		active BOOLEAN DEFAULT TRUE,
		created_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_autopay_loan_id ON autopay(loan_id);
	CREATE INDEX IF NOT EXISTS idx_autopay_borrower_id ON autopay(borrower_id);
	`

	if db != nil {
		if _, err := db.Exec(query); err != nil {
			log.Println("⚠ Table creation error:", err)
		} else {
			log.Println("✓ Payments & Autopay tables ready")
			seedPayments()
		}
	}
}

func seedPayments() {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM payments").Scan(&count)
	if count > 0 {
		return
	}

	payments := []struct {
		loanID, borrowerID, method, status string
		amount, principal, interest, fee   float64
	}{
		{"LN-seed0001", "BRW-seed0001", "ach", "completed", 776.72, 620.00, 156.72, 0},
		{"LN-seed0001", "BRW-seed0001", "ach", "completed", 776.72, 623.88, 152.84, 0},
		{"LN-seed0001", "BRW-seed0001", "ach", "completed", 776.72, 627.78, 148.94, 0},
		{"LN-seed0002", "BRW-seed0005", "card", "completed", 651.33, 569.91, 81.42, 0},
		{"LN-seed0002", "BRW-seed0005", "card", "completed", 651.33, 573.00, 78.33, 0},
		{"LN-seed0003", "BRW-seed0003", "wire", "completed", 1419.47, 273.22, 1146.25, 0},
	}

	for _, p := range payments {
		paymentID := "PAY-" + uuid.New().String()[:8]
		processedAt := time.Now().AddDate(0, -1, 0)
		db.Exec(`INSERT INTO payments (payment_id, loan_id, borrower_id, amount, principal_amount, interest_amount, late_fee, payment_method, status, processed_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			paymentID, p.loanID, p.borrowerID, p.amount, p.principal, p.interest, p.fee, p.method, p.status, processedAt)
	}
	log.Println("✓ Seed payments inserted")
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func processPayment(c *gin.Context) {
	var req CreatePaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	// Validate payment method
	validMethods := map[string]bool{"ach": true, "card": true, "wire": true}
	if !validMethods[req.PaymentMethod] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payment method. Must be: ach, card, wire"})
		return
	}

	// Validate amount
	if req.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payment amount must be positive"})
		return
	}

	// Get loan details to split principal/interest
	var loanBalance, interestRate float64
	var loanStatus string
	err := db.QueryRow("SELECT outstanding_balance, interest_rate, status FROM loans WHERE loan_id = $1", req.LoanID).Scan(&loanBalance, &interestRate, &loanStatus)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Loan not found"})
		return
	}
	if loanStatus != "active" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only make payments on active loans"})
		return
	}

	// Cap payment at outstanding balance + any late fees
	var overdueCount int
	db.QueryRow("SELECT COUNT(*) FROM repayment_schedule WHERE loan_id = $1 AND status = 'pending' AND due_date < NOW()", req.LoanID).Scan(&overdueCount)
	lateFee := float64(overdueCount) * 25.0 // $25 per overdue installment

	maxPayment := loanBalance + lateFee
	if req.Amount > maxPayment {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":       "Payment exceeds outstanding balance + late fees",
			"max_payment": fmt.Sprintf("%.2f", maxPayment),
		})
		return
	}

	// Split payment: late fees first, then interest, then principal
	remaining := req.Amount
	actualLateFee := math.Min(lateFee, remaining)
	remaining -= actualLateFee

	monthlyRate := interestRate / 100.0 / 12.0
	interestPortion := math.Round(loanBalance*monthlyRate*100) / 100
	actualInterest := math.Min(interestPortion, remaining)
	remaining -= actualInterest

	actualPrincipal := remaining

	// Idempotency check
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if idempotencyKey != "" {
		cached, err := redisClient.Get(ctx, "payment:idempotency:"+idempotencyKey).Result()
		if err == nil {
			var payment Payment
			if json.Unmarshal([]byte(cached), &payment) == nil {
				c.JSON(http.StatusOK, gin.H{"status": "success", "data": payment, "source": "idempotent"})
				return
			}
		}
	}

	paymentID := "PAY-" + uuid.New().String()[:8]
	processedAt := time.Now()

	var payment Payment
	err = db.QueryRow(`
		INSERT INTO payments (payment_id, loan_id, borrower_id, amount, principal_amount, interest_amount, late_fee, payment_method, status, processed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'completed', $9)
		RETURNING id, payment_id, loan_id, borrower_id, amount, principal_amount, interest_amount, late_fee, payment_method, status, payment_date, processed_at`,
		paymentID, req.LoanID, req.BorrowerID, req.Amount, actualPrincipal, actualInterest, actualLateFee, req.PaymentMethod, processedAt,
	).Scan(&payment.ID, &payment.PaymentID, &payment.LoanID, &payment.BorrowerID, &payment.Amount,
		&payment.PrincipalAmount, &payment.InterestAmount, &payment.LateFee, &payment.PaymentMethod,
		&payment.Status, &payment.PaymentDate, &payment.ProcessedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process payment", "details": err.Error()})
		return
	}

	// Update loan balance
	newBalance := loanBalance - actualPrincipal
	if newBalance <= 0.01 {
		newBalance = 0
		db.Exec("UPDATE loans SET outstanding_balance = 0, total_paid = total_paid + $1, total_interest_paid = total_interest_paid + $2, status = 'paid_off', updated_at = NOW() WHERE loan_id = $3",
			req.Amount, actualInterest, req.LoanID)
	} else {
		db.Exec("UPDATE loans SET outstanding_balance = $1, total_paid = total_paid + $2, total_interest_paid = total_interest_paid + $3, next_payment_due = $4, updated_at = NOW() WHERE loan_id = $5",
			newBalance, req.Amount, actualInterest, time.Now().AddDate(0, 1, 0), req.LoanID)
	}

	// Mark oldest pending installment as paid
	db.Exec(`UPDATE repayment_schedule SET status = 'paid', paid_date = NOW(), paid_amount = $1
		WHERE id = (SELECT id FROM repayment_schedule WHERE loan_id = $2 AND status = 'pending' ORDER BY due_date ASC LIMIT 1)`,
		req.Amount, req.LoanID)

	// Cache idempotency
	if idempotencyKey != "" {
		data, _ := json.Marshal(payment)
		redisClient.Set(ctx, "payment:idempotency:"+idempotencyKey, data, 24*time.Hour)
	}

	publishEvent("payment.completed", payment)
	paymentsProcessed.Inc()
	paymentAmountTotal.Add(req.Amount)
	if actualLateFee > 0 {
		lateFeeCollected.Add(actualLateFee)
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "success",
		"data":   payment,
		"breakdown": gin.H{
			"principal": actualPrincipal,
			"interest":  actualInterest,
			"late_fee":  actualLateFee,
		},
		"new_balance": fmt.Sprintf("%.2f", newBalance),
	})
}

func getPayment(c *gin.Context) {
	paymentID := c.Param("id")

	var payment Payment
	err := db.QueryRow(`
		SELECT id, payment_id, loan_id, borrower_id, amount, principal_amount, interest_amount, late_fee, payment_method, status, payment_date, processed_at
		FROM payments WHERE payment_id = $1`, paymentID,
	).Scan(&payment.ID, &payment.PaymentID, &payment.LoanID, &payment.BorrowerID, &payment.Amount,
		&payment.PrincipalAmount, &payment.InterestAmount, &payment.LateFee, &payment.PaymentMethod,
		&payment.Status, &payment.PaymentDate, &payment.ProcessedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": payment})
}

func listPayments(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	method := c.DefaultQuery("payment_method", "")
	limit := c.DefaultQuery("limit", "50")
	offset := c.DefaultQuery("offset", "0")

	query := `SELECT id, payment_id, loan_id, borrower_id, amount, principal_amount, interest_amount, late_fee, payment_method, status, payment_date, processed_at FROM payments WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	if method != "" {
		query += fmt.Sprintf(" AND payment_method = $%d", argIdx)
		args = append(args, method)
		argIdx++
	}

	query += fmt.Sprintf(" ORDER BY payment_date DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	payments := []Payment{}
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.PaymentID, &p.LoanID, &p.BorrowerID, &p.Amount,
			&p.PrincipalAmount, &p.InterestAmount, &p.LateFee, &p.PaymentMethod,
			&p.Status, &p.PaymentDate, &p.ProcessedAt); err != nil {
			continue
		}
		payments = append(payments, p)
	}

	var total int
	db.QueryRow("SELECT COUNT(*) FROM payments").Scan(&total)

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": payments, "total": total})
}

func getPaymentsByLoan(c *gin.Context) {
	loanID := c.Param("loan_id")

	rows, err := db.Query(`
		SELECT id, payment_id, loan_id, borrower_id, amount, principal_amount, interest_amount, late_fee, payment_method, status, payment_date, processed_at
		FROM payments WHERE loan_id = $1 ORDER BY payment_date DESC`, loanID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	payments := []Payment{}
	var totalPaid float64
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.PaymentID, &p.LoanID, &p.BorrowerID, &p.Amount,
			&p.PrincipalAmount, &p.InterestAmount, &p.LateFee, &p.PaymentMethod,
			&p.Status, &p.PaymentDate, &p.ProcessedAt); err != nil {
			continue
		}
		if p.Status == "completed" {
			totalPaid += p.Amount
		}
		payments = append(payments, p)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"data":       payments,
		"count":      len(payments),
		"total_paid": fmt.Sprintf("%.2f", totalPaid),
	})
}

func getPaymentsByBorrower(c *gin.Context) {
	borrowerID := c.Param("borrower_id")

	rows, err := db.Query(`
		SELECT id, payment_id, loan_id, borrower_id, amount, payment_method, status, payment_date
		FROM payments WHERE borrower_id = $1 ORDER BY payment_date DESC LIMIT 50`, borrowerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	payments := []Payment{}
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.PaymentID, &p.LoanID, &p.BorrowerID, &p.Amount,
			&p.PaymentMethod, &p.Status, &p.PaymentDate); err != nil {
			continue
		}
		payments = append(payments, p)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": payments, "count": len(payments)})
}

func refundPayment(c *gin.Context) {
	paymentID := c.Param("id")

	var payment Payment
	err := db.QueryRow(`SELECT payment_id, loan_id, borrower_id, amount, principal_amount, interest_amount, status FROM payments WHERE payment_id = $1`, paymentID).
		Scan(&payment.PaymentID, &payment.LoanID, &payment.BorrowerID, &payment.Amount, &payment.PrincipalAmount, &payment.InterestAmount, &payment.Status)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}
	if payment.Status != "completed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can only refund completed payments"})
		return
	}

	// Mark as refunded
	db.Exec("UPDATE payments SET status = 'refunded' WHERE payment_id = $1", paymentID)

	// Reverse loan balance
	db.Exec("UPDATE loans SET outstanding_balance = outstanding_balance + $1, total_paid = total_paid - $2, total_interest_paid = total_interest_paid - $3, updated_at = NOW() WHERE loan_id = $4",
		payment.PrincipalAmount, payment.Amount, payment.InterestAmount, payment.LoanID)

	publishEvent("payment.refunded", gin.H{"payment_id": paymentID, "amount": payment.Amount})

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Payment refunded", "refunded_amount": payment.Amount})
}

// ─── Autopay Handlers ────────────────────────────────────────────────────────

func setupAutopay(c *gin.Context) {
	var req CreateAutopayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request", "details": err.Error()})
		return
	}

	validMethods := map[string]bool{"ach": true, "card": true}
	if !validMethods[req.PaymentMethod] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Autopay only supports ach or card"})
		return
	}

	// Check for existing active autopay
	var exists bool
	db.QueryRow("SELECT EXISTS(SELECT 1 FROM autopay WHERE loan_id = $1 AND active = TRUE)", req.LoanID).Scan(&exists)
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "Active autopay already exists for this loan"})
		return
	}

	autopayID := "AP-" + uuid.New().String()[:8]
	accountHash := fmt.Sprintf("sha256_%s", uuid.New().String()) // Mock hash

	var autopay Autopay
	err := db.QueryRow(`
		INSERT INTO autopay (autopay_id, loan_id, borrower_id, payment_method, account_number_hash, active)
		VALUES ($1, $2, $3, $4, $5, TRUE)
		RETURNING id, autopay_id, loan_id, borrower_id, payment_method, account_number_hash, active, created_at`,
		autopayID, req.LoanID, req.BorrowerID, req.PaymentMethod, accountHash,
	).Scan(&autopay.ID, &autopay.AutopayID, &autopay.LoanID, &autopay.BorrowerID,
		&autopay.PaymentMethod, &autopay.AccountNumberHash, &autopay.Active, &autopay.CreatedAt)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to setup autopay", "details": err.Error()})
		return
	}

	autopay.AccountNumberHash = "" // Don't return hash
	publishEvent("autopay.setup", autopay)

	c.JSON(http.StatusCreated, gin.H{"status": "success", "data": autopay})
}

func getAutopayByLoan(c *gin.Context) {
	loanID := c.Param("loan_id")

	var autopay Autopay
	err := db.QueryRow(`
		SELECT id, autopay_id, loan_id, borrower_id, payment_method, active, created_at
		FROM autopay WHERE loan_id = $1 AND active = TRUE`, loanID,
	).Scan(&autopay.ID, &autopay.AutopayID, &autopay.LoanID, &autopay.BorrowerID,
		&autopay.PaymentMethod, &autopay.Active, &autopay.CreatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "No active autopay for this loan"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": autopay})
}

func updateAutopay(c *gin.Context) {
	autopayID := c.Param("id")

	var req struct {
		PaymentMethod string `json:"payment_method"`
		AccountNumber string `json:"account_number"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if req.PaymentMethod != "" {
		validMethods := map[string]bool{"ach": true, "card": true}
		if !validMethods[req.PaymentMethod] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Autopay only supports ach or card"})
			return
		}
	}

	result, err := db.Exec("UPDATE autopay SET payment_method = COALESCE(NULLIF($1, ''), payment_method) WHERE autopay_id = $2 AND active = TRUE",
		req.PaymentMethod, autopayID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update autopay"})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Autopay not found or inactive"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Autopay updated"})
}

func cancelAutopay(c *gin.Context) {
	autopayID := c.Param("id")

	result, err := db.Exec("UPDATE autopay SET active = FALSE WHERE autopay_id = $1 AND active = TRUE", autopayID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel autopay"})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Autopay not found or already inactive"})
		return
	}

	publishEvent("autopay.cancelled", gin.H{"autopay_id": autopayID})

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Autopay cancelled"})
}

func calculateLateFees(c *gin.Context) {
	loanID := c.Param("loan_id")

	rows, err := db.Query(`
		SELECT installment_number, due_date, total_amount
		FROM repayment_schedule WHERE loan_id = $1 AND status = 'pending' AND due_date < NOW()
		ORDER BY due_date ASC`, loanID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	defer rows.Close()

	type OverdueItem struct {
		InstallmentNumber int       `json:"installment_number"`
		DueDate           time.Time `json:"due_date"`
		Amount            float64   `json:"amount"`
		DaysPastDue       int       `json:"days_past_due"`
		LateFee           float64   `json:"late_fee"`
	}

	items := []OverdueItem{}
	var totalLateFee float64
	for rows.Next() {
		var item OverdueItem
		if err := rows.Scan(&item.InstallmentNumber, &item.DueDate, &item.Amount); err != nil {
			continue
		}
		item.DaysPastDue = int(time.Since(item.DueDate).Hours() / 24)

		// Late fee: $25 base + 5% of amount if more than 15 days late
		item.LateFee = 25.0
		if item.DaysPastDue > 15 {
			item.LateFee += item.Amount * 0.05
		}
		item.LateFee = math.Round(item.LateFee*100) / 100
		totalLateFee += item.LateFee
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":         "success",
		"loan_id":        loanID,
		"overdue_items":  items,
		"total_late_fee": fmt.Sprintf("%.2f", totalLateFee),
		"count":          len(items),
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
		"service":    "payment-service",
	})
	rabbitCh.Publish("", "payment_events", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
}

func healthCheck(c *gin.Context) {
	health := gin.H{
		"status":  "UP",
		"service": "lendfast-payment-service",
		"port":    "8305",
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
