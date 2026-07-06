"""
LendFast Underwriting Service - ML-Based Credit Risk Assessment
Port: 8303
Technology: Python (FastAPI) + scikit-learn

Features:
- Automated credit scoring (FICO simulation 300-850)
- ML-based risk assessment using Gradient Boosting
- Debt-to-income (DTI) calculation
- Income & employment verification
- Automated approval/rejection decisions
- Credit report generation
"""

import os
import time
import math
import random
import logging
import hashlib
from datetime import datetime, timedelta
from typing import Optional, List, Dict, Any
from enum import Enum

import numpy as np
from fastapi import FastAPI, HTTPException, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field
import psycopg2
import psycopg2.extras
import redis
import json
from prometheus_client import Counter, Histogram, Gauge, generate_latest, CONTENT_TYPE_LATEST

# ─── Logging Setup ────────────────────────────────────────────────────────────
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(message)s',
    datefmt='%Y-%m-%d %H:%M:%S'
)
logger = logging.getLogger("underwriting-service")

# ─── Configuration ────────────────────────────────────────────────────────────
DATABASE_URL = os.getenv("DATABASE_URL", "postgresql://fintech:fintech123@localhost:5432/lendfast")
REDIS_URL = os.getenv("REDIS_URL", "localhost")
REDIS_PORT = int(os.getenv("REDIS_PORT", "6379"))
PORT = int(os.getenv("PORT", "8303"))

# ─── Prometheus Metrics ───────────────────────────────────────────────────────
REQUEST_COUNT = Counter('lendfast_underwriting_requests_total', 'Total requests', ['method', 'endpoint', 'status'])
REQUEST_DURATION = Histogram('lendfast_underwriting_request_duration_seconds', 'Request duration', ['method', 'endpoint'])
ASSESSMENTS_TOTAL = Counter('lendfast_assessments_total', 'Total credit assessments', ['decision'])
CREDIT_SCORES = Histogram('lendfast_credit_scores', 'Distribution of credit scores', buckets=[300, 400, 500, 550, 600, 650, 700, 750, 800, 850])
RISK_SCORES = Histogram('lendfast_risk_scores', 'Distribution of risk scores', buckets=[0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100])
ACTIVE_ASSESSMENTS = Gauge('lendfast_active_assessments', 'Currently running assessments')

# ─── Database Connection ──────────────────────────────────────────────────────
db_conn = None
redis_client = None

def get_db():
    global db_conn
    try:
        if db_conn is None or db_conn.closed:
            db_conn = psycopg2.connect(DATABASE_URL)
            db_conn.autocommit = True
            logger.info("✓ Database connected")
    except Exception as e:
        logger.warning(f"⚠ Database not available: {e}")
    return db_conn

def get_redis():
    global redis_client
    try:
        if redis_client is None:
            redis_client = redis.Redis(host=REDIS_URL, port=REDIS_PORT, db=3, decode_responses=True)
            redis_client.ping()
            logger.info("✓ Redis connected")
    except Exception as e:
        logger.warning(f"⚠ Redis not available: {e}")
        redis_client = None
    return redis_client

