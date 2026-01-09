package handlers

import (
	"net/http"

	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
)

// MonitorHistoryPageHandler serves the request logs history page with pagination
func MonitorHistoryPageHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(monitorHistoryHTML))
	}
}

var monitorHistoryHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Request History - OAuth-LLM-Nexus</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }
        .container { max-width: 1400px; margin: 0 auto; padding: 1.5rem; }
        header { margin-bottom: 1.5rem; }
        .back-link { color: #60a5fa; text-decoration: none; font-size: 0.875rem; }
        .back-link:hover { color: #93c5fd; }
        h1 { font-size: 1.5rem; font-weight: 700; color: white; }
        .subtitle { color: #94a3b8; font-size: 0.75rem; }
        
        /* Search */
        .search-bar { display: flex; gap: 1rem; align-items: center; margin-bottom: 1rem; flex-wrap: wrap; }
        .search-input { background: #1e293b; border: 1px solid #334155; border-radius: 0.5rem; padding: 0.5rem 1rem; color: white; font-size: 0.875rem; width: 300px; }
        .search-input:focus { outline: none; border-color: #60a5fa; }
        .search-input::placeholder { color: #64748b; }
        .btn { padding: 0.5rem 1rem; border-radius: 0.5rem; border: none; cursor: pointer; font-size: 0.875rem; font-weight: 500; transition: all 0.15s; }
        .btn-primary { background: #3b82f6; color: white; }
        .btn-primary:hover { background: #2563eb; }
        .btn-ghost { background: #1e293b; border: 1px solid #334155; color: #94a3b8; }
        .btn-ghost:hover { background: #334155; color: white; }
        
        .stats { font-size: 0.875rem; color: #94a3b8; }
        .stats strong { color: #60a5fa; }
        
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
        .account { color: #94a3b8; font-size: 0.6875rem; max-width: 150px; overflow: hidden; text-overflow: ellipsis; }
        .tokens { font-size: 0.6875rem; color: #94a3b8; }
        .duration { font-family: monospace; }
        .time { color: #64748b; font-size: 0.75rem; }
        
        .empty { text-align: center; padding: 3rem; color: #64748b; }
        
        /* Pagination */
        .pagination { display: flex; justify-content: center; align-items: center; gap: 0.5rem; margin-top: 1rem; padding: 1rem; }
        .page-btn { padding: 0.5rem 0.75rem; border-radius: 0.375rem; border: 1px solid #334155; background: #1e293b; color: #94a3b8; cursor: pointer; font-size: 0.75rem; }
        .page-btn:hover:not(:disabled) { background: #334155; color: white; }
        .page-btn:disabled { opacity: 0.5; cursor: not-allowed; }
        .page-btn.active { background: #3b82f6; border-color: #3b82f6; color: white; }
        .page-info { color: #64748b; font-size: 0.75rem; }
        
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
        
        .footer { text-align: center; padding: 1.5rem; color: #64748b; font-size: 0.875rem; border-top: 1px solid #1e293b; margin-top: 1.5rem; }
        .footer a { color: #60a5fa; text-decoration: none; }
        .footer a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <a href="/monitor" class="back-link">← Back to Live Monitor</a>
            <h1>📜 Request History</h1>
            <p class="subtitle">Browse and search all request logs</p>
        </header>

        <div class="search-bar">
            <input type="text" id="search-input" class="search-input" placeholder="Search by model, URL, account, error..." onkeypress="if(event.key==='Enter')search()">
            <button class="btn btn-primary" onclick="search()">🔍 Search</button>
            <button class="btn btn-ghost" onclick="clearSearch()">Clear</button>
            <div class="stats">
                Showing <strong id="showing">0</strong> of <strong id="total">0</strong> logs
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
                    <tr><td colspan="8" class="empty">Loading...</td></tr>
                </tbody>
            </table>
        </div>

        <div class="pagination" id="pagination"></div>

        <div class="footer">
            <a href="/">Dashboard</a> • <a href="/monitor">Live Monitor</a> • <a href="/tools">Config Inspector</a> • <span style="color:#cbd5e1;font-weight:bold;">{{VERSION}}</span>
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
        let logsData = [];
        let currentPage = 1;
        let totalPages = 1;
        let searchQuery = '';

        async function loadLogs(page = 1) {
            currentPage = page;
            const params = new URLSearchParams({ page, size: 100 });
            if (searchQuery) params.set('q', searchQuery);

            try {
                const res = await fetch('/api/request-logs/history?' + params);
                if (res.ok) {
                    const data = await res.json();
                    logsData = data.logs || [];
                    totalPages = data.total_pages || 1;
                    document.getElementById('showing').textContent = logsData.length;
                    document.getElementById('total').textContent = data.total || 0;
                    renderLogs();
                    renderPagination();
                }
            } catch (e) { console.error(e); }
        }

        function search() {
            searchQuery = document.getElementById('search-input').value.trim();
            loadLogs(1);
        }

        function clearSearch() {
            document.getElementById('search-input').value = '';
            searchQuery = '';
            loadLogs(1);
        }

        function renderLogs() {
            const tbody = document.getElementById('logs-tbody');
            
            if (logsData.length === 0) {
                tbody.innerHTML = '<tr><td colspan="8" class="empty">No logs found.</td></tr>';
                return;
            }
            
            let html = '';
            logsData.forEach((log, idx) => {
                const isSuccess = log.status >= 200 && log.status < 400;
                const badgeClass = isSuccess ? 'badge-success' : (log.status === 429 ? 'badge-warning' : 'badge-error');
                const time = new Date(log.timestamp).toLocaleString();
                const account = log.account_email ? log.account_email.replace(/(.{3}).*(@.*)/, '$1***$2') : '-';
                
                html += '<tr onclick="showDetail(' + idx + ')">';
                html += '<td><span class="badge ' + badgeClass + '">' + log.status + '</span></td>';
                html += '<td><strong>' + log.method + '</strong></td>';
                html += '<td><div class="model">' + (log.model || '-') + '</div></td>';
                html += '<td class="account">' + account + '</td>';
                html += '<td>' + log.url + '</td>';
                
                html += '<td class="right tokens">';
                if (log.input_tokens || log.output_tokens) {
                    html += (log.input_tokens || 0) + ' / ' + (log.output_tokens || 0);
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

        function renderPagination() {
            const container = document.getElementById('pagination');
            let html = '';
            
            html += '<button class="page-btn" onclick="loadLogs(1)" ' + (currentPage === 1 ? 'disabled' : '') + '>«</button>';
            html += '<button class="page-btn" onclick="loadLogs(' + (currentPage - 1) + ')" ' + (currentPage === 1 ? 'disabled' : '') + '>‹</button>';
            
            // Show page numbers
            const start = Math.max(1, currentPage - 2);
            const end = Math.min(totalPages, currentPage + 2);
            
            for (let i = start; i <= end; i++) {
                html += '<button class="page-btn ' + (i === currentPage ? 'active' : '') + '" onclick="loadLogs(' + i + ')">' + i + '</button>';
            }
            
            html += '<button class="page-btn" onclick="loadLogs(' + (currentPage + 1) + ')" ' + (currentPage >= totalPages ? 'disabled' : '') + '>›</button>';
            html += '<button class="page-btn" onclick="loadLogs(' + totalPages + ')" ' + (currentPage >= totalPages ? 'disabled' : '') + '>»</button>';
            
            html += '<span class="page-info">Page ' + currentPage + ' of ' + totalPages + '</span>';
            
            container.innerHTML = html;
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
            html += '<div class="detail-item"><label>Account</label><div class="value">' + (log.account_email || '-') + '</div></div>';
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
            if (!str) return '<pre style="color:#64748b;font-style:italic">empty</pre>';
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

        // Initial load
        window.addEventListener('load', () => loadLogs(1));
    </script>
</body>
</html>`
