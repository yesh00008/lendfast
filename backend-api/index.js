/**
 * LendFast Backend-for-Frontend (BFF) API
 * Port: 5001
 * 
 * Aggregation layer routing requests to LendFast microservices:
 * - API Gateway:           :8300
 * - Borrower Service:      :8301
 * - Application Service:   :8302
 * - Underwriting Service:  :8303
 * - Loan Management:       :8304
 * - Payment Service:       :8305
 * - Collections Service:   :8306
 * - Reporting Service:     :8307
 */

const express = require('express');
const cors = require('cors');
const helmet = require('helmet');
const compression = require('compression');
const morgan = require('morgan');
const axios = require('axios');
const rateLimit = require('express-rate-limit');
const winston = require('winston');

// ─── Logger Setup ────────────────────────────────────────────────────────────
const logger = winston.createLogger({
  level: process.env.LOG_LEVEL || 'info',
  format: winston.format.combine(
    winston.format.timestamp(),
    winston.format.printf(({ timestamp, level, message }) =>
      `${timestamp} [${level.toUpperCase()}] ${message}`
    )
  ),
  transports: [new winston.transports.Console()],
});

// ─── Configuration ───────────────────────────────────────────────────────────
const PORT = process.env.PORT || 5001;
const SERVICES = {
  gateway:       process.env.GATEWAY_URL       || 'http://lendfast-api-gateway:8300',
  borrower:      process.env.BORROWER_URL      || 'http://lendfast-borrower-service:8301',
  application:   process.env.APPLICATION_URL   || 'http://lendfast-application-service:8302',
  underwriting:  process.env.UNDERWRITING_URL  || 'http://lendfast-underwriting-service:8303',
  loan:          process.env.LOAN_URL          || 'http://lendfast-loan-management-service:8304',
  payment:       process.env.PAYMENT_URL       || 'http://lendfast-payment-service:8305',
  collections:   process.env.COLLECTIONS_URL   || 'http://lendfast-collections-service:8306',
  reporting:     process.env.REPORTING_URL     || 'http://lendfast-reporting-service:8307',
};

// ─── Express App ─────────────────────────────────────────────────────────────
const app = express();

// Request logging middleware

app.use(helmet());
app.use(cors({ origin: '*', credentials: true }));
app.use(compression());
app.use(express.json({ limit: '10mb' }));
app.use(morgan('combined', { stream: { write: (msg) => logger.info(msg.trim()) } }));

// Rate limiting
const limiter = rateLimit({
  windowMs: 15 * 60 * 1000,
  max: 500,
  message: { error: 'Too many requests, please try again later' },
});
app.use('/api/', limiter);

// ─── Request Counter ─────────────────────────────────────────────────────────
let requestCount = 0;
const startTime = Date.now();

// ─── Proxy Helper ────────────────────────────────────────────────────────────
async function proxyRequest(req, res, serviceUrl, path) {
  const url = `${serviceUrl}${path || req.path}`;
  try {
    const config = {
      method: req.method.toLowerCase(),
      url,
      headers: {
        'Content-Type': 'application/json',
        ...(req.headers.authorization && { Authorization: req.headers.authorization }),
      },
      params: req.query,
      timeout: 30000,
    };

    if (['post', 'put', 'patch'].includes(config.method)) {
      config.data = req.body;
    }

    const response = await axios(config);
    res.status(response.status).json(response.data);
  } catch (error) {
    if (error.response) {
      logger.warn(`Proxy error ${error.response.status}: ${url}`);
      res.status(error.response.status).json(error.response.data);
    } else if (error.code === 'ECONNREFUSED') {
      logger.error(`Service unreachable: ${url}`);
      res.status(503).json({ error: 'Service temporarily unavailable', service: url });
    } else {
      logger.error(`Proxy error: ${error.message}`);
      res.status(500).json({ error: 'Internal server error' });
    }
  }
}

