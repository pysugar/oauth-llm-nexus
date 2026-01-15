package handlers

import (
	"net/http"

	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
)

// MonitorPageHandler serves the dedicated request monitor page
func MonitorPageHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(monitorPageHTML))
	}
}

var monitorPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Request Monitor - OAuth-LLM-Nexus</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }
        .container { max-width: 1400px; margin: 0 auto; padding: 1.5rem; }
        header { margin-bottom: 1.5rem; display: flex; justify-content: space-between; align-items: center; }
        .back-link { color: #60a5fa; text-decoration: none; font-size: 0.875rem; }
        .back-link:hover { color: #93c5fd; }
        h1 { font-size: 1.5rem; font-weight: 700; color: white; }
        .subtitle { color: #94a3b8; font-size: 0.75rem; }
        
        /* Controls */
        .controls { display: flex; gap: 1rem; align-items: center; flex-wrap: wrap; margin-bottom: 1rem; }
        .stats { display: flex; gap: 1.5rem; font-size: 0.875rem; font-weight: 600; }
        .stat-total { color: #60a5fa; }
        .stat-success { color: #4ade80; }
        .stat-error { color: #f87171; }
        
        .btn { padding: 0.5rem 1rem; border-radius: 0.5rem; border: none; cursor: pointer; font-size: 0.875rem; font-weight: 500; transition: all 0.15s; display: inline-flex; align-items: center; gap: 0.5rem; }
        .btn-record { background: #dc2626; color: white; }
        .btn-record.paused { background: #374151; }
        .btn-record:hover { opacity: 0.9; }
        .btn-ghost { background: #1e293b; border: 1px solid #334155; color: #94a3b8; }
        .btn-ghost:hover { background: #334155; color: white; }
        
        .dot { width: 8px; height: 8px; border-radius: 50%; }
        .dot.recording { background: white; animation: pulse 1.5s infinite; }
        .dot.paused { background: #6b7280; }
        @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
        
        /* Table */
        .table-container { background: #1e293b; border-radius: 0.75rem; border: 1px solid #334155; overflow: hidden; }
        table { width: 100%; border-collapse: collapse; font-size: 0.8125rem; }
        thead { background: #0f172a; position: sticky; top: 0; z-index: 10; }
        th { padding: 0.75rem 1rem; text-align: left; font-weight: 600; color: #94a3b8; text-transform: uppercase; font-size: 0.6875rem; letter-spacing: 0.05em; }
        th.right { text-align: right; }
        tbody tr { border-top: 1px solid #334155; cursor: pointer; transition: background 0.1s; }
        tbody tr:hover { background: #334155; }
        td { padding: 0.625rem 1rem; vertical-align: middle; }
        td.right { text-align: right; }
        
        .badge { display: inline-block; padding: 0.125rem 0.5rem; border-radius: 0.25rem; font-size: 0.6875rem; font-weight: 600; }
        .badge-success { background: rgba(34,197,94,0.2); color: #4ade80; }
        .badge-error { background: rgba(248,113,113,0.2); color: #f87171; }
        .badge-warning { background: rgba(251,191,36,0.2); color: #fbbf24; }
        
        .model { color: #60a5fa; font-family: monospace; font-size: 0.75rem; }
        .model-mapped { color: #4ade80; font-size: 0.6875rem; }
        .account { color: #94a3b8; font-size: 0.6875rem; max-width: 150px; overflow: hidden; text-overflow: ellipsis; }
        .tokens { font-size: 0.6875rem; color: #94a3b8; }
        .tokens span { display: block; }
        .duration { font-family: monospace; }
        .time { color: #64748b; font-size: 0.75rem; }
        .error-tooltip { position: relative; }
        .error-tooltip:hover::after { 
            content: attr(title); 
            position: absolute; top: 100%; left: 0; 
            background: #1e293b; border: 1px solid #334155; 
            padding: 0.5rem; border-radius: 0.375rem; 
            font-size: 0.75rem; max-width: 300px; white-space: pre-wrap;
            z-index: 100;
        }
        
        .empty { text-align: center; padding: 3rem; color: #64748b; }
        
        /* Modal */
        .modal { position: fixed; inset: 0; background: rgba(0,0,0,0.7); display: none; align-items: center; justify-content: center; z-index: 1000; padding: 1rem; }
        .modal.open { display: flex; }
        .modal-content { background: #1e293b; border: 1px solid #334155; border-radius: 0.75rem; max-width: 900px; width: 100%; max-height: 90vh; overflow: hidden; display: flex; flex-direction: column; }
        .modal-header { padding: 1rem 1.5rem; border-bottom: 1px solid #334155; display: flex; justify-content: space-between; align-items: center; }
        .modal-header h3 { font-size: 1rem; font-weight: 600; }
        .modal-close { background: none; border: none; color: #94a3b8; font-size: 1.5rem; cursor: pointer; }
        .modal-close:hover { color: white; }
        .modal-body { padding: 1.5rem; overflow-y: auto; flex: 1; }
        
        .detail-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1rem; margin-bottom: 1.5rem; background: #0f172a; padding: 1rem; border-radius: 0.5rem; }
        .detail-item label { display: block; font-size: 0.6875rem; color: #64748b; text-transform: uppercase; margin-bottom: 0.25rem; }
        .detail-item .value { font-weight: 600; font-size: 0.875rem; }
        .detail-item .value.success { color: #4ade80; }
        .detail-item .value.error { color: #f87171; }
        
        .payload-section { margin-bottom: 1.5rem; }
        .payload-section h4 { font-size: 0.75rem; color: #94a3b8; text-transform: uppercase; margin-bottom: 0.5rem; }
        .payload-content { background: #020617; border-radius: 0.5rem; padding: 1rem; max-height: 300px; overflow: auto; }
        .payload-content pre { font-family: 'Monaco', 'Menlo', monospace; font-size: 0.6875rem; color: #a5f3fc; white-space: pre-wrap; word-break: break-all; }
        .payload-empty { color: #64748b; font-style: italic; }
        
        .footer { text-align: center; padding: 1.5rem; color: #64748b; font-size: 0.875rem; border-top: 1px solid #1e293b; margin-top: 1.5rem; }
        .footer a { color: #60a5fa; text-decoration: none; }
        .footer a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <div>
                <a href="/" class="back-link">← Back to Dashboard</a>
                <h1>📊 Request Monitor</h1>
                <p class="subtitle">Real-time API request logging and analysis</p>
            </div>
        </header>

        <div class="controls">
            <button id="toggle-btn" class="btn btn-record paused" onclick="toggleLogging()">
                <span class="dot paused" id="dot"></span>
                <span id="toggle-label">Start Recording</span>
            </button>
            <button class="btn btn-ghost" onclick="loadLogs()">↻ Refresh</button>
            <button class="btn btn-ghost" onclick="clearLogs()">🗑️ Clear</button>
            <a href="/monitor/history" class="btn btn-ghost">📜 Full History</a>
            
            <div class="stats">
                <span class="stat-total"><span id="stat-total">0</span> requests</span>
                <span class="stat-success"><span id="stat-success">0</span> success</span>
                <span class="stat-error"><span id="stat-error">0</span> errors</span>
                <span style="color:#64748b;font-size:0.75rem">(last 30 min)</span>
            </div>
        </div>

        <div class="table-container">
            <table>
                <thead>
                    <tr>
                        <th>Status</th>
                        <th>Method</th>
                        <th>Model</th>
                        <th>Account</th>
                        <th>Path</th>
                        <th class="right">Tokens</th>
                        <th class="right">Duration</th>
                        <th class="right">Time</th>
                    </tr>
                </thead>
                <tbody id="logs-tbody">
                    <tr><td colspan="8" class="empty">No logs yet. Click "Start Recording" to begin.</td></tr>
                </tbody>
            </table>
        </div>

        <div class="footer">
            <a href="/">Dashboard</a> • <a href="/monitor/history">Full History</a> • <a href="/tools">Config Inspector</a> • 
            <span style="color:#cbd5e1;font-weight:bold;">{{VERSION}}</span>
        </div>
    </div>

    <!-- Detail Modal -->
    <div class="modal" id="detail-modal" onclick="closeModal(event)">
        <div class="modal-content" onclick="event.stopPropagation()">
            <div class="modal-header">
                <h3>Request Details</h3>
                <button class="modal-close" onclick="closeModal()">&times;</button>
            </div>
            <div class="modal-body" id="detail-body"></div>
        </div>
    </div>

    <script>
        let isLogging = false;
        let logsData = [];
        
        // Always mask sensitive data (emails)
        function maskEmail(email) {
            if (!email) return '-';
            const parts = email.split('@');
            if (parts.length !== 2) return 'u***r@example.com';
            const local = parts[0];
            const domain = parts[1];
            const maskedLocal = local.charAt(0) + '***' + (local.length > 1 ? local.charAt(local.length - 1) : '');
            const domainParts = domain.split('.');
            const maskedDomain = domainParts[0].charAt(0) + '***' + '.' + (domainParts[1] || 'com');
            return maskedLocal + '@' + maskedDomain;
        }

        async function loadStatus() {
            try {
                const res = await fetch('/api/request-logs/status');
                if (res.ok) {
                    const data = await res.json();
                    isLogging = data.enabled;
                    updateUI();
                }
            } catch (e) { console.error(e); }
        }

        function updateUI() {
            const btn = document.getElementById('toggle-btn');
            const dot = document.getElementById('dot');
            const label = document.getElementById('toggle-label');
            
            if (isLogging) {
                btn.classList.remove('paused');
                dot.classList.remove('paused');
                dot.classList.add('recording');
                label.textContent = 'Recording...';
            } else {
                btn.classList.add('paused');
                dot.classList.add('paused');
                dot.classList.remove('recording');
                label.textContent = 'Start Recording';
            }
        }

        async function toggleLogging() {
            try {
                const res = await fetch('/api/request-logs/toggle', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ enabled: !isLogging })
                });
                if (res.ok) {
                    const data = await res.json();
                    isLogging = data.enabled;
                    updateUI();
                }
            } catch (e) { console.error(e); }
        }

        async function loadLogs() {
            try {
                const [logsRes, statsRes] = await Promise.all([
                    fetch('/api/request-logs?limit=100'),
                    fetch('/api/request-stats')
                ]);
                
                if (statsRes.ok) {
                    const stats = await statsRes.json();
                    document.getElementById('stat-total').textContent = stats.total_requests;
                    document.getElementById('stat-success').textContent = stats.success_count;
                    document.getElementById('stat-error').textContent = stats.error_count;
                }
                
                if (logsRes.ok) {
                    const data = await logsRes.json();
                    logsData = data.logs || [];
                    renderLogs();
                }
            } catch (e) { console.error(e); }
        }

        function renderLogs() {
            const tbody = document.getElementById('logs-tbody');
            
            if (logsData.length === 0) {
                tbody.innerHTML = '<tr><td colspan="8" class="empty">No logs yet. Click "Start Recording" to begin.</td></tr>';
                return;
            }
            
            let html = '';
            logsData.forEach((log, idx) => {
                const isSuccess = log.status >= 200 && log.status < 400;
                const badgeClass = isSuccess ? 'badge-success' : (log.status === 429 ? 'badge-warning' : 'badge-error');
                const time = new Date(log.timestamp).toLocaleTimeString();
                const account = maskEmail(log.account_email);
                
                html += '<tr onclick="showDetail(' + idx + ')">';
                
                // Status with error tooltip
                if (log.error && !isSuccess) {
                    html += '<td><span class="badge ' + badgeClass + ' error-tooltip" title="' + escapeHtml(log.error) + '">' + log.status + '</span></td>';
                } else {
                    html += '<td><span class="badge ' + badgeClass + '">' + log.status + '</span></td>';
                }
                
                html += '<td><strong>' + log.method + '</strong></td>';
                
                // Model
                html += '<td>';
                html += '<div class="model">' + (log.model || '-') + '</div>';
                if (log.mapped_model && log.model !== log.mapped_model) {
                    html += '<div class="model-mapped">→ ' + log.mapped_model + '</div>';
                }
                html += '</td>';
                
                html += '<td class="account" title="' + (log.account_email || '') + '">' + account + '</td>';
                html += '<td>' + log.url + '</td>';
                
                // Tokens
                html += '<td class="right tokens">';
                if (log.input_tokens || log.output_tokens) {
                    html += '<span>In: ' + (log.input_tokens || 0) + '</span>';
                    html += '<span>Out: ' + (log.output_tokens || 0) + '</span>';
                } else {
                    html += '-';
                }
                html += '</td>';
                
                html += '<td class="right duration">' + log.duration + 'ms</td>';
                html += '<td class="right time">' + time + '</td>';
                html += '</tr>';
            });
            
            tbody.innerHTML = html;
        }

        function escapeHtml(str) {
            return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
        }

        function showDetail(idx) {
            const log = logsData[idx];
            const isSuccess = log.status >= 200 && log.status < 400;
            
            let html = '<div class="detail-grid">';
            html += '<div class="detail-item"><label>Status</label><div class="value ' + (isSuccess ? 'success' : 'error') + '">' + log.status + '</div></div>';
            html += '<div class="detail-item"><label>Duration</label><div class="value">' + log.duration + 'ms</div></div>';
            html += '<div class="detail-item"><label>Model</label><div class="value" style="color:#60a5fa">' + (log.model || '-') + '</div></div>';
            html += '<div class="detail-item"><label>Mapped To</label><div class="value" style="color:#4ade80">' + (log.mapped_model || '-') + '</div></div>';
            html += '</div>';
            
            html += '<div class="detail-grid" style="grid-template-columns: repeat(3, 1fr);">';
            html += '<div class="detail-item"><label>Account</label><div class="value cursor-help" title="' + (log.account_email || '') + '">' + maskEmail(log.account_email) + '</div></div>';
            html += '<div class="detail-item"><label>Input Tokens</label><div class="value">' + (log.input_tokens || 0) + '</div></div>';
            html += '<div class="detail-item"><label>Output Tokens</label><div class="value">' + (log.output_tokens || 0) + '</div></div>';
            html += '</div>';
            
            if (log.error) {
                html += '<div class="payload-section">';
                html += '<h4>⚠️ Error</h4>';
                html += '<div class="payload-content" style="background:#450a0a"><pre style="color:#fca5a5">' + escapeHtml(log.error) + '</pre></div>';
                html += '</div>';
            }
            
            html += '<div class="payload-section">';
            html += '<h4>📤 Request Body</h4>';
            html += '<div class="payload-content">' + formatPayload(log.request_body) + '</div>';
            html += '</div>';
            
            html += '<div class="payload-section">';
            html += '<h4>📥 Response Body</h4>';
            html += '<div class="payload-content">' + formatPayload(log.response_body) + '</div>';
            html += '</div>';
            
            document.getElementById('detail-body').innerHTML = html;
            document.getElementById('detail-modal').classList.add('open');
        }

        function formatPayload(str) {
            if (!str) return '<pre class="payload-empty">empty</pre>';
            try {
                const obj = JSON.parse(str);
                return '<pre>' + escapeHtml(JSON.stringify(obj, null, 2)) + '</pre>';
            } catch (e) {
                return '<pre>' + escapeHtml(str.substring(0, 5000) + (str.length > 5000 ? '...' : '')) + '</pre>';
            }
        }

        function closeModal(e) {
            if (!e || e.target.classList.contains('modal')) {
                document.getElementById('detail-modal').classList.remove('open');
            }
        }

        async function clearLogs() {
            if (!confirm('Clear all request logs?')) return;
            try {
                await fetch('/api/request-logs/clear', { method: 'POST' });
                logsData = [];
                renderLogs();
                document.getElementById('stat-total').textContent = '0';
                document.getElementById('stat-success').textContent = '0';
                document.getElementById('stat-error').textContent = '0';
            } catch (e) { console.error(e); }
        }

        // Initial load
        window.addEventListener('load', () => {
            loadStatus();
            loadLogs();
        });

        // Auto-refresh every 5 seconds
        setInterval(loadLogs, 5000);
    </script>
</body>
</html>`