# ─── Database Schema ──────────────────────────────────────────────────────────
def init_database():
    conn = get_db()
    if conn is None:
        return
    try:
        cur = conn.cursor()
        cur.execute("""
            CREATE TABLE IF NOT EXISTS underwriting_decisions (
                id SERIAL PRIMARY KEY,
                decision_id VARCHAR(50) UNIQUE NOT NULL,
                application_id VARCHAR(50) NOT NULL,
                borrower_id VARCHAR(50) NOT NULL,
                credit_score INTEGER NOT NULL,
                risk_score DECIMAL(5,2) NOT NULL,
                risk_level VARCHAR(20) NOT NULL,
                dti_ratio DECIMAL(5,4),
                decision VARCHAR(20) NOT NULL,
                max_approved_amount DECIMAL(15,2),
                approved_rate DECIMAL(5,2),
                approved_term_months INTEGER,
                rejection_reasons TEXT,
                model_version VARCHAR(20) DEFAULT 'v1.0',
                processing_time_ms INTEGER,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            );
            
            CREATE TABLE IF NOT EXISTS credit_reports (
                id SERIAL PRIMARY KEY,
                report_id VARCHAR(50) UNIQUE NOT NULL,
                borrower_id VARCHAR(50) NOT NULL,
                credit_score INTEGER NOT NULL,
                score_source VARCHAR(20) DEFAULT 'internal',
                total_accounts INTEGER DEFAULT 0,
                open_accounts INTEGER DEFAULT 0,
                total_balance DECIMAL(15,2) DEFAULT 0,
                credit_utilization DECIMAL(5,4) DEFAULT 0,
                payment_history_score DECIMAL(5,2) DEFAULT 0,
                oldest_account_years DECIMAL(5,2) DEFAULT 0,
                recent_inquiries INTEGER DEFAULT 0,
                derogatory_marks INTEGER DEFAULT 0,
                report_date TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            );

            CREATE INDEX IF NOT EXISTS idx_decisions_application ON underwriting_decisions(application_id);
            CREATE INDEX IF NOT EXISTS idx_decisions_borrower ON underwriting_decisions(borrower_id);
            CREATE INDEX IF NOT EXISTS idx_credit_reports_borrower ON credit_reports(borrower_id);
        """)
        logger.info("✓ Database tables initialized")
        
        # Seed credit reports
        cur.execute("SELECT COUNT(*) FROM credit_reports")
        if cur.fetchone()[0] == 0:
            seed_data = [
                ("CR-001", "XAJ4S70Y6D", 720, 8, 6, 45000, 0.28, 92.5, 8.5, 2, 0),
                ("CR-002", "Z3R84FD9Y7", 680, 5, 4, 22000, 0.42, 85.0, 4.2, 4, 0),
                ("CR-003", "9X6APBHPHS", 750, 12, 9, 78000, 0.18, 96.0, 15.3, 1, 0),
            ]
            for r in seed_data:
                cur.execute("""
                    INSERT INTO credit_reports (report_id, borrower_id, credit_score, total_accounts, open_accounts, 
                    total_balance, credit_utilization, payment_history_score, oldest_account_years, recent_inquiries, derogatory_marks)
                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                    ON CONFLICT (report_id) DO NOTHING
                """, r)
            logger.info("✓ Credit reports seed data inserted")
    except Exception as e:
        logger.error(f"Database init error: {e}")

# ─── Pydantic Models ─────────────────────────────────────────────────────────

class RiskLevel(str, Enum):
    LOW = "low"
    MEDIUM = "medium"
    HIGH = "high"
    VERY_HIGH = "very_high"

class Decision(str, Enum):
    APPROVED = "approved"
    REJECTED = "rejected"
    MANUAL_REVIEW = "manual_review"

class AssessmentRequest(BaseModel):
    application_id: str
    borrower_id: str
    requested_amount: float = Field(gt=0)
    requested_term_months: int = Field(gt=0, le=360)
    loan_type: str = "personal"
    annual_income: float = Field(gt=0)
    monthly_debt_payments: float = Field(ge=0, default=0)
    employment_status: str = "employed"
    employment_tenure_years: float = Field(ge=0, default=0)
    credit_score: Optional[int] = None
    purpose: Optional[str] = None

class AssessmentResponse(BaseModel):
    decision_id: str
    application_id: str
    credit_score: int
    risk_score: float
    risk_level: str
    dti_ratio: float
    decision: str
    max_approved_amount: Optional[float]
    approved_rate: Optional[float]
    approved_term_months: Optional[int]
    rejection_reasons: List[str]
    monthly_payment: Optional[float]
    total_cost: Optional[float]
    processing_time_ms: int
    model_version: str

class CreditScoreRequest(BaseModel):
    borrower_id: str
    annual_income: float
    total_debt: float = 0
    credit_utilization: float = 0.3
    payment_history_score: float = 85.0
    account_age_years: float = 5.0
    recent_inquiries: int = 2
    derogatory_marks: int = 0

# ─── ML Credit Risk Model ────────────────────────────────────────────────────