// ─── Health & Status ─────────────────────────────────────────────────────────
app.get('/health', (req, res) => {
  res.json({
    status: 'UP',
    service: 'lendfast-backend-api',
    port: PORT,
    uptime: Math.floor((Date.now() - startTime) / 1000),
    requests: ++requestCount,
    timestamp: new Date().toISOString(),
  });
});

app.get('/api/v1/status', async (req, res) => {
  const statuses = {};
  
  await Promise.allSettled(
    Object.entries(SERVICES).map(async ([name, url]) => {
      try {
        const resp = await axios.get(`${url}/health`, { timeout: 3000 });
        statuses[name] = { status: 'UP', port: resp.data.port };
      } catch {
        statuses[name] = { status: 'DOWN', url };
      }
    })
  );

  res.json({
    platform: 'LendFast',
    bff: { status: 'UP', port: PORT },
    services: statuses,
    timestamp: new Date().toISOString(),
  });
});

// ─── Borrower Routes ─────────────────────────────────────────────────────────
app.get('/api/v1/borrowers', (req, res) => proxyRequest(req, res, SERVICES.borrower));
app.get('/api/v1/borrowers/:id', (req, res) => proxyRequest(req, res, SERVICES.borrower, `/api/v1/borrowers/${req.params.id}`));
app.post('/api/v1/borrowers', (req, res) => proxyRequest(req, res, SERVICES.borrower));
app.put('/api/v1/borrowers/:id', (req, res) => proxyRequest(req, res, SERVICES.borrower, `/api/v1/borrowers/${req.params.id}`));
app.delete('/api/v1/borrowers/:id', (req, res) => proxyRequest(req, res, SERVICES.borrower, `/api/v1/borrowers/${req.params.id}`));

// ─── Application Routes ──────────────────────────────────────────────────────
app.get('/api/v1/applications', (req, res) => proxyRequest(req, res, SERVICES.application));
app.get('/api/v1/applications/:id', (req, res) => proxyRequest(req, res, SERVICES.application, `/api/v1/applications/${req.params.id}`));
app.post('/api/v1/applications', (req, res) => proxyRequest(req, res, SERVICES.application));
app.put('/api/v1/applications/:id', (req, res) => proxyRequest(req, res, SERVICES.application, `/api/v1/applications/${req.params.id}`));
app.post('/api/v1/applications/:id/submit', (req, res) => proxyRequest(req, res, SERVICES.application, `/api/v1/applications/${req.params.id}/submit`));
app.post('/api/v1/applications/:id/approve', (req, res) => proxyRequest(req, res, SERVICES.application, `/api/v1/applications/${req.params.id}/approve`));
app.post('/api/v1/applications/:id/reject', (req, res) => proxyRequest(req, res, SERVICES.application, `/api/v1/applications/${req.params.id}/reject`));

// ─── Underwriting Routes ────────────────────────────────────────────────────
app.post('/api/v1/underwriting/assess', (req, res) => proxyRequest(req, res, SERVICES.underwriting));
app.get('/api/v1/underwriting/:id', (req, res) => proxyRequest(req, res, SERVICES.underwriting, `/api/v1/underwriting/${req.params.id}`));
app.post('/api/v1/underwriting/approve', (req, res) => proxyRequest(req, res, SERVICES.underwriting));
app.post('/api/v1/underwriting/reject', (req, res) => proxyRequest(req, res, SERVICES.underwriting));
app.get('/api/v1/underwriting/credit-score/:id', (req, res) => proxyRequest(req, res, SERVICES.underwriting, `/api/v1/underwriting/credit-score/${req.params.id}`));
app.post('/api/v1/underwriting/credit-score/calculate', (req, res) => proxyRequest(req, res, SERVICES.underwriting));
app.get('/api/v1/underwriting/history/:id', (req, res) => proxyRequest(req, res, SERVICES.underwriting, `/api/v1/underwriting/history/${req.params.id}`));
app.get('/api/v1/underwriting/model/info', (req, res) => proxyRequest(req, res, SERVICES.underwriting));
app.post('/api/v1/underwriting/emi-calculator', (req, res) => proxyRequest(req, res, SERVICES.underwriting));

