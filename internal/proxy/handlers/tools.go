package handlers

import (
	"net/http"
)

// ToolsPageHandler serves the tools page with Config Inspector and IDE Configuration
func ToolsPageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(toolsPageHTML))
	}
}

var toolsPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Config Inspector - OAuth-LLM-Nexus</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; line-height: 1.5; }
        .container { max-width: 1200px; margin: 0 auto; padding: 1.5rem; }
        
        /* Header */
        header { margin-bottom: 2rem; }
        .back-link { color: #60a5fa; text-decoration: none; font-size: 0.875rem; }
        .back-link:hover { color: #93c5fd; }
        h1 { font-size: 1.75rem; font-weight: 700; color: white; margin-top: 1rem; }
        .subtitle { color: #94a3b8; font-size: 0.875rem; margin-top: 0.25rem; }
        
        /* Buttons */
        .btn { display: inline-flex; align-items: center; gap: 0.5rem; padding: 0.625rem 1.25rem; border-radius: 0.5rem; border: none; cursor: pointer; font-size: 0.875rem; font-weight: 500; transition: all 0.15s; }
        .btn-primary { background: linear-gradient(135deg, #3b82f6, #6366f1); color: white; }
        .btn-primary:hover { background: linear-gradient(135deg, #2563eb, #4f46e5); transform: translateY(-1px); }
        .btn-sm { padding: 0.375rem 0.75rem; font-size: 0.75rem; }
        .btn-ghost { background: transparent; color: #94a3b8; border: 1px solid #334155; }
        .btn-ghost:hover { background: #1e293b; color: white; }
        
        /* Cards */
        .card { background: #1e293b; border-radius: 0.75rem; border: 1px solid #334155; margin-bottom: 1.5rem; overflow: hidden; }
        .card-header { padding: 1rem 1.25rem; border-bottom: 1px solid #334155; display: flex; justify-content: space-between; align-items: center; }
        .card-title { font-size: 1rem; font-weight: 600; color: white; display: flex; align-items: center; gap: 0.5rem; }
        .card-body { padding: 1.25rem; }
        
        /* Tabs */
        .tabs { display: flex; gap: 0.25rem; background: #0f172a; padding: 0.25rem; border-radius: 0.5rem; margin-bottom: 1rem; }
        .tab { padding: 0.5rem 1rem; border-radius: 0.375rem; border: none; background: transparent; color: #94a3b8; cursor: pointer; font-size: 0.875rem; transition: all 0.15s; }
        .tab:hover { color: white; }
        .tab.active { background: #334155; color: white; }
        .tab-content { display: none; }
        .tab-content.active { display: block; }
        
        /* Config Cards */
        .config-item { background: #0f172a; border-radius: 0.5rem; margin-bottom: 0.75rem; overflow: hidden; }
        .config-header { padding: 0.875rem 1rem; display: flex; justify-content: space-between; align-items: center; cursor: pointer; transition: background 0.15s; }
        .config-header:hover { background: #1e293b; }
        .config-header-left { display: flex; align-items: center; gap: 0.75rem; }
        .config-icon { width: 2rem; height: 2rem; border-radius: 0.375rem; display: flex; align-items: center; justify-content: center; font-size: 1.125rem; }
        .config-icon.claude { background: linear-gradient(135deg, #f97316, #ea580c); }
        .config-icon.codex { background: linear-gradient(135deg, #22c55e, #16a34a); }
        .config-icon.gemini { background: linear-gradient(135deg, #3b82f6, #1d4ed8); }
        .config-icon.ccswitch { background: linear-gradient(135deg, #8b5cf6, #7c3aed); }
        .config-icon.antigravity { background: linear-gradient(135deg, #ec4899, #db2777); }
        .config-icon.default { background: #334155; }
        .config-name { font-weight: 600; color: white; }
        .config-path { font-size: 0.75rem; color: #64748b; font-family: monospace; }
        .config-status { font-size: 0.75rem; padding: 0.25rem 0.625rem; border-radius: 1rem; }
        .config-status.found { background: rgba(34, 197, 94, 0.2); color: #4ade80; }
        .config-status.missing { background: rgba(100, 116, 139, 0.2); color: #94a3b8; }
        .config-details { padding: 1rem; background: #0f172a; border-top: 1px solid #1e293b; display: none; }
        .config-details.open { display: block; }
        .config-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 1rem; margin-bottom: 1rem; }
        .config-field { }
        .config-field-label { font-size: 0.75rem; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.25rem; }
        .config-field-value { font-family: monospace; font-size: 0.875rem; color: #e2e8f0; word-break: break-all; }
        .config-field-value.empty { color: #64748b; font-style: italic; }
        
        /* Code Block */
        .code-block { background: #020617; border-radius: 0.5rem; padding: 1rem; margin-top: 0.75rem; position: relative; max-height: 300px; overflow-y: auto; }
        .code-block pre { font-family: 'Monaco', 'Menlo', monospace; font-size: 0.75rem; color: #94a3b8; white-space: pre-wrap; word-break: break-all; }
        .code-block-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
        .code-block-title { font-size: 0.75rem; color: #64748b; }
        .copy-btn { font-size: 0.75rem; color: #64748b; background: none; border: none; cursor: pointer; }
        .copy-btn:hover { color: white; }
        
        /* MCP & Skills */
        .mcp-item, .skill-item, .prompt-item { background: #0f172a; border-radius: 0.5rem; padding: 0.875rem 1rem; margin-bottom: 0.5rem; }
        .mcp-item { display: flex; justify-content: space-between; align-items: center; }
        .mcp-name { font-weight: 500; color: white; }
        .mcp-command { font-size: 0.75rem; color: #64748b; font-family: monospace; }
        .prompt-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
        .prompt-title { font-weight: 500; color: white; }
        .prompt-stats { font-size: 0.75rem; color: #64748b; }
        .prompt-preview { font-size: 0.75rem; color: #94a3b8; max-height: 100px; overflow: hidden; white-space: pre-wrap; }
        
        /* Empty State */
        .empty-state { text-align: center; padding: 2rem; color: #64748b; }
        
        /* Loading */
        .loading { display: flex; align-items: center; justify-content: center; padding: 2rem; color: #64748b; }
        .spinner { width: 1.5rem; height: 1.5rem; border: 2px solid #334155; border-top-color: #3b82f6; border-radius: 50%; animation: spin 0.8s linear infinite; margin-right: 0.75rem; }
        @keyframes spin { to { transform: rotate(360deg); } }
        
        /* Footer */
        .footer { text-align: center; padding: 2rem 0; color: #64748b; font-size: 0.875rem; }
        .footer a { color: #60a5fa; text-decoration: none; }
        .footer a:hover { text-decoration: underline; }
        
        /* Collapsible chevron */
        .chevron { transition: transform 0.2s; color: #64748b; }
        .chevron.open { transform: rotate(90deg); }
        
        /* Syntax highlighting */
        .json-key { color: #7dd3fc; }
        .json-string { color: #a5f3fc; }
        .json-number { color: #c4b5fd; }
        .json-boolean { color: #fbbf24; }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <a href="/" class="back-link">← Back to Dashboard</a>
            <h1>🔍 Config Inspector</h1>
            <p class="subtitle">View and inspect all AI IDE configurations on your system</p>
        </header>

        <!-- Card 1: Local Discovery -->
        <div class="card" id="discovery-card">
            <div class="card-header">
                <span class="card-title">🔍 Local Discovery</span>
                <button onclick="checkConfigs()" class="btn btn-primary" id="check-btn">
                    <span>🔄</span> Scan All
                </button>
            </div>
            <div class="card-body">
                <div style="background:#1e3a5f;border:1px solid #3b82f6;border-radius:0.5rem;padding:0.75rem;margin-bottom:1rem;font-size:0.75rem;color:#93c5fd;">
                    🔒 <strong>Privacy:</strong> All scans run locally on your machine. No data is sent to any cloud service.
                </div>
                <div class="tabs">
                    <button class="tab active" data-tab="ides">IDEs</button>
                    <button class="tab" data-tab="mcp">MCP Servers</button>
                    <button class="tab" data-tab="discovery">Account Discovery</button>
                    <button class="tab" data-tab="prompts">Prompts</button>
                    <button class="tab" data-tab="skills">Skills</button>
                </div>
                
                <div id="tab-discovery" class="tab-content">
                    <div id="discovery-container">
                        <div class="empty-state">Click "Scan All" to discover local credentials.</div>
                    </div>
                </div>
                
                <div id="tab-ides" class="tab-content active">
                    <div id="ide-container">
                        <div class="empty-state">Click "Check All Configs" to inspect your IDE configurations.</div>
                    </div>
                </div>
                
                <div id="tab-mcp" class="tab-content">
                    <div id="mcp-container">
                        <div class="empty-state">Run config check to see MCP servers.</div>
                    </div>
                </div>
                
                <div id="tab-prompts" class="tab-content">
                    <div id="prompts-container">
                        <div class="empty-state">Run config check to see prompts.</div>
                    </div>
                </div>
                
                <div id="tab-skills" class="tab-content">
                    <div id="skills-container">
                        <div class="empty-state">Run scan to see installed skills.</div>
                    </div>
                </div>
            </div>
        </div>

        <!-- Card 2: Config Reference -->
        <div class="card" style="margin-top:1.5rem;">
            <div class="card-header">
                <span class="card-title">📚 Config Reference</span>
            </div>
            <div class="card-body">
                <div id="tab-guide" class="tab-content active">
                    <div class="config-section" style="background:#0f172a;border-radius:0.5rem;padding:1rem;margin-bottom:1rem;">
                        <h3 style="color:white;margin-bottom:0.75rem;">🔧 Configure Your IDE to Use Nexus Proxy</h3>
                        <p style="color:#94a3b8;font-size:0.875rem;margin-bottom:1rem;">Use these examples to configure your AI IDEs. API Key: <code id="api-key-display" style="background:#1e293b;padding:0.25rem 0.5rem;border-radius:0.25rem;">Loading...</code></p>
                        
                        <div style="display:grid;gap:1rem;">
                            <!-- Claude Code -->
                            <div style="background:#1e293b;border-radius:0.5rem;padding:1rem;">
                                <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem;">
                                    <span style="color:#f97316;font-weight:600;">🟠 Claude Code</span>
                                    <button onclick="copyConfig('claude')" class="btn btn-sm btn-ghost">📋 Copy</button>
                                </div>
                                <div style="color:#64748b;font-size:0.75rem;margin-bottom:0.5rem;">~/.claude/settings.json</div>
                                <pre id="claude-config" style="background:#020617;padding:0.75rem;border-radius:0.375rem;font-size:0.75rem;color:#a5f3fc;overflow-x:auto;"></pre>
                            </div>
                            
                            <!-- Codex -->
                            <div style="background:#1e293b;border-radius:0.5rem;padding:1rem;">
                                <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem;">
                                    <span style="color:#22c55e;font-weight:600;">🟢 Codex</span>
                                    <button onclick="copyConfig('codex')" class="btn btn-sm btn-ghost">📋 Copy</button>
                                </div>
                                <div style="color:#64748b;font-size:0.75rem;margin-bottom:0.5rem;">~/.codex/auth.json + config.toml</div>
                                <pre id="codex-config" style="background:#020617;padding:0.75rem;border-radius:0.375rem;font-size:0.75rem;color:#a5f3fc;overflow-x:auto;"></pre>
                            </div>
                            
                            <!-- Gemini CLI -->
                            <div style="background:#1e293b;border-radius:0.5rem;padding:1rem;">
                                <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem;">
                                    <span style="color:#3b82f6;font-weight:600;">🔵 Gemini CLI</span>
                                    <button onclick="copyConfig('gemini')" class="btn btn-sm btn-ghost">📋 Copy</button>
                                </div>
                                <div style="color:#64748b;font-size:0.75rem;margin-bottom:0.5rem;">~/.gemini/.env</div>
                                <pre id="gemini-config" style="background:#020617;padding:0.75rem;border-radius:0.375rem;font-size:0.75rem;color:#a5f3fc;overflow-x:auto;"></pre>
                            </div>

                            <!-- Google GenAI SDK -->
                            <div style="background:#1e293b;border-radius:0.5rem;padding:1rem;">
                                <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem;">
                                    <span style="color:#f43f5e;font-weight:600;">🐍 Google GenAI SDK</span>
                                    <button onclick="copyConfig('genai')" class="btn btn-sm btn-ghost">📋 Copy</button>
                                </div>
                                <div style="color:#64748b;font-size:0.75rem;margin-bottom:0.5rem;">Python SDK (v0.2+)</div>
                                <pre id="genai-config" style="background:#020617;padding:0.75rem;border-radius:0.375rem;font-size:0.75rem;color:#a5f3fc;overflow-x:auto;"></pre>
                            </div>
                            
                            <!-- Zed -->
                            <div style="background:#1e293b;border-radius:0.5rem;padding:1rem;">
                                <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem;">
                                    <span style="color:#f59e0b;font-weight:600;">⚡ Zed Editor</span>
                                    <button onclick="copyConfig('zed')" class="btn btn-sm btn-ghost">📋 Copy</button>
                                </div>
                                <div style="color:#64748b;font-size:0.75rem;margin-bottom:0.5rem;">~/.config/zed/settings.json (language_models section)</div>
                                <pre id="zed-config" style="background:#020617;padding:0.75rem;border-radius:0.375rem;font-size:0.75rem;color:#a5f3fc;overflow-x:auto;"></pre>
                            </div>
                            
                            <!-- Cursor / OpenAI -->
                            <div style="background:#1e293b;border-radius:0.5rem;padding:1rem;">
                                <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem;">
                                    <span style="color:#a78bfa;font-weight:600;">🎯 Cursor / OpenAI SDK</span>
                                    <button onclick="copyConfig('openai')" class="btn btn-sm btn-ghost">📋 Copy</button>
                                </div>
                                <div style="color:#64748b;font-size:0.75rem;margin-bottom:0.5rem;">OpenAI-compatible configuration</div>
                                <pre id="openai-config" style="background:#020617;padding:0.75rem;border-radius:0.375rem;font-size:0.75rem;color:#a5f3fc;overflow-x:auto;"></pre>
                            </div>
                            
                            <!-- Shell / Aider -->
                            <div style="background:#1e293b;border-radius:0.5rem;padding:1rem;">
                                <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem;">
                                    <span style="color:#10b981;font-weight:600;">💻 Shell / Aider</span>
                                    <button onclick="copyConfig('shell')" class="btn btn-sm btn-ghost">📋 Copy</button>
                                </div>
                                <div style="color:#64748b;font-size:0.75rem;margin-bottom:0.5rem;">Environment variables</div>
                                <pre id="shell-config" style="background:#020617;padding:0.75rem;border-radius:0.375rem;font-size:0.75rem;color:#a5f3fc;overflow-x:auto;"></pre>
                            </div>
                        </div>
                    </div>
                </div>
            </div>

        <div class="footer">
            <a href="/">← Dashboard</a> • <span style="color:#cbd5e1; font-weight:bold;">{{VERSION}}</span> • <a href="/healthz">Health Check</a>
        </div>

        <!-- Import Preview Modal -->
        <div id="import-modal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,0.7);z-index:1000;align-items:center;justify-content:center;">
            <div style="background:#1e293b;border:1px solid #334155;border-radius:0.75rem;padding:1.5rem;max-width:400px;width:90%;">
                <h3 style="margin:0 0 1rem;color:#f1f5f9;">Import Account</h3>
                <form id="import-form" onsubmit="confirmImport(event)">
                    <input type="hidden" id="import-source">
                    <input type="hidden" id="import-index">
                    <div style="margin-bottom:0.75rem;">
                        <label style="display:block;font-size:0.75rem;color:#94a3b8;margin-bottom:0.25rem;">Email <span style="color:#ef4444;">*</span></label>
                        <input type="email" id="import-email" required style="width:100%;background:#0f172a;border:1px solid #334155;border-radius:0.375rem;padding:0.5rem;color:#f1f5f9;">
                    </div>
                    <div style="margin-bottom:0.75rem;">
                        <label style="display:block;font-size:0.75rem;color:#94a3b8;margin-bottom:0.25rem;">Source</label>
                        <input type="text" id="import-source-display" readonly style="width:100%;background:#020617;border:1px solid #334155;border-radius:0.375rem;padding:0.5rem;color:#64748b;">
                    </div>
                    <div style="margin-bottom:0.75rem;">
                        <label style="display:block;font-size:0.75rem;color:#94a3b8;margin-bottom:0.25rem;">Project ID (optional)</label>
                        <input type="text" id="import-project" style="width:100%;background:#0f172a;border:1px solid #334155;border-radius:0.375rem;padding:0.5rem;color:#f1f5f9;" placeholder="e.g., my-gcp-project">
                    </div>
                    <div style="display:flex;gap:0.5rem;justify-content:flex-end;margin-top:1rem;">
                        <button type="button" onclick="closeImportModal()" class="btn btn-ghost">Cancel</button>
                        <button type="submit" class="btn btn-primary">Confirm Import</button>
                    </div>
                </form>
            </div>
        </div>
    </div>

    <script>
        // Tab switching (scoped to discovery card only)
        const discoveryCard = document.getElementById('discovery-card');
        discoveryCard.querySelectorAll('.tab').forEach(tab => {
            tab.addEventListener('click', () => {
                discoveryCard.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
                discoveryCard.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
                tab.classList.add('active');
                document.getElementById('tab-' + tab.dataset.tab).classList.add('active');
            });
        });

        function getIDEIcon(ide) {
            const lower = ide.toLowerCase();
            if (lower.includes('claude')) return { emoji: '🟠', class: 'claude' };
            if (lower.includes('codex')) return { emoji: '🟢', class: 'codex' };
            if (lower.includes('gemini')) return { emoji: '🔵', class: 'gemini' };
            if (lower.includes('zed')) return { emoji: '⚡', class: 'default' };
            if (lower.includes('alma')) return { emoji: '🤖', class: 'default' };
            if (lower.includes('cc-switch')) return { emoji: '🟣', class: 'ccswitch' };
            if (lower.includes('antigravity')) return { emoji: '🩷', class: 'antigravity' };
            if (lower.includes('cursor')) return { emoji: '🎯', class: 'default' };
            return { emoji: '⚙️', class: 'default' };
        }

        function syntaxHighlight(json) {
            if (typeof json !== 'string') {
                json = JSON.stringify(json, null, 2);
            }
            return json.replace(/("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g, function (match) {
                let cls = 'json-number';
                if (/^"/.test(match)) {
                    if (/:$/.test(match)) {
                        cls = 'json-key';
                    } else {
                        cls = 'json-string';
                    }
                } else if (/true|false/.test(match)) {
                    cls = 'json-boolean';
                }
                return '<span class="' + cls + '">' + match + '</span>';
            });
        }

        function toggleDetails(id) {
            const details = document.getElementById('details-' + id);
            const chevron = document.getElementById('chevron-' + id);
            if (details.classList.contains('open')) {
                details.classList.remove('open');
                chevron.classList.remove('open');
            } else {
                details.classList.add('open');
                chevron.classList.add('open');
            }
        }

        function copyToClipboard(text) {
            navigator.clipboard.writeText(text);
            alert('Copied to clipboard!');
        }

        async function checkConfigs() {
            const btn = document.getElementById('check-btn');
            btn.disabled = true;
            btn.innerHTML = '<span class="spinner"></span> Checking...';
            
            document.getElementById('ide-container').innerHTML = '<div class="loading"><span class="spinner"></span>Scanning configurations...</div>';

            try {
                const res = await fetch('/api/discovery/check');
                const data = await res.json();
                
                // Render IDE configs
                renderIDEConfigs(data.ide_configs || []);
                
                // Render MCP servers
                renderMCPServers(data.mcp_servers || []);
                
                // Render Prompts
                renderPrompts(data.prompts || []);
                
                // Render Skills
                renderSkills(data.skills || []);

                // Also scan for credentials automatically on check
                scanCredentials();
                
            } catch (e) {
                document.getElementById('ide-container').innerHTML = '<div class="empty-state" style="color:#f87171;">Error: ' + e.message + '</div>';
            }
            
            btn.disabled = false;
            btn.innerHTML = '<span>🔄</span> Scan All';
        }

        function renderIDEConfigs(configs) {
            const container = document.getElementById('ide-container');
            
            if (configs.length === 0) {
                container.innerHTML = '<div class="empty-state">No IDE configurations found.</div>';
                return;
            }

            let html = '';
            configs.forEach((cfg, idx) => {
                const icon = getIDEIcon(cfg.ide);
                const statusClass = cfg.exists ? 'found' : 'missing';
                const statusText = cfg.exists ? '✓ Found' : '✗ Not found';
                
                html += '<div class="config-item">';
                html += '<div class="config-header" onclick="toggleDetails(' + idx + ')">';
                html += '<div class="config-header-left">';
                html += '<div class="config-icon ' + icon.class + '">' + icon.emoji + '</div>';
                html += '<div>';
                html += '<div class="config-name">' + cfg.ide + '</div>';
                html += '<div class="config-path">' + cfg.config_path + '</div>';
                html += '</div>';
                html += '</div>';
                html += '<div style="display:flex;align-items:center;gap:0.75rem;">';
                html += '<span class="config-status ' + statusClass + '">' + statusText + '</span>';
                html += '<span class="chevron" id="chevron-' + idx + '">▶</span>';
                html += '</div>';
                html += '</div>';
                
                html += '<div class="config-details" id="details-' + idx + '">';
                
                if (cfg.exists) {
                    // Summary grid
                    html += '<div class="config-grid">';
                    html += '<div class="config-field"><div class="config-field-label">Base URL</div><div class="config-field-value' + (cfg.base_url ? '' : ' empty') + '">' + (cfg.base_url || 'Not configured') + '</div></div>';
                    html += '<div class="config-field"><div class="config-field-label">API Key</div><div class="config-field-value' + (cfg.api_key ? '' : ' empty') + '">' + (cfg.api_key || 'Not configured') + '</div></div>';
                    html += '<div class="config-field"><div class="config-field-label">Model</div><div class="config-field-value' + (cfg.model ? '' : ' empty') + '">' + (cfg.model || 'Not configured') + '</div></div>';
                    html += '</div>';
                    
                    // Environment variables
                    if (cfg.env_vars && Object.keys(cfg.env_vars).length > 0) {
                        html += '<div class="code-block">';
                        html += '<div class="code-block-header"><span class="code-block-title">Environment Variables</span></div>';
                        html += '<pre>';
                        for (const [k, v] of Object.entries(cfg.env_vars)) {
                            html += '<span class="json-key">' + k + '</span>=<span class="json-string">' + v + '</span>\n';
                        }
                        html += '</pre></div>';
                    }
                    
                    // Raw content
                    if (cfg.raw_content) {
                        html += '<div class="code-block">';
                        html += '<div class="code-block-header">';
                        html += '<span class="code-block-title">Raw Configuration</span>';
                        html += '<button class="copy-btn" onclick="copyToClipboard(decodeURIComponent(\'' + encodeURIComponent(cfg.raw_content) + '\'))">📋 Copy</button>';
                        html += '</div>';
                        try {
                            const parsed = JSON.parse(cfg.raw_content);
                            html += '<pre>' + syntaxHighlight(parsed) + '</pre>';
                        } catch {
                            html += '<pre>' + cfg.raw_content.replace(/</g, '&lt;') + '</pre>';
                        }
                        html += '</div>';
                    }
                    
                    // Config files (for multi-file configs like Codex)
                    if (cfg.config_files && cfg.config_files.length > 0) {
                        cfg.config_files.forEach(file => {
                            if (file.exists && file.raw_content) {
                                html += '<div class="code-block">';
                                html += '<div class="code-block-header">';
                                html += '<span class="code-block-title">' + file.path + ' (' + file.format + ')</span>';
                                html += '<button class="copy-btn" onclick="copyToClipboard(decodeURIComponent(\'' + encodeURIComponent(file.raw_content) + '\'))">📋 Copy</button>';
                                html += '</div>';
                                if (file.format === 'json') {
                                    try {
                                        const parsed = JSON.parse(file.raw_content);
                                        html += '<pre>' + syntaxHighlight(parsed) + '</pre>';
                                    } catch {
                                        html += '<pre>' + file.raw_content.replace(/</g, '&lt;') + '</pre>';
                                    }
                                } else {
                                    html += '<pre>' + file.raw_content.replace(/</g, '&lt;') + '</pre>';
                                }
                                html += '</div>';
                            }
                            });
                    }
                    
                    // Extra info (for CC-Switch database, etc.)
                    if (cfg.extra && Object.keys(cfg.extra).length > 0) {
                        html += '<div class="code-block">';
                        html += '<div class="code-block-header"><span class="code-block-title">Additional Info</span></div>';
                        html += '<pre>';
                        for (const [k, v] of Object.entries(cfg.extra)) {
                            html += '<span class="json-key">' + k + '</span>: <span class="json-string">' + v + '</span>\n';
                        }
                        html += '</pre></div>';
                    }
                } else {
                    html += '<div class="empty-state">Configuration file not found at this location.</div>';
                }
                
                html += '</div></div>';
            });
            
            container.innerHTML = html;
        }

        function renderMCPServers(servers) {
            const container = document.getElementById('mcp-container');
            
            if (servers.length === 0) {
                container.innerHTML = '<div class="empty-state">No MCP servers configured.</div>';
                return;
            }

            let html = '';
            servers.forEach(mcp => {
                html += '<div class="mcp-item">';
                html += '<div>';
                html += '<div class="mcp-name">🔌 ' + mcp.name + '</div>';
                html += '<div class="mcp-command">' + (mcp.command || 'No command') + ' ' + (mcp.args ? mcp.args.join(' ') : '') + '</div>';
                html += '</div>';
                html += '</div>';
            });
            
            container.innerHTML = html;
        }

        function renderPrompts(prompts) {
            const container = document.getElementById('prompts-container');
            
            const found = prompts.filter(p => p.exists);
            if (found.length === 0) {
                container.innerHTML = '<div class="empty-state">No prompt files found (CLAUDE.md, AGENTS.md, GEMINI.md)</div>';
                return;
            }

            let html = '';
            found.forEach(prompt => {
                html += '<div class="prompt-item">';
                html += '<div class="prompt-header">';
                html += '<span class="prompt-title">📝 ' + prompt.ide + '</span>';
                html += '<span class="prompt-stats">' + prompt.line_count + ' lines, ' + prompt.char_count + ' chars</span>';
                html += '</div>';
                html += '<div class="config-path" style="margin-bottom:0.5rem;">' + prompt.path + '</div>';
                html += '<div class="prompt-preview">' + (prompt.content ? prompt.content.substring(0, 500).replace(/</g, '&lt;') : '') + (prompt.content && prompt.content.length > 500 ? '...' : '') + '</div>';
                html += '</div>';
            });
            
            container.innerHTML = html;
        }

        function renderSkills(skills) {
            const container = document.getElementById('skills-container');
            
            if (skills.length === 0) {
                container.innerHTML = '<div class="empty-state">No skills installed.</div>';
                return;
            }

            let html = '';
            skills.forEach(skill => {
                html += '<div class="skill-item">';
                html += '<div class="mcp-name">🧩 ' + skill.name + '</div>';
                if (skill.description) {
                    html += '<div class="mcp-command">' + skill.description + '</div>';
                }
                html += '<div class="config-path">' + skill.directory + '</div>';
                html += '</div>';
            });
            
            container.innerHTML = html;
        }

        async function scanCredentials() {
            const container = document.getElementById('discovery-container');
            container.innerHTML = '<div class="loading"><span class="spinner"></span>Scanning local sources...</div>';
            
            try {
                const res = await fetch('/api/discovery/scan');
                const data = await res.json();
                renderDiscovery(data.credentials || []);
            } catch (e) {
                container.innerHTML = '<div class="empty-state" style="color:#f87171;">Error scanning: ' + e.message + '</div>';
            }
        }

        // Store discovered credentials for preview
        let discoveredCreds = [];

        function renderDiscovery(creds) {
            discoveredCreds = creds;
            const container = document.getElementById('discovery-container');
            if (creds.length === 0) {
                container.innerHTML = '<div class="empty-state">No credentials found in local files.</div>';
                return;
            }

            let html = '<div style="margin-bottom:1rem;font-size:0.875rem;color:#94a3b8;">Found ' + creds.length + ' credentials. Click "Preview" to review before importing.</div>';
            creds.forEach((cred, idx) => {
                const icon = getIDEIcon(cred.source);
                html += '<div class="mcp-item" style="margin-bottom:0.75rem;background:#0f172a;border:1px solid #334155;">';
                html += '<div style="display:flex;align-items:center;gap:0.75rem;">';
                html += '<div class="config-icon ' + icon.class + '" style="width:2.5rem;height:2.5rem;">' + icon.emoji + '</div>';
                html += '<div>';
                html += '<div class="mcp-name">' + (cred.email || '<span style="color:#f59e0b;">Email missing</span>') + '</div>';
                html += '<div class="mcp-command" style="font-size:0.75rem;">Source: ' + cred.source + ' • Path: ' + cred.config_path + '</div>';
                if (cred.project_id) {
                    html += '<div class="mcp-command" style="font-size:0.75rem;color:#60a5fa;">Project: ' + cred.project_id + '</div>';
                }
                html += '</div></div>';
                html += '<button onclick="showImportModal(\'' + cred.source + '\', ' + idx + ')" class="btn btn-sm btn-primary" id="import-btn-' + cred.source + '-' + idx + '">🔍 Preview</button>';
                html += '</div>';
            });
            container.innerHTML = html;
        }

        function showImportModal(source, index) {
            const cred = discoveredCreds[index];
            document.getElementById('import-source').value = source;
            document.getElementById('import-index').value = index;
            document.getElementById('import-email').value = cred.email || '';
            document.getElementById('import-source-display').value = cred.source + ' (' + cred.config_path + ')';
            document.getElementById('import-project').value = cred.project_id || '';
            document.getElementById('import-modal').style.display = 'flex';
        }

        function closeImportModal() {
            document.getElementById('import-modal').style.display = 'none';
        }

        async function confirmImport(e) {
            e.preventDefault();
            const source = document.getElementById('import-source').value;
            const index = parseInt(document.getElementById('import-index').value);
            const email = document.getElementById('import-email').value;
            const btn = document.getElementById('import-btn-' + source + '-' + index);
            
            closeImportModal();
            btn.disabled = true;
            btn.innerHTML = '⏳...';

            try {
                const res = await fetch('/api/discovery/import', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ source, index, email })
                });
                const result = await res.json();
                
                if (result.success) {
                    btn.innerHTML = '✅ Imported';
                    btn.classList.remove('btn-primary');
                    btn.classList.add('btn-ghost');
                    btn.style.color = '#4ade80';
                } else if (result.skip) {
                    btn.innerHTML = '⏭️ Exists';
                    btn.classList.remove('btn-primary');
                    btn.classList.add('btn-ghost');
                    btn.title = result.message;
                } else {
                    alert('Error: ' + result.message);
                    btn.disabled = false;
                    btn.innerHTML = '🔍 Preview';
                }
            } catch (e) {
                alert('Import failed: ' + e.message);
                btn.disabled = false;
                btn.innerHTML = '🔍 Preview';
            }
        }
        const baseUrl = window.location.origin;

        async function loadAPIKey() {
            try {
                const res = await fetch('/api/config/apikey');
                if (res.ok) {
                    const data = await res.json();
                    apiKey = data.api_key || 'sk-xxx';
                    document.getElementById('api-key-display').textContent = apiKey;
                    generateConfigExamples();
                }
            } catch (e) { console.error(e); }
        }

        function generateConfigExamples() {
            // Claude Code - Complete example with all env vars
            document.getElementById('claude-config').textContent = JSON.stringify({
                "env": {
                    "ANTHROPIC_AUTH_TOKEN": apiKey,
                    "ANTHROPIC_BASE_URL": baseUrl + "/anthropic",
                    "ANTHROPIC_MODEL": "claude-sonnet-4-5",
                    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gemini-3-flash",
                    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-5",
                    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-4-5-thinking"
                },
                "model": "sonnet"
            }, null, 2);

            // Codex
            document.getElementById('codex-config').textContent = 
                '# auth.json\n' + JSON.stringify({"OPENAI_API_KEY": apiKey}, null, 2) + '\n\n' +
                '# config.toml\nmodel_provider = "nexus"\nmodel = "gpt-4o"\nmodel_reasoning_effort = "high"\n\n' +
                '[model_providers.nexus]\nname = "Nexus Proxy"\nbase_url = "' + baseUrl + '/v1"\n' +
                'wire_api = "responses"\nrequires_openai_auth = true';

            // Gemini CLI - Use /v1 endpoint (OpenAI-compatible)
            document.getElementById('gemini-config').textContent = 
                '# Gemini CLI can use OpenAI-compatible endpoint\n' +
                'OPENAI_API_KEY=' + apiKey + '\n' +
                'OPENAI_BASE_URL=' + baseUrl + '/v1\n' +
                '# Or use native Gemini endpoint (requires Gemini API key)\n' +
                '# GEMINI_API_KEY=your-gemini-api-key';

            // Google GenAI SDK
            document.getElementById('genai-config').textContent = 
                'from google import genai\n\n' +
                'client = genai.Client(\n' +
                '    api_key="' + apiKey + '",\n' +
                '    http_options={"base_url": "' + baseUrl + '/genai"}\n' +
                ')\n\n' +
                '# response = client.models.generate_content(model="gemini-3-flash", contents="Hello")';

            // Zed Editor
            document.getElementById('zed-config').textContent = JSON.stringify({
                "language_models": {
                    "openai": {
                        "api_url": baseUrl + "/v1",
                        "api_key": apiKey,
                        "available_models": [
                            {"name": "gpt-4o", "max_tokens": 128000},
                            {"name": "claude-sonnet-4-5", "max_tokens": 200000},
                            {"name": "claude-opus-4-5-thinking", "max_tokens": 200000}
                        ]
                    }
                }
            }, null, 2);

            // OpenAI SDK / Cursor
            document.getElementById('openai-config').textContent = 
                'Base URL: ' + baseUrl + '/v1\nAPI Key: ' + apiKey + '\n\n' +
                '# Python SDK\n' +
                'from openai import OpenAI\n' +
                'client = OpenAI(\n' +
                '    base_url="' + baseUrl + '/v1",\n' +
                '    api_key="' + apiKey + '"\n' +
                ')';

            // Shell / Aider
            document.getElementById('shell-config').textContent = 
                '# Claude/Anthropic-compatible\n' +
                'export ANTHROPIC_BASE_URL=' + baseUrl + '/anthropic\n' +
                'export ANTHROPIC_API_KEY=' + apiKey + '\n\n' +
                '# OpenAI-compatible (works with most tools)\n' +
                'export OPENAI_BASE_URL=' + baseUrl + '/v1\n' +
                'export OPENAI_API_KEY=' + apiKey;
        }

        function copyConfig(type) {
            let text = '';
            switch(type) {
                case 'claude':
                    text = JSON.stringify({
                        "env": {
                            "ANTHROPIC_AUTH_TOKEN": apiKey,
                            "ANTHROPIC_BASE_URL": baseUrl + "/anthropic",
                            "ANTHROPIC_MODEL": "claude-sonnet-4-5",
                            "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gemini-3-flash",
                            "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-5",
                            "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-4-5-thinking"
                        },
                        "model": "sonnet"
                    }, null, 2);
                    break;
                case 'codex':
                    text = JSON.stringify({"OPENAI_API_KEY": apiKey}, null, 2);
                    break;
                case 'gemini':
                    text = 'OPENAI_API_KEY=' + apiKey + '\nOPENAI_BASE_URL=' + baseUrl + '/v1';
                    break;
                case 'genai':
                    text = 'from google import genai\n\nclient = genai.Client(api_key="' + apiKey + '", http_options={"base_url": "' + baseUrl + '/genai"})';
                    break;
                case 'zed':
                    text = JSON.stringify({"language_models": {"openai": {"api_url": baseUrl + "/v1", "api_key": apiKey}}}, null, 2);
                    break;
                case 'openai':
                    text = 'Base URL: ' + baseUrl + '/v1\nAPI Key: ' + apiKey;
                    break;
                case 'shell':
                    text = 'export ANTHROPIC_BASE_URL=' + baseUrl + '/anthropic\nexport ANTHROPIC_API_KEY=' + apiKey + '\nexport OPENAI_BASE_URL=' + baseUrl + '/v1\nexport OPENAI_API_KEY=' + apiKey;
                    break;
            }
            navigator.clipboard.writeText(text);
            alert('Copied to clipboard!');
        }

        window.addEventListener('load', loadAPIKey);
    </script>
</body>
</html>`