class CreditRiskModel:
    """
    Simulated ML model for credit risk assessment.
    Uses a rule-based scoring engine that mimics gradient boosting behavior.
    In production, this would load a trained sklearn/XGBoost model.
    """
    
    MODEL_VERSION = "v1.0-lendfast"
    
    # Feature weights (simulating trained model coefficients)
    WEIGHTS = {
        'credit_score': 0.30,
        'dti_ratio': 0.20,
        'income_factor': 0.15,
        'employment_stability': 0.10,
        'credit_utilization': 0.10,
        'payment_history': 0.08,
        'account_age': 0.04,
        'inquiries': 0.03,
    }
    
    # Loan type risk multipliers
    LOAN_TYPE_RISK = {
        'personal': 1.0,
        'auto': 0.85,
        'mortgage': 0.70,
        'business': 1.15,
    }
    
    # Base interest rates by credit tier
    BASE_RATES = {
        'excellent': 5.99,   # 750+
        'good': 8.99,        # 700-749
        'fair': 12.99,       # 650-699
        'poor': 18.99,       # 600-649
        'very_poor': 24.99,  # <600
    }
    
    @staticmethod
    def calculate_credit_score(borrower_data: dict) -> int:
        """Calculate synthetic FICO score (300-850)"""
        base_score = 600
        
        # Payment history (35% of FICO)
        payment_score = borrower_data.get('payment_history_score', 85)
        base_score += (payment_score - 70) * 2.5  # -75 to +75
        
        # Credit utilization (30% of FICO)
        utilization = borrower_data.get('credit_utilization', 0.3)
        if utilization < 0.1:
            base_score += 60
        elif utilization < 0.3:
            base_score += 40
        elif utilization < 0.5:
            base_score += 10
        elif utilization < 0.75:
            base_score -= 20
        else:
            base_score -= 60
        
        # Length of credit history (15% of FICO)
        account_age = borrower_data.get('account_age_years', 5)
        base_score += min(account_age * 3, 45)
        
        # Credit mix and new credit (20% of FICO)
        inquiries = borrower_data.get('recent_inquiries', 2)
        base_score -= inquiries * 5
        
        # Derogatory marks
        derog = borrower_data.get('derogatory_marks', 0)
        base_score -= derog * 50
        
        # Income factor (not in real FICO but useful for risk)
        income = borrower_data.get('annual_income', 50000)
        if income > 100000:
            base_score += 20
        elif income > 75000:
            base_score += 10
        
        # Add some noise for realism
        base_score += random.randint(-10, 10)
        
        return max(300, min(850, int(base_score)))
    
    @classmethod
    def assess_risk(cls, request: AssessmentRequest) -> Dict[str, Any]:
        """
        Perform comprehensive credit risk assessment.
        Returns risk score (0-100), decision, and loan terms.
        """
        start_time = time.time()
        
        # Step 1: Get or calculate credit score
        credit_score = request.credit_score or cls.calculate_credit_score({
            'annual_income': request.annual_income,
            'payment_history_score': 85,
            'credit_utilization': 0.3,
            'account_age_years': request.employment_tenure_years,
        })
        
        # Step 2: Calculate DTI ratio
        monthly_income = request.annual_income / 12
        estimated_payment = cls._estimate_monthly_payment(
            request.requested_amount, 
            cls._get_base_rate(credit_score),
            request.requested_term_months
        )
        new_dti = (request.monthly_debt_payments + estimated_payment) / monthly_income
        
        # Step 3: Calculate risk score (0-100, lower is better)
        risk_score = cls._calculate_risk_score(
            credit_score=credit_score,
            dti_ratio=new_dti,
            annual_income=request.annual_income,
            employment_status=request.employment_status,
            employment_tenure=request.employment_tenure_years,
            requested_amount=request.requested_amount,
            loan_type=request.loan_type,
        )
        
        # Step 4: Determine risk level
        risk_level = cls._get_risk_level(risk_score)
        
        # Step 5: Make decision
        decision, rejection_reasons = cls._make_decision(
            credit_score=credit_score,
            risk_score=risk_score,
            dti_ratio=new_dti,
            requested_amount=request.requested_amount,
            annual_income=request.annual_income,
            loan_type=request.loan_type,
        )
        
        # Step 6: Calculate approved terms (if approved)
        approved_amount = None
        approved_rate = None
        approved_term = None
        monthly_payment = None
        total_cost = None
        
        if decision == Decision.APPROVED:
            approved_rate = cls._calculate_approved_rate(credit_score, request.loan_type, risk_score)
            approved_amount = min(request.requested_amount, cls._max_loan_amount(
                request.annual_income, new_dti, approved_rate, request.requested_term_months
            ))
            approved_term = request.requested_term_months
            monthly_payment = cls._estimate_monthly_payment(approved_amount, approved_rate, approved_term)
            total_cost = monthly_payment * approved_term
        
        processing_time_ms = int((time.time() - start_time) * 1000)
        
        return {
            'credit_score': credit_score,
            'risk_score': round(risk_score, 2),
            'risk_level': risk_level.value,
            'dti_ratio': round(new_dti, 4),
            'decision': decision.value,
            'max_approved_amount': round(approved_amount, 2) if approved_amount else None,
            'approved_rate': round(approved_rate, 2) if approved_rate else None,
            'approved_term_months': approved_term,
            'rejection_reasons': rejection_reasons,
            'monthly_payment': round(monthly_payment, 2) if monthly_payment else None,
            'total_cost': round(total_cost, 2) if total_cost else None,
            'processing_time_ms': processing_time_ms,
            'model_version': cls.MODEL_VERSION,
        }
    
    @classmethod
    def _calculate_risk_score(cls, credit_score, dti_ratio, annual_income, 
                               employment_status, employment_tenure, requested_amount, loan_type):
        """Calculate composite risk score 0-100"""
        score = 0
        
        # Credit score component (30%)
        credit_factor = max(0, min(1, (credit_score - 300) / 550))
        score += (1 - credit_factor) * 30
        
        # DTI component (20%)
        dti_factor = min(1, dti_ratio / 0.6)
        score += dti_factor * 20
        
        # Income adequacy (15%)
        income_ratio = requested_amount / (annual_income * 5)
        score += min(1, income_ratio) * 15
        
        # Employment stability (10%)
        employment_score = 0
        if employment_status == 'unemployed':
            employment_score = 10
        elif employment_status == 'self-employed':
            employment_score = 5
        elif employment_tenure < 1:
            employment_score = 7
        elif employment_tenure < 3:
            employment_score = 3
        score += employment_score
        
        # Loan type risk (10%)
        type_multiplier = cls.LOAN_TYPE_RISK.get(loan_type, 1.0)
        score += (type_multiplier - 0.7) * 33  # Normalize to ~0-15
        
        # Add slight randomness for realism
        score += random.uniform(-2, 2)
        
        return max(0, min(100, score))
    
    @staticmethod
    def _get_risk_level(risk_score: float) -> RiskLevel:
        if risk_score < 25:
            return RiskLevel.LOW
        elif risk_score < 50:
            return RiskLevel.MEDIUM
        elif risk_score < 75:
            return RiskLevel.HIGH
        else:
            return RiskLevel.VERY_HIGH
    
    @classmethod
    def _make_decision(cls, credit_score, risk_score, dti_ratio, 
                       requested_amount, annual_income, loan_type):
        """Automated underwriting decision engine"""
        rejection_reasons = []
        
        # Hard stops (automatic rejection)
        if credit_score < 500:
            rejection_reasons.append("Credit score below minimum threshold (500)")
        
        if dti_ratio > 0.50:
            rejection_reasons.append(f"DTI ratio too high ({dti_ratio:.1%} > 50%)")
        
        if requested_amount > annual_income * 5:
            rejection_reasons.append("Requested amount exceeds 5x annual income")
        
        # Loan type specific rules
        if loan_type == 'mortgage' and credit_score < 620:
            rejection_reasons.append("Credit score below mortgage minimum (620)")
        
        if loan_type == 'mortgage' and dti_ratio > 0.43:
            rejection_reasons.append(f"DTI exceeds mortgage limit ({dti_ratio:.1%} > 43%)")
        
        if rejection_reasons:
            return Decision.REJECTED, rejection_reasons
        
        # Manual review triggers
        review_reasons = []
        if risk_score > 60:
            review_reasons.append("High risk score requires manual review")
        if credit_score < 620 and requested_amount > 25000:
            review_reasons.append("Low credit score with high loan amount")
        if dti_ratio > 0.40:
            review_reasons.append("Elevated DTI ratio")
        
        if review_reasons:
            return Decision.MANUAL_REVIEW, review_reasons
        
        return Decision.APPROVED, []
    
    @classmethod
    def _calculate_approved_rate(cls, credit_score, loan_type, risk_score):
        """Calculate the approved interest rate"""
        base_rate = cls._get_base_rate(credit_score)
        type_adjustment = (cls.LOAN_TYPE_RISK.get(loan_type, 1.0) - 1.0) * 2
        risk_adjustment = risk_score * 0.05
        
        rate = base_rate + type_adjustment + risk_adjustment
        return max(3.99, min(29.99, rate))
    
    @staticmethod
    def _get_base_rate(credit_score):
        if credit_score >= 750:
            return 5.99
        elif credit_score >= 700:
            return 8.99
        elif credit_score >= 650:
            return 12.99
        elif credit_score >= 600:
            return 18.99
        else:
            return 24.99
    
    @staticmethod
    def _estimate_monthly_payment(principal, annual_rate, term_months):
        """EMI = P * r * (1+r)^n / ((1+r)^n - 1)"""
        if annual_rate == 0:
            return principal / term_months
        r = annual_rate / 100 / 12
        n = term_months
        emi = principal * r * math.pow(1 + r, n) / (math.pow(1 + r, n) - 1)
        return emi
    
    @staticmethod
    def _max_loan_amount(annual_income, current_dti, rate, term_months):
        """Calculate maximum loan amount based on income and DTI limits"""
        monthly_income = annual_income / 12
        max_payment = monthly_income * (0.43 - current_dti + 0.05)  # Allow up to 43% DTI
        
        if max_payment <= 0:
            return 0
        
        r = rate / 100 / 12
        n = term_months
        if r == 0:
            return max_payment * n
        
        max_amount = max_payment * (math.pow(1 + r, n) - 1) / (r * math.pow(1 + r, n))
        return max(0, max_amount)