// ─── Loan Management Routes ─────────────────────────────────────────────────
app.get('/api/v1/loans', (req, res) => proxyRequest(req, res, SERVICES.loan));
app.get('/api/v1/loans/:id', (req, res) => proxyRequest(req, res, SERVICES.loan, `/api/v1/loans/${req.params.id}`));
app.post('/api/v1/loans', (req, res) => proxyRequest(req, res, SERVICES.loan));
app.put('/api/v1/loans/:id', (req, res) => proxyRequest(req, res, SERVICES.loan, `/api/v1/loans/${req.params.id}`));
app.get('/api/v1/loans/:id/schedule', (req, res) => proxyRequest(req, res, SERVICES.loan, `/api/v1/loans/${req.params.id}/schedule`));
app.post('/api/v1/loans/:id/disburse', (req, res) => proxyRequest(req, res, SERVICES.loan, `/api/v1/loans/${req.params.id}/disburse`));
app.post('/api/v1/loans/:id/close', (req, res) => proxyRequest(req, res, SERVICES.loan, `/api/v1/loans/${req.params.id}/close`));

// ─── Payment Routes ──────────────────────────────────────────────────────────
app.get('/api/v1/payments', (req, res) => proxyRequest(req, res, SERVICES.payment));
app.get('/api/v1/payments/:id', (req, res) => proxyRequest(req, res, SERVICES.payment, `/api/v1/payments/${req.params.id}`));
app.post('/api/v1/payments', (req, res) => proxyRequest(req, res, SERVICES.payment));
app.get('/api/v1/payments/loan/:id', (req, res) => proxyRequest(req, res, SERVICES.payment, `/api/v1/payments/loan/${req.params.id}`));
app.post('/api/v1/payments/:id/process', (req, res) => proxyRequest(req, res, SERVICES.payment, `/api/v1/payments/${req.params.id}/process`));
app.post('/api/v1/payments/auto-debit', (req, res) => proxyRequest(req, res, SERVICES.payment));

// ─── Collections Routes ──────────────────────────────────────────────────────
app.get('/api/v1/collections', (req, res) => proxyRequest(req, res, SERVICES.collections));
app.get('/api/v1/collections/:id', (req, res) => proxyRequest(req, res, SERVICES.collections, `/api/v1/collections/${req.params.id}`));
app.post('/api/v1/collections', (req, res) => proxyRequest(req, res, SERVICES.collections));
app.get('/api/v1/collections/loan/:id', (req, res) => proxyRequest(req, res, SERVICES.collections, `/api/v1/collections/loan/${req.params.id}`));
app.post('/api/v1/collections/:id/action', (req, res) => proxyRequest(req, res, SERVICES.collections, `/api/v1/collections/${req.params.id}/action`));
app.get('/api/v1/collections/delinquent', (req, res) => proxyRequest(req, res, SERVICES.collections));

// ─── Reporting Routes ────────────────────────────────────────────────────────
app.get('/api/v1/reports', (req, res) => proxyRequest(req, res, SERVICES.reporting));
app.get('/api/v1/reports/:id', (req, res) => proxyRequest(req, res, SERVICES.reporting, `/api/v1/reports/${req.params.id}`));
app.post('/api/v1/reports/generate', (req, res) => proxyRequest(req, res, SERVICES.reporting));
app.get('/api/v1/reports/portfolio/summary', (req, res) => proxyRequest(req, res, SERVICES.reporting));
app.get('/api/v1/reports/delinquency', (req, res) => proxyRequest(req, res, SERVICES.reporting));
app.get('/api/v1/reports/origination', (req, res) => proxyRequest(req, res, SERVICES.reporting));

