# LendFast API Documentation - Application 4

## Base URLs

| Layer | URL |
|-------|-----|
| Frontend | http://localhost:3004 |
| Backend API (BFF) | http://localhost:5001 |
| API Gateway | http://localhost:8300 |

All API routes below are accessed through the BFF at `http://localhost:5001`.

---

## Health & Status

### Platform Status
```
GET /api/v1/status
```
Returns health of all services.

### Health Check
```
GET /health
```
BFF health endpoint.

---

## Borrowers

### List Borrowers
```
GET /api/v1/borrowers
```

### Get Borrower
```
GET /api/v1/borrowers/:id
```

### Create Borrower
```
POST /api/v1/borrowers
Content-Type: application/json

{
  "first_name": "Sarah",
  "last_name": "Mitchell",
  "email": "sarah@email.com",
  "phone": "+1-555-0101",
  "date_of_birth": "1990-05-15",
  "ssn_last_four": "4532",
  "annual_income": 85000,
  "employment_status": "employed",
  "employer_name": "TechCorp",
  "employment_tenure_years": 5
}
```

### Update Borrower
```
PUT /api/v1/borrowers/:id
```

### Delete Borrower
```
DELETE /api/v1/borrowers/:id
```

---

## Loan Applications

### List Applications
```
GET /api/v1/applications
```

### Get Application
```
GET /api/v1/applications/:id
```

### Create Application
```
POST /api/v1/applications
Content-Type: application/json

{
  "borrower_id": "XAJ4S70Y6D",
  "loan_type": "personal",
  "loan_amount": 25000,
  "term_months": 36,
  "purpose": "debt_consolidation"
}
```

### Submit Application
```
POST /api/v1/applications/:id/submit
```

### Approve Application
```
POST /api/v1/applications/:id/approve
```

### Reject Application
```
POST /api/v1/applications/:id/reject
```

---

## Underwriting (ML Credit Assessment)

### Assess Creditworthiness
```
POST /api/v1/underwriting/assess
Content-Type: application/json

{
  "application_id": "APP-001",
  "borrower_id": "XAJ4S70Y6D",
  "requested_amount": 25000,
  "requested_term_months": 36,
  "loan_type": "personal",
  "annual_income": 85000,
  "monthly_debt_payments": 500,
  "employment_status": "employed",
  "employment_tenure_years": 5,
  "credit_score": 720
}
```

**Response:**
```json
{
  "decision_id": "UW-A1B2C3D4E5F6",
  "application_id": "APP-001",
  "credit_score": 720,
  "risk_score": 22.5,
  "risk_level": "low",
  "dti_ratio": 0.1547,
  "decision": "approved",
  "max_approved_amount": 25000.00,
  "approved_rate": 8.99,
  "approved_term_months": 36,
  "rejection_reasons": [],
  "monthly_payment": 793.41,
  "total_cost": 28562.76,
  "processing_time_ms": 12,
  "model_version": "v1.0-lendfast"
}
```

### Get Assessment
```
GET /api/v1/underwriting/:application_id
```

### Calculate Credit Score
```
POST /api/v1/underwriting/credit-score/calculate
Content-Type: application/json

{
  "borrower_id": "XAJ4S70Y6D",
  "annual_income": 85000,
  "total_debt": 12000,
  "credit_utilization": 0.28,
  "payment_history_score": 92.5,
  "account_age_years": 8.5,
  "recent_inquiries": 2,
  "derogatory_marks": 0
}
```

### Get Credit Score
```
GET /api/v1/underwriting/credit-score/:borrower_id
```

### Assessment History
```
GET /api/v1/underwriting/history/:borrower_id
```

### Manual Approve
```
POST /api/v1/underwriting/approve
Content-Type: application/json

{
  "application_id": "APP-001",
  "borrower_id": "XAJ4S70Y6D",
  "approved_amount": 25000,
  "approved_rate": 8.99,
  "approved_term_months": 36
}
```

### Manual Reject
```
POST /api/v1/underwriting/reject
Content-Type: application/json

{
  "application_id": "APP-002",
  "borrower_id": "Z3R84FD9Y7",
  "reasons": ["Insufficient income", "High DTI ratio"]
}
```

