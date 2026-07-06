# LendFast Architecture - Application 4

## Overview

LendFast is a comprehensive lending and credit platform built with a microservices architecture. It supports personal, auto, mortgage, and business loans with ML-powered automated underwriting.

## System Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    LendFast Platform                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐    ┌──────────────────┐    ┌──────────────┐  │
│  │   Frontend    │───▶│  Backend API      │───▶│ API Gateway  │  │
│  │  (React SPA)  │    │  (Node.js BFF)    │    │   (Go/Gin)   │  │
│  │  Port: 3004   │    │  Port: 5001       │    │  Port: 8300  │  │
│  └──────────────┘    └──────────────────┘    └──────┬───────┘  │
│                                                       │          │
│  ┌────────────────────────────────────────────────────┼────────┐ │
│  │                  Microservices Layer                │        │ │
│  │                                                    ▼        │ │
│  │  ┌─────────────┐  ┌───────────────┐  ┌──────────────────┐  │ │
│  │  │  Borrower    │  │  Application  │  │  Underwriting    │  │ │
│  │  │  Service     │  │  Service      │  │  Service (ML)    │  │ │
│  │  │  Go :8301    │  │  Go :8302     │  │  Python :8303    │  │ │
│  │  └─────────────┘  └───────────────┘  └──────────────────┘  │ │
│  │                                                             │ │
│  │  ┌─────────────┐  ┌───────────────┐  ┌──────────────────┐  │ │
│  │  │  Loan Mgmt  │  │   Payment     │  │  Collections     │  │ │
│  │  │  Service    │  │   Service     │  │  Service         │  │ │
│  │  │  Go :8304   │  │   Go :8305    │  │  Go :8306        │  │ │
│  │  └─────────────┘  └───────────────┘  └──────────────────┘  │ │
│  │                                                             │ │
│  │  ┌─────────────┐                                            │ │
│  │  │  Reporting  │                                            │ │
│  │  │  Service    │                                            │ │
│  │  │  Go :8307   │                                            │ │
│  │  └─────────────┘                                            │ │
│  └─────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │                   Shared Infrastructure                      │ │
│  │  PostgreSQL :5432 │ Redis :6379 │ RabbitMQ :5672            │ │
│  │  (DB: lendfast)   │ (DB: 3)     │ (Events/Queues)          │ │
│  └─────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

## Technology Stack

| Component | Technology | Version |
|-----------|-----------|---------|
| Go Services | Go + Gin | 1.21 |
| ML Service | Python + FastAPI | 3.11 |
| BFF API | Node.js + Express | 18 |
| Frontend | React | 19 |
| Database | PostgreSQL | 16 |
| Cache | Redis | 7 |
| Messaging | RabbitMQ | 3.13 |
| Containers | Docker | Multi-stage |

## Services Detail

### 1. API Gateway (Port 8300)
- **Language**: Go (Gin)
- **Role**: Entry point for all API requests
- **Features**: 
  - JWT authentication & validation
  - Rate limiting (100 req/min per IP)
  - Request routing to downstream services
  - CORS handling
  - Prometheus metrics export

### 2. Borrower Service (Port 8301)
- **Language**: Go (Gin)
- **Role**: Borrower lifecycle management
- **Features**:
  - Borrower registration & KYC
  - Profile management (personal, financial, employment)
  - Credit history tracking
  - Document management
  - Income & employment verification

### 3. Application Service (Port 8302)
- **Language**: Go (Gin)
- **Role**: Loan application processing
- **Features**:
  - Application intake & validation
  - Multi-step application workflow
  - Document collection & verification
  - Application status tracking
  - Integration with underwriting for decisions

### 4. Underwriting Service (Port 8303)
- **Language**: Python (FastAPI)
- **Role**: ML-based credit risk assessment
- **Features**:
  - Automated credit scoring (FICO 300-850)
  - ML risk model (Gradient Boosting simulation)
  - Debt-to-income (DTI) calculation
  - Automated approve/reject/review decisions
  - EMI calculator with amortization
  - Credit report generation