// ─── Aggregated Dashboard ────────────────────────────────────────────────────
app.get('/api/v1/dashboard', async (req, res) => {
  try {
    const [loansSummary, collectionsSummary, reportingSummary] = await Promise.allSettled([
      axios.get(`${SERVICES.loan}/api/v1/loans?limit=5`, { timeout: 5000 }),
      axios.get(`${SERVICES.collections}/api/v1/collections/delinquent`, { timeout: 5000 }),
      axios.get(`${SERVICES.reporting}/api/v1/reports/portfolio/summary`, { timeout: 5000 }),
    ]);

    res.json({
      platform: 'LendFast',
      dashboard: {
        recent_loans: loansSummary.status === 'fulfilled' ? loansSummary.value.data : [],
        delinquent_accounts: collectionsSummary.status === 'fulfilled' ? collectionsSummary.value.data : [],
        portfolio_summary: reportingSummary.status === 'fulfilled' ? reportingSummary.value.data : {},
      },
      generated_at: new Date().toISOString(),
    });
  } catch (error) {
    logger.error(`Dashboard aggregation error: ${error.message}`);
    res.status(500).json({ error: 'Failed to aggregate dashboard data' });
  }
});

// ─── Loan Origination Flow (Orchestrated) ────────────────────────────────────
app.post('/api/v1/originate', async (req, res) => {
  const { borrower_id, loan_amount, loan_type, term_months, purpose, annual_income, monthly_debt } = req.body;

  try {
    logger.info(`Starting loan origination for borrower ${borrower_id}`);

    // Step 1: Create application
    const appResp = await axios.post(`${SERVICES.application}/api/v1/applications`, {
      borrower_id,
      loan_amount,
      loan_type: loan_type || 'personal',
      term_months: term_months || 36,
      purpose: purpose || 'general',
    }, { timeout: 10000 });

    const applicationId = appResp.data.application_id || appResp.data.id;

    // Step 2: Request underwriting assessment
    const uwResp = await axios.post(`${SERVICES.underwriting}/api/v1/underwriting/assess`, {
      application_id: applicationId,
      borrower_id,
      requested_amount: loan_amount,
      requested_term_months: term_months || 36,
      loan_type: loan_type || 'personal',
      annual_income: annual_income || 60000,
      monthly_debt_payments: monthly_debt || 0,
      employment_status: 'employed',
    }, { timeout: 15000 });

    const decision = uwResp.data;

    // Step 3: If approved, create loan
    let loan = null;
    if (decision.decision === 'approved') {
      const loanResp = await axios.post(`${SERVICES.loan}/api/v1/loans`, {
        application_id: applicationId,
        borrower_id,
        loan_type: loan_type || 'personal',
        principal_amount: decision.max_approved_amount || loan_amount,
        interest_rate: decision.approved_rate,
        term_months: decision.approved_term_months || term_months,
      }, { timeout: 10000 });
      loan = loanResp.data;
    }

    res.json({
      status: 'success',
      origination: {
        application_id: applicationId,
        underwriting: {
          decision_id: decision.decision_id,
          decision: decision.decision,
          credit_score: decision.credit_score,
          risk_score: decision.risk_score,
          approved_amount: decision.max_approved_amount,
          approved_rate: decision.approved_rate,
        },
        loan: loan,
      },
      timestamp: new Date().toISOString(),
    });
  } catch (error) {
    logger.error(`Origination failed: ${error.message}`);
    res.status(error.response?.status || 500).json({
      error: 'Loan origination failed',
      details: error.response?.data || error.message,
    });
  }
});

// ─── 404 Handler ─────────────────────────────────────────────────────────────
app.use((req, res) => {
  res.status(404).json({ error: 'Route not found', path: req.path });
});

// ─── Error Handler ───────────────────────────────────────────────────────────
app.use((err, req, res, next) => {
  logger.error(`Unhandled error: ${err.message}`);
  res.status(500).json({ error: 'Internal server error' });
});

// ─── Start Server ────────────────────────────────────────────────────────────
app.listen(PORT, () => {
  logger.info(`🚀 LendFast BFF API running on port ${PORT}`);
  logger.info('Services:');
  Object.entries(SERVICES).forEach(([name, url]) => {
    logger.info(`  ${name.padEnd(14)} → ${url}`);
  });
});
