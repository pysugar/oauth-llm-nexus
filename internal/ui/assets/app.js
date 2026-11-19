// Standalone Magpie-inspired console for oauth-llm-nexus (/ui).
(function () {
  "use strict";

  const TEXT = {
    zh: {
      nav_providers: "后端与模型",
      nav_routing: "路由与矩阵",
      nav_tester: "四协议测试台",
      nav_connect: "接入与日志",
      nav_usage: "用量与成本",
      usage_title: "用量与成本",
      usage_sub: "持久化账本：每次调用的 token、缓存与按 models.dev / 自定义价格计算的成本（美元）",
      usage_requests: "请求",
      usage_errors: "错误",
      usage_input: "输入",
      usage_output: "输出",
      usage_cache: "缓存读/写",
      usage_cost: "成本",
      usage_latency: "平均延迟",
      usage_empty: "所选时间段内没有记录",
      usage_unpriced: "未定价",
      sync: "同步状态",
      clear_all_cd: "清除全部冷却",
      providers_title: "可用后端",
      ready_count: "{n}/{total} 个后端可用",
      ready_stats: "{n}/{total} 可用 · {req} 次 · {tok} tok",
      free_only_hint: "OpenRouter 已默认开启仅免费模型过滤（free_only）。",
      refresh_models: "刷新模型",
      clear_cd: "清除冷却",
      endpoints: "接入端点",
      protocols: "原生协议",
      keys: "凭据状态",
      catalog_status: "目录同步",
      free_only_banner: "已开启仅显示免费模型（free_only: true），仅列出 0 费用文本模型；可在下方运行时开关直接切换。",
      sw_enabled: "启用后端",
      sw_free_only: "仅免费模型",
      sw_include_non_chat: "包含非对话模型",
      lbl_prefer_proto: "首选协议",
      add_key_placeholder: "追加 API Key…",
      btn_add_key: "+ 添加",
      lbl_key_strategy: "Key 调度策略",
      remove_key_confirm: "从内存密钥环移除 {masked}？",
      key_active_tag: "当前",
      provider_updated: "已更新 {id} 配置",
      stats_title: "实时用量与延迟指标",
      reset_stats: "重置统计",
      stats_reset_ok: "已重置网关统计计数",
      stat_requests: "请求数 (成功率)",
      stat_latency: "平均 / EWMA 延迟",
      stat_tokens: "输入 / 输出 Token",
      stat_cache_reason: "缓存 / 推理 Token",
      search_models: "搜索当前后端模型名称…",
      showing_models: "显示 {shown} / {total} 个模型",
      no_models: "暂无可用模型，请检查密钥与端点后点击刷新。",
      no_matches: "未找到匹配 “{q}” 的模型。",
      btn_test: "Test",
      btn_testing: "测试中…",
      btn_3apis: "4 协议测试 →",
      btn_bind: "配置映射 →",
      btn_hide: "收起详情",
      route_editor_title: "模型映射与跨后端矩阵",
      route_editor_sub: "输入任意模型名即可查看所有支持该模型的后端，支持一键切换主用（#1）或同步为多后端接力 Failover 组。",
      lbl_client_model: "客户端模型名",
      lbl_provider: "目标后端",
      lbl_target_model: "上游模型 ID（可选）",
      lbl_strategy: "调度策略",
      btn_switch_primary: "切换主后端",
      btn_add_backup: "+ 添加备选",
      tab_routes: "显式路由",
      tab_matrix: "多后端矩阵",
      tab_catalog: "目录发现",
      tab_legacy: "旧版 OAuth",
      tab_all: "全部",
      matrix_multi_only: "仅看多后端模型",
      sync_failover_btn: "⚡ 同步为多后端 Failover 组",
      synced_failover: "已为 {model} 生成多后端 Failover 接力组",
      no_routes_empty: "当前视图下暂无匹配模型。可在左侧搜索或切换到「多后端矩阵」「目录发现」浏览全部模型。",
      active_label: "当前生效",
      promote_btn: "设为主用",
      remove_btn: "删除",
      tester_title: "四协议兼容性测试台",
      tester_sub: "实时穿透 Nexus 网关验证 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages 与 Google Gemini（含流式 SSE 与工具调用）。",
      lbl_test_model: "模型 ID",
      lbl_test_provider: "指定后端（可选）",
      lbl_test_prompt: "测试提示词",
      auto_provider: "自动路由（按映射 / 归一化目录）",
      btn_test_all3: "🚀 一键测齐四大协议",
      mode_sync: "同步 JSON",
      mode_stream: "流式 SSE",
      mode_tools: "工具调用",
      idle_proto_hint: "尚未触发测试。点击「一键测齐四大协议」或单个协议按钮开始验证。",
      wire_inspect: "查看报文详情（Request / Response）",
      connect_title: "客户端接入指南",
      connect_sub: "将任意 SDK、Claude Code 或 Codex 的 Base URL 指向当前 Nexus 实例即可透明切换后端。",
      copy_snippet: "复制代码片段",
      activity_title: "近期网关调用流水",
      activity_sub: "默认关闭请求落盘以保护性能；开启后可在此展开查看完整路由与请求报文。",
      enable_logging: "开启请求记录",
      disable_logging: "关闭请求记录",
      logging_off_hint: "请求日志记录当前处于关闭状态，仅记录你开启之后的真实接口调用。",
      no_logs: "暂无调用记录。",
      copied: "已复制到剪贴板",
      switched: "已切换主后端：{model} → {provider}",
      added_route: "已添加备选路由：{model} → {provider}",
      removed_route: "已移除路由项",
      refreshed: "已刷新 {id} 模型目录（{n} 个）",
      cleared_cd: "已清除冷却状态",
      keys_title: "客户端密钥",
      keys_sub: "为不同调用方（知识库应用、编码 Agent、局域网设备）签发独立密钥，可限定可用的后端与模型。未选择即不限制。",
      key_name: "名称",
      key_providers: "允许的后端（不选 = 全部）",
      key_models: "允许的模型别名（逗号分隔，支持 前缀*；留空 = 全部）",
      btn_create_key: "+ 签发密钥",
      key_issued: "密钥「{name}」已签发。明文仅显示这一次，请立即保存：",
      btn_use_in_snippets: "用于上方代码片段",
      btn_dismiss: "我已保存",
      key_legacy: "默认密钥 · 不受限",
      key_disabled: "已停用",
      key_active: "启用中",
      all_providers: "全部后端",
      all_models: "全部模型",
      btn_edit: "编辑",
      btn_save: "保存",
      btn_cancel: "取消",
      btn_disable: "停用",
      btn_enable: "启用",
      btn_delete: "删除",
      btn_reset_key: "恢复默认密钥",
      confirm_delete_key: "确定删除密钥「{name}」？使用它的客户端将立即失效。",
      key_created_toast: "已签发密钥 {name}",
      key_updated_toast: "已更新密钥 {name}",
      key_deleted_toast: "已删除密钥 {name}",
      no_client_keys: "尚未签发客户端密钥。",
      key_last_used: "最近使用",
      key_never_used: "未使用",
      key_name_required: "请填写密钥名称"
    },
    en: {
      nav_providers: "Providers",
      nav_routing: "Routing",
      nav_tester: "Protocol Tester",
      nav_connect: "Connect & Logs",
      nav_usage: "Usage & Cost",
      usage_title: "Usage & Cost",
      usage_sub: "Persisted ledger: tokens, cache and cost per call, priced from models.dev or your overrides (USD)",
      usage_requests: "Requests",
      usage_errors: "Errors",
      usage_input: "Input",
      usage_output: "Output",
      usage_cache: "Cache R/W",
      usage_cost: "Cost",
      usage_latency: "Avg latency",
      usage_empty: "No records in this range",
      usage_unpriced: "unpriced",
      sync: "Sync",
      clear_all_cd: "Clear Cooldowns",
      providers_title: "Backends",
      ready_count: "{n}/{total} ready",
      ready_stats: "{n}/{total} ready · {req} req · {tok} tok",
      free_only_hint: "OpenRouter defaults to free models only (free_only).",
      refresh_models: "Refresh Models",
      clear_cd: "Clear Cooldown",
      endpoints: "Endpoints",
      protocols: "Native Protocols",
      keys: "Credentials",
      catalog_status: "Catalog",
      free_only_banner: "OpenRouter free-only filtering is active (free_only: true). Toggle Free Only below to list paid catalog models.",
      sw_enabled: "Enabled",
      sw_free_only: "Free Only",
      sw_include_non_chat: "Include Non-Chat",
      lbl_prefer_proto: "Preferred",
      add_key_placeholder: "Append API key…",
      btn_add_key: "+ Add",
      lbl_key_strategy: "Key Relay Mode",
      remove_key_confirm: "Remove {masked} from active key ring?",
      key_active_tag: "active",
      provider_updated: "Updated {id} config",
      stats_title: "Live Telemetry & Latency",
      reset_stats: "Reset Stats",
      stats_reset_ok: "Gateway stats reset",
      stat_requests: "Requests (OK)",
      stat_latency: "Avg / EWMA Latency",
      stat_tokens: "In / Out Tokens",
      stat_cache_reason: "Cached / Reasoning",
      search_models: "Filter models on this provider…",
      showing_models: "Showing {shown} / {total} models",
      no_models: "No discovered models yet. Check credentials/endpoints and click Refresh.",
      no_matches: "No models matching “{q}”.",
      btn_test: "Test",
      btn_testing: "Testing…",
      btn_3apis: "Test 4 APIs →",
      btn_bind: "Map Route →",
      btn_hide: "Hide Details",
      route_editor_title: "Model Mappings & Backend Matrix",
      route_editor_sub: "Inspect every backend supporting any canonical model, promote a primary (#1), or sync multi-backend failover chains.",
      lbl_client_model: "Client Model",
      lbl_provider: "Target Backend",
      lbl_target_model: "Upstream Model (optional)",
      lbl_strategy: "Strategy",
      btn_switch_primary: "Switch Primary",
      btn_add_backup: "+ Add Backup",
      tab_routes: "Explicit Routes",
      tab_matrix: "Model Matrix",
      tab_catalog: "Auto Catalog",
      tab_legacy: "Legacy OAuth",
      tab_all: "All",
      matrix_multi_only: "Multi-Backend Only",
      sync_failover_btn: "⚡ Sync Failover Group",
      synced_failover: "Synced multi-backend failover routes for {model}",
      no_routes_empty: "No matching routes here. Try switching to Model Matrix or Auto Catalog.",
      active_label: "Active",
      promote_btn: "Use",
      remove_btn: "Delete",
      tester_title: "4-Protocol Compatibility Tester",
      tester_sub: "Verify OpenAI Chat, OpenAI Responses, Anthropic Messages, and Google Gemini API end-to-end (sync, SSE stream, tool calls).",
      lbl_test_model: "Model ID",
      lbl_test_provider: "Backend Pin (optional)",
      lbl_test_prompt: "Prompt",
      auto_provider: "Auto (Follow Route / Canonical Catalog)",
      btn_test_all3: "🚀 Test All 4 Protocols",
      mode_sync: "Sync JSON",
      mode_stream: "Stream SSE",
      mode_tools: "Tool Call",
      idle_proto_hint: "Not tested yet. Click Test All 4 Protocols or an individual protocol button.",
      wire_inspect: "Inspect Wire (Request / Response)",
      connect_title: "Connect Client SDKs & Agents",
      connect_sub: "Point any OpenAI, Responses, Anthropic, or Gemini client at Nexus without code changes.",
      copy_snippet: "Copy Snippet",
      activity_title: "Recent Gateway Activity",
      activity_sub: "Request logging is off by default; enable it to inspect payloads and provenance headers.",
      enable_logging: "Enable Logging",
      disable_logging: "Disable Logging",
      logging_off_hint: "Request logging is currently disabled.",
      no_logs: "No recent requests logged.",
      copied: "Copied to clipboard",
      switched: "Primary switched: {model} → {provider}",
      added_route: "Added backup route: {model} → {provider}",
      removed_route: "Route deleted",
      refreshed: "Refreshed {id} catalog ({n} models)",
      cleared_cd: "Cleared cooldowns",
      keys_title: "Client Keys",
      keys_sub: "Issue a separate key per caller (PKM app, coding agents, LAN devices) and optionally restrict which backends and models it may reach. Nothing selected = unrestricted.",
      key_name: "Name",
      key_providers: "Allowed backends (none = all)",
      key_models: "Allowed model aliases (comma-separated, prefix* supported; empty = all)",
      btn_create_key: "+ Issue Key",
      key_issued: "Key \"{name}\" issued. The plaintext is shown only once — store it now:",
      btn_use_in_snippets: "Use in snippets above",
      btn_dismiss: "I've saved it",
      key_legacy: "Default key · unrestricted",
      key_disabled: "Disabled",
      key_active: "Active",
      all_providers: "All backends",
      all_models: "All models",
      btn_edit: "Edit",
      btn_save: "Save",
      btn_cancel: "Cancel",
      btn_disable: "Disable",
      btn_enable: "Enable",
      btn_delete: "Delete",
      btn_reset_key: "Use default key",
      confirm_delete_key: "Delete key \"{name}\"? Clients using it stop working immediately.",
      key_created_toast: "Issued key {name}",
      key_updated_toast: "Updated key {name}",
      key_deleted_toast: "Deleted key {name}",
      no_client_keys: "No client keys issued yet.",
      key_last_used: "Last used",
      key_never_used: "never",
      key_name_required: "Key name is required"
    }
  };

  const PROTOCOL_CARDS = [
    { id: "openai-chat", name: "OpenAI Chat Completions", path: "POST /v1/chat/completions" },
    { id: "openai-responses", name: "OpenAI Responses API", path: "POST /v1/responses" },
    { id: "anthropic", name: "Anthropic Messages API", path: "POST /anthropic/v1/messages" },
    { id: "gemini", name: "Google Gemini API", path: "POST /v1beta/models/{model}:generateContent" }
  ];

  const DEFAULT_PROMPT_TEXT = "Reply briefly with OK.";
  const DEFAULT_TOOL_PROMPT = "Check the weather in Tokyo using the get_weather tool.";

  const state = {
    view: "providers",
    lang: readPref("nexus.ui.lang", "zh"),
    theme: readPref("nexus.ui.theme", "auto"),
    providers: [],
    models: [],
    matrix: [],
    matrixMultiCount: 0,
    matrixMultiOnly: false,
    stats: null,
    selectedProviderId: "",
    providerQueries: {},
    modelProbes: {},
    probeExpanded: {},
    routingTab: "routes",
    routingQuery: "",
    routingShowAll: false,
    tester: {
      model: "",
      provider: "",
      prompt: DEFAULT_PROMPT_TEXT,
      mode: "sync",
      results: {},
      showGemini: true,
      runningAll: false
    },
    connect: {
      flavor: "openai-chat",
      lang: "shell",
      apiKey: "",
      keySource: "legacy", // "legacy" or the client key name shown in snippets
      masked: false,
      revealKey: false,
      loggingEnabled: false,
      logs: [],
      expandedLogs: {}
    },
    keys: {
      list: [],
      draftProviders: [],
      editing: {},
      issued: null
    }
  };

  let toastTimer = null;

  function readPref(key, fallback) {
    try {
      return localStorage.getItem(key) || fallback;
    } catch (_) {
      return fallback;
    }
  }

  function writePref(key, value) {
    try {
      localStorage.setItem(key, value);
    } catch (_) {}
  }

  function t(key, vars) {
    const table = TEXT[state.lang] || TEXT.zh;
    let str = table[key] || TEXT.en[key] || key;
    if (vars) {
      Object.keys(vars).forEach((k) => {
        str = str.replace("{" + k + "}", String(vars[k]));
      });
    }
    return str;
  }

  function $(sel) {
    return document.querySelector(sel);
  }

  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach((k) => {
        const val = attrs[k];
        if (val === null || val === undefined || val === false) return;
        if (k === "class") {
          node.className = val;
        } else if (k === "text") {
          node.textContent = String(val);
        } else if (k.startsWith("on") && typeof val === "function") {
          node.addEventListener(k.slice(2).toLowerCase(), val);
        } else if (k === "value" && ("value" in node)) {
          node.value = val;
        } else if (k === "hidden") {
          node.hidden = !!val;
        } else if (k === "disabled") {
          node.disabled = !!val;
        } else {
          node.setAttribute(k, String(val));
        }
      });
    }
    children.flat(Infinity).forEach((ch) => {
      if (ch === null || ch === undefined || ch === false) return;
      node.appendChild(typeof ch === "string" ? document.createTextNode(ch) : ch);
    });
    return node;
  }

  function showToast(msg) {
    const box = $("#toast");
    if (!box) return;
    box.textContent = msg;
    box.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => {
      box.hidden = true;
    }, 2400);
  }

  async function copyText(str, okMsg) {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(str);
      } else {
        const ta = el("textarea", { value: str });
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      showToast(okMsg || t("copied"));
    } catch (_) {
      showToast("Copy failed");
    }
  }

  function formatDurationSec(sec) {
    const n = Number(sec || 0);
    if (!Number.isFinite(n) || n <= 0) return "0s";
    if (n < 60) return Math.ceil(n) + "s";
    const m = Math.floor(n / 60);
    const s = Math.ceil(n % 60);
    return m + "m" + (s > 0 ? s + "s" : "");
  }

  function formatCompactNum(v) {
    const n = Number(v || 0);
    if (!Number.isFinite(n) || n <= 0) return "0";
    if (n >= 1000000) return (n / 1000000).toFixed(1) + "M";
    if (n >= 10000) return (n / 1000).toFixed(1) + "k";
    return n.toLocaleString("en-US");
  }

  const EP_KEY_TO_PROTO = {
    openai_chat: "openai-chat",
    openai_responses: "openai-responses",
    anthropic: "anthropic",
    gemini: "gemini"
  };

  async function patchProvider(providerId, patch, toastMsg) {
    const res = await apiJSON("/api/gateway/providers/" + encodeURIComponent(providerId), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(patch || {})
    });
    if (!res.ok) {
      showToast((res.data && res.data.error) || "Update failed");
      return false;
    }
    showToast(toastMsg || t("provider_updated", { id: providerId }));
    await syncAll();
    return true;
  }

  async function resetGatewayStats() {
    const res = await apiJSON("/api/gateway/stats/reset", { method: "POST" });
    if (!res.ok) {
      showToast((res.data && res.data.error) || "Reset failed");
      return;
    }
    state.stats = res.data || null;
    showToast(t("stats_reset_ok"));
    await syncAll();
  }

  async function refreshStatsOnly() {
    const res = await apiJSON("/api/gateway/stats");
    if (!res.ok || !res.data) return;
    state.stats = res.data;
    const byProv = new Map((res.data.providers || []).map((item) => [item.provider_id, item]));
    state.providers.forEach((prov) => {
      prov.stats = byProv.get(prov.id) || null;
    });
    renderGlobalStatus();
    const mount = $("#providerStatsMount");
    const active = state.providers.find((item) => item.id === state.selectedProviderId);
    if (mount && active) {
      const strip = renderProviderStatsStrip(active);
      mount.replaceChildren(...(strip ? [strip] : []));
    }
  }

  function renderTogglePill(label, active, onClick) {
    return el("button", {
      type: "button",
      class: "toggle-pill" + (active ? " on" : ""),
      "aria-pressed": active ? "true" : "false",
      onClick
    },
      el("i", { class: "toggle-dot" }),
      el("span", { text: label })
    );
  }

  function prettyJSON(raw) {
    if (!raw) return "";
    try {
      const parsed = typeof raw === "string" ? JSON.parse(raw) : raw;
      return JSON.stringify(parsed, null, 2);
    } catch (_) {
      return String(raw);
    }
  }

  async function apiJSON(path, opts) {
    const res = await fetch(path, Object.assign({ cache: "no-cache" }, opts || {}));
    const text = await res.text();
    let data = {};
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = { error: text };
    }
    return { ok: res.ok, status: res.status, data };
  }

  // Theme & Language
  function applyTheme() {
    const root = document.documentElement;
    if (state.theme === "light" || state.theme === "dark") {
      root.setAttribute("data-theme", state.theme);
    } else {
      root.removeAttribute("data-theme");
    }
    const labels = { auto: "Auto", light: "Light", dark: "Dark" };
    const btn = $("#themeToggleBtn");
    if (btn) btn.textContent = "◐ " + (labels[state.theme] || "Auto");
  }

  function cycleTheme() {
    const order = ["auto", "dark", "light"];
    const next = order[(order.indexOf(state.theme) + 1) % order.length];
    state.theme = next;
    writePref("nexus.ui.theme", next);
    applyTheme();
  }

  function toggleLang() {
    state.lang = state.lang === "zh" ? "en" : "zh";
    writePref("nexus.ui.lang", state.lang);
    applyStaticTranslations();
    renderAll();
  }

  function applyStaticTranslations() {
    const navMap = {
      providers: t("nav_providers"),
      routing: t("nav_routing"),
      tester: t("nav_tester"),
      usage: t("nav_usage"),
      connect: t("nav_connect")
    };
    document.querySelectorAll("#mainNav [data-view]").forEach((btn) => {
      btn.textContent = navMap[btn.dataset.view] || btn.dataset.view;
    });
    $("#syncBtnLabel").textContent = t("sync");
    $("#clearAllCooldownsBtn").textContent = "🧹 " + t("clear_all_cd");
    $("#langToggleBtn").textContent = state.lang === "zh" ? "中 / EN" : "EN / 中";

    $("#providersRailTitle").textContent = t("providers_title");
    $("#providersRailFoot").textContent = t("free_only_hint");

    $("#routeEditorTitle").textContent = t("route_editor_title");
    $("#routeEditorSub").textContent = t("route_editor_sub");
    $("#lblClientModel").textContent = t("lbl_client_model");
    $("#lblBackendProvider").textContent = t("lbl_provider");
    $("#lblTargetModel").textContent = t("lbl_target_model");
    $("#lblStrategy").textContent = t("lbl_strategy");
    $("#bindSelectBtn").textContent = t("btn_switch_primary");
    $("#bindAddBackupBtn").textContent = t("btn_add_backup");
    if ($("#matrixMultiOnlyLabel")) $("#matrixMultiOnlyLabel").textContent = t("matrix_multi_only");

    $("#testerTitle").textContent = t("tester_title");
    $("#testerSub").textContent = t("tester_sub");
    $("#lblTesterModel").textContent = t("lbl_test_model");
    $("#lblTesterProvider").textContent = t("lbl_test_provider");
    $("#lblTesterPrompt").textContent = t("lbl_test_prompt");
    $("#testAllThreeBtn").textContent = t("btn_test_all3");

    const modeLabels = { sync: t("mode_sync"), stream: t("mode_stream"), tools: t("mode_tools") };
    document.querySelectorAll("#testerModeSeg [data-tmode]").forEach((btn) => {
      btn.textContent = modeLabels[btn.dataset.tmode] || btn.dataset.tmode;
    });

    $("#usageTitle").textContent = t("usage_title");
    $("#usageSub").textContent = t("usage_sub");
    $("#connectTitle").textContent = t("connect_title");
    $("#connectSub").textContent = t("connect_sub");
    $("#copySnippetBtn").textContent = "📋 " + t("copy_snippet");
    $("#activityTitle").textContent = t("activity_title");
    $("#activitySub").textContent = t("activity_sub");
    $("#keysTitle").textContent = t("keys_title");
    $("#keysSub").textContent = t("keys_sub");
    $("#lblKeyName").textContent = t("key_name");
    $("#lblKeyModels").textContent = t("key_models");
    $("#lblKeyProviders").textContent = t("key_providers");
    $("#createKeyBtn").textContent = t("btn_create_key");
  }

  function setView(viewName, syncHash = true) {
    const allowed = ["providers", "routing", "tester", "usage", "connect"];
    if (!allowed.includes(viewName)) viewName = "providers";
    state.view = viewName;
    if (syncHash && window.location.hash !== "#" + viewName) {
      window.location.hash = "#" + viewName;
    }
    document.querySelectorAll("#mainNav [data-view]").forEach((btn) => {
      btn.classList.toggle("on", btn.dataset.view === viewName);
    });
    document.querySelectorAll("[data-view-panel]").forEach((sec) => {
      sec.hidden = sec.dataset.viewPanel !== viewName;
    });
    if (viewName === "connect") {
      loadConnectData();
    }
    if (viewName === "usage") {
      loadUsage();
    }
  }

  function formatUSD(v) {
    const n = Number(v || 0);
    if (n === 0) return "$0";
    if (n < 0.01) return "$" + n.toFixed(4);
    return "$" + n.toFixed(2);
  }

  async function loadUsage() {
    const range = $("#usageRange").value || "24h";
    const group = $("#usageGroup").value || "provider";
    const res = await apiJSON("/api/gateway/usage?from=" + encodeURIComponent(range) + "&group_by=" + encodeURIComponent(group));
    if (!res.ok || !res.data) {
      showToast((res.data && res.data.error) || "Usage load failed");
      return;
    }
    renderUsage(res.data.summary || {});
  }

  function renderUsage(sum) {
    const total = sum.total || {};
    const tile = (label, value) => el("div", { class: "usage-tile" }, el("span", { class: "sub" }, label), el("strong", {}, value));
    $("#usageTotals").replaceChildren(
      tile(t("usage_requests"), formatCompactNum(total.requests)),
      tile(t("usage_errors"), formatCompactNum(total.errors)),
      tile(t("usage_input"), formatCompactNum(total.input_tokens)),
      tile(t("usage_output"), formatCompactNum(total.output_tokens)),
      tile(t("usage_cache"), formatCompactNum(total.cache_read_tokens) + " / " + formatCompactNum(total.cache_write_tokens)),
      tile(t("usage_cost"), formatUSD(total.cost_usd))
    );
    const rows = sum.rows || [];
    const head = el("tr", {}, ...["", t("usage_requests"), t("usage_errors"), t("usage_input"), t("usage_output"), t("usage_cache"), t("usage_latency"), t("usage_cost")].map((h) => el("th", {}, h)));
    if (rows.length === 0) {
      $("#usageTable").replaceChildren(el("thead", {}, head), el("tbody", {}, el("tr", {}, el("td", { colspan: "8", class: "sub" }, t("usage_empty")))));
      return;
    }
    const body = rows.map((r) => el("tr", {},
      el("td", { class: "mono" }, r.key || "—"),
      el("td", {}, formatCompactNum(r.requests)),
      el("td", {}, formatCompactNum(r.errors)),
      el("td", {}, formatCompactNum(r.input_tokens)),
      el("td", {}, formatCompactNum(r.output_tokens)),
      el("td", {}, formatCompactNum(r.cache_read_tokens) + " / " + formatCompactNum(r.cache_write_tokens)),
      el("td", {}, (r.avg_latency_ms || 0) + " ms"),
      el("td", {}, formatUSD(r.cost_usd) + (r.unpriced_requests ? " (" + r.unpriced_requests + " " + t("usage_unpriced") + ")" : ""))
    ));
    $("#usageTable").replaceChildren(el("thead", {}, head), el("tbody", {}, ...body));
  }

  // Data loading
  async function syncAll() {
    const syncBtn = $("#syncAllBtn");
    if (syncBtn) syncBtn.disabled = true;
    try {
      const [provRes, modRes, matrixRes, statsRes, keyRes] = await Promise.all([
        apiJSON("/api/gateway/providers"),
        apiJSON("/api/gateway/models"),
        apiJSON("/api/gateway/models/matrix"),
        apiJSON("/api/gateway/stats"),
        state.connect.apiKey ? Promise.resolve({ ok: false }) : apiJSON("/api/config/apikey")
      ]);
      if (keyRes && keyRes.ok) {
        state.connect.apiKey = keyRes.data.api_key || "";
        state.connect.masked = !!keyRes.data.masked;
      }
      if (provRes.ok) {
        state.providers = provRes.data.providers || [];
        if (!state.selectedProviderId || !state.providers.some((p) => p.id === state.selectedProviderId)) {
          const firstAvail = state.providers.find((p) => p.available) || state.providers[0];
          state.selectedProviderId = firstAvail ? firstAvail.id : "";
        }
      }
      if (modRes.ok) {
        state.models = modRes.data.models || [];
      }
      if (matrixRes && matrixRes.ok) {
        state.matrix = matrixRes.data.models || [];
        state.matrixMultiCount = Number(matrixRes.data.multi_count || 0);
      }
      if (statsRes && statsRes.ok) {
        state.stats = statsRes.data || null;
      }
      seedDefaultTesterModel();
      populateProviderSelects();
      populateModelDatalist();
      renderGlobalStatus();
      renderAll();
    } finally {
      if (syncBtn) syncBtn.disabled = false;
    }
  }

  function seedDefaultTesterModel() {
    if (state.tester.model) return;
    const activeRoute = state.models.find((m) => !!m.active_provider && hasExplicitRoute(m));
    if (activeRoute) {
      state.tester.model = activeRoute.client_model;
    } else {
      const firstProv = state.providers.find((p) => p.available && Array.isArray(p.models) && p.models.length > 0);
      if (firstProv) {
        state.tester.model = firstProv.models[0];
        state.tester.provider = firstProv.id;
      }
    }
    const input = $("#testerModelInput");
    if (input && !input.value && state.tester.model) input.value = state.tester.model;
    const provSel = $("#testerProviderSelect");
    if (provSel && state.tester.provider) provSel.value = state.tester.provider;
  }

  function hasCoolingKey(p) {
    return Array.isArray(p && p.key_statuses) && p.key_statuses.some((ks) => !ks.healthy);
  }

  function renderGlobalStatus() {
    const total = state.providers.length;
    const ready = state.providers.filter((p) => p.available && !(p.cooldown && p.cooldown.cooling_down)).length;
    const cooling = state.providers.some((p) => (p.cooldown && p.cooldown.cooling_down) || hasCoolingKey(p));
    const dot = $("#globalStatusPill .dot");
    if (dot) {
      dot.className = "dot " + (ready > 0 ? (cooling ? "warn" : "ok") : "err");
    }
    const st = state.stats;
    if (st && st.requests > 0) {
      const tok = Number(st.input_tokens || 0) + Number(st.output_tokens || 0);
      $("#globalStatusText").textContent = t("ready_stats", {
        n: ready,
        total,
        req: formatCompactNum(st.requests),
        tok: formatCompactNum(tok)
      });
    } else {
      $("#globalStatusText").textContent = t("ready_count", { n: ready, total });
    }
    $("#clearAllCooldownsBtn").hidden = !cooling;
  }

  function populateProviderSelects() {
    const testerSel = $("#testerProviderSelect");
    if (testerSel) {
      const prev = state.tester.provider || testerSel.value;
      testerSel.replaceChildren(el("option", { value: "", text: t("auto_provider") }));
      state.providers.forEach((p) => {
        testerSel.appendChild(el("option", {
          value: p.id,
          text: p.id + " (" + (p.display_name || p.id) + (p.available ? "" : " · unavailable") + ")"
        }));
      });
      testerSel.value = prev || "";
    }

    const bindSel = $("#bindProvider");
    if (bindSel) {
      const prev = bindSel.value;
      bindSel.replaceChildren();
      state.providers.forEach((p) => {
        bindSel.appendChild(el("option", {
          value: p.id,
          text: p.id + (p.available ? "" : " (unavailable)")
        }));
      });
      ["google", "codex", "vertex"].forEach((leg) => {
        bindSel.appendChild(el("option", { value: leg, text: leg + " (legacy)" }));
      });
      if (prev) bindSel.value = prev;
      updateBindProviderModelsDatalist();
    }
  }

  function updateBindProviderModelsDatalist() {
    const dl = $("#bindProviderModels");
    const pid = ($("#bindProvider") || {}).value || "";
    if (!dl) return;
    dl.replaceChildren();
    const prov = state.providers.find((p) => p.id === pid);
    if (!prov || !Array.isArray(prov.models)) return;
    prov.models.forEach((mid) => dl.appendChild(el("option", { value: mid })));
  }

  function populateModelDatalist() {
    const dl = $("#allModelOptions");
    if (!dl) return;
    const seen = new Set();
    dl.replaceChildren();
    (state.matrix || []).forEach((m) => {
      const id = m.primary_model || m.canonical_id;
      if (id && !seen.has(id)) {
        seen.add(id);
        dl.appendChild(el("option", { value: id }));
      }
    });
    state.models.forEach((m) => {
      if (!seen.has(m.client_model)) {
        seen.add(m.client_model);
        dl.appendChild(el("option", { value: m.client_model }));
      }
    });
    state.providers.forEach((p) => {
      (p.models || []).forEach((mid) => {
        if (!seen.has(mid)) {
          seen.add(mid);
          dl.appendChild(el("option", { value: mid }));
        }
      });
    });
  }

  function renderAll() {
    renderProvidersRail();
    renderProviderDetail();
    renderRoutingView();
    renderTesterResults();
    renderConnectMetaAndSnippet();
    renderClientKeys();
  }

  // View 1: Providers & Model Catalog
  function providerStatusState(p) {
    if (!p.enabled) return { cls: "", label: "Disabled", tag: "tag" };
    if (p.cooldown && p.cooldown.cooling_down) {
      return { cls: "warn", label: "CD " + formatDurationSec(p.cooldown.remaining_seconds), tag: "tag warn" };
    }
    if (p.available) return { cls: "ok", label: p.no_key ? "Ready (Local)" : "Ready", tag: "tag ok" };
    return { cls: "err", label: "No Key", tag: "tag err" };
  }

  function renderProvidersRail() {
    const list = $("#providersList");
    if (!list) return;
    $("#providersRailCount").textContent = String(state.providers.length);
    list.replaceChildren();

    state.providers.forEach((p) => {
      const st = providerStatusState(p);
      const countText = String(p.model_count || 0) + (p.free_only ? " free" : " models");
      const item = el("button", {
        type: "button",
        class: "rail-item" + (p.id === state.selectedProviderId ? " on" : ""),
        onClick: () => {
          state.selectedProviderId = p.id;
          renderProvidersRail();
          renderProviderDetail();
        }
      },
        el("div", null,
          el("div", { class: "rail-item-title" },
            el("i", { class: "dot " + st.cls }),
            el("span", { text: p.display_name || p.id })
          ),
          el("div", { class: "rail-item-sub", text: p.id + " · " + (p.prefer || "-") })
        ),
        el("div", { class: "rail-item-right" },
          el("span", { class: st.tag, text: st.label }),
          el("span", { class: "muted-count", text: countText })
        )
      );
      list.appendChild(item);
    });
  }

  function renderProviderStatsStrip(p) {
    const ps = p.stats;
    if (!ps || !ps.requests) return null;
    const pct = ps.requests > 0 ? Math.round((Number(ps.success || 0) / Number(ps.requests)) * 100) : 0;
    const avgMs = Math.round(Number(ps.avg_latency_ms || 0));
    const ewmaMs = Math.round(Number(ps.ewma_latency_ms || 0));
    const statusTag = ps.last_status
      ? el("span", { class: "tag " + (ps.last_status >= 200 && ps.last_status < 300 ? "ok" : "err"), text: "HTTP " + ps.last_status })
      : null;

    return el("div", { class: "stats-strip" },
      el("div", { class: "stats-strip-head" },
        el("div", { style: "display:flex;align-items:center;gap:8px;flex-wrap:wrap" },
          el("span", { class: "stats-strip-title", text: t("stats_title") }),
          statusTag
        ),
        el("button", {
          type: "button",
          class: "btn sm subtle",
          text: t("reset_stats"),
          onClick: resetGatewayStats
        })
      ),
      el("div", { class: "stats-grid" },
        el("div", { class: "stat-tile" },
          el("div", { class: "stat-k", text: t("stat_requests") }),
          el("div", { class: "stat-v", text: formatCompactNum(ps.requests) + " (" + pct + "%)" }),
          el("div", { class: "stat-sub", text: Number(ps.errors || 0) + " err" })
        ),
        el("div", { class: "stat-tile" },
          el("div", { class: "stat-k", text: t("stat_latency") }),
          el("div", { class: "stat-v", text: avgMs + " / " + ewmaMs + " ms" }),
          ps.last_error ? el("div", { class: "stat-sub", text: String(ps.last_error).slice(0, 48) }) : null
        ),
        el("div", { class: "stat-tile" },
          el("div", { class: "stat-k", text: t("stat_tokens") }),
          el("div", { class: "stat-v", text: formatCompactNum(ps.input_tokens) + " / " + formatCompactNum(ps.output_tokens) })
        ),
        el("div", { class: "stat-tile" },
          el("div", { class: "stat-k", text: t("stat_cache_reason") }),
          el("div", { class: "stat-v", text: formatCompactNum(ps.cached_tokens) + " / " + formatCompactNum(ps.reasoning_tokens) })
        )
      )
    );
  }

  function renderProviderDetail() {
    const pane = $("#providerDetailPane");
    if (!pane) return;
    const p = state.providers.find((item) => item.id === state.selectedProviderId);
    if (!p) {
      pane.replaceChildren(el("div", { class: "card empty-state", text: "No gateway backend selected." }));
      return;
    }

    const st = providerStatusState(p);
    const cd = p.cooldown && p.cooldown.cooling_down ? p.cooldown : null;
    const epEntries = Object.entries(p.endpoints || {}).filter(([, v]) => !!v);
    const epProtos = epEntries.map(([k]) => EP_KEY_TO_PROTO[k]).filter(Boolean);
    const epNodes = epEntries.length
      ? el("div", { class: "ep-list" }, epEntries.map(([k, v]) =>
          el("div", { class: "ep-row" },
            el("span", { class: "tag", text: k }),
            el("button", { type: "button", class: "ep-url", title: "Click to copy URL", text: v, onClick: () => copyText(v) })
          )
        ))
      : el("span", { text: "-" });
    const keysStr = p.no_key
      ? "No key required"
      : ((p.key_count || 0) + " key(s)" + (p.key_count > 1 ? " · " + (p.key_strategy || "sticky") : ""));

    let keyPillsNode = null;
    if (!p.no_key && Array.isArray(p.key_statuses) && p.key_statuses.length > 0) {
      keyPillsNode = el("div", { class: "key-pills" }, p.key_statuses.map((ks) => {
        const cls = "key-pill" + (!ks.healthy ? " cooling" : (ks.active ? " active" : ""));
        const infoParts = [];
        if (ks.requests > 0) infoParts.push(ks.requests + "r");
        if (!ks.healthy && ks.cooldown_seconds) infoParts.push("CD " + formatDurationSec(ks.cooldown_seconds));
        if (ks.last_status && ks.last_status >= 400) infoParts.push("HTTP " + ks.last_status);
        return el("span", { class: cls, title: ks.last_error || ks.masked },
          el("i", { class: "dot " + (ks.healthy ? "ok" : "warn") }),
          el("span", { text: ks.masked }),
          infoParts.length ? el("span", { class: "muted-count", text: infoParts.join("·") }) : null,
          ks.active && p.key_count > 1 && ks.healthy ? el("span", { class: "muted-count", text: "★" }) : null,
          el("button", {
            type: "button",
            class: "key-rm-btn",
            title: "Remove key " + ks.masked,
            text: "×",
            onClick: () => {
              if (!window.confirm(t("remove_key_confirm", { masked: ks.masked }))) return;
              patchProvider(p.id, { remove_api_key: ks.masked, refresh: true });
            }
          })
        );
      }));
    }

    let inlineKeyNode = null;
    if (!p.no_key) {
      const keyInput = el("input", {
        type: "password",
        class: "inline-key-input",
        placeholder: t("add_key_placeholder"),
        autocomplete: "off"
      });
      const addKeyBtn = el("button", {
        type: "submit",
        class: "btn sm",
        text: t("btn_add_key")
      });
      inlineKeyNode = el("form", {
        class: "inline-key-form",
        onSubmit: async (ev) => {
          ev.preventDefault();
          const val = keyInput.value.trim();
          if (!val) return;
          addKeyBtn.disabled = true;
          try {
            if (await patchProvider(p.id, { add_api_key: val, refresh: true })) {
              keyInput.value = "";
            }
          } finally {
            addKeyBtn.disabled = false;
          }
        }
      }, keyInput, addKeyBtn);
    }

    const refreshBtn = el("button", {
      type: "button",
      class: "btn",
      text: "↻ " + t("refresh_models"),
      onClick: async (e) => {
        const b = e.currentTarget;
        b.disabled = true;
        try {
          const r = await apiJSON("/api/gateway/providers/" + encodeURIComponent(p.id) + "/refresh", { method: "POST" });
          if (r.ok) {
            showToast(t("refreshed", { id: p.id, n: r.data.model_count || 0 }));
          } else {
            showToast((r.data && r.data.error) || "Refresh failed");
          }
          await syncAll();
        } finally {
          b.disabled = false;
        }
      }
    });

    const headerActions = [refreshBtn];
    if (cd || hasCoolingKey(p)) {
      headerActions.push(el("button", {
        type: "button",
        class: "btn warn",
        text: "🧹 " + t("clear_cd"),
        onClick: () => clearCooldown(p.id)
      }));
    }

    const switches = [
      renderTogglePill(t("sw_enabled"), !!p.enabled, (ev) => {
        ev.currentTarget.disabled = true;
        patchProvider(p.id, { enabled: !p.enabled });
      }),
      (p.id === "openrouter" || p.free_only) ? renderTogglePill(t("sw_free_only"), !!p.free_only, (ev) => {
        ev.currentTarget.disabled = true;
        patchProvider(p.id, { free_only: !p.free_only, refresh: true });
      }) : null,
      renderTogglePill(t("sw_include_non_chat"), !!p.include_non_chat, (ev) => {
        ev.currentTarget.disabled = true;
        patchProvider(p.id, { include_non_chat: !p.include_non_chat, refresh: true });
      })
    ].filter(Boolean);

    let preferCtl = null;
    if (epProtos.length > 1) {
      const sel = el("select", { class: "select-sm", "aria-label": t("lbl_prefer_proto") },
        epProtos.map((proto) => el("option", { value: proto, text: proto }))
      );
      sel.value = epProtos.includes(p.prefer) ? p.prefer : epProtos[0];
      sel.addEventListener("change", () => {
        sel.disabled = true;
        patchProvider(p.id, { prefer: sel.value });
      });
      preferCtl = el("label", { class: "prefer-ctl" },
        el("span", { text: t("lbl_prefer_proto") }),
        sel
      );
    }

    let keyStratCtl = null;
    if (!p.no_key && (p.key_count || 0) > 1) {
      const ksel = el("select", { class: "select-sm", "aria-label": t("lbl_key_strategy") }, [
        el("option", { value: "round-robin", text: "round-robin" }),
        el("option", { value: "sticky", text: "sticky" })
      ]);
      ksel.value = p.key_strategy === "round-robin" ? "round-robin" : "sticky";
      ksel.addEventListener("change", () => {
        ksel.disabled = true;
        patchProvider(p.id, { key_strategy: ksel.value });
      });
      keyStratCtl = el("label", { class: "prefer-ctl" },
        el("span", { text: t("lbl_key_strategy") }),
        ksel
      );
    }

    const summaryCard = el("div", { class: "card detail-card" },
      el("div", { class: "panel-head" },
        el("div", null,
          el("div", { style: "display:flex;align-items:center;gap:8px;flex-wrap:wrap" },
            el("h2", { style: "margin:0;font-size:16px", text: p.display_name || p.id }),
            el("span", { class: "tag mono", text: p.id }),
            el("span", { class: st.tag, text: st.label }),
            p.free_only ? el("span", { class: "tag ok", text: "Free Only" }) : null,
            p.include_non_chat ? el("span", { class: "tag purple", text: "+Non-Chat" }) : null
          )
        ),
        el("div", { class: "top-actions" }, headerActions)
      ),
      el("div", { class: "kv-grid" },
        el("div", { class: "kv-box" },
          el("div", { class: "kv-label", text: t("protocols") }),
          el("div", { class: "kv-val", text: (p.native || []).join(", ") || p.prefer || "-" })
        ),
        el("div", { class: "kv-box" },
          el("div", { class: "kv-label", text: t("keys") }),
          el("div", { class: "kv-val", text: keysStr }),
          keyPillsNode,
          inlineKeyNode
        ),
        el("div", { class: "kv-box" },
          el("div", { class: "kv-label", text: t("endpoints") }),
          epNodes
        )
      ),
      el("div", { class: "runtime-controls" },
        el("div", { class: "runtime-switches" }, switches),
        el("div", { class: "runtime-switches" }, [keyStratCtl, preferCtl].filter(Boolean))
      ),
      el("div", { id: "providerStatsMount" }, renderProviderStatsStrip(p)),
      p.free_only ? el("div", { class: "notice-banner", text: t("free_only_banner") }) : null,
      cd && cd.reason ? el("div", { class: "notice-banner warn", text: "Cooldown (" + (cd.failures || 1) + "x): " + cd.reason }) : null,
      p.catalog_error ? el("div", { class: "notice-banner err", text: "Catalog error: " + p.catalog_error }) : null
    );

    const searchInput = el("input", {
      type: "search",
      class: "models-search",
      placeholder: t("search_models"),
      value: state.providerQueries[p.id] || ""
    });
    searchInput.addEventListener("input", () => {
      state.providerQueries[p.id] = searchInput.value;
      renderProviderModelRows(p);
    });
    searchInput.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape" && searchInput.value) {
        ev.stopPropagation();
        searchInput.value = "";
        state.providerQueries[p.id] = "";
        renderProviderModelRows(p);
      }
    });

    const modelsCard = el("div", { class: "card" },
      el("div", { class: "models-head" },
        el("div", null,
          el("strong", { text: p.display_name || p.id }),
          " ",
          el("span", { class: "muted-count", id: "providerModelsCountLabel" })
        ),
        searchInput
      ),
      el("div", { class: "model-rows", id: "providerModelsRows" })
    );

    pane.replaceChildren(summaryCard, modelsCard);
    renderProviderModelRows(p);
  }

  function renderProviderModelRows(p) {
    const container = $("#providerModelsRows");
    const countLabel = $("#providerModelsCountLabel");
    if (!container) return;

    const allModels = Array.isArray(p.models) ? p.models : [];
    const q = (state.providerQueries[p.id] || "").trim().toLowerCase();
    const words = q ? q.split(/\s+/).filter(Boolean) : [];
    const filtered = words.length
      ? allModels.filter((mid) => words.every((w) => mid.toLowerCase().includes(w)))
      : allModels;

    if (countLabel) {
      countLabel.textContent = "(" + t("showing_models", { shown: filtered.length, total: allModels.length }) + ")";
    }

    if (allModels.length === 0) {
      container.replaceChildren(el("div", { class: "empty-state", text: t("no_models") }));
      return;
    }
    if (filtered.length === 0) {
      container.replaceChildren(el("div", { class: "empty-state", text: t("no_matches", { q: q }) }));
      return;
    }

    const frag = document.createDocumentFragment();
    filtered.forEach((modelId) => {
      const probeKey = p.id + "|" + modelId;
      const probe = state.modelProbes[probeKey];
      const isExpanded = !!state.probeExpanded[probeKey];

      let dotCls = "";
      let statusTag = null;
      if (probe) {
        if (probe.busy) {
          dotCls = "busy";
          statusTag = el("span", { class: "tag warn", text: t("btn_testing") });
        } else if (probe.ok) {
          dotCls = "ok";
          statusTag = el("span", { class: "tag ok mono", text: "HTTP " + probe.status_code + " · " + probe.duration_ms + "ms" });
        } else {
          dotCls = "err";
          statusTag = el("span", { class: "tag err mono", text: "HTTP " + (probe.status_code || 0) + " ERR" });
        }
      }

      const detailBox = probe && isExpanded ? buildInlineProbeDetail(p.id, modelId, probe, probeKey) : null;

      const row = el("div", { class: "model-row" },
        el("div", { class: "model-row-main" },
          el("div", { class: "model-row-id" },
            el("i", { class: "dot " + dotCls }),
            el("button", {
              type: "button",
              class: "model-id-btn",
              title: "Click to copy model ID",
              text: modelId,
              onClick: () => copyText(modelId)
            }),
            statusTag
          ),
          el("div", { class: "model-row-actions" },
            probe && !probe.busy ? el("button", {
              type: "button",
              class: "btn sm subtle",
              text: isExpanded ? t("btn_hide") : "Details",
              onClick: () => {
                state.probeExpanded[probeKey] = !isExpanded;
                renderProviderModelRows(p);
              }
            }) : null,
            el("button", {
              type: "button",
              class: "btn sm",
              disabled: !!(probe && probe.busy),
              text: (probe && probe.busy) ? t("btn_testing") : t("btn_test"),
              onClick: () => probeProviderModel(p, modelId)
            }),
            el("button", {
              type: "button",
              class: "btn sm primary",
              text: t("btn_3apis"),
              onClick: () => jumpToTester(modelId, p.id, true)
            }),
            el("button", {
              type: "button",
              class: "btn sm subtle",
              text: t("btn_bind"),
              onClick: () => jumpToRoutingEditor(modelId, p.id, modelId)
            })
          )
        ),
        detailBox
      );
      frag.appendChild(row);
    });
    container.replaceChildren(frag);
  }

  function buildInlineProbeDetail(providerId, modelId, probe, probeKey) {
    if (probe.busy) {
      return el("div", { class: "model-probe-detail", text: t("btn_testing") });
    }
    const routeTxt = probe.route ? ("route: " + probe.route + (probe.route_source ? " (" + probe.route_source + ")" : "")) : "";
    const msg = probe.ok ? (probe.text || probe.summary || "OK") : (probe.error || probe.summary || probe.snippet || "Failed");
    return el("div", { class: "model-probe-detail" + (probe.ok ? "" : " err") },
      el("div", { class: "model-probe-meta" },
        el("span", { class: "mono", text: (probe.provider || providerId) + " → " + (probe.upstream_model || modelId) }),
        routeTxt ? el("span", { class: "tag " + (probe.route === "passthrough" ? "ok" : "purple"), text: routeTxt }) : null,
        el("span", { class: "mono", text: String(probe.duration_ms || 0) + " ms" }),
        el("button", {
          type: "button",
          class: "btn sm primary",
          style: "margin-left:auto",
          text: t("btn_3apis"),
          onClick: () => jumpToTester(modelId, providerId, true)
        })
      ),
      el("div", { style: "white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word", text: msg })
    );
  }

  async function probeProviderModel(p, modelId) {
    const key = p.id + "|" + modelId;
    state.modelProbes[key] = { busy: true };
    state.probeExpanded[key] = true;
    renderProviderModelRows(p);

    const { data } = await apiJSON("/api/gateway/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        protocol: "openai-chat",
        model: modelId,
        provider: p.id,
        prompt: DEFAULT_PROMPT_TEXT
      })
    });
    state.modelProbes[key] = Object.assign({ busy: false }, data);
    state.probeExpanded[key] = true;
    if (state.selectedProviderId === p.id) {
      renderProviderModelRows(p);
    }
    refreshStatsOnly();
  }

  // View 2: Routing, Model-to-Backends Matrix & Switching
  function hasExplicitRoute(m) {
    return Array.isArray(m.candidates) && m.candidates.some((c) => c.source === "db" && !c.legacy);
  }

  function isLegacyOnlyModel(m) {
    return Array.isArray(m.candidates) && m.candidates.length > 0 && m.candidates.every((c) => !!c.legacy);
  }

  function renderCandidateRow(clientModel, c, idx, isActiveProvider) {
    const isActive = c.provider === isActiveProvider;
    const cd = c.cooldown && c.cooldown.cooling_down ? c.cooldown : null;
    let dotCls = c.available ? "ok" : "err";
    if (cd) dotCls = "warn";
    const nativeStr = Array.isArray(c.native) && c.native.length > 0 ? c.native.join(" / ") : "";

    return el("div", { class: "cand-row" + (isActive ? " active" : "") },
      el("div", { class: "cand-left" },
        el("span", { class: "tag mono", text: "#" + (idx + 1) }),
        el("i", { class: "dot " + dotCls }),
        el("strong", { text: c.provider }),
        el("span", { class: "mono", text: "→ " + (c.target_model || clientModel) }),
        el("span", { class: "tag", text: c.source + (c.legacy ? (c.available ? "·oauth" : "·no-auth") : (c.match_kind === "canonical" ? "·canon" : "")) }),
        nativeStr ? el("span", { class: "tag", text: nativeStr }) : null,
        c.free_only ? el("span", { class: "tag ok", text: "free" }) : null,
        c.latency_ms > 0 ? el("span", { class: "tag", text: c.latency_ms + " ms" }) : null,
        cd ? el("span", { class: "tag warn", text: "CD " + formatDurationSec(cd.remaining_seconds) }) : null
      ),
      el("div", { class: "cand-right" },
        el("button", {
          type: "button",
          class: "btn sm subtle",
          text: t("btn_test"),
          onClick: () => jumpToTester(c.target_model || clientModel, c.provider, true)
        }),
        isActive
          ? el("span", { class: "tag accent", text: "★ " + t("active_label") })
          : el("button", {
              type: "button",
              class: "btn sm",
              text: t("promote_btn"),
              onClick: () => selectPrimaryBackend(clientModel, c.provider, c.target_model || clientModel)
            }),
        c.id ? el("button", {
          type: "button",
          class: "btn sm danger",
          text: t("remove_btn"),
          onClick: () => deleteRouteCandidate(c.id)
        }) : null
      )
    );
  }

  function renderMatrixCards(listEl, q) {
    const items = (state.matrix || []).filter((entry) => {
      if (state.matrixMultiOnly && !entry.multi_backend) return false;
      if (!q) return true;
      if ((entry.canonical_id || "").toLowerCase().includes(q)) return true;
      if ((entry.primary_model || "").toLowerCase().includes(q)) return true;
      if ((entry.raw_ids || []).some((r) => r.toLowerCase().includes(q))) return true;
      return (entry.backends || []).some((b) =>
        (b.provider || "").toLowerCase().includes(q) ||
        (b.target_model || "").toLowerCase().includes(q)
      );
    });

    if (items.length === 0) {
      listEl.replaceChildren(el("div", { class: "empty-state", text: t("no_routes_empty") }));
      return;
    }

    const limit = state.routingShowAll ? items.length : 80;
    const shown = items.slice(0, limit);
    const frag = document.createDocumentFragment();

    shown.forEach((m) => {
      const clientModel = m.primary_model || m.canonical_id;
      const hasAllDB = (m.backends || []).every((b) => b.source === "db");
      const rawNote = (m.raw_ids || []).filter((id) => id !== clientModel).slice(0, 4).join(", ");
      const candNodes = (m.backends || []).map((b, idx) => renderCandidateRow(clientModel, b, idx, m.active_provider));

      frag.appendChild(el("div", { class: "route-card" },
        el("div", { class: "route-card-head" },
          el("div", { class: "route-title" },
            el("code", { style: "font-size:13px;font-weight:600;cursor:pointer", text: clientModel, title: "Fill editor", onClick: () => jumpToRoutingEditor(clientModel, m.active_provider || "", clientModel) }),
            el("span", { class: "tag " + (m.multi_backend ? "ok" : ""), text: m.backend_count + " Backend" + (m.backend_count > 1 ? "s" : "") }),
            m.active_provider ? el("span", { class: "tag accent", text: t("active_label") + ": " + m.active_provider }) : null,
            rawNote ? el("span", { class: "muted-count mono", text: "aliases: " + rawNote }) : null
          ),
          el("div", { class: "top-actions" },
            (m.backend_count >= 2 && !hasAllDB) ? el("button", {
              type: "button",
              class: "btn sm",
              text: t("sync_failover_btn"),
              onClick: () => syncModelFailover(clientModel)
            }) : null,
            el("button", {
              type: "button",
              class: "btn sm primary",
              text: "🧪 " + t("btn_3apis"),
              onClick: () => jumpToTester(clientModel, "", true)
            })
          )
        ),
        el("div", { class: "route-candidates" }, candNodes)
      ));
    });

    if (items.length > shown.length) {
      frag.appendChild(el("div", { class: "empty-state", style: "padding:14px" },
        el("span", { text: (state.lang === "zh" ? "当前已显示前 " : "Showing first ") + shown.length + " / " + items.length + " · " }),
        el("button", {
          type: "button",
          class: "btn sm",
          text: (state.lang === "zh" ? "展开全部 (" : "Show All (") + items.length + ")",
          onClick: () => {
            state.routingShowAll = true;
            renderRoutingView();
          }
        })
      ));
    }
    listEl.replaceChildren(frag);
  }

  function renderRoutingView() {
    const listEl = $("#routingModelsList");
    if (!listEl) return;

    let explicitCount = 0;
    let catalogCount = 0;
    let legacyCount = 0;
    state.models.forEach((m) => {
      if (hasExplicitRoute(m)) explicitCount++;
      if (Array.isArray(m.candidates) && m.candidates.some((c) => c.source === "catalog")) catalogCount++;
      if (isLegacyOnlyModel(m)) legacyCount++;
    });

    const matrixLabel = t("tab_matrix") + " (" + state.matrixMultiCount + "/" + state.matrix.length + ")";
    const tabLabels = {
      routes: t("tab_routes") + " (" + explicitCount + ")",
      matrix: matrixLabel,
      catalog: t("tab_catalog") + " (" + catalogCount + ")",
      legacy: t("tab_legacy") + " (" + legacyCount + ")",
      all: t("tab_all") + " (" + state.models.length + ")"
    };
    document.querySelectorAll("#routingFilterTabs [data-rtab]").forEach((btn) => {
      const k = btn.dataset.rtab;
      btn.textContent = tabLabels[k] || k;
      btn.classList.toggle("on", k === state.routingTab);
    });

    const multiBtn = $("#matrixMultiOnlyBtn");
    if (multiBtn) {
      multiBtn.hidden = state.routingTab !== "matrix";
      multiBtn.classList.toggle("on", !!state.matrixMultiOnly);
    }

    const q = state.routingQuery.trim().toLowerCase();
    if (state.routingTab === "matrix") {
      renderMatrixCards(listEl, q);
      return;
    }

    const filtered = state.models.filter((m) => {
      if (state.routingTab === "routes" && !hasExplicitRoute(m)) return false;
      if (state.routingTab === "catalog" && !(m.candidates || []).some((c) => c.source === "catalog")) return false;
      if (state.routingTab === "legacy" && !isLegacyOnlyModel(m)) return false;
      if (!q) return true;
      if ((m.client_model || "").toLowerCase().includes(q)) return true;
      if ((m.canonical_id || "").toLowerCase().includes(q)) return true;
      if ((m.active_provider || "").toLowerCase().includes(q)) return true;
      return (m.candidates || []).some((c) =>
        (c.provider || "").toLowerCase().includes(q) ||
        (c.target_model || "").toLowerCase().includes(q)
      );
    });

    if (filtered.length === 0) {
      const box = el("div", { class: "empty-state" }, el("div", { text: t("no_routes_empty") }));
      if (state.routingTab === "routes" && state.matrix.length > 0 && !q) {
        box.appendChild(el("button", {
          type: "button",
          class: "btn primary",
          style: "margin-top:10px",
          text: matrixLabel,
          onClick: () => {
            state.routingTab = "matrix";
            renderRoutingView();
          }
        }));
      }
      listEl.replaceChildren(box);
      return;
    }

    const limit = state.routingShowAll ? filtered.length : 80;
    const shown = filtered.slice(0, limit);

    const frag = document.createDocumentFragment();
    shown.forEach((m) => {
      const strategy = (m.strategy || "failover").toLowerCase();
      const isPinned = strategy === "pinned";
      const explicit = hasExplicitRoute(m);

      const candNodes = (m.candidates || []).map((c, idx) => renderCandidateRow(m.client_model, c, idx, m.active_provider));
      const firstReadyCand = (m.candidates || []).find((c) => c.available) || (m.candidates || [])[0];
      const card = el("div", { class: "route-card" },
        el("div", { class: "route-card-head" },
          el("div", { class: "route-title" },
            el("code", { style: "font-size:13px;font-weight:600;cursor:pointer", text: m.client_model, title: "Fill editor", onClick: () => jumpToRoutingEditor(m.client_model, m.active_provider || "", m.client_model) }),
            m.active_provider
              ? el("span", { class: "tag ok", text: t("active_label") + ": " + m.active_provider })
              : el("span", { class: "tag err", text: "No active backend" }),
            m.multi_backend ? el("span", { class: "tag purple", text: (m.candidates || []).length + " backends" }) : null,
            !explicit ? el("span", { class: "tag", text: isLegacyOnlyModel(m) ? "Legacy" : "Catalog" }) : null
          ),
          el("div", { class: "top-actions" },
            !explicit && firstReadyCand ? el("button", {
              type: "button",
              class: "btn sm",
              text: state.lang === "zh" ? "📌 固定路由" : "📌 Pin Route",
              onClick: () => selectPrimaryBackend(m.client_model, firstReadyCand.provider, firstReadyCand.target_model || m.client_model)
            }) : null,
            el("button", {
              type: "button",
              class: "btn sm primary",
              text: "🧪 " + t("btn_3apis"),
              onClick: () => jumpToTester(m.client_model, "", true)
            }),
            el("div", { class: "seg" },
              el("button", {
                type: "button",
                class: "seg-btn" + (!isPinned ? " on" : ""),
                text: "Failover",
                onClick: () => updateModelPolicy(m.client_model, "failover")
              }),
              el("button", {
                type: "button",
                class: "seg-btn" + (isPinned ? " on" : ""),
                text: "Pinned",
                onClick: () => updateModelPolicy(m.client_model, "pinned")
              })
            )
          )
        ),
        el("div", { class: "route-candidates" }, candNodes)
      );
      frag.appendChild(card);
    });
    if (filtered.length > shown.length) {
      frag.appendChild(el("div", { class: "empty-state", style: "padding:14px" },
        el("span", { text: (state.lang === "zh" ? "当前已显示前 " : "Showing first ") + shown.length + " / " + filtered.length + (state.lang === "zh" ? " 条，可输入搜索关键词或 " : " items · ") }),
        el("button", {
          type: "button",
          class: "btn sm",
          text: (state.lang === "zh" ? "展开全部 (" : "Show All (") + filtered.length + ")",
          onClick: () => {
            state.routingShowAll = true;
            renderRoutingView();
          }
        })
      ));
    }
    listEl.replaceChildren(frag);
  }

  function jumpToRoutingEditor(clientModel, providerId, targetModel) {
    setView("routing");
    if (clientModel) $("#bindClientModel").value = clientModel;
    if (providerId) {
      $("#bindProvider").value = providerId;
      updateBindProviderModelsDatalist();
    }
    if (targetModel) $("#bindTargetModel").value = targetModel;
  }

  async function syncModelFailover(clientModel) {
    const r = await apiJSON("/api/gateway/models/" + encodeURIComponent(clientModel) + "/failover", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ client_model: clientModel, strategy: "failover" })
    });
    if (!r.ok) {
      showToast((r.data && r.data.error) || "Failover sync failed");
      return;
    }
    showToast(t("synced_failover", { model: clientModel }));
    state.routingTab = "routes";
    await syncAll();
  }

  async function selectPrimaryBackend(clientModel, provider, targetModel, strategy) {
    if (strategy) {
      await apiJSON("/api/gateway/policy", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ client_model: clientModel, strategy })
      });
    }
    const r = await apiJSON("/api/gateway/select", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        client_model: clientModel,
        provider: provider,
        target_model: targetModel || clientModel,
        clear_cooldown: true
      })
    });
    if (!r.ok) {
      showToast((r.data && r.data.error) || "Switch failed");
      return;
    }
    showToast(t("switched", { model: clientModel, provider }));
    state.routingTab = "routes";
    await syncAll();
  }

  async function addBackupRoute(clientModel, provider, targetModel) {
    const r = await apiJSON("/api/model-routes", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        client_model: clientModel,
        target_provider: provider,
        target_model: targetModel || clientModel,
        priority: 50
      })
    });
    if (!r.ok) {
      showToast((r.data && r.data.error) || "Add backup route failed");
      return;
    }
    showToast(t("added_route", { model: clientModel, provider }));
    state.routingTab = "routes";
    await syncAll();
  }

  async function deleteRouteCandidate(id) {
    const r = await apiJSON("/api/model-routes/" + encodeURIComponent(id), { method: "DELETE" });
    if (!r.ok) {
      showToast((r.data && r.data.error) || "Delete failed");
      return;
    }
    showToast(t("removed_route"));
    await syncAll();
  }

  async function updateModelPolicy(clientModel, strategy) {
    const r = await apiJSON("/api/gateway/policy", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ client_model: clientModel, strategy })
    });
    if (!r.ok) {
      showToast((r.data && r.data.error) || "Policy update failed");
      return;
    }
    await syncAll();
  }

  async function clearCooldown(providerId) {
    const payload = providerId ? { provider: providerId } : {};
    const r = await apiJSON("/api/gateway/cooldown/clear", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    if (r.ok) {
      showToast(t("cleared_cd"));
      await syncAll();
    }
  }

  // View 3: Protocol Tester
  function jumpToTester(modelId, providerId, runImmediately) {
    setView("tester");
    state.tester.model = modelId || "";
    state.tester.provider = providerId || "";
    if ($("#testerModelInput")) $("#testerModelInput").value = state.tester.model;
    if ($("#testerProviderSelect")) $("#testerProviderSelect").value = state.tester.provider;
    if (runImmediately && state.tester.model) {
      runThreeProtocols();
    }
  }

  function formatUsageBrief(usage) {
    if (!usage || typeof usage !== "object") return "";
    const inTok = usage.prompt_tokens ?? usage.input_tokens ?? usage.promptTokenCount;
    const outTok = usage.completion_tokens ?? usage.output_tokens ?? usage.candidatesTokenCount;
    const totTok = usage.total_tokens ?? usage.totalTokenCount;
    const cached = usage.prompt_tokens_details?.cached_tokens ?? usage.input_tokens_details?.cached_tokens ?? usage.cache_read_input_tokens;
    const reason = usage.completion_tokens_details?.reasoning_tokens ?? usage.output_tokens_details?.reasoning_tokens;
    const parts = [];
    if (inTok !== undefined) parts.push("in " + inTok);
    if (outTok !== undefined) parts.push("out " + outTok);
    if (cached) parts.push("cached " + cached);
    if (reason) parts.push("reason " + reason);
    if (totTok !== undefined && (inTok === undefined || outTok === undefined || totTok !== Number(inTok) + Number(outTok))) {
      parts.push("total " + totTok);
    }
    return parts.join(" / ");
  }

  function buildTesterCurl(def, st) {
    const origin = window.location.origin;
    const key = state.connect.apiKey || "YOUR_NEXUS_API_KEY";
    const path = (st && st.path) || def.path.replace(/^POST\s+/, "").replace("{model}", state.tester.model || "gpt-4o-mini");
    const hdrs = ['-H "Content-Type: application/json"'];
    if (def.id === "anthropic") {
      hdrs.push('-H "x-api-key: ' + key + '"', '-H "anthropic-version: 2023-06-01"');
    } else if (def.id === "gemini") {
      hdrs.push('-H "x-goog-api-key: ' + key + '"');
    } else {
      hdrs.push('-H "Authorization: Bearer ' + key + '"');
    }
    const reqProv = (st && st.requested_provider) || state.tester.provider;
    if (reqProv) {
      hdrs.push('-H "X-Nexus-Provider: ' + reqProv + '"');
    }
    const payload = (st && st.request_payload) || JSON.stringify({ model: state.tester.model || "gpt-4o-mini" });
    return "curl -s " + JSON.stringify(origin + path) + " \\\n  " + hdrs.join(" \\\n  ") + " \\\n  -d '" + payload.replace(/'/g, "'\\''") + "'";
  }

  function renderTesterSummaryBar(visibleCards) {
    const bar = $("#testerSummaryBar");
    if (!bar) return;
    const entries = visibleCards.map((c) => state.tester.results[c.id]).filter(Boolean);
    if (entries.length === 0) {
      bar.hidden = true;
      return;
    }
    bar.hidden = false;
    const busyCount = entries.filter((e) => e.busy).length;
    const okCount = entries.filter((e) => !e.busy && e.ok).length;
    const errCount = entries.filter((e) => !e.busy && !e.ok).length;
    const maxMs = entries.reduce((acc, e) => Math.max(acc, Number(e.duration_ms || 0)), 0);

    let badgeCls = "tag";
    let label = "";
    if (busyCount > 0) {
      badgeCls = "tag warn";
      label = (state.lang === "zh" ? "正在并发测试 (" : "Probing (") + busyCount + " remaining)…";
    } else {
      badgeCls = errCount === 0 ? "tag ok" : (okCount > 0 ? "tag warn" : "tag err");
      label = okCount + "/" + entries.length + (state.lang === "zh" ? " 个协议通过" : " passed") + " · max " + maxMs + " ms";
    }
    const targetTxt = (state.tester.provider || t("auto_provider")) + " · " + (state.tester.model || "-");
    bar.replaceChildren(
      el("div", { style: "display:flex;align-items:center;gap:8px;flex-wrap:wrap" },
        el("span", { class: badgeCls, text: label }),
        el("span", { class: "mono", style: "font-size:12px", text: targetTxt })
      ),
      el("span", { class: "tag", text: state.tester.mode.toUpperCase() })
    );
  }

  function renderTesterResults() {
    const grid = $("#testerResultsGrid");
    if (!grid) return;

    const visibleCards = PROTOCOL_CARDS.filter((c) => !c.optional || state.tester.showGemini || !!state.tester.results[c.id]);
    renderTesterSummaryBar(visibleCards);
    const frag = document.createDocumentFragment();

    visibleCards.forEach((def) => {
      const st = state.tester.results[def.id];
      let statusNode = el("span", { class: "tag", text: "Idle" });
      if (st) {
        if (st.busy) {
          statusNode = el("span", { class: "tag warn", text: t("btn_testing") });
        } else if (st.ok) {
          statusNode = el("span", { class: "tag ok mono", text: "HTTP " + st.status_code + " OK" });
        } else {
          statusNode = el("span", { class: "tag err mono", text: "HTTP " + (st.status_code || 0) + " ERR" });
        }
      }

      let routeNode = el("span", { class: "muted-count", text: "-" });
      if (st && st.route) {
        const isPass = st.route === "passthrough";
        routeNode = el("span", { style: "display:inline-flex;align-items:center;gap:4px;flex-wrap:wrap" },
          el("span", { class: "tag " + (isPass ? "ok" : "purple"), text: st.route }),
          st.route_source ? el("span", { class: "muted-count", text: "(" + st.route_source + ")" }) : null
        );
      }

      const backendTxt = st && st.provider ? (st.provider + " → " + (st.upstream_model || st.model)) : "-";
      const latencyTxt = st && !st.busy ? (String(st.duration_ms || 0) + " ms") : "-";
      const usageBrief = st && !st.busy ? formatUsageBrief(st.usage) : "";
      const finishTxt = st && !st.busy
        ? ((st.finish_reason || "-") + (usageBrief ? " · " + usageBrief : ""))
        : "-";

      let outputNode;
      if (!st) {
        outputNode = el("div", { class: "proto-output", style: "color:var(--muted)", text: t("idle_proto_hint") });
      } else if (st.busy) {
        outputNode = el("div", { class: "proto-output", text: t("btn_testing") });
      } else if (st.ok) {
        outputNode = el("div", { class: "proto-output", text: st.text || st.summary || "OK" });
      } else {
        outputNode = el("div", { class: "proto-output err", text: st.error || st.summary || st.snippet || "Request failed" });
      }

      const wireDetails = st && !st.busy && (st.request_payload || st.snippet || st.usage)
        ? el("details", { class: "wire-details" },
            el("summary", { text: t("wire_inspect") }),
            st.request_payload ? el("pre", { class: "code-block", text: "REQUEST (" + (st.path || def.path) + "):\n" + prettyJSON(st.request_payload) }) : null,
            st.usage ? el("pre", { class: "code-block", text: "USAGE:\n" + prettyJSON(st.usage) }) : null,
            st.snippet ? el("pre", { class: "code-block", text: "RESPONSE:\n" + prettyJSON(st.snippet) }) : null
          )
        : null;

      const footBar = st && !st.busy
        ? el("div", { class: "proto-foot" },
            wireDetails,
            el("button", {
              type: "button",
              class: "btn sm subtle",
              text: "📋 curl",
              onClick: () => copyText(buildTesterCurl(def, st))
            })
          )
        : null;

      frag.appendChild(el("div", { class: "card proto-card" },
        el("div", { class: "proto-head" },
          el("div", { style: "min-width:0" },
            el("div", { class: "proto-name", text: def.name }),
            el("div", { class: "proto-path", text: (st && st.path) ? ("POST " + st.path) : def.path })
          ),
          statusNode
        ),
        el("div", { class: "proto-meta" },
          el("div", null, el("div", { class: "proto-meta-label", text: "Backend → Model" }), el("div", { class: "mono", text: backendTxt })),
          el("div", null, el("div", { class: "proto-meta-label", text: "Translation / Route" }), routeNode),
          el("div", null, el("div", { class: "proto-meta-label", text: "Latency" }), el("div", { class: "mono", text: latencyTxt })),
          el("div", null, el("div", { class: "proto-meta-label", text: "Finish / Usage" }), el("div", { class: "mono", text: finishTxt }))
        ),
        outputNode,
        footBar
      ));
    });
    grid.replaceChildren(frag);
  }

  async function runProtocolProbe(protoId) {
    const model = ($("#testerModelInput") || {}).value.trim();
    const provider = ($("#testerProviderSelect") || {}).value.trim();
    const prompt = ($("#testerPromptInput") || {}).value.trim() || DEFAULT_PROMPT_TEXT;
    if (!model) {
      showToast("Model ID is required");
      return;
    }
    state.tester.model = model;
    state.tester.provider = provider;
    state.tester.prompt = prompt;
    if (protoId === "gemini") state.tester.showGemini = true;

    state.tester.results[protoId] = { busy: true };
    renderTesterResults();

    const { data } = await apiJSON("/api/gateway/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        protocol: protoId,
        model: model,
        provider: provider,
        prompt: prompt,
        stream: state.tester.mode === "stream",
        tools: state.tester.mode === "tools"
      })
    });
    state.tester.results[protoId] = Object.assign({ busy: false }, data);
    renderTesterResults();
    refreshStatsOnly();
  }

  async function runThreeProtocols() {
    const model = ($("#testerModelInput") || {}).value.trim();
    if (!model) {
      showToast("Model ID is required");
      return;
    }
    const allBtn = $("#testAllThreeBtn");
    if (allBtn) allBtn.disabled = true;
    state.tester.showGemini = true;
    state.tester.results = {};
    try {
      await Promise.all([
        runProtocolProbe("openai-chat"),
        runProtocolProbe("openai-responses"),
        runProtocolProbe("anthropic"),
        runProtocolProbe("gemini")
      ]);
    } finally {
      if (allBtn) allBtn.disabled = false;
    }
  }

  // View 4: Connect Snippets & Recent Activity
  async function loadConnectData() {
    loadClientKeys();
    const [keyRes, statusRes, logsRes] = await Promise.all([
      apiJSON("/api/config/apikey"),
      apiJSON("/api/request-logs/status"),
      apiJSON("/api/request-logs?limit=40&since=120")
    ]);
    if (keyRes.ok && state.connect.keySource === "legacy") {
      state.connect.apiKey = keyRes.data.api_key || "";
      state.connect.masked = !!keyRes.data.masked;
    }
    if (statusRes.ok) {
      state.connect.loggingEnabled = !!statusRes.data.enabled;
    }
    if (logsRes.ok) {
      state.connect.logs = logsRes.data.logs || [];
    }
    renderConnectMetaAndSnippet();
    renderActivityLogs();
  }

  function connectFlavorSpec() {
    const origin = window.location.origin;
    const key = state.connect.apiKey || "YOUR_NEXUS_API_KEY";
    const model = state.tester.model || "gpt-4o-mini";

    switch (state.connect.flavor) {
      case "openai-responses":
        return {
          baseUrl: origin + "/v1",
          envVars: 'export OPENAI_BASE_URL="' + origin + '/v1"\nexport OPENAI_API_KEY="' + key + '"',
          curl: 'curl -s "' + origin + '/v1/responses" \\\n  -H "Authorization: Bearer ' + key + '" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"model":"' + model + '","input":"Say OK."}\'',
          python: 'from openai import OpenAI\n\nclient = OpenAI(base_url="' + origin + '/v1", api_key="' + key + '")\nresp = client.responses.create(model="' + model + '", input="Say OK.")\nprint(resp.output_text)'
        };
      case "anthropic":
        return {
          baseUrl: origin + "/anthropic",
          envVars: 'export ANTHROPIC_BASE_URL="' + origin + '/anthropic"\nexport ANTHROPIC_API_KEY="' + key + '"',
          curl: 'curl -s "' + origin + '/anthropic/v1/messages" \\\n  -H "x-api-key: ' + key + '" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"model":"' + model + '","max_tokens":128,"messages":[{"role":"user","content":"Say OK."}]}\'',
          python: 'import anthropic\n\nclient = anthropic.Anthropic(base_url="' + origin + '/anthropic", api_key="' + key + '")\nmsg = client.messages.create(\n    model="' + model + '",\n    max_tokens=128,\n    messages=[{"role": "user", "content": "Say OK."}],\n)\nprint(msg.content[0].text)'
        };
      case "gemini":
        return {
          baseUrl: origin + "/v1beta",
          envVars: 'export GOOGLE_GEMINI_BASE_URL="' + origin + '"\nexport GEMINI_API_KEY="' + key + '"',
          curl: 'curl -s "' + origin + '/v1beta/models/' + model + ':generateContent" \\\n  -H "x-goog-api-key: ' + key + '" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"contents":[{"role":"user","parts":[{"text":"Say OK."}]}]}\'',
          python: 'from google import genai\nfrom google.genai import types\n\nclient = genai.Client(\n    api_key="' + key + '",\n    http_options=types.HttpOptions(base_url="' + origin + '"),\n)\nresp = client.models.generate_content(model="' + model + '", contents="Say OK.")\nprint(resp.text)'
        };
      default:
        return {
          baseUrl: origin + "/v1",
          envVars: 'export OPENAI_BASE_URL="' + origin + '/v1"\nexport OPENAI_API_KEY="' + key + '"',
          curl: 'curl -s "' + origin + '/v1/chat/completions" \\\n  -H "Authorization: Bearer ' + key + '" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"model":"' + model + '","messages":[{"role":"user","content":"Say OK."}]}\'',
          python: 'from openai import OpenAI\n\nclient = OpenAI(base_url="' + origin + '/v1", api_key="' + key + '")\nresp = client.chat.completions.create(\n    model="' + model + '",\n    messages=[{"role": "user", "content": "Say OK."}],\n)\nprint(resp.choices[0].message.content)'
        };
    }
  }

  function renderConnectMetaAndSnippet() {
    const meta = $("#connectMeta");
    const codeEl = $("#connectSnippetCode");
    if (!meta || !codeEl) return;

    const spec = connectFlavorSpec();
    const rawKey = state.connect.apiKey || "-";
    const displayKey = (!state.connect.revealKey && rawKey.length > 12)
      ? rawKey.slice(0, 6) + "••••••" + rawKey.slice(-4)
      : rawKey;

    meta.replaceChildren(
      el("div", { class: "kv-box" },
        el("div", { class: "kv-label", text: "Base URL" }),
        el("div", { class: "kv-val", style: "display:flex;align-items:center;justify-content:space-between;gap:8px" },
          el("span", { text: spec.baseUrl }),
          el("button", { type: "button", class: "btn sm", text: "Copy", onClick: () => copyText(spec.baseUrl) })
        )
      ),
      el("div", { class: "kv-box" },
        el("div", { class: "kv-label", text: "Gateway API Key" + (state.connect.keySource !== "legacy" ? " · " + state.connect.keySource : "") }),
        el("div", { class: "kv-val", style: "display:flex;align-items:center;justify-content:space-between;gap:8px" },
          el("span", { text: displayKey }),
          el("span", { style: "display:flex;gap:4px" },
            el("button", {
              type: "button",
              class: "btn sm subtle",
              text: state.connect.revealKey ? "Hide" : "Show",
              onClick: () => {
                state.connect.revealKey = !state.connect.revealKey;
                renderConnectMetaAndSnippet();
              }
            }),
            rawKey !== "-" ? el("button", { type: "button", class: "btn sm", text: "Copy", onClick: () => copyText(rawKey) }) : null,
            state.connect.keySource !== "legacy"
              ? el("button", { type: "button", class: "btn sm subtle", text: t("btn_reset_key"), onClick: resetSnippetKey })
              : null
          )
        )
      )
    );

    const langKey = state.connect.lang === "shell" ? "envVars" : state.connect.lang;
    codeEl.textContent = spec[langKey] || spec.envVars;
  }

  function renderActivityLogs() {
    const toggleBtn = $("#toggleLoggingBtn");
    const box = $("#activityLogsList");
    if (!box) return;
    if (toggleBtn) {
      toggleBtn.textContent = state.connect.loggingEnabled ? t("disable_logging") : t("enable_logging");
      toggleBtn.className = "btn " + (state.connect.loggingEnabled ? "danger" : "primary");
    }

    const children = [];
    if (!state.connect.loggingEnabled) {
      children.push(el("div", { class: "notice-banner warn", style: "margin-bottom:10px", text: t("logging_off_hint") }));
    }

    const logs = Array.isArray(state.connect.logs) ? state.connect.logs : [];
    if (logs.length === 0) {
      children.push(el("div", { class: "empty-state", text: t("no_logs") }));
      box.replaceChildren(...children);
      return;
    }

    logs.forEach((row) => {
      const ok = row.status >= 200 && row.status < 300;
      const expanded = !!state.connect.expandedLogs[row.id];
      const ts = row.timestamp ? new Date(row.timestamp).toLocaleTimeString() : "-";
      const modelTxt = row.mapped_model && row.mapped_model !== row.model
        ? row.model + " → " + row.mapped_model
        : (row.model || "-");

      children.push(el("div", { class: "call-row" },
        el("div", {
          class: "call-summary",
          onClick: () => {
            state.connect.expandedLogs[row.id] = !expanded;
            renderActivityLogs();
          }
        },
          el("div", { class: "call-left" },
            el("span", { class: "muted-count", text: ts }),
            el("span", { class: "tag mono " + (ok ? "ok" : "err"), text: String(row.status) }),
            el("span", { class: "mono", text: row.method + " " + row.url }),
            row.provider ? el("span", { class: "tag accent", text: row.provider }) : null,
            el("span", { class: "mono", text: modelTxt })
          ),
          el("div", { class: "call-right" },
            el("span", { class: "muted-count", text: (row.input_tokens || 0) + " / " + (row.output_tokens || 0) + " tok" }),
            el("span", { class: "muted-count", text: String(row.duration || 0) + " ms" })
          )
        ),
        expanded ? el("div", { class: "call-body" },
          el("pre", { class: "code-block", text: "REQUEST:\n" + prettyJSON(row.request_body || "(not captured)") }),
          el("pre", { class: "code-block", text: row.error ? ("ERROR:\n" + row.error) : ("RESPONSE:\n" + prettyJSON(row.response_body || "(not captured)")) })
        ) : null
      ));
    });
    box.replaceChildren(...children);
  }

  // Client keys: named per-caller gateway keys with provider/model allowlists
  const LEGACY_PROVIDER_IDS = ["google", "codex", "vertex"];

  function keyProviderChoices(selected) {
    const ids = state.providers.map((p) => p.id);
    LEGACY_PROVIDER_IDS.concat(selected || []).forEach((id) => {
      if (!ids.includes(id)) ids.push(id);
    });
    return ids;
  }

  function parseModelList(str) {
    return String(str || "").split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
  }

  function toggleInList(list, id) {
    const idx = list.indexOf(id);
    if (idx >= 0) list.splice(idx, 1);
    else list.push(id);
  }

  function resetSnippetKey() {
    state.connect.keySource = "legacy";
    state.connect.apiKey = "";
    loadConnectData();
  }

  async function loadClientKeys() {
    const r = await apiJSON("/api/gateway/keys");
    if (r.ok) state.keys.list = r.data.keys || [];
    renderClientKeys();
  }

  async function clientKeyCall(method, path, body) {
    const r = await apiJSON(path, {
      method,
      headers: { "Content-Type": "application/json" },
      body: body ? JSON.stringify(body) : undefined
    });
    if (!r.ok) showToast((r.data && r.data.error) || ("HTTP " + r.status));
    return r;
  }

  async function createClientKey() {
    const name = $("#keyNameInput").value.trim();
    if (!name) {
      showToast(t("key_name_required"));
      return;
    }
    const btn = $("#createKeyBtn");
    btn.disabled = true;
    try {
      const r = await clientKeyCall("POST", "/api/gateway/keys", {
        name,
        allowed_providers: state.keys.draftProviders,
        allowed_models: parseModelList($("#keyModelsInput").value)
      });
      if (!r.ok) return;
      state.keys.issued = { name: r.data.key.name, apiKey: r.data.api_key };
      state.keys.draftProviders = [];
      $("#keyNameInput").value = "";
      $("#keyModelsInput").value = "";
      showToast(t("key_created_toast", { name }));
      await loadClientKeys();
    } finally {
      btn.disabled = false;
    }
  }

  async function patchClientKey(row, patch) {
    const r = await clientKeyCall("PATCH", "/api/gateway/keys/" + row.id, patch);
    if (!r.ok) return;
    delete state.keys.editing[row.id];
    showToast(t("key_updated_toast", { name: row.name }));
    await loadClientKeys();
  }

  async function deleteClientKey(row) {
    if (!window.confirm(t("confirm_delete_key", { name: row.name }))) return;
    const r = await clientKeyCall("DELETE", "/api/gateway/keys/" + row.id);
    if (!r.ok) return;
    delete state.keys.editing[row.id];
    if (state.keys.issued && state.keys.issued.name === row.name) state.keys.issued = null;
    if (state.connect.keySource === row.name) resetSnippetKey();
    showToast(t("key_deleted_toast", { name: row.name }));
    await loadClientKeys();
  }

  function renderProviderPills(selected, onToggle) {
    return keyProviderChoices(selected).map((id) =>
      renderTogglePill(id, selected.includes(id), () => onToggle(id)));
  }

  function renderIssuedKey() {
    const box = $("#keyIssuedBox");
    if (!box) return;
    const issued = state.keys.issued;
    box.hidden = !issued;
    if (!issued) {
      box.replaceChildren();
      return;
    }
    box.replaceChildren(el("div", { class: "notice-banner warn key-issued" },
      el("div", { text: t("key_issued", { name: issued.name }) }),
      el("code", { class: "mono", text: issued.apiKey }),
      el("div", { class: "form-actions" },
        el("button", { type: "button", class: "btn sm primary", text: "Copy", onClick: () => copyText(issued.apiKey) }),
        el("button", {
          type: "button",
          class: "btn sm",
          text: t("btn_use_in_snippets"),
          onClick: () => {
            state.connect.apiKey = issued.apiKey;
            state.connect.keySource = issued.name;
            state.connect.revealKey = false;
            renderConnectMetaAndSnippet();
          }
        }),
        el("button", {
          type: "button",
          class: "btn sm subtle",
          text: t("btn_dismiss"),
          onClick: () => {
            state.keys.issued = null;
            renderIssuedKey();
          }
        })
      )
    ));
  }

  function renderKeyEditor(row, edit) {
    return el("div", { class: "key-edit" },
      el("div", { class: "field" },
        el("label", { text: t("key_providers") }),
        el("div", { class: "pill-row" }, renderProviderPills(edit.providers, (id) => {
          toggleInList(edit.providers, id);
          renderClientKeys();
        }))
      ),
      el("div", { class: "field" },
        el("label", { text: t("key_models") }),
        el("input", {
          type: "text",
          value: edit.models,
          placeholder: "notes-*, kb-embed",
          onInput: (ev) => { edit.models = ev.target.value; }
        })
      ),
      el("div", { class: "form-actions" },
        el("button", {
          type: "button",
          class: "btn sm primary",
          text: t("btn_save"),
          onClick: () => patchClientKey(row, {
            allowed_providers: edit.providers,
            allowed_models: parseModelList(edit.models)
          })
        }),
        el("button", {
          type: "button",
          class: "btn sm subtle",
          text: t("btn_cancel"),
          onClick: () => {
            delete state.keys.editing[row.id];
            renderClientKeys();
          }
        })
      )
    );
  }

  function renderClientKeyRow(row) {
    const stats = row.stats || null;
    const lastUsed = (stats && stats.last_used_at) || row.last_used_at;
    const providers = row.allowed_providers || [];
    const modelsList = row.allowed_models || [];
    const statusTag = row.legacy
      ? el("span", { class: "tag purple", text: t("key_legacy") })
      : el("span", { class: "tag " + (row.disabled ? "err" : "ok"), text: row.disabled ? t("key_disabled") : t("key_active") });
    const statsText = stats
      ? formatCompactNum(stats.requests) + " req · " + formatCompactNum(stats.errors) + " err · " +
        formatCompactNum(stats.input_tokens) + " / " + formatCompactNum(stats.output_tokens) + " tok"
      : "0 req";
    const edit = state.keys.editing[row.id];

    return el("div", { class: "key-row" },
      el("div", { class: "key-head" },
        el("div", { class: "key-meta" },
          el("strong", { text: row.name }),
          el("span", { class: "mono muted-count", text: row.masked_key }),
          statusTag
        ),
        el("div", { class: "key-meta" },
          el("span", { class: "muted-count", text: statsText }),
          el("span", {
            class: "muted-count",
            text: t("key_last_used") + ": " + (lastUsed ? new Date(lastUsed).toLocaleString() : t("key_never_used"))
          }),
          row.legacy ? null : [
            el("button", {
              type: "button",
              class: "btn sm subtle",
              text: t("btn_edit"),
              onClick: () => {
                state.keys.editing[row.id] = { providers: providers.slice(), models: modelsList.join(", ") };
                renderClientKeys();
              }
            }),
            el("button", {
              type: "button",
              class: "btn sm",
              text: row.disabled ? t("btn_enable") : t("btn_disable"),
              onClick: () => patchClientKey(row, { disabled: !row.disabled })
            }),
            el("button", { type: "button", class: "btn sm danger", text: t("btn_delete"), onClick: () => deleteClientKey(row) })
          ]
        )
      ),
      row.legacy ? null : el("div", { class: "key-meta" },
        providers.length
          ? providers.map((p) => el("span", { class: "tag accent", text: p }))
          : el("span", { class: "tag", text: t("all_providers") }),
        modelsList.length
          ? modelsList.map((m) => el("span", { class: "tag mono", text: m }))
          : el("span", { class: "tag", text: t("all_models") })
      ),
      edit ? renderKeyEditor(row, edit) : null
    );
  }

  function renderClientKeys() {
    const pills = $("#keyCreateProviders");
    if (pills) {
      pills.replaceChildren(...renderProviderPills(state.keys.draftProviders, (id) => {
        toggleInList(state.keys.draftProviders, id);
        renderClientKeys();
      }));
    }
    renderIssuedKey();
    const box = $("#clientKeysList");
    if (!box) return;
    const rows = state.keys.list || [];
    const children = rows.map(renderClientKeyRow);
    if (!rows.some((r) => !r.legacy)) {
      children.push(el("div", { class: "empty-state", text: t("no_client_keys") }));
    }
    box.replaceChildren(...children);
  }

  function wireStaticEvents() {
    $("#mainNav").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-view]");
      if (btn) setView(btn.dataset.view);
    });
    window.addEventListener("hashchange", () => {
      const h = window.location.hash.replace("#", "").trim();
      if (h) setView(h, false);
    });
    $("#usageRange").addEventListener("change", loadUsage);
    $("#usageGroup").addEventListener("change", loadUsage);
    $("#usageRefreshBtn").addEventListener("click", loadUsage);

    $("#syncAllBtn").addEventListener("click", syncAll);
    $("#clearAllCooldownsBtn").addEventListener("click", () => clearCooldown(""));
    $("#langToggleBtn").addEventListener("click", toggleLang);
    $("#themeToggleBtn").addEventListener("click", cycleTheme);

    // Routing bindings
    $("#bindProvider").addEventListener("change", updateBindProviderModelsDatalist);
    $("#bindSelectBtn").addEventListener("click", () => {
      const cm = $("#bindClientModel").value.trim();
      const pv = $("#bindProvider").value.trim();
      const tm = $("#bindTargetModel").value.trim();
      const st = $("#bindStrategy").value.trim();
      if (!cm || !pv) {
        showToast("Client model & backend are required");
        return;
      }
      selectPrimaryBackend(cm, pv, tm || cm, st);
    });
    $("#bindAddBackupBtn").addEventListener("click", () => {
      const cm = $("#bindClientModel").value.trim();
      const pv = $("#bindProvider").value.trim();
      const tm = $("#bindTargetModel").value.trim();
      if (!cm || !pv) {
        showToast("Client model & backend are required");
        return;
      }
      addBackupRoute(cm, pv, tm || cm);
    });

    $("#routingFilterTabs").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-rtab]");
      if (!btn) return;
      state.routingTab = btn.dataset.rtab;
      state.routingShowAll = false;
      renderRoutingView();
    });
    if ($("#matrixMultiOnlyBtn")) {
      $("#matrixMultiOnlyBtn").addEventListener("click", () => {
        state.matrixMultiOnly = !state.matrixMultiOnly;
        state.routingShowAll = false;
        renderRoutingView();
      });
    }
    $("#routingSearchInput").addEventListener("input", (ev) => {
      state.routingQuery = ev.target.value || "";
      state.routingShowAll = false;
      renderRoutingView();
    });

    // Tester bindings
    $("#testerModelInput").addEventListener("input", (ev) => {
      state.tester.model = ev.target.value.trim();
    });
    $("#testerProviderSelect").addEventListener("change", (ev) => {
      state.tester.provider = ev.target.value.trim();
    });
    $("#testerModeSeg").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-tmode]");
      if (!btn) return;
      const mode = btn.dataset.tmode;
      state.tester.mode = mode;
      document.querySelectorAll("#testerModeSeg [data-tmode]").forEach((b) => {
        b.classList.toggle("on", b.dataset.tmode === mode);
      });
      const promptEl = $("#testerPromptInput");
      if (promptEl) {
        if (mode === "tools" && promptEl.value.trim() === DEFAULT_PROMPT_TEXT) {
          promptEl.value = DEFAULT_TOOL_PROMPT;
        } else if (mode !== "tools" && promptEl.value.trim() === DEFAULT_TOOL_PROMPT) {
          promptEl.value = DEFAULT_PROMPT_TEXT;
        }
      }
    });
    $("#testAllThreeBtn").addEventListener("click", runThreeProtocols);
    document.querySelectorAll("[data-single-proto]").forEach((btn) => {
      btn.addEventListener("click", () => runProtocolProbe(btn.dataset.singleProto));
    });

    // Connect bindings
    $("#connectFlavorSeg").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-flavor]");
      if (!btn) return;
      state.connect.flavor = btn.dataset.flavor;
      document.querySelectorAll("#connectFlavorSeg [data-flavor]").forEach((b) => {
        b.classList.toggle("on", b.dataset.flavor === state.connect.flavor);
      });
      renderConnectMetaAndSnippet();
    });
    $("#connectLangSeg").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-clang]");
      if (!btn) return;
      state.connect.lang = btn.dataset.clang;
      document.querySelectorAll("#connectLangSeg [data-clang]").forEach((b) => {
        b.classList.toggle("on", b.dataset.clang === state.connect.lang);
      });
      renderConnectMetaAndSnippet();
    });
    $("#copySnippetBtn").addEventListener("click", () => {
      copyText($("#connectSnippetCode").textContent || "");
    });
    $("#toggleLoggingBtn").addEventListener("click", async () => {
      const next = !state.connect.loggingEnabled;
      const r = await apiJSON("/api/request-logs/toggle", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next })
      });
      if (r.ok) {
        state.connect.loggingEnabled = !!r.data.enabled;
        loadConnectData();
      }
    });
    $("#refreshLogsBtn").addEventListener("click", loadConnectData);
    $("#refreshKeysBtn").addEventListener("click", loadClientKeys);
    $("#createKeyBtn").addEventListener("click", createClientKey);
    $("#keyNameInput").addEventListener("keydown", (ev) => {
      if (ev.key === "Enter") createClientKey();
    });
  }

  async function init() {
    applyTheme();
    applyStaticTranslations();
    wireStaticEvents();
    const initialView = window.location.hash.replace("#", "").trim() || "providers";
    setView(initialView, false);
    await syncAll();
  }

  window.addEventListener("DOMContentLoaded", init);
})();