### EMI Calculator
```
POST /api/v1/underwriting/emi-calculator
Content-Type: application/json

{
  "principal": 25000,
  "annual_rate": 8.99,
  "term_months": 36
}
```

### Model Info
```
GET /api/v1/underwriting/model/info
```

---

## Loans

### List Loans
```
GET /api/v1/loans
```

### Get Loan
```
GET /api/v1/loans/:id
```

### Create Loan
```
POST /api/v1/loans
Content-Type: application/json

{
  "application_id": "APP-001",
  "borrower_id": "XAJ4S70Y6D",
  "loan_type": "personal",
  "principal_amount": 25000,
  "interest_rate": 8.99,
  "term_months": 36
}
```

### Get Amortization Schedule
```
GET /api/v1/loans/:id/schedule
```

### Disburse Loan
```
POST /api/v1/loans/:id/disburse
```

### Close Loan
```
POST /api/v1/loans/:id/close
```

---

## Payments

### List Payments
```
GET /api/v1/payments
```

### Get Payment
```
GET /api/v1/payments/:id
```

### Make Payment
```
POST /api/v1/payments
Content-Type: application/json

{
  "loan_id": "LN-001",
  "amount": 793.41,
  "payment_method": "auto-debit"
}
```

### Get Payments by Loan
```
GET /api/v1/payments/loan/:loan_id
```

### Process Payment
```
POST /api/v1/payments/:id/process
```

### Auto-Debit
```
POST /api/v1/payments/auto-debit
Content-Type: application/json

{
  "loan_id": "LN-001"
}
```

---

## Collections

### List Collections
```
GET /api/v1/collections
```

### Get Collection Case
```
GET /api/v1/collections/:id
```

### Create Collection Case
```
POST /api/v1/collections
Content-Type: application/json

{
  "loan_id": "LN-006",
  "borrower_id": "D7F2K5H8",
  "amount_due": 1250.00,
  "days_overdue": 15
}
```

### Get Collections by Loan
```
GET /api/v1/collections/loan/:loan_id
```

### Record Collection Action
```
POST /api/v1/collections/:id/action
Content-Type: application/json

{
  "action_type": "phone_call",
  "notes": "Left voicemail regarding overdue payment",
  "outcome": "no_contact"
}
```

### Get Delinquent Accounts
```
GET /api/v1/collections/delinquent
```

---

## Reports

### List Reports
```
GET /api/v1/reports
```

### Get Report
```
GET /api/v1/reports/:id
```

### Generate Report
```
POST /api/v1/reports/generate
Content-Type: application/json

{
  "report_type": "portfolio_summary",
  "start_date": "2024-01-01",
  "end_date": "2024-01-31"
}
```

### Portfolio Summary
```
GET /api/v1/reports/portfolio/summary
```

### Delinquency Report
```
GET /api/v1/reports/delinquency
```

### Origination Report
```
GET /api/v1/reports/origination
```

---

## Orchestrated Flows

### Full Loan Origination
```
POST /api/v1/originate
Content-Type: application/json

{
  "borrower_id": "XAJ4S70Y6D",
  "loan_amount": 25000,
  "loan_type": "personal",
  "term_months": 36,
  "purpose": "debt_consolidation",
  "annual_income": 85000,
  "monthly_debt": 500
}
```

This endpoint orchestrates the full flow:
1. Creates a loan application
2. Runs ML underwriting assessment
3. If approved, creates the loan

---

## Dashboard
```
GET /api/v1/dashboard
```

Aggregated view of recent loans, delinquent accounts, and portfolio summary.

---

## Error Responses

All errors follow this format:
```json
{
  "error": "Error description",
  "code": 400,
  "details": "Additional information"
}
```

| Status | Meaning |
|--------|---------|
| 400 | Bad Request - Invalid input |
| 401 | Unauthorized - Missing/invalid JWT |
| 404 | Not Found - Resource doesn't exist |
| 429 | Too Many Requests - Rate limit exceeded |
| 500 | Internal Server Error |
| 503 | Service Unavailable |