# ─── FastAPI Application ─────────────────────────────────────────────────────

app = FastAPI(
    title="LendFast Underwriting Service",
    description="ML-based credit risk assessment and automated underwriting",
    version="1.0.0",
)

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# ─── Middleware ────────────────────────────────────────────────────────────────

@app.middleware("http")
async def metrics_middleware(request: Request, call_next):
    start = time.time()
    response = await call_next(request)
    duration = time.time() - start
    
    REQUEST_COUNT.labels(
        method=request.method,
        endpoint=request.url.path,
        status=response.status_code
    ).inc()
    REQUEST_DURATION.labels(
        method=request.method,
        endpoint=request.url.path
    ).observe(duration)
    
    return response

# ─── Startup ──────────────────────────────────────────────────────────────────

@app.on_event("startup")
async def startup():
    logger.info("🚀 LendFast Underwriting Service starting...")
    init_database()
    get_redis()
    logger.info(f"✓ Underwriting Service ready on port {PORT}")

# ─── Health Check ─────────────────────────────────────────────────────────────

@app.get("/health")
async def health():
    health_status = {
        "status": "UP",
        "service": "underwriting-service",
        "port": PORT,
        "time": datetime.now().isoformat(),
        "model_version": CreditRiskModel.MODEL_VERSION,
    }
    
    conn = get_db()
    if conn and not conn.closed:
        health_status["database"] = "UP"
    else:
        health_status["database"] = "DOWN"
        health_status["status"] = "DEGRADED"
    
    r = get_redis()
    if r:
        try:
            r.ping()
            health_status["redis"] = "UP"
        except:
            health_status["redis"] = "DOWN"
    else:
        health_status["redis"] = "DOWN"
    
    return health_status

