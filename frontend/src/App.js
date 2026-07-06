import React, { useState, useEffect, useCallback } from 'react';
import { BrowserRouter as Router, Routes, Route, Link, useLocation } from 'react-router-dom';
import axios from 'axios';
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer,
  PieChart, Pie, Cell, LineChart, Line, AreaChart, Area
} from 'recharts';
import {
  Home, Users, FileText, DollarSign, CreditCard, AlertTriangle, BarChart3,
  Calculator, Menu, X, RefreshCw, CheckCircle, XCircle, Clock, TrendingUp, Shield
} from 'lucide-react';

const API = axios.create({ baseURL: process.env.REACT_APP_API_URL || 'http://localhost:5001' });

// ─── Styles ──────────────────────────────────────────────────────────────────
const styles = {
  app: { fontFamily: "'Inter', sans-serif", background: '#f0fdf4', minHeight: '100vh', display: 'flex' },
  sidebar: {
    width: 260, background: 'linear-gradient(180deg, #064e3b 0%, #065f46 100%)',
    color: 'white', display: 'flex', flexDirection: 'column', position: 'fixed',
    top: 0, left: 0, bottom: 0, zIndex: 100,
  },
  sidebarHeader: { padding: '24px 20px', borderBottom: '1px solid rgba(255,255,255,0.1)' },
  logo: { fontSize: 24, fontWeight: 700, letterSpacing: '-0.5px' },
  logoSub: { fontSize: 11, color: '#6ee7b7', marginTop: 4 },
  nav: { flex: 1, padding: '12px 0', overflowY: 'auto' },
  navItem: (active) => ({
    display: 'flex', alignItems: 'center', gap: 12, padding: '12px 20px', cursor: 'pointer',
    background: active ? 'rgba(255,255,255,0.15)' : 'transparent', color: active ? '#fff' : '#a7f3d0',
    textDecoration: 'none', fontSize: 14, fontWeight: active ? 600 : 400, borderLeft: active ? '3px solid #34d399' : '3px solid transparent',
    transition: 'all 0.2s',
  }),
  main: { flex: 1, marginLeft: 260, padding: '24px 32px' },
  pageTitle: { fontSize: 28, fontWeight: 700, color: '#064e3b', marginBottom: 24 },
  grid: (cols) => ({ display: 'grid', gridTemplateColumns: `repeat(${cols}, 1fr)`, gap: 20, marginBottom: 24 }),
  card: {
    background: 'white', borderRadius: 12, padding: 24, boxShadow: '0 1px 3px rgba(0,0,0,0.08)',
    border: '1px solid #d1fae5',
  },
  cardTitle: { fontSize: 13, color: '#6b7280', fontWeight: 500, marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px' },
  cardValue: { fontSize: 32, fontWeight: 700, color: '#064e3b' },
  cardSub: { fontSize: 12, color: '#059669', marginTop: 4 },
  table: { width: '100%', borderCollapse: 'collapse', fontSize: 14 },
  th: { textAlign: 'left', padding: '12px 16px', borderBottom: '2px solid #d1fae5', color: '#374151', fontWeight: 600, fontSize: 13 },
  td: { padding: '12px 16px', borderBottom: '1px solid #ecfdf5', color: '#1f2937' },
  badge: (type) => ({
    display: 'inline-block', padding: '4px 10px', borderRadius: 20, fontSize: 12, fontWeight: 600,
    background: type === 'approved' ? '#d1fae5' : type === 'rejected' ? '#fee2e2' : type === 'active' ? '#dbeafe' : type === 'delinquent' ? '#fef3c7' : '#f3f4f6',
    color: type === 'approved' ? '#065f46' : type === 'rejected' ? '#991b1b' : type === 'active' ? '#1e40af' : type === 'delinquent' ? '#92400e' : '#374151',
  }),
  btn: (variant) => ({
    padding: '10px 20px', borderRadius: 8, border: 'none', cursor: 'pointer', fontWeight: 600, fontSize: 14,
    background: variant === 'primary' ? '#059669' : variant === 'danger' ? '#ef4444' : '#e5e7eb',
    color: variant === 'primary' || variant === 'danger' ? 'white' : '#374151',
    transition: 'opacity 0.2s',
  }),
  input: { padding: '10px 14px', borderRadius: 8, border: '1px solid #d1d5db', fontSize: 14, outline: 'none', width: '100%' },
  select: { padding: '10px 14px', borderRadius: 8, border: '1px solid #d1d5db', fontSize: 14, outline: 'none', width: '100%', background: 'white' },
};

const COLORS = ['#059669', '#0891b2', '#7c3aed', '#ea580c', '#e11d48', '#0284c7'];
const fmt = (n) => typeof n === 'number' ? `$${n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}` : '$0.00';

// ─── Navigation ──────────────────────────────────────────────────────────────
const navItems = [
  { path: '/', icon: Home, label: 'Dashboard' },
  { path: '/borrowers', icon: Users, label: 'Borrowers' },
  { path: '/applications', icon: FileText, label: 'Applications' },
  { path: '/loans', icon: DollarSign, label: 'Loans' },
  { path: '/payments', icon: CreditCard, label: 'Payments' },
  { path: '/collections', icon: AlertTriangle, label: 'Collections' },
  { path: '/reports', icon: BarChart3, label: 'Reports' },
  { path: '/calculator', icon: Calculator, label: 'EMI Calculator' },
];

function Sidebar() {
  const location = useLocation();
  return (
    <div style={styles.sidebar}>
      <div style={styles.sidebarHeader}>
        <div style={styles.logo}>💰 LendFast</div>
        <div style={styles.logoSub}>Lending & Credit Platform</div>
      </div>
      <nav style={styles.nav}>
        {navItems.map(({ path, icon: Icon, label }) => (
          <Link key={path} to={path} style={styles.navItem(location.pathname === path)}>
            <Icon size={18} /> {label}
          </Link>
        ))}
      </nav>
      <div style={{ padding: '16px 20px', borderTop: '1px solid rgba(255,255,255,0.1)', fontSize: 12, color: '#6ee7b7' }}>
        Application 4 • v1.0.0
      </div>
    </div>
  );
}

// ─── Dashboard Page ──────────────────────────────────────────────────────────
function Dashboard() {
  const [status, setStatus] = useState(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    API.get('/api/v1/status').then(r => setStatus(r.data)).catch(() => {}).finally(() => setLoading(false));
  }, []);

  const loanData = [
    { month: 'Jan', originated: 125, amount: 3200000 },
    { month: 'Feb', originated: 148, amount: 3800000 },
    { month: 'Mar', originated: 162, amount: 4100000 },
    { month: 'Apr', originated: 191, amount: 4900000 },
    { month: 'May', originated: 178, amount: 4500000 },
    { month: 'Jun', originated: 210, amount: 5400000 },
  ];

  const typeData = [
    { name: 'Personal', value: 45 },
    { name: 'Auto', value: 25 },
    { name: 'Mortgage', value: 20 },
    { name: 'Business', value: 10 },
  ];

  const riskData = [
    { month: 'Jan', delinquency: 2.1, writeOff: 0.3 },
    { month: 'Feb', delinquency: 2.3, writeOff: 0.4 },
    { month: 'Mar', delinquency: 1.9, writeOff: 0.2 },
    { month: 'Apr', delinquency: 2.0, writeOff: 0.3 },
    { month: 'May', delinquency: 1.8, writeOff: 0.2 },
    { month: 'Jun', delinquency: 1.6, writeOff: 0.1 },
  ];

  const serviceCount = status?.services ? Object.keys(status.services).length : 0;
  const onlineCount = status?.services ? Object.values(status.services).filter(s => s.status === 'UP').length : 0;

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
        <h1 style={styles.pageTitle}>Dashboard</h1>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: '#6b7280' }}>
          <Shield size={16} />
          Services: {onlineCount}/{serviceCount} online
        </div>
      </div>

      <div style={styles.grid(4)}>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Total Portfolio</div>
          <div style={styles.cardValue}>$47.2M</div>
          <div style={styles.cardSub}>+12.5% from last month</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Active Loans</div>
          <div style={styles.cardValue}>1,847</div>
          <div style={styles.cardSub}>+89 new this month</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Approval Rate</div>
          <div style={styles.cardValue}>68.4%</div>
          <div style={styles.cardSub}>+2.1% improvement</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Delinquency Rate</div>
          <div style={styles.cardValue}>1.6%</div>
          <div style={styles.cardSub}>-0.2% decrease ✓</div>
        </div>
      </div>

      <div style={styles.grid(2)}>
        <div style={styles.card}>
          <h3 style={{ margin: '0 0 16px', color: '#064e3b' }}>Loan Origination Trend</h3>
          <ResponsiveContainer width="100%" height={280}>
            <BarChart data={loanData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
              <XAxis dataKey="month" />
              <YAxis />
              <Tooltip />
              <Legend />
              <Bar dataKey="originated" fill="#059669" name="Loans Originated" radius={[4, 4, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
        <div style={styles.card}>
          <h3 style={{ margin: '0 0 16px', color: '#064e3b' }}>Portfolio by Loan Type</h3>
          <ResponsiveContainer width="100%" height={280}>
            <PieChart>
              <Pie data={typeData} cx="50%" cy="50%" outerRadius={100} dataKey="value" label={({name, value}) => `${name}: ${value}%`}>
                {typeData.map((_, i) => <Cell key={i} fill={COLORS[i]} />)}
              </Pie>
              <Tooltip />
            </PieChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div style={styles.card}>
        <h3 style={{ margin: '0 0 16px', color: '#064e3b' }}>Risk Metrics Trend</h3>
        <ResponsiveContainer width="100%" height={250}>
          <AreaChart data={riskData}>
            <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
            <XAxis dataKey="month" />
            <YAxis />
            <Tooltip />
            <Legend />
            <Area type="monotone" dataKey="delinquency" fill="#fef3c7" stroke="#f59e0b" name="Delinquency %" />
            <Area type="monotone" dataKey="writeOff" fill="#fee2e2" stroke="#ef4444" name="Write-off %" />
          </AreaChart>
        </ResponsiveContainer>
      </div>

      {status?.services && (
        <div style={{ ...styles.card, marginTop: 20 }}>
          <h3 style={{ margin: '0 0 16px', color: '#064e3b' }}>Service Health</h3>
          <div style={styles.grid(4)}>
            {Object.entries(status.services).map(([name, svc]) => (
              <div key={name} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 0' }}>
                {svc.status === 'UP' ? <CheckCircle size={16} color="#059669" /> : <XCircle size={16} color="#ef4444" />}
                <span style={{ fontSize: 14, textTransform: 'capitalize' }}>{name}</span>
                {svc.port && <span style={{ fontSize: 12, color: '#9ca3af' }}>:{svc.port}</span>}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// ─── Borrowers Page ──────────────────────────────────────────────────────────
function Borrowers() {
  const [borrowers, setBorrowers] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    API.get('/api/v1/borrowers').then(r => setBorrowers(Array.isArray(r.data) ? r.data : r.data.borrowers || []))
      .catch(() => setBorrowers(sampleBorrowers)).finally(() => setLoading(false));
  }, []);

  const sampleBorrowers = [
    { id: 'XAJ4S70Y6D', first_name: 'Sarah', last_name: 'Mitchell', email: 'sarah@email.com', credit_score: 720, annual_income: 85000, status: 'active' },
    { id: 'Z3R84FD9Y7', first_name: 'James', last_name: 'Rodriguez', email: 'james@email.com', credit_score: 680, annual_income: 62000, status: 'active' },
    { id: '9X6APBHPHS', first_name: 'Emily', last_name: 'Chen', email: 'emily@email.com', credit_score: 750, annual_income: 110000, status: 'active' },
    { id: 'B5K7M2N4Q1', first_name: 'Michael', last_name: 'Thompson', email: 'michael@email.com', credit_score: 640, annual_income: 48000, status: 'review' },
    { id: 'C8L3P9R6T2', first_name: 'Lisa', last_name: 'Anderson', email: 'lisa@email.com', credit_score: 790, annual_income: 135000, status: 'active' },
  ];

  const data = borrowers.length > 0 ? borrowers : sampleBorrowers;

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
        <h1 style={styles.pageTitle}>Borrowers</h1>
        <button style={styles.btn('primary')}>+ Add Borrower</button>
      </div>

      <div style={styles.grid(3)}>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Total Borrowers</div>
          <div style={styles.cardValue}>{data.length}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Avg Credit Score</div>
          <div style={styles.cardValue}>{Math.round(data.reduce((a, b) => a + (b.credit_score || 700), 0) / data.length)}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Avg Income</div>
          <div style={styles.cardValue}>{fmt(data.reduce((a, b) => a + (b.annual_income || 60000), 0) / data.length)}</div>
        </div>
      </div>

      <div style={styles.card}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>ID</th>
              <th style={styles.th}>Name</th>
              <th style={styles.th}>Email</th>
              <th style={styles.th}>Credit Score</th>
              <th style={styles.th}>Annual Income</th>
              <th style={styles.th}>Status</th>
            </tr>
          </thead>
          <tbody>
            {data.map((b, i) => (
              <tr key={i} style={{ cursor: 'pointer' }}>
                <td style={styles.td}><code>{(b.id || b.borrower_id || '').slice(0, 10)}</code></td>
                <td style={styles.td}>{b.first_name || ''} {b.last_name || ''}</td>
                <td style={styles.td}>{b.email || '-'}</td>
                <td style={styles.td}>
                  <span style={{ fontWeight: 600, color: (b.credit_score || 0) >= 700 ? '#059669' : (b.credit_score || 0) >= 600 ? '#f59e0b' : '#ef4444' }}>
                    {b.credit_score || '-'}
                  </span>
                </td>
                <td style={styles.td}>{fmt(b.annual_income)}</td>
                <td style={styles.td}><span style={styles.badge(b.status || 'active')}>{b.status || 'active'}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─── Applications Page ───────────────────────────────────────────────────────
function Applications() {
  const [applications, setApplications] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    API.get('/api/v1/applications').then(r => setApplications(Array.isArray(r.data) ? r.data : r.data.applications || []))
      .catch(() => setApplications(sampleApps)).finally(() => setLoading(false));
  }, []);

  const sampleApps = [
    { id: 'APP-001', borrower_id: 'XAJ4S70Y6D', loan_type: 'personal', amount: 25000, term_months: 36, status: 'approved', credit_score: 720 },
    { id: 'APP-002', borrower_id: 'Z3R84FD9Y7', loan_type: 'auto', amount: 35000, term_months: 60, status: 'pending', credit_score: 680 },
    { id: 'APP-003', borrower_id: '9X6APBHPHS', loan_type: 'mortgage', amount: 350000, term_months: 360, status: 'approved', credit_score: 750 },
    { id: 'APP-004', borrower_id: 'B5K7M2N4Q1', loan_type: 'personal', amount: 15000, term_months: 24, status: 'rejected', credit_score: 540 },
    { id: 'APP-005', borrower_id: 'C8L3P9R6T2', loan_type: 'business', amount: 100000, term_months: 48, status: 'approved', credit_score: 790 },
  ];

  const data = applications.length > 0 ? applications : sampleApps;
  const approved = data.filter(a => a.status === 'approved').length;
  const rejected = data.filter(a => a.status === 'rejected').length;
  const pending = data.filter(a => a.status === 'pending' || a.status === 'submitted').length;

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
        <h1 style={styles.pageTitle}>Loan Applications</h1>
        <button style={styles.btn('primary')}>+ New Application</button>
      </div>

      <div style={styles.grid(4)}>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Total Applications</div>
          <div style={styles.cardValue}>{data.length}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Approved</div>
          <div style={{ ...styles.cardValue, color: '#059669' }}>{approved}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Rejected</div>
          <div style={{ ...styles.cardValue, color: '#ef4444' }}>{rejected}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Pending Review</div>
          <div style={{ ...styles.cardValue, color: '#f59e0b' }}>{pending}</div>
        </div>
      </div>

      <div style={styles.card}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Application ID</th>
              <th style={styles.th}>Borrower</th>
              <th style={styles.th}>Type</th>
              <th style={styles.th}>Amount</th>
              <th style={styles.th}>Term</th>
              <th style={styles.th}>Credit Score</th>
              <th style={styles.th}>Status</th>
            </tr>
          </thead>
          <tbody>
            {data.map((a, i) => (
              <tr key={i}>
                <td style={styles.td}><code>{a.id || a.application_id}</code></td>
                <td style={styles.td}>{a.borrower_id}</td>
                <td style={{ ...styles.td, textTransform: 'capitalize' }}>{a.loan_type}</td>
                <td style={styles.td}>{fmt(a.amount || a.loan_amount)}</td>
                <td style={styles.td}>{a.term_months}mo</td>
                <td style={styles.td}>{a.credit_score || '-'}</td>
                <td style={styles.td}><span style={styles.badge(a.status)}>{a.status}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─── Loans Page ──────────────────────────────────────────────────────────────
function Loans() {
  const [loans, setLoans] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    API.get('/api/v1/loans').then(r => setLoans(Array.isArray(r.data) ? r.data : r.data.loans || []))
      .catch(() => setLoans(sampleLoans)).finally(() => setLoading(false));
  }, []);

  const sampleLoans = [
    { id: 'LN-001', borrower_id: 'XAJ4S70Y6D', type: 'personal', principal: 25000, rate: 8.99, term: 36, balance: 18750, status: 'active', monthly_payment: 793.41 },
    { id: 'LN-002', borrower_id: '9X6APBHPHS', type: 'mortgage', principal: 350000, rate: 5.99, term: 360, balance: 342000, status: 'active', monthly_payment: 2095.67 },
    { id: 'LN-003', borrower_id: 'C8L3P9R6T2', type: 'business', principal: 100000, rate: 7.49, term: 48, balance: 82000, status: 'active', monthly_payment: 2418.30 },
    { id: 'LN-004', borrower_id: 'Z3R84FD9Y7', type: 'auto', principal: 28000, rate: 12.99, term: 60, balance: 14200, status: 'active', monthly_payment: 636.02 },
    { id: 'LN-005', borrower_id: 'B5K7M2N4Q1', type: 'personal', principal: 10000, rate: 15.99, term: 24, balance: 0, status: 'closed', monthly_payment: 488.73 },
  ];

  const data = loans.length > 0 ? loans : sampleLoans;
  const totalPortfolio = data.reduce((s, l) => s + (l.balance || l.principal || 0), 0);
  const activeLoans = data.filter(l => l.status === 'active').length;

  return (
    <div>
      <h1 style={styles.pageTitle}>Loan Portfolio</h1>

      <div style={styles.grid(4)}>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Total Portfolio</div>
          <div style={styles.cardValue}>{fmt(totalPortfolio)}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Active Loans</div>
          <div style={styles.cardValue}>{activeLoans}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Avg Interest Rate</div>
          <div style={styles.cardValue}>{(data.reduce((s, l) => s + (l.rate || l.interest_rate || 0), 0) / data.length).toFixed(2)}%</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Monthly Revenue</div>
          <div style={styles.cardValue}>{fmt(data.filter(l => l.status === 'active').reduce((s, l) => s + (l.monthly_payment || 0), 0))}</div>
        </div>
      </div>

      <div style={styles.card}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Loan ID</th>
              <th style={styles.th}>Borrower</th>
              <th style={styles.th}>Type</th>
              <th style={styles.th}>Principal</th>
              <th style={styles.th}>Rate</th>
              <th style={styles.th}>Term</th>
              <th style={styles.th}>Balance</th>
              <th style={styles.th}>Monthly PMT</th>
              <th style={styles.th}>Status</th>
            </tr>
          </thead>
          <tbody>
            {data.map((l, i) => (
              <tr key={i}>
                <td style={styles.td}><code>{l.id || l.loan_id}</code></td>
                <td style={styles.td}>{l.borrower_id}</td>
                <td style={{ ...styles.td, textTransform: 'capitalize' }}>{l.type || l.loan_type}</td>
                <td style={styles.td}>{fmt(l.principal || l.principal_amount)}</td>
                <td style={styles.td}>{(l.rate || l.interest_rate || 0).toFixed(2)}%</td>
                <td style={styles.td}>{l.term || l.term_months}mo</td>
                <td style={styles.td}>{fmt(l.balance || l.remaining_balance)}</td>
                <td style={styles.td}>{fmt(l.monthly_payment)}</td>
                <td style={styles.td}><span style={styles.badge(l.status)}>{l.status}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─── Payments Page ───────────────────────────────────────────────────────────
function Payments() {
  const [payments, setPayments] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    API.get('/api/v1/payments').then(r => setPayments(Array.isArray(r.data) ? r.data : r.data.payments || []))
      .catch(() => setPayments(samplePayments)).finally(() => setLoading(false));
  }, []);

  const samplePayments = [
    { id: 'PMT-001', loan_id: 'LN-001', amount: 793.41, principal: 650.00, interest: 143.41, date: '2024-01-15', method: 'auto-debit', status: 'completed' },
    { id: 'PMT-002', loan_id: 'LN-002', amount: 2095.67, principal: 1345.50, interest: 750.17, date: '2024-01-15', method: 'auto-debit', status: 'completed' },
    { id: 'PMT-003', loan_id: 'LN-003', amount: 2418.30, principal: 1795.00, interest: 623.30, date: '2024-01-15', method: 'bank-transfer', status: 'completed' },
    { id: 'PMT-004', loan_id: 'LN-004', amount: 636.02, principal: 333.50, interest: 302.52, date: '2024-01-20', method: 'auto-debit', status: 'pending' },
    { id: 'PMT-005', loan_id: 'LN-001', amount: 793.41, principal: 655.00, interest: 138.41, date: '2024-02-15', method: 'auto-debit', status: 'completed' },
  ];

  const data = payments.length > 0 ? payments : samplePayments;
  const totalCollected = data.filter(p => p.status === 'completed').reduce((s, p) => s + (p.amount || 0), 0);

  return (
    <div>
      <h1 style={styles.pageTitle}>Payments</h1>

      <div style={styles.grid(4)}>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Total Collected</div>
          <div style={styles.cardValue}>{fmt(totalCollected)}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Payments This Month</div>
          <div style={styles.cardValue}>{data.length}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Avg Payment</div>
          <div style={styles.cardValue}>{fmt(totalCollected / Math.max(data.filter(p => p.status === 'completed').length, 1))}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Pending</div>
          <div style={{ ...styles.cardValue, color: '#f59e0b' }}>{data.filter(p => p.status === 'pending').length}</div>
        </div>
      </div>

      <div style={styles.card}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Payment ID</th>
              <th style={styles.th}>Loan</th>
              <th style={styles.th}>Amount</th>
              <th style={styles.th}>Principal</th>
              <th style={styles.th}>Interest</th>
              <th style={styles.th}>Date</th>
              <th style={styles.th}>Method</th>
              <th style={styles.th}>Status</th>
            </tr>
          </thead>
          <tbody>
            {data.map((p, i) => (
              <tr key={i}>
                <td style={styles.td}><code>{p.id || p.payment_id}</code></td>
                <td style={styles.td}>{p.loan_id}</td>
                <td style={{ ...styles.td, fontWeight: 600 }}>{fmt(p.amount)}</td>
                <td style={styles.td}>{fmt(p.principal)}</td>
                <td style={styles.td}>{fmt(p.interest)}</td>
                <td style={styles.td}>{p.date || p.payment_date || '-'}</td>
                <td style={styles.td}>{p.method || p.payment_method || '-'}</td>
                <td style={styles.td}><span style={styles.badge(p.status === 'completed' ? 'approved' : p.status)}>{p.status}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─── Collections Page ────────────────────────────────────────────────────────
function Collections() {
  const [collections, setCollections] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    API.get('/api/v1/collections').then(r => setCollections(Array.isArray(r.data) ? r.data : r.data.collections || []))
      .catch(() => setCollections(sampleCollections)).finally(() => setLoading(false));
  }, []);

  const sampleCollections = [
    { id: 'COL-001', loan_id: 'LN-006', borrower_id: 'D7F2K5H8', days_overdue: 15, amount_due: 1250.00, stage: 'early', last_contact: '2024-01-10', status: 'delinquent' },
    { id: 'COL-002', loan_id: 'LN-007', borrower_id: 'E9G4L6J1', days_overdue: 45, amount_due: 3200.00, stage: 'mid', last_contact: '2024-01-05', status: 'delinquent' },
    { id: 'COL-003', loan_id: 'LN-008', borrower_id: 'F1H3M7K4', days_overdue: 90, amount_due: 8500.00, stage: 'late', last_contact: '2023-12-20', status: 'delinquent' },
    { id: 'COL-004', loan_id: 'LN-009', borrower_id: 'G2I5N8L6', days_overdue: 5, amount_due: 450.00, stage: 'early', last_contact: '2024-01-12', status: 'delinquent' },
  ];

  const data = collections.length > 0 ? collections : sampleCollections;
  const totalOverdue = data.reduce((s, c) => s + (c.amount_due || 0), 0);

  return (
    <div>
      <h1 style={styles.pageTitle}>Collections</h1>

      <div style={styles.grid(4)}>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Delinquent Accounts</div>
          <div style={{ ...styles.cardValue, color: '#ef4444' }}>{data.length}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Total Overdue</div>
          <div style={{ ...styles.cardValue, color: '#ef4444' }}>{fmt(totalOverdue)}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Early Stage (1-30d)</div>
          <div style={styles.cardValue}>{data.filter(c => c.stage === 'early').length}</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Late Stage (60+d)</div>
          <div style={{ ...styles.cardValue, color: '#ef4444' }}>{data.filter(c => c.stage === 'late').length}</div>
        </div>
      </div>

      <div style={styles.card}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Case ID</th>
              <th style={styles.th}>Loan</th>
              <th style={styles.th}>Borrower</th>
              <th style={styles.th}>Days Overdue</th>
              <th style={styles.th}>Amount Due</th>
              <th style={styles.th}>Stage</th>
              <th style={styles.th}>Last Contact</th>
              <th style={styles.th}>Action</th>
            </tr>
          </thead>
          <tbody>
            {data.map((c, i) => (
              <tr key={i}>
                <td style={styles.td}><code>{c.id || c.collection_id}</code></td>
                <td style={styles.td}>{c.loan_id}</td>
                <td style={styles.td}>{c.borrower_id}</td>
                <td style={{ ...styles.td, fontWeight: 600, color: c.days_overdue > 60 ? '#ef4444' : c.days_overdue > 30 ? '#f59e0b' : '#6b7280' }}>
                  {c.days_overdue}d
                </td>
                <td style={{ ...styles.td, fontWeight: 600 }}>{fmt(c.amount_due)}</td>
                <td style={styles.td}><span style={styles.badge(c.stage === 'late' ? 'rejected' : 'delinquent')}>{c.stage}</span></td>
                <td style={styles.td}>{c.last_contact || '-'}</td>
                <td style={styles.td}>
                  <button style={{ ...styles.btn('primary'), padding: '6px 12px', fontSize: 12 }}>Contact</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─── Reports Page ────────────────────────────────────────────────────────────
function Reports() {
  const originationData = [
    { month: 'Jul', personal: 45, auto: 22, mortgage: 8, business: 12 },
    { month: 'Aug', personal: 52, auto: 28, mortgage: 10, business: 15 },
    { month: 'Sep', personal: 48, auto: 25, mortgage: 12, business: 18 },
    { month: 'Oct', personal: 55, auto: 30, mortgage: 14, business: 20 },
    { month: 'Nov', personal: 60, auto: 32, mortgage: 11, business: 16 },
    { month: 'Dec', personal: 58, auto: 35, mortgage: 15, business: 22 },
  ];

  const performanceData = [
    { month: 'Jul', collections: 95.2, delinquency: 2.3, writeOff: 0.4 },
    { month: 'Aug', collections: 94.8, delinquency: 2.5, writeOff: 0.5 },
    { month: 'Sep', collections: 96.1, delinquency: 2.0, writeOff: 0.3 },
    { month: 'Oct', collections: 95.8, delinquency: 1.9, writeOff: 0.3 },
    { month: 'Nov', collections: 96.5, delinquency: 1.7, writeOff: 0.2 },
    { month: 'Dec', collections: 97.0, delinquency: 1.6, writeOff: 0.1 },
  ];

  return (
    <div>
      <h1 style={styles.pageTitle}>Reports & Analytics</h1>

      <div style={styles.grid(3)}>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Portfolio Yield</div>
          <div style={styles.cardValue}>9.42%</div>
          <div style={styles.cardSub}>Weighted avg interest rate</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Recovery Rate</div>
          <div style={styles.cardValue}>72.3%</div>
          <div style={styles.cardSub}>Collections efficiency</div>
        </div>
        <div style={styles.card}>
          <div style={styles.cardTitle}>Net Interest Margin</div>
          <div style={styles.cardValue}>4.18%</div>
          <div style={styles.cardSub}>After cost of funds</div>
        </div>
      </div>

      <div style={styles.grid(2)}>
        <div style={styles.card}>
          <h3 style={{ margin: '0 0 16px', color: '#064e3b' }}>Loan Origination by Type</h3>
          <ResponsiveContainer width="100%" height={300}>
            <BarChart data={originationData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
              <XAxis dataKey="month" />
              <YAxis />
              <Tooltip />
              <Legend />
              <Bar dataKey="personal" stackId="a" fill="#059669" name="Personal" />
              <Bar dataKey="auto" stackId="a" fill="#0891b2" name="Auto" />
              <Bar dataKey="mortgage" stackId="a" fill="#7c3aed" name="Mortgage" />
              <Bar dataKey="business" stackId="a" fill="#ea580c" name="Business" />
            </BarChart>
          </ResponsiveContainer>
        </div>
        <div style={styles.card}>
          <h3 style={{ margin: '0 0 16px', color: '#064e3b' }}>Portfolio Performance</h3>
          <ResponsiveContainer width="100%" height={300}>
            <LineChart data={performanceData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
              <XAxis dataKey="month" />
              <YAxis />
              <Tooltip />
              <Legend />
              <Line type="monotone" dataKey="collections" stroke="#059669" strokeWidth={2} name="Collection Rate %" />
              <Line type="monotone" dataKey="delinquency" stroke="#f59e0b" strokeWidth={2} name="Delinquency %" />
              <Line type="monotone" dataKey="writeOff" stroke="#ef4444" strokeWidth={2} name="Write-off %" />
            </LineChart>
          </ResponsiveContainer>
        </div>
      </div>
    </div>
  );
}

// ─── EMI Calculator Page ─────────────────────────────────────────────────────
function EMICalculator() {
  const [principal, setPrincipal] = useState(25000);
  const [rate, setRate] = useState(8.99);
  const [term, setTerm] = useState(36);
  const [result, setResult] = useState(null);

  const calculate = useCallback(() => {
    const r = rate / 100 / 12;
    const n = term;
    const emi = principal * r * Math.pow(1 + r, n) / (Math.pow(1 + r, n) - 1);
    const totalPayment = emi * n;
    const totalInterest = totalPayment - principal;

    const schedule = [];
    let balance = principal;
    for (let i = 1; i <= Math.min(n, 12); i++) {
      const interest = balance * r;
      const principalPortion = emi - interest;
      balance -= principalPortion;
      schedule.push({ month: i, payment: emi, principal: principalPortion, interest, balance: Math.max(0, balance) });
    }

    setResult({ emi, totalPayment, totalInterest, schedule });
  }, [principal, rate, term]);

  useEffect(() => { calculate(); }, [calculate]);

  return (
    <div>
      <h1 style={styles.pageTitle}>EMI Calculator</h1>

      <div style={styles.grid(2)}>
        <div style={styles.card}>
          <h3 style={{ margin: '0 0 20px', color: '#064e3b' }}>Loan Parameters</h3>
          
          <div style={{ marginBottom: 16 }}>
            <label style={{ display: 'block', fontSize: 13, fontWeight: 600, color: '#374151', marginBottom: 6 }}>Loan Amount ($)</label>
            <input type="number" value={principal} onChange={e => setPrincipal(+e.target.value)} style={styles.input} />
            <input type="range" min="1000" max="500000" step="1000" value={principal} onChange={e => setPrincipal(+e.target.value)}
              style={{ width: '100%', marginTop: 8 }} />
          </div>

          <div style={{ marginBottom: 16 }}>
            <label style={{ display: 'block', fontSize: 13, fontWeight: 600, color: '#374151', marginBottom: 6 }}>Interest Rate (%)</label>
            <input type="number" value={rate} onChange={e => setRate(+e.target.value)} step="0.01" style={styles.input} />
            <input type="range" min="1" max="30" step="0.25" value={rate} onChange={e => setRate(+e.target.value)}
              style={{ width: '100%', marginTop: 8 }} />
          </div>

          <div style={{ marginBottom: 16 }}>
            <label style={{ display: 'block', fontSize: 13, fontWeight: 600, color: '#374151', marginBottom: 6 }}>Term (months)</label>
            <select value={term} onChange={e => setTerm(+e.target.value)} style={styles.select}>
              <option value="12">12 months (1 year)</option>
              <option value="24">24 months (2 years)</option>
              <option value="36">36 months (3 years)</option>
              <option value="48">48 months (4 years)</option>
              <option value="60">60 months (5 years)</option>
              <option value="120">120 months (10 years)</option>
              <option value="180">180 months (15 years)</option>
              <option value="240">240 months (20 years)</option>
              <option value="360">360 months (30 years)</option>
            </select>
          </div>

          <button onClick={calculate} style={{ ...styles.btn('primary'), width: '100%', marginTop: 8 }}>
            <Calculator size={16} style={{ marginRight: 8, verticalAlign: 'middle' }} />
            Calculate EMI
          </button>
        </div>

        {result && (
          <div>
            <div style={styles.grid(1)}>
              <div style={{ ...styles.card, background: 'linear-gradient(135deg, #064e3b, #059669)', color: 'white', textAlign: 'center' }}>
                <div style={{ fontSize: 14, opacity: 0.8, marginBottom: 8 }}>Monthly Payment (EMI)</div>
                <div style={{ fontSize: 42, fontWeight: 700 }}>{fmt(result.emi)}</div>
              </div>
            </div>
            <div style={styles.grid(2)}>
              <div style={styles.card}>
                <div style={styles.cardTitle}>Total Payment</div>
                <div style={{ fontSize: 24, fontWeight: 700, color: '#064e3b' }}>{fmt(result.totalPayment)}</div>
              </div>
              <div style={styles.card}>
                <div style={styles.cardTitle}>Total Interest</div>
                <div style={{ fontSize: 24, fontWeight: 700, color: '#ea580c' }}>{fmt(result.totalInterest)}</div>
              </div>
            </div>

            <div style={styles.card}>
              <h4 style={{ margin: '0 0 12px', color: '#064e3b' }}>Payment Breakdown</h4>
              <ResponsiveContainer width="100%" height={200}>
                <PieChart>
                  <Pie data={[
                    { name: 'Principal', value: principal },
                    { name: 'Interest', value: result.totalInterest },
                  ]} cx="50%" cy="50%" outerRadius={80} dataKey="value" label={({name, value}) => `${name}: ${fmt(value)}`}>
                    <Cell fill="#059669" />
                    <Cell fill="#ea580c" />
                  </Pie>
                  <Tooltip formatter={(v) => fmt(v)} />
                </PieChart>
              </ResponsiveContainer>
            </div>
          </div>
        )}
      </div>

      {result && (
        <div style={{ ...styles.card, marginTop: 20 }}>
          <h3 style={{ margin: '0 0 16px', color: '#064e3b' }}>Amortization Schedule (First 12 Months)</h3>
          <table style={styles.table}>
            <thead>
              <tr>
                <th style={styles.th}>Month</th>
                <th style={styles.th}>Payment</th>
                <th style={styles.th}>Principal</th>
                <th style={styles.th}>Interest</th>
                <th style={styles.th}>Balance</th>
              </tr>
            </thead>
            <tbody>
              {result.schedule.map((row, i) => (
                <tr key={i}>
                  <td style={styles.td}>{row.month}</td>
                  <td style={styles.td}>{fmt(row.payment)}</td>
                  <td style={{ ...styles.td, color: '#059669' }}>{fmt(row.principal)}</td>
                  <td style={{ ...styles.td, color: '#ea580c' }}>{fmt(row.interest)}</td>
                  <td style={{ ...styles.td, fontWeight: 600 }}>{fmt(row.balance)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// ─── App Root ────────────────────────────────────────────────────────────────
function App() {
  return (
    <Router>
      <div style={styles.app}>
        <Sidebar />
        <main style={styles.main}>
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/borrowers" element={<Borrowers />} />
            <Route path="/applications" element={<Applications />} />
            <Route path="/loans" element={<Loans />} />
            <Route path="/payments" element={<Payments />} />
            <Route path="/collections" element={<Collections />} />
            <Route path="/reports" element={<Reports />} />
            <Route path="/calculator" element={<EMICalculator />} />
          </Routes>
        </main>
      </div>
    </Router>
  );
}

export default App;