### 5. Loan Management Service (Port 8304)
- **Language**: Go (Gin)
- **Role**: Active loan lifecycle
- **Features**:
  - Loan creation & disbursement
  - Amortization schedule generation
  - Balance tracking & updates
  - Interest accrual calculations
  - Loan modification & restructuring
  - Loan closure processing

### 6. Payment Service (Port 8305)
- **Language**: Go (Gin)
- **Role**: Payment processing
- **Features**:
  - Payment collection & recording
  - Auto-debit scheduling
  - Payment allocation (principal/interest)
  - Overpayment handling
  - Payment history & receipts
  - Reconciliation

### 7. Collections Service (Port 8306)
- **Language**: Go (Gin)
- **Role**: Delinquency management
- **Features**:
  - Delinquency detection & staging (early/mid/late)
  - Contact scheduling & tracking
  - Collection action management
  - Promise-to-pay tracking
  - Write-off processing
  - Recovery tracking

### 8. Reporting Service (Port 8307)
- **Language**: Go (Gin)
- **Role**: Analytics & reporting
- **Features**:
  - Portfolio summary reports
  - Origination reports
  - Delinquency reports
  - Collection performance
  - Revenue & interest reports
  - Regulatory compliance reports

## Data Flow

### Loan Origination Flow
```
Borrower → Application → Underwriting → Loan Creation → Disbursement
   │            │              │              │              │
   ▼            ▼              ▼              ▼              ▼
 Register    Submit App    ML Assessment   Create Loan   Fund Loan
 KYC Check   Validate      Credit Score    Gen Schedule   Transfer
 Profile     Docs Check    DTI Check       Set Terms      Notify
                           Decision
```

### Payment Flow
```
Due Date → Auto-Debit → Payment Processing → Balance Update → Statement
                │                │                 │
                ▼                ▼                 ▼
           Collect PMT     Allocate P&I      Update Schedule
           Process         Record            Check Payoff
           Receipt         Event             Notify
```

### Collections Flow
```
Missed Payment → Detection → Staging → Action → Resolution
      │              │          │         │          │
      ▼              ▼          ▼         ▼          ▼
  Mark Late     Flag Account   Early    Contact    Payment
  Notify        Set Stage      Mid      Negotiate  Restructure
  Event         Queue          Late     Legal      Write-off
```

## Security

- JWT-based authentication on all endpoints
- Rate limiting at API Gateway level
- Input validation and sanitization
- CORS configuration
- Helmet security headers (BFF)
- Database connection pooling
- Redis-backed session management

## Observability

- **Metrics**: Prometheus endpoints on all services (`/metrics`)
- **Health**: Health check endpoints on all services (`/health`)
- **Logging**: Structured JSON logging (Winston for Node.js, Go stdlib for Go, Python logging for FastAPI)
- **Tracing**: Request ID propagation across services

## Deployment

```bash
# Start LendFast
docker-compose -f docker-compose.lendfast.yml up -d

# Check status
docker-compose -f docker-compose.lendfast.yml ps

# View logs
docker-compose -f docker-compose.lendfast.yml logs -f

# Stop
docker-compose -f docker-compose.lendfast.yml down
```

## Port Map

| Service | Port | Technology |
|---------|------|-----------|
| API Gateway | 8300 | Go/Gin |
| Borrower Service | 8301 | Go/Gin |
| Application Service | 8302 | Go/Gin |
| Underwriting Service | 8303 | Python/FastAPI |
| Loan Management | 8304 | Go/Gin |
| Payment Service | 8305 | Go/Gin |
| Collections Service | 8306 | Go/Gin |
| Reporting Service | 8307 | Go/Gin |
| Backend API (BFF) | 5001 | Node.js/Express |
| Frontend | 3004 | React/Nginx |