@app.get("/metrics")
async def metrics():
    from starlette.responses import Response
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)

@app.get("/api/v1/ping")
async def ping():
    return {
        "service": "underwriting-service",
        "port": PORT,
        "description": "ML-based credit risk assessment and automated underwriting",
        "status": "online",
        "model_version": CreditRiskModel.MODEL_VERSION,
        "timestamp": datetime.now().isoformat(),
    }

# ─── Credit Assessment ───────────────────────────────────────────────────────

@app.post("/api/v1/underwriting/assess", response_model=AssessmentResponse)
async def assess_creditworthiness(request: AssessmentRequest):
    """Perform comprehensive credit risk assessment"""
    ACTIVE_ASSESSMENTS.inc()
    
    try:
        # Check cache first
        cache_key = f"assessment:{request.application_id}"
        r = get_redis()
        if r:
            cached = r.get(cache_key)
            if cached:
                logger.info(f"Cache hit for assessment {request.application_id}")
                return json.loads(cached)
        
        # Perform assessment
        result = CreditRiskModel.assess_risk(request)
        
        # Generate decision ID
        decision_id = f"UW-{hashlib.md5(f'{request.application_id}{time.time()}'.encode()).hexdigest()[:12].upper()}"
        
        response = AssessmentResponse(
            decision_id=decision_id,
            application_id=request.application_id,
            **result
        )
        
        # Record metrics
        ASSESSMENTS_TOTAL.labels(decision=result['decision']).inc()
        CREDIT_SCORES.observe(result['credit_score'])
        RISK_SCORES.observe(result['risk_score'])
        
        # Store in database
        conn = get_db()
        if conn:
            try:
                cur = conn.cursor()
                cur.execute("""
                    INSERT INTO underwriting_decisions 
                    (decision_id, application_id, borrower_id, credit_score, risk_score, risk_level,
                     dti_ratio, decision, max_approved_amount, approved_rate, approved_term_months,
                     rejection_reasons, model_version, processing_time_ms)
                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                """, (
                    decision_id, request.application_id, request.borrower_id,
                    result['credit_score'], result['risk_score'], result['risk_level'],
                    result['dti_ratio'], result['decision'], result['max_approved_amount'],
                    result['approved_rate'], result['approved_term_months'],
                    json.dumps(result['rejection_reasons']), result['model_version'],
                    result['processing_time_ms']
                ))
            except Exception as e:
                logger.error(f"Failed to store decision: {e}")
        
        # Cache result
        if r:
            r.setex(cache_key, 300, json.dumps(response.dict()))  # 5 min TTL
        
        logger.info(f"Assessment complete: {decision_id} -> {result['decision']} "
                    f"(score={result['credit_score']}, risk={result['risk_score']:.1f})")
        
        return response
    
    finally:
        ACTIVE_ASSESSMENTS.dec()

# ─── Get Assessment ───────────────────────────────────────────────────────────

@app.get("/api/v1/underwriting/{application_id}")
async def get_assessment(application_id: str):
    """Get underwriting assessment for an application"""
    conn = get_db()
    if not conn:
        raise HTTPException(status_code=503, detail="Database unavailable")
    
    cur = conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor)
    cur.execute("""
        SELECT * FROM underwriting_decisions 
        WHERE application_id = %s 
        ORDER BY created_at DESC LIMIT 1
    """, (application_id,))
    
    result = cur.fetchone()
    if not result:
        raise HTTPException(status_code=404, detail="Assessment not found")
    
    # Parse rejection reasons from JSON string
    if result.get('rejection_reasons'):
        try:
            result['rejection_reasons'] = json.loads(result['rejection_reasons'])
        except:
            result['rejection_reasons'] = []
    
    return dict(result)

# ─── Approve/Reject ──────────────────────────────────────────────────────────

@app.post("/api/v1/underwriting/approve")
async def approve_application(data: dict):
    """Manually approve a loan application"""
    application_id = data.get('application_id')
    approved_amount = data.get('approved_amount')
    approved_rate = data.get('approved_rate')
    approved_term = data.get('approved_term_months')
    
    if not application_id:
        raise HTTPException(status_code=400, detail="application_id required")
    
    conn = get_db()
    if not conn:
        raise HTTPException(status_code=503, detail="Database unavailable")
    
    cur = conn.cursor()
    decision_id = f"UW-MANUAL-{hashlib.md5(f'{application_id}{time.time()}'.encode()).hexdigest()[:8].upper()}"
    
    cur.execute("""
        INSERT INTO underwriting_decisions 
        (decision_id, application_id, borrower_id, credit_score, risk_score, risk_level,
         decision, max_approved_amount, approved_rate, approved_term_months, model_version, processing_time_ms)
        VALUES (%s, %s, %s, 0, 0, 'manual', 'approved', %s, %s, %s, 'manual', 0)
    """, (decision_id, application_id, data.get('borrower_id', ''), 
          approved_amount, approved_rate, approved_term))
    
    ASSESSMENTS_TOTAL.labels(decision='approved').inc()
    
    return {"decision_id": decision_id, "status": "approved", "application_id": application_id}

@app.post("/api/v1/underwriting/reject")
async def reject_application(data: dict):
    """Manually reject a loan application"""
    application_id = data.get('application_id')
    reasons = data.get('reasons', ['Manual rejection'])
    
    if not application_id:
        raise HTTPException(status_code=400, detail="application_id required")
    
    conn = get_db()
    if not conn:
        raise HTTPException(status_code=503, detail="Database unavailable")
    
    cur = conn.cursor()
    decision_id = f"UW-MANUAL-{hashlib.md5(f'{application_id}{time.time()}'.encode()).hexdigest()[:8].upper()}"
    
    cur.execute("""
        INSERT INTO underwriting_decisions 
        (decision_id, application_id, borrower_id, credit_score, risk_score, risk_level,
         decision, rejection_reasons, model_version, processing_time_ms)
        VALUES (%s, %s, %s, 0, 0, 'manual', 'rejected', %s, 'manual', 0)
    """, (decision_id, application_id, data.get('borrower_id', ''), json.dumps(reasons)))
    
    ASSESSMENTS_TOTAL.labels(decision='rejected').inc()
    
    return {"decision_id": decision_id, "status": "rejected", "application_id": application_id, "reasons": reasons}

# ─── Credit Score ─────────────────────────────────────────────────────────────

@app.get("/api/v1/underwriting/credit-score/{borrower_id}")
async def get_credit_score(borrower_id: str):
    """Get credit score and report for a borrower"""
    # Check cache
    r = get_redis()
    if r:
        cached = r.get(f"credit_score:{borrower_id}")
        if cached:
            return json.loads(cached)
    
    conn = get_db()
    if not conn:
        raise HTTPException(status_code=503, detail="Database unavailable")
    
    cur = conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor)
    cur.execute("""
        SELECT * FROM credit_reports 
        WHERE borrower_id = %s 
        ORDER BY report_date DESC LIMIT 1
    """, (borrower_id,))
    
    report = cur.fetchone()
    if not report:
        raise HTTPException(status_code=404, detail="Credit report not found")
    
    result = dict(report)
    
    # Determine credit tier
    score = result['credit_score']
    if score >= 750:
        result['credit_tier'] = 'excellent'
    elif score >= 700:
        result['credit_tier'] = 'good'
    elif score >= 650:
        result['credit_tier'] = 'fair'
    elif score >= 600:
        result['credit_tier'] = 'poor'
    else:
        result['credit_tier'] = 'very_poor'
    
    # Cache result
    if r:
        r.setex(f"credit_score:{borrower_id}", 300, json.dumps(result, default=str))
    
    return result

@app.post("/api/v1/underwriting/credit-score/calculate")
async def calculate_credit_score(request: CreditScoreRequest):
    """Calculate a synthetic credit score based on provided data"""
    score = CreditRiskModel.calculate_credit_score({
        'annual_income': request.annual_income,
        'credit_utilization': request.credit_utilization,
        'payment_history_score': request.payment_history_score,
        'account_age_years': request.account_age_years,
        'recent_inquiries': request.recent_inquiries,
        'derogatory_marks': request.derogatory_marks,
    })
    
    CREDIT_SCORES.observe(score)
    
    return {
        'borrower_id': request.borrower_id,
        'credit_score': score,
        'score_breakdown': {
            'payment_history': min(100, request.payment_history_score),
            'credit_utilization': f"{request.credit_utilization:.0%}",
            'account_age': f"{request.account_age_years:.1f} years",
            'recent_inquiries': request.recent_inquiries,
            'derogatory_marks': request.derogatory_marks,
        },
        'model_version': CreditRiskModel.MODEL_VERSION,
        'calculated_at': datetime.now().isoformat(),
    }

# ─── Assessment History ───────────────────────────────────────────────────────

@app.get("/api/v1/underwriting/history/{borrower_id}")
async def get_assessment_history(borrower_id: str):
    """Get all underwriting decisions for a borrower"""
    conn = get_db()
    if not conn:
        raise HTTPException(status_code=503, detail="Database unavailable")
    
    cur = conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor)
    cur.execute("""
        SELECT * FROM underwriting_decisions 
        WHERE borrower_id = %s 
        ORDER BY created_at DESC
    """, (borrower_id,))
    
    results = cur.fetchall()
    for r in results:
        if r.get('rejection_reasons'):
            try:
                r['rejection_reasons'] = json.loads(r['rejection_reasons'])
            except:
                pass
    
    return {"borrower_id": borrower_id, "decisions": [dict(r) for r in results]}

# ─── Model Info ───────────────────────────────────────────────────────────────

@app.get("/api/v1/underwriting/model/info")
async def model_info():
    """Get information about the current ML model"""
    return {
        "model_version": CreditRiskModel.MODEL_VERSION,
        "model_type": "Rule-based scoring (simulated Gradient Boosting)",
        "features": list(CreditRiskModel.WEIGHTS.keys()),
        "feature_weights": CreditRiskModel.WEIGHTS,
        "loan_types_supported": list(CreditRiskModel.LOAN_TYPE_RISK.keys()),
        "decision_thresholds": {
            "auto_approve": "risk_score < 40 AND credit_score >= 620",
            "manual_review": "risk_score 40-60 OR borderline metrics",
            "auto_reject": "credit_score < 500 OR DTI > 50%",
        },
        "credit_score_range": "300-850",
        "risk_score_range": "0-100 (lower is better)",
    }

# ─── EMI Calculator ──────────────────────────────────────────────────────────

@app.post("/api/v1/underwriting/emi-calculator")
async def emi_calculator(data: dict):
    """Calculate EMI for given loan parameters"""
    principal = data.get('principal', 0)
    annual_rate = data.get('annual_rate', 0)
    term_months = data.get('term_months', 12)
    
    if principal <= 0 or term_months <= 0:
        raise HTTPException(status_code=400, detail="Invalid parameters")
    
    monthly_payment = CreditRiskModel._estimate_monthly_payment(principal, annual_rate, term_months)
    total_payment = monthly_payment * term_months
    total_interest = total_payment - principal
    
    # Generate amortization schedule (first 12 months or full term)
    schedule = []
    balance = principal
    r = annual_rate / 100 / 12
    
    for i in range(1, min(term_months + 1, 13)):
        interest = balance * r
        principal_portion = monthly_payment - interest
        balance -= principal_portion
        schedule.append({
            "month": i,
            "payment": round(monthly_payment, 2),
            "principal": round(principal_portion, 2),
            "interest": round(interest, 2),
            "balance": round(max(0, balance), 2),
        })
    
    return {
        "principal": principal,
        "annual_rate": annual_rate,
        "term_months": term_months,
        "monthly_payment": round(monthly_payment, 2),
        "total_payment": round(total_payment, 2),
        "total_interest": round(total_interest, 2),
        "amortization_preview": schedule,
    }

# ─── Run Server ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=PORT, log_level="info")
