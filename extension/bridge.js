// Gemini Web2API Live Web Bridge Content Script
// Supports gemini.google.com, vertexaisearch.cloud.google.com, and business.gemini.google
// Dual-engine response capture:
//   1. Primary: Transparent network stream interception via net-hook.js (MAIN world)
//   2. Fallback: Shadow-DOM & <slot>-aware DOM observer with strict UI/chrome filtering

(function (globalScope) {
  "use strict";

  const BRIDGE_URL = "http://localhost:8081";
  const NET_MSG_KEY = "__web2api_net";
  const BADGE_ID = "web2api-bridge-badge";

  // ---------------------------------------------------------------------------
  // Pure Text Filtering & Classification Helpers (shared with unit tests)
  // ---------------------------------------------------------------------------

  const MODEL_HEADER_RE = /^(?:gemini(?:\s+\d+(?:\.\d+)?)?(?:\s+(?:hydrogen|argon|carbon|flash|pro|ultra|nano|auto|thinking|lite|enterprise|experimental|preview))*|auto|hydrogen|argon|carbon|gemini\s+enterprise)$/i;
  const THINKING_ONLY_RE = /^(?:thinking|generating|searching|loading|working|analyzing|reasoning|show thinking|hide thinking|thought for \d+[^.\n]*|preparing response)(?:\.{1,3}|…)?$/i;

  const UI_CONTROL_TERMS = new Set([
    "auto",
    "keyboard_arrow_down",
    "arrow_upward",
    "arrow_downward",
    "arrow_forward",
    "expand_more",
    "expand_less",
    "arrow_drop_down",
    "mic",
    "send",
    "stop",
    "stop_circle",
    "thumb_up",
    "thumb_down",
    "thumb_up_off_alt",
    "thumb_down_off_alt",
    "content_copy",
    "copy",
    "copy code",
    "copied",
    "download",
    "more_vert",
    "more_horiz",
    "share",
    "refresh",
    "add",
    "tune",
    "cloud",
    "close",
    "check",
    "show thinking",
    "hide thinking"
  ]);

  function isModelHeaderLine(line) {
    const trimmed = String(line || "").trim();
    if (!trimmed) return false;
    return MODEL_HEADER_RE.test(trimmed);
  }

  function isThinkingOnlyLine(line) {
    const trimmed = String(line || "").trim();
    if (!trimmed) return false;
    return THINKING_ONLY_RE.test(trimmed);
  }

  function cleanResponseText(rawText) {
    if (!rawText) return "";

    // 1. Strip Lit / Polymer template comment markers
    let text = String(rawText)
      .replace(/<!--\??lit\$[0-9a-zA-Z]+\$-->/g, "")
      .replace(/\??lit\$[0-9a-zA-Z]+\$/g, "");

    // 2. Strip disclaimer footers
    text = text
      .replace(/^.*Generative AI (?:may display inaccurate information|is experimental).*$/gim, "")
      .replace(/^.*AI-generated content may be incorrect.*$/gim, "")
      .replace(/^.*Gemini can make mistakes.*$/gim, "");

    // 3. Filter standalone UI control lines
    const lines = text.split(/\r?\n/);
    const filtered = [];
    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) {
        filtered.push("");
        continue;
      }
      if (UI_CONTROL_TERMS.has(trimmed.toLowerCase())) {
        continue;
      }
      filtered.push(line);
    }

    // 4. Strip leading blank lines and leading standalone model header / thinking header lines
    //    (e.g. a turn header rendering "Gemini 4 Hydrogen" or "Show thinking" above the reply).
    //    Model names appearing in the body of an answer are never stripped.
    while (filtered.length > 0) {
      const first = filtered[0].trim();
      if (!first || isModelHeaderLine(first) || isThinkingOnlyLine(first)) {
        filtered.shift();
      } else {
        break;
      }
    }

    // 5. Strip trailing blank lines and trailing standalone model badges
    while (filtered.length > 0) {
      const last = filtered[filtered.length - 1].trim();
      if (!last || isModelHeaderLine(last)) {
        filtered.pop();
      } else {
        break;
      }
    }

    return filtered.join("\n").replace(/\n{3,}/g, "\n\n").trim();
  }

  function isNonAnswerBoilerplate(text, prompt) {
    const cleaned = String(text || "").trim();
    if (!cleaned) return true;
    if (isModelHeaderLine(cleaned)) return true;
    if (isThinkingOnlyLine(cleaned)) return true;

    if (prompt) {
      const normPrompt = String(prompt).trim();
      if (normPrompt && cleaned === normPrompt) return true;
      // Also guard against short user message echo (e.g. last line of prompt)
      const promptLines = normPrompt.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
      const lastPromptLine = promptLines.length > 0 ? promptLines[promptLines.length - 1] : "";
      if (lastPromptLine && cleaned === lastPromptLine && lastPromptLine.length <= 200) {
        return true;
      }
    }

    return false;
  }

  function resolveModelKeywords(requestedModel) {
    const m = String(requestedModel || "").trim().toLowerCase();
    if (!m) return null;
    if (m.includes("hydrogen")) return { family: "hydrogen", keywords: ["gemini 4 hydrogen", "hydrogen"], label: "Gemini 4 Hydrogen" };
    if (m.includes("argon")) return { family: "argon", keywords: ["gemini 4 argon", "argon"], label: "Gemini 4 Argon" };
    if (m.includes("carbon")) return { family: "carbon", keywords: ["gemini 4 carbon", "carbon"], label: "Gemini 4 Carbon" };
    if (m.includes("flash")) return { family: "flash", keywords: ["flash"], label: "Gemini Flash" };
    if (m.includes("pro")) return { family: "pro", keywords: ["pro"], label: "Gemini Pro" };
    if (m === "auto" || m === "gemini-auto") return { family: "auto", keywords: ["auto"], label: "Auto" };
    return null;
  }

  function escapeHtml(str) {
    return String(str || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  // ---------------------------------------------------------------------------
  // Shadow-DOM & <slot>-Aware DOM Traversal Helpers
  // ---------------------------------------------------------------------------

  const BLOCK_TAGS = new Set([
    "P", "DIV", "LI", "H1", "H2", "H3", "H4", "H5", "H6",
    "PRE", "BLOCKQUOTE", "TR", "SECTION", "ARTICLE", "HEADER", "FOOTER"
  ]);

  const SKIP_TAGS = new Set([
    "SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE", "SVG",
    "TEXTAREA", "INPUT", "SELECT", "OPTION", "BUTTON",
    "MAT-ICON", "MD-ICON", "GOOGLE-ICON"
  ]);

  const SKIP_ROLES = new Set([
    "button", "combobox", "menu", "menuitem", "listbox", "option", "toolbar", "navigation"
  ]);

  function isOwnExtensionElement(el, doc) {
    if (!el) return false;
    let curr = el;
    while (curr) {
      if (curr.id === BADGE_ID) return true;
      if (curr.parentElement) {
        curr = curr.parentElement;
      } else if (curr.getRootNode) {
        const root = curr.getRootNode();
        curr = root && root.host ? root.host : null;
      } else {
        break;
      }
    }
    return false;
  }

  function closestDeep(el, selector) {
    let curr = el;
    while (curr) {
      if (curr.nodeType === 1) {
        try {
          if (curr.matches && curr.matches(selector)) return curr;
        } catch (e) {}
      }
      if (curr.parentElement) {
        curr = curr.parentElement;
      } else if (curr.getRootNode) {
        const root = curr.getRootNode();
        curr = root && root.host ? root.host : null;
      } else {
        break;
      }
    }
    return null;
  }

  function containsDeep(container, target) {
    if (!container || !target) return false;
    let curr = target;
    while (curr) {
      if (curr === container) return true;
      if (curr.parentNode) {
        curr = curr.parentNode;
      } else if (curr.host) {
        curr = curr.host;
      } else {
        break;
      }
    }
    return false;
  }

  // Deduplicated Shadow-DOM & iframe deep query.
  // Calls querySelectorAll ONCE per document/shadowRoot (fixing O(N * depth) duplicates).
  function queryDeep(selector, root) {
    const startRoot = root || (typeof document !== "undefined" ? document : null);
    if (!startRoot) return [];

    const results = [];
    const seen = new Set();

    function walkScope(scope) {
      if (!scope) return;

      if (scope.querySelectorAll) {
        try {
          const matched = scope.querySelectorAll(selector);
          for (let i = 0; i < matched.length; i++) {
            const m = matched[i];
            if (!seen.has(m) && !isOwnExtensionElement(m)) {
              seen.add(m);
              results.push(m);
            }
          }
        } catch (e) {}
      }

      // Walk child elements ONLY to discover shadowRoots and same-origin iframes
      const stack = [];
      const initialChildren = scope.children || [];
      for (let i = initialChildren.length - 1; i >= 0; i--) {
        stack.push(initialChildren[i]);
      }

      while (stack.length > 0) {
        const el = stack.pop();
        if (!el || isOwnExtensionElement(el)) continue;

        if (el.shadowRoot) {
          walkScope(el.shadowRoot);
        }

        if (el.tagName === "IFRAME") {
          try {
            if (el.contentDocument) {
              walkScope(el.contentDocument);
            }
          } catch (e) {}
        }

        const kids = el.children || [];
        for (let i = kids.length - 1; i >= 0; i--) {
          stack.push(kids[i]);
        }
      }
    }

    walkScope(startRoot);
    return results;
  }

  function isElementHidden(node) {
    if (!node || node.nodeType !== 1) return false;
    if (node.hidden) return true;
    if (node.getAttribute) {
      if (node.getAttribute("aria-hidden") === "true") return true;
      if (node.hasAttribute && node.hasAttribute("hidden")) return true;
    }
    if (typeof window !== "undefined" && typeof window.getComputedStyle === "function") {
      try {
        const style = window.getComputedStyle(node);
        if (style && (style.display === "none" || style.visibility === "hidden")) {
          return true;
        }
      } catch (e) {}
    }
    return false;
  }

  // Extract visible text recursively across Shadow DOM AND <slot> projected light DOM,
  // skipping interactive controls, hidden elements, inputs, and our own HUD.
  function extractDeepText(node) {
    if (!node) return "";

    // Text node
    if (node.nodeType === 3) {
      return node.textContent || "";
    }

    // Comment or non-element/non-fragment node
    if (node.nodeType !== 1 && node.nodeType !== 11) {
      return "";
    }

    if (node.nodeType === 1) {
      if (isOwnExtensionElement(node)) return "";
      const tag = (node.tagName || "").toUpperCase();
      if (SKIP_TAGS.has(tag)) return "";
      if (tag === "BR") return "\n";
      if (isElementHidden(node)) return "";

      const role = node.getAttribute ? (node.getAttribute("role") || "").toLowerCase() : "";
      if (SKIP_ROLES.has(role)) return "";

      const cls = typeof node.className === "string" ? node.className.toLowerCase() : "";
      if (
        cls.includes("model-selector") ||
        cls.includes("model-picker") ||
        cls.includes("bottom-bar") ||
        cls.includes("action-bar") ||
        cls.includes("feedback-bar") ||
        cls.includes("disclaimer") ||
        cls.includes("thinking-container") ||
        cls.includes("thought-container")
      ) {
        return "";
      }

      // Handle <slot> projection inside Shadow DOM
      if (tag === "SLOT" && typeof node.assignedNodes === "function") {
        const assigned = node.assignedNodes({ flatten: true });
        if (assigned && assigned.length > 0) {
          let out = "";
          for (let i = 0; i < assigned.length; i++) {
            out += extractDeepText(assigned[i]);
          }
          return out;
        }
      }

      // Traverse Shadow DOM if present (slots inside shadowRoot will pull in assigned light DOM)
      if (node.shadowRoot) {
        const shadowOut = extractDeepText(node.shadowRoot);
        if (shadowOut && shadowOut.trim()) {
          return BLOCK_TAGS.has(tag) ? `\n${shadowOut}\n` : shadowOut;
        }
      }
    }

    // Traverse childNodes
    const children = node.childNodes || [];
    let combined = "";
    for (let i = 0; i < children.length; i++) {
      combined += extractDeepText(children[i]);
    }

    if (node.nodeType === 1 && BLOCK_TAGS.has((node.tagName || "").toUpperCase()) && combined.trim()) {
      return `\n${combined}\n`;
    }

    return combined;
  }

  function isNavigationOrSidebar(el, inputEl) {
    if (!el) return false;
    if (isOwnExtensionElement(el)) return true;
    if (inputEl) {
      const chatContainer = closestDeep(
        inputEl,
        "form, .bottom-bar, .input-area, .chat-box, .input-container, discovery-engine-chat, mat-form-field"
      );
      if (chatContainer && containsDeep(chatContainer, el)) {
        return false;
      }
    }
    return !!closestDeep(
      el,
      "nav, aside.left-nav, mat-sidenav.sidebar-nav, .side-nav, [role='navigation'], [aria-label*='sidebar' i]"
    );
  }

  const REJECT_ANCESTOR_SELECTOR = [
    "form",
    "nav",
    "aside",
    "header",
    "footer",
    "[role='navigation']",
    "[role='banner']",
    "[role='menu']",
    "[role='listbox']",
    "[role='dialog']",
    "[role='combobox']",
    ".cdk-overlay-container",
    ".cdk-overlay-pane",
    ".bottom-bar",
    "[class*='bottom-bar']",
    "[class*='bottom-controls']",
    "[class*='input-area']",
    "[class*='input-container']",
    "[class*='chat-input']",
    "[class*='chat-box-input']",
    "[class*='search-bar']",
    "[class*='search-box']",
    "[class*='prompt-bar']",
    "[class*='composer']",
    "[class*='model-picker']",
    "[class*='model-selector']",
    "mat-form-field",
    "rich-textarea",
    "discovery-engine-input",
    "user-query",
    ".user-query",
    "[user]",
    "[data-message-author='user']",
    "[class*='user-query']",
    "[class*='user-message']"
  ].join(", ");

  const INPUT_DESCENDANT_SELECTOR =
    'textarea, input:not([type="hidden"]), [contenteditable="true"], rich-textarea, mat-form-field';

  function isResponseElement(el, inputArea, prompt) {
    if (!el) return false;
    if (isOwnExtensionElement(el)) return false;
    if (isNavigationOrSidebar(el)) return false;

    // Reject input control itself or anything containing / inside the chat input
    const tag = (el.tagName || "").toUpperCase();
    if (tag === "TEXTAREA" || tag === "INPUT" || el.isContentEditable) {
      return false;
    }
    if (inputArea && (el === inputArea || containsDeep(el, inputArea) || containsDeep(inputArea, el))) {
      return false;
    }

    // Reject anything inside input areas, forms, overlays, sidebars, model pickers, or user queries
    if (closestDeep(el, REJECT_ANCESTOR_SELECTOR)) {
      return false;
    }

    // Reject outer wrappers that contain chat inputs (keeps buttons allowed for action bars / code copy!)
    if (queryDeep(INPUT_DESCENDANT_SELECTOR, el).length > 0) {
      return false;
    }

    const raw = extractDeepText(el);
    const cleaned = cleanResponseText(raw);
    if (!cleaned || isNonAnswerBoilerplate(cleaned, prompt)) {
      return false;
    }

    return true;
  }

  // Ordered from innermost content/markdown components to outer turn containers.
  // NOTE: Intentionally does NOT use [class*='bot'] (which matches .bottom-bar / .chat-bottom!).
  const RESPONSE_CANDIDATE_SELECTORS = [
    // Vertex AI Search / Gemini Enterprise (UCS & Discovery Engine) inner markdown/answer components
    "ucs-text-markdown",
    "ucs-response-markdown",
    "ucs-markdown",
    "ucs-summary",
    "ucs-proactive-answer",
    "ucs-assist-answer",
    "ucs-answer",
    "discovery-engine-formatted-text",
    "discovery-engine-markdown",
    "discovery-engine-answer-content",
    "discovery-engine-answer",
    "discovery-engine-message:not([user]):not(.user-message)",
    "discovery-engine-chat-message:not([user]):not(.user-message)",
    ".summary-content",
    ".answer-content",
    ".answer-text",
    ".search-result-summary",
    ".agent-message",
    ".bot-message",
    ".bot-response",
    ".response-text",
    "[data-test-id*='answer']",
    "[data-test-id*='response']",
    "[data-test-id*='agent']",
    // Consumer Gemini Web
    "message-content",
    ".model-response-text",
    ".markdown",
    "model-response"
  ];

  const RESPONSE_FALLBACK_SELECTOR =
    ".message-content, [class*='bot-message'], [class*='bot-response'], [class*='agent-message'], [class*='agent-response'], [class*='answer-content'], [class*='answer-text'], [class*='markdown'], [role='article']";

  function findResponseElements(inputArea, prompt, root) {
    for (const sel of RESPONSE_CANDIDATE_SELECTORS) {
      try {
        const els = queryDeep(sel, root);
        const filtered = els.filter((el) => isResponseElement(el, inputArea, prompt));
        if (filtered.length > 0) {
          return filtered;
        }
      } catch (e) {}
    }

    try {
      const fallbackEls = queryDeep(RESPONSE_FALLBACK_SELECTOR, root);
      const valid = fallbackEls.filter((el) => isResponseElement(el, inputArea, prompt));
      if (valid.length > 0) {
        return valid;
      }
    } catch (e) {}

    return [];
  }

  function snapshotResponses(inputArea, root) {
    const map = new Map();
    const els = findResponseElements(inputArea, "", root);
    for (const el of els) {
      map.set(el, cleanResponseText(extractDeepText(el)));
    }
    return map;
  }

  function extractActiveDomResponseText(inputArea, prompt, baselineMap, root) {
    const currentResponses = findResponseElements(inputArea, prompt, root);
    if (currentResponses.length === 0) return "";

    // 1. Prefer brand-new response elements that did NOT exist before submitting this prompt
    const newResponses = currentResponses.filter((el) => !baselineMap.has(el));
    if (newResponses.length > 0) {
      const latest = newResponses[newResponses.length - 1];
      const text = cleanResponseText(extractDeepText(latest));
      return isNonAnswerBoilerplate(text, prompt) ? "" : text;
    }

    // 2. Otherwise, check if the latest pre-existing element genuinely grew
    const lastEl = currentResponses[currentResponses.length - 1];
    const oldText = baselineMap.get(lastEl) || "";
    const fullText = cleanResponseText(extractDeepText(lastEl));
    if (oldText && fullText.length > oldText.length && fullText.startsWith(oldText)) {
      const delta = fullText.slice(oldText.length).trimStart();
      return isNonAnswerBoilerplate(delta, prompt) ? "" : delta;
    }

    // Never re-return unchanged / stale pre-existing text!
    return "";
  }

  // Export pure/DOM helpers for Node.js unit testing
  if (typeof module !== "undefined" && module.exports) {
    module.exports = {
      BADGE_ID,
      NET_MSG_KEY,
      isModelHeaderLine,
      isThinkingOnlyLine,
      cleanResponseText,
      isNonAnswerBoilerplate,
      resolveModelKeywords,
      escapeHtml,
      isOwnExtensionElement,
      closestDeep,
      containsDeep,
      queryDeep,
      extractDeepText,
      isResponseElement,
      findResponseElements,
      snapshotResponses,
      extractActiveDomResponseText
    };
    return;
  }

  // ---------------------------------------------------------------------------
  // Live Browser Bridge Runtime
  // ---------------------------------------------------------------------------

  const CLIENT_ID = `tab_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
  console.log("[Web2API Bridge] Initializing live web bridge on", location.hostname, "client:", CLIENT_ID);

  let isPolling = false;
  let activeReqId = null;
  let lastFocusedInput = null;
  let autoRefreshEnabled = false;
  let currentActiveStream = null;
  let activeNetCapture = null;

  // Listen for transparent network stream events from net-hook.js (MAIN world)
  window.addEventListener("message", (event) => {
    const data = event && event.data;
    if (!data || data[NET_MSG_KEY] !== true) return;
    const cap = activeNetCapture;
    if (!cap) return;

    const { type, streamId, url } = data;

    if (type === "start") {
      if (!cap.activeStreamId || (!cap.receivedText && !cap.active)) {
        cap.activeStreamId = streamId;
        cap.url = url || "";
        cap.started = true;
        cap.active = true;
        cap.done = false;
        cap.error = null;
        console.log("[Web2API Bridge] Intercepted live response stream:", url);
        cap.notify();
      }
      return;
    }

    if (cap.activeStreamId && streamId && streamId !== cap.activeStreamId) {
      return;
    }
    if (!cap.activeStreamId && streamId && (type === "delta" || type === "replace" || type === "thinking")) {
      cap.activeStreamId = streamId;
      cap.url = url || "";
      cap.started = true;
      cap.active = true;
    }

    if (type === "thinking") {
      cap.thinking = true;
      cap.active = true;
      cap.notify();
    } else if (type === "delta" && typeof data.text === "string" && data.text.length > 0) {
      cap.receivedText = true;
      cap.thinking = false;
      cap.accumulated += data.text;
      cap.notify();
    } else if (type === "replace" && typeof data.text === "string" && data.text.length > 0) {
      cap.receivedText = true;
      cap.thinking = false;
      cap.accumulated = data.text;
      cap.notify();
    } else if (type === "done") {
      cap.active = false;
      cap.thinking = false;
      cap.done = true;
      cap.skipped = Boolean(data.skipped);
      cap.skippedReasons = data.skippedReasons || [];
      cap.notify();
    } else if (type === "error") {
      cap.active = false;
      cap.thinking = false;
      cap.error = data.error || "stream error";
      cap.notify();
    }
  });

  // Load user preference for auto-refresh
  if (typeof chrome !== "undefined" && chrome.storage && chrome.storage.local) {
    chrome.storage.local.get(["autoRefreshEnabled"], (res) => {
      if (typeof res.autoRefreshEnabled === "boolean") {
        autoRefreshEnabled = res.autoRefreshEnabled;
      }
      updateBadgeUI();
    });
  }

  // Listen for messages from popup
  if (typeof chrome !== "undefined" && chrome.runtime && chrome.runtime.onMessage) {
    chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
      if (msg.action === "refresh") {
        resetOrRefreshPage();
        sendResponse({ ok: true });
      } else if (msg.action === "forceFinish") {
        if (currentActiveStream && currentActiveStream.finish) {
          currentActiveStream.finish();
        } else {
          resetOrRefreshPage();
        }
        sendResponse({ ok: true });
      } else if (msg.action === "setAutoRefresh") {
        autoRefreshEnabled = !!msg.enabled;
        if (chrome.storage && chrome.storage.local) {
          chrome.storage.local.set({ autoRefreshEnabled });
        }
        updateBadgeUI();
        sendResponse({ ok: true, autoRefreshEnabled });
      }
    });
  }

  // Track any input or contenteditable element the user focuses
  document.addEventListener("focusin", (e) => {
    if (isEditable(e.target) && !isOwnExtensionElement(e.target)) {
      lastFocusedInput = e.target;
    }
  }, true);

  function isEditable(el) {
    if (!el) return false;
    if (el.tagName === "TEXTAREA") return true;
    if (el.tagName === "INPUT" && (el.type === "text" || el.type === "search" || !el.type)) return true;
    if (el.isContentEditable || el.getAttribute("contenteditable") === "true") return true;
    if (el.getAttribute("role") === "textbox" || el.getAttribute("role") === "searchbox") return true;
    return false;
  }

  function isVisible(el) {
    if (!el) return false;
    if (el.offsetWidth === 0 && el.offsetHeight === 0 && (!el.getClientRects || el.getClientRects().length === 0)) {
      return false;
    }
    const style = window.getComputedStyle(el);
    if (style.display === "none" || style.visibility === "hidden" || style.opacity === "0") {
      return false;
    }
    return true;
  }

  // ---------------------------------------------------------------------------
  // 1. In-Page Floating Control Widget
  // ---------------------------------------------------------------------------
  const badgeContainer = document.createElement("div");
  badgeContainer.id = BADGE_ID;
  badgeContainer.style.cssText = `
    position: fixed;
    bottom: 20px;
    right: 20px;
    z-index: 999999;
    background: rgba(17, 24, 39, 0.95);
    color: #f9fafb;
    padding: 6px 12px;
    border-radius: 20px;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    font-size: 12px;
    font-weight: 500;
    box-shadow: 0 4px 16px rgba(0,0,0,0.35);
    display: flex;
    align-items: center;
    gap: 8px;
    backdrop-filter: blur(8px);
    border: 1px solid rgba(255,255,255,0.1);
    transition: all 0.2s ease;
    user-select: none;
  `;

  const dot = document.createElement("span");
  dot.style.cssText = `
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #ef4444;
    display: inline-block;
    flex-shrink: 0;
  `;

  const statusText = document.createElement("span");
  statusText.textContent = "Web2API: Connecting...";

  const modelBtn = document.createElement("button");
  modelBtn.id = "web2api-model-btn";
  modelBtn.textContent = "⚡ Hydrogen";
  modelBtn.title = "Click to switch web model dropdown to Gemini 4 Hydrogen";
  modelBtn.style.cssText = `
    background: rgba(59, 130, 246, 0.35);
    color: #93c5fd;
    border: 1px solid rgba(147, 197, 253, 0.4);
    border-radius: 12px;
    padding: 2px 8px;
    font-size: 11px;
    cursor: pointer;
    margin-left: 2px;
    transition: all 0.15s ease;
  `;
  modelBtn.onmouseover = () => { modelBtn.style.background = "rgba(59, 130, 246, 0.6)"; };
  modelBtn.onmouseout = () => { modelBtn.style.background = "rgba(59, 130, 246, 0.35)"; };
  modelBtn.onclick = async (e) => {
    e.stopPropagation();
    modelBtn.textContent = "⏳ Setting...";
    const ok = await ensureModelSelected("hydrogen");
    modelBtn.textContent = ok ? "✅ Hydrogen" : "❌ Click 'Auto ∨'";
    setTimeout(() => { modelBtn.textContent = "⚡ Hydrogen"; }, 2500);
  };

  const refreshBtn = document.createElement("button");
  refreshBtn.id = "web2api-refresh-btn";
  refreshBtn.textContent = "🔄 Refresh";
  refreshBtn.title = "Reload page to reset chat & ensure full interactivity";
  refreshBtn.style.cssText = `
    background: rgba(255,255,255,0.15);
    color: #fff;
    border: none;
    border-radius: 12px;
    padding: 2px 8px;
    font-size: 11px;
    cursor: pointer;
    margin-left: 4px;
    transition: background 0.15s ease;
  `;
  refreshBtn.onmouseover = () => { refreshBtn.style.background = "rgba(255,255,255,0.3)"; };
  refreshBtn.onmouseout = () => { refreshBtn.style.background = "rgba(255,255,255,0.15)"; };
  refreshBtn.onclick = (e) => {
    e.stopPropagation();
    if (activeReqId && currentActiveStream) {
      currentActiveStream.finish();
    } else {
      resetOrRefreshPage();
    }
  };

  badgeContainer.appendChild(dot);
  badgeContainer.appendChild(statusText);
  badgeContainer.appendChild(modelBtn);
  badgeContainer.appendChild(refreshBtn);
  document.body.appendChild(badgeContainer);

  function updateBadgeUI() {
    refreshBtn.textContent = activeReqId ? "⏹️ Send & Refresh" : "🔄 Refresh";
    refreshBtn.title = activeReqId
      ? "Force complete answer and refresh page"
      : "Refresh page to start fresh clean chat";
  }

  function setStatus(status, msg) {
    if (status === "connected") {
      dot.style.background = "#10b981";
      statusText.textContent = msg || "Web2API: Ready (:8081)";
    } else if (status === "busy") {
      dot.style.background = "#f59e0b";
      statusText.textContent = msg || "Web2API: Generating...";
    } else {
      dot.style.background = "#ef4444";
      statusText.textContent = msg || "Web2API: Disconnected";
    }
    updateBadgeUI();
  }

  function resetOrRefreshPage() {
    console.log("[Web2API Bridge] Refreshing page to ensure fresh interactive state...");
    if (window.location.hostname.includes("gemini.google.com")) {
      window.location.href = "https://gemini.google.com/app";
    } else {
      window.location.reload();
    }
  }

  // ---------------------------------------------------------------------------
  // 2. Chat Input, Model Picker & Submission Interaction
  // ---------------------------------------------------------------------------

  async function findChatInput(maxWaitMs = 3500) {
    const start = Date.now();

    const inputSelectors = [
      // Gemini Web
      'rich-textarea div[contenteditable="true"]',
      'div[contenteditable="true"][role="textbox"]',
      'div[contenteditable="true"]',
      // Vertex AI Search & Enterprise Google Cloud Discovery Engine
      'textarea[placeholder*="Ask" i]',
      'textarea[placeholder*="Search" i]',
      'textarea[placeholder*="Question" i]',
      'textarea[placeholder*="Prompt" i]',
      'textarea[placeholder*="Message" i]',
      'input[placeholder*="Ask" i]',
      'input[placeholder*="Search" i]',
      'input[placeholder*="Question" i]',
      'input[placeholder*="Prompt" i]',
      'input[placeholder*="Message" i]',
      "mat-form-field textarea",
      "mat-form-field input",
      "textarea[matinput]",
      "input[matinput]",
      '[role="searchbox"]',
      '[role="textbox"]',
      "textarea",
      'input[type="text"]:not([readonly])',
      'input[type="search"]:not([readonly])',
      'input:not([type]):not([readonly])'
    ];

    while (Date.now() - start < maxWaitMs) {
      if (lastFocusedInput && isEditable(lastFocusedInput) && !isOwnExtensionElement(lastFocusedInput) && containsDeep(document, lastFocusedInput)) {
        const rect = lastFocusedInput.getBoundingClientRect();
        if (rect.width > 0 && rect.height > 0 && !lastFocusedInput.disabled && !lastFocusedInput.readOnly) {
          return lastFocusedInput;
        }
      }

      if (isEditable(document.activeElement) && !isOwnExtensionElement(document.activeElement)) {
        return document.activeElement;
      }

      for (const sel of inputSelectors) {
        const found = queryDeep(sel);
        for (const el of found) {
          if (isOwnExtensionElement(el)) continue;
          const rect = el.getBoundingClientRect();
          if (rect.width > 0 && rect.height > 0 && !el.disabled && !el.readOnly) {
            return el;
          }
        }
      }

      await new Promise((r) => setTimeout(r, 150));
    }

    throw new Error("Could not find Gemini chat input box. Please click inside chat box or refresh page.");
  }

  function simulateClick(el) {
    if (!el) return;
    try { if (el.focus) el.focus(); } catch (e) {}
    const rect = el.getBoundingClientRect();
    const x = rect.left + Math.max(1, rect.width / 2);
    const y = rect.top + Math.max(1, rect.height / 2);
    const opts = {
      bubbles: true,
      cancelable: true,
      composed: true,
      view: window,
      clientX: x,
      clientY: y
    };
    el.dispatchEvent(new PointerEvent("pointerdown", opts));
    el.dispatchEvent(new MouseEvent("mousedown", opts));
    el.dispatchEvent(new PointerEvent("pointerup", opts));
    el.dispatchEvent(new MouseEvent("mouseup", opts));
    el.click();
  }

  function closeAnyOpenDropdown() {
    document.dispatchEvent(new KeyboardEvent("keydown", {
      key: "Escape",
      code: "Escape",
      keyCode: 27,
      which: 27,
      bubbles: true,
      composed: true
    }));
    const backdrops = queryDeep('.cdk-overlay-backdrop, .mat-overlay-backdrop, [class*="backdrop"]');
    for (const bd of backdrops) {
      if (isVisible(bd)) simulateClick(bd);
    }
  }

  const ALL_MODEL_FAMILIES = ["hydrogen", "carbon", "argon", "flash", "pro", "auto"];

  function findModelOption(target, triggerEl) {
    if (!target) return null;
    const competing = ALL_MODEL_FAMILIES.filter((f) => f !== target.family);
    const all = queryDeep("*");
    const matches = [];

    for (const el of all) {
      if (isOwnExtensionElement(el) || isNavigationOrSidebar(el)) continue;
      if (triggerEl && (el === triggerEl || containsDeep(triggerEl, el) || containsDeep(el, triggerEl))) {
        continue;
      }
      if (!isVisible(el)) continue;

      const t = (el.innerText || el.textContent || "").trim();
      if (!t || t.length > 140) continue;
      const lower = t.toLowerCase();

      if (!target.keywords.some((kw) => lower.includes(kw))) continue;

      // Reject menu containers that list multiple competing models together
      if (competing.some((other) => new RegExp(`\\b${other}\\b`, "i").test(lower))) {
        continue;
      }

      const clickable =
        closestDeep(el, 'button, [role="menuitem"], [role="option"], [role="button"], li, mat-option, md-menu-item, [class*="option"], [class*="item"]') || el;

      if (isOwnExtensionElement(clickable) || !isVisible(clickable)) continue;
      if (triggerEl && (clickable === triggerEl || containsDeep(triggerEl, clickable) || containsDeep(clickable, triggerEl))) {
        continue;
      }

      // Check clickable text too (don't climb into a multi-option menu container)
      const cText = (clickable.innerText || clickable.textContent || "").trim();
      if (cText.length > 160 || competing.some((other) => new RegExp(`\\b${other}\\b`, "i").test(cText.toLowerCase()))) {
        continue;
      }

      const inMenuOverlay = !!closestDeep(
        clickable,
        '[role="menu"], [role="listbox"], .cdk-overlay-pane, [class*="menu"], [class*="dropdown"], [class*="popup"], [class*="popover"], [class*="overlay"]'
      );

      matches.push({ el: clickable, inMenuOverlay, len: cText.length });
    }

    if (matches.length === 0) return null;

    // Prefer options inside an actual dropdown/menu overlay, then shortest/most specific option
    matches.sort((a, b) => {
      if (a.inMenuOverlay !== b.inMenuOverlay) return a.inMenuOverlay ? -1 : 1;
      return a.len - b.len;
    });

    return matches[0].el;
  }

  function findModelTriggerElement() {
    // Strategy 1: Look near the mic icon (In Vertex AI Search, model selector is right next to mic)
    const micIcons = queryDeep('mat-icon, [data-mat-icon-name="mic"], button[aria-label*="mic" i], [data-icon*="mic"]');
    for (const mic of micIcons) {
      if (isOwnExtensionElement(mic) || isNavigationOrSidebar(mic)) continue;
      const t = (mic.innerText || mic.textContent || "").trim().toLowerCase();
      const n = (mic.getAttribute("data-mat-icon-name") || "").toLowerCase();
      if (t === "mic" || n === "mic") {
        const container = closestDeep(mic, ".bottom-bar, form, discovery-engine-chat, div") || mic.parentElement;
        if (container) {
          const candidates = queryDeep('button, [role="button"], [role="combobox"], [aria-haspopup], mat-select', container);
          for (const b of candidates) {
            if (isOwnExtensionElement(b) || containsDeep(b, mic) || containsDeep(mic, b)) continue;
            const txt = (b.innerText || b.textContent || "").trim().toLowerCase();
            if (/^(auto|hydrogen|gemini|carbon|argon|flash|pro)(\s|$)/i.test(txt) && isVisible(b)) {
              return b;
            }
          }
        }
      }
    }

    // Strategy 2: Explicit menu trigger / combobox / button matching model name
    const candidates = queryDeep(
      'button, [role="button"], [role="combobox"], [aria-haspopup], mat-select, .mat-mdc-menu-trigger, [class*="model-selector"], [class*="model-picker"]'
    );
    for (const el of candidates) {
      if (isOwnExtensionElement(el) || isNavigationOrSidebar(el) || !isVisible(el)) continue;
      const t = (el.innerText || el.textContent || "").trim();
      if (/^(auto|hydrogen|gemini(?:\s*\d+(?:\.\d+)?)?(?:\s*(?:hydrogen|carbon|argon|flash|pro))?|carbon|argon|flash|pro)(\s+(keyboard_arrow_down|expand_more|arrow_drop_down|[\u25bc\u25bd\u2228\u2304v\u25be]))?$/i.test(t)) {
        return el;
      }
    }

    // Strategy 3: Dropdown arrow icon near model trigger text
    const arrowIcons = queryDeep("mat-icon, md-icon, svg, [data-icon]");
    for (const icon of arrowIcons) {
      if (isOwnExtensionElement(icon) || isNavigationOrSidebar(icon)) continue;
      const iconTxt = (icon.innerText || icon.textContent || "").trim().toLowerCase();
      const iconName = (icon.getAttribute("data-mat-icon-name") || "").toLowerCase();
      if (
        iconTxt === "keyboard_arrow_down" || iconTxt === "expand_more" || iconTxt === "arrow_drop_down" ||
        iconName.includes("arrow_down") || iconName.includes("expand_more")
      ) {
        const btn = closestDeep(icon, 'button, [role="button"], [role="combobox"], [aria-haspopup], mat-select') || icon.parentElement;
        if (btn && !isOwnExtensionElement(btn) && isVisible(btn) && !isNavigationOrSidebar(btn)) {
          const btnTxt = (btn.innerText || btn.textContent || "").trim().toLowerCase();
          if (/^(auto|hydrogen|gemini|carbon|argon|flash|pro)/i.test(btnTxt)) {
            return btn;
          }
        }
      }
    }

    return null;
  }

  async function ensureModelSelected(requestedModel) {
    const target = resolveModelKeywords(requestedModel);
    if (!target) return false;

    const trigger = findModelTriggerElement();
    if (trigger) {
      const currentText = (trigger.innerText || trigger.textContent || "").trim().toLowerCase();
      if (target.keywords.some((kw) => currentText.includes(kw))) {
        console.log("[Web2API Bridge] Target model already selected:", currentText);
        if (target.family === "hydrogen") {
          modelBtn.textContent = "✅ Hydrogen";
        }
        return true;
      }
    }

    // 1. If a dropdown menu is already open with the option visible, click it directly
    let item = findModelOption(target, trigger);
    if (item && isVisible(item)) {
      console.log("[Web2API Bridge] Dropdown already open, clicking model option:", item);
      simulateClick(item);
      await new Promise((r) => setTimeout(r, 250));
      if (target.family === "hydrogen") modelBtn.textContent = "✅ Hydrogen";
      return true;
    }

    // 2. Open dropdown via trigger button (click ONLY trigger, never parentElement)
    if (trigger) {
      console.log("[Web2API Bridge] Opening model dropdown via trigger:", trigger);
      simulateClick(trigger);
      await new Promise((r) => setTimeout(r, 250));

      const start = Date.now();
      while (Date.now() - start < 2500) {
        item = findModelOption(target, trigger);
        if (item && isVisible(item)) {
          console.log("[Web2API Bridge] Found model menu option, clicking:", item);
          simulateClick(item);
          await new Promise((r) => setTimeout(r, 250));
          if (target.family === "hydrogen") modelBtn.textContent = "✅ Hydrogen";
          return true;
        }
        await new Promise((r) => setTimeout(r, 100));
      }
    }

    console.warn("[Web2API Bridge] Target model not found in dropdown:", target.label);
    closeAnyOpenDropdown();
    return false;
  }

  function isNotSendButton(btn) {
    if (!btn || isOwnExtensionElement(btn)) return true;
    const t = (btn.innerText || btn.textContent || "").trim().toLowerCase();
    const aria = (btn.getAttribute("aria-label") || "").toLowerCase();
    const tip = (btn.getAttribute("mattooltip") || "").toLowerCase();
    const icon = btn.querySelector ? btn.querySelector("mat-icon, md-icon, svg, [data-mat-icon-name]") : null;
    const iconName = (icon?.getAttribute?.("data-mat-icon-name") || "").toLowerCase();
    const iconText = (icon?.innerText || icon?.textContent || "").trim().toLowerCase();

    // Model selectors
    if (/auto|hydrogen|carbon|argon|flash|pro/i.test(t) || /auto|hydrogen/i.test(aria) || /auto|hydrogen/i.test(tip)) return true;
    if (t.includes("keyboard_arrow_down") || iconName.includes("arrow_down") || iconText.includes("keyboard_arrow_down")) return true;

    // Mic button
    if (t === "mic" || aria.includes("mic") || tip.includes("mic") || iconName === "mic" || iconText === "mic") return true;

    // Add / attachment (+)
    if (t === "add" || t === "+" || aria.includes("add") || aria.includes("attach") || aria.includes("upload") || iconName === "add" || iconText === "add" || tip.includes("add")) return true;

    // Tune / sliders / filter / parameters
    if (t === "tune" || t === "sliders" || aria.includes("tune") || aria.includes("filter") || aria.includes("parameter") || iconName === "tune" || iconText === "tune" || tip.includes("tune")) return true;

    // Cloud / data store / grounding
    if (t.includes("cloud") || aria.includes("cloud") || aria.includes("store") || aria.includes("ground") || iconName.includes("cloud") || iconText.includes("cloud")) return true;

    // Thumbs, copy, share, download, stop
    if (aria.includes("thumb") || aria.includes("copy") || aria.includes("share") || aria.includes("download") || aria.includes("stop") || iconName.includes("thumb") || iconName.includes("copy") || iconName === "stop" || iconText === "stop") return true;

    return false;
  }

  function findAllCandidateSendButtons(inputEl) {
    const candidates = [];

    // Priority 1: Icons with arrow_upward / send / arrow_forward
    const iconCandidates = queryDeep("mat-icon, md-icon, svg, [data-icon], .material-icons, .google-symbols, span, i");
    for (const icon of iconCandidates) {
      if (isOwnExtensionElement(icon) || isNavigationOrSidebar(icon, inputEl)) continue;
      const text = (icon.innerText || icon.textContent || "").trim().toLowerCase();
      const name = (icon.getAttribute("data-mat-icon-name") || "").toLowerCase();
      const dataIcon = (icon.getAttribute("data-icon") || "").toLowerCase();
      const aria = (icon.getAttribute("aria-label") || "").toLowerCase();

      if (
        text === "arrow_upward" || text === "send" || text === "arrow_forward" ||
        name === "arrow_upward" || name === "send" || name === "arrow_forward" ||
        dataIcon.includes("arrow-up") || dataIcon.includes("send") ||
        aria.includes("send") || aria.includes("submit")
      ) {
        const btn = closestDeep(icon, 'button, [role="button"]') || icon.parentElement;
        if (btn && isVisible(btn) && !isNotSendButton(btn) && !candidates.includes(btn)) {
          candidates.push(btn);
        }
      }
    }

    // Priority 2: Standard button selectors
    const btnSelectors = [
      'button[aria-label*="Send" i]',
      'button[aria-label*="发送" i]',
      'button[aria-label*="Submit" i]',
      'button[mattooltip*="Send" i]',
      'button[mattooltip*="Submit" i]',
      "button.send-button",
      'button[type="submit"]'
    ];
    for (const sel of btnSelectors) {
      const btns = queryDeep(sel);
      for (const b of btns) {
        if (!isOwnExtensionElement(b) && !isNavigationOrSidebar(b, inputEl) && isVisible(b) && !isNotSendButton(b) && !candidates.includes(b)) {
          candidates.push(b);
        }
      }
    }

    return candidates;
  }

  function isInputCleared(inputArea) {
    if (!inputArea) return false;
    const val = (inputArea.tagName === "TEXTAREA" || inputArea.tagName === "INPUT")
      ? (inputArea.value || "")
      : (inputArea.textContent || "");
    return val.trim().length === 0;
  }

  function isGenerating(netCapture) {
    if (netCapture && netCapture.active) {
      return true;
    }

    // 1. Visible stop/cancel buttons
    const stopBtnSelectors = [
      'button[aria-label*="Stop" i]',
      'button[aria-label*="Cancel" i]',
      'button[aria-label*="停止" i]',
      "button.stop-generating-button",
      'button[mattooltip*="Stop" i]'
    ];
    for (const sel of stopBtnSelectors) {
      const btns = queryDeep(sel);
      for (const b of btns) {
        if (!isOwnExtensionElement(b) && isVisible(b)) return true;
      }
    }

    // 2. Visible stop icon inside a visible button (attribute OR text content!)
    const stopIcons = queryDeep("mat-icon, md-icon, .material-icons, .google-symbols");
    for (const icon of stopIcons) {
      if (isOwnExtensionElement(icon) || !isVisible(icon)) continue;
      const name = (icon.getAttribute("data-mat-icon-name") || "").toLowerCase();
      const txt = (icon.innerText || icon.textContent || "").trim().toLowerCase();
      if (name === "stop" || name === "stop_circle" || txt === "stop" || txt === "stop_circle") {
        const parentBtn = closestDeep(icon, 'button, [role="button"]');
        if (parentBtn && isVisible(parentBtn)) {
          return true;
        }
      }
    }

    // 3. Visible progress bars / spinners inside main content area
    const progressSelectors = [
      '[role="progressbar"]',
      "mat-progress-spinner",
      "mat-progress-bar",
      "md-circular-progress",
      "md-linear-progress"
    ];
    for (const sel of progressSelectors) {
      const els = queryDeep(sel);
      for (const el of els) {
        if (!isOwnExtensionElement(el) && isVisible(el)) return true;
      }
    }

    return false;
  }

  function setInputValue(el, val) {
    el.focus();
    if (el.tagName === "TEXTAREA" || el.tagName === "INPUT") {
      try {
        el.dispatchEvent(new InputEvent("beforeinput", {
          bubbles: true,
          cancelable: true,
          composed: true,
          inputType: "insertFromPaste",
          data: val
        }));
      } catch (e) {}

      const proto = el.tagName === "TEXTAREA"
        ? window.HTMLTextAreaElement.prototype
        : window.HTMLInputElement.prototype;
      const setMethod = Object.getOwnPropertyDescriptor(proto, "value")?.set;
      if (setMethod) {
        setMethod.call(el, val);
      } else {
        el.value = val;
      }

      el.dispatchEvent(new Event("input", { bubbles: true, cancelable: true, composed: true }));
      try {
        el.dispatchEvent(new InputEvent("input", {
          bubbles: true,
          cancelable: true,
          composed: true,
          inputType: "insertFromPaste",
          data: val
        }));
      } catch (e) {}
      el.dispatchEvent(new Event("change", { bubbles: true, cancelable: true, composed: true }));
    } else {
      el.focus();
      try {
        document.execCommand("selectAll", false, null);
        document.execCommand("delete", false, null);
      } catch (e) {}
      let ok = false;
      try {
        ok = document.execCommand("insertText", false, val);
      } catch (e) {}
      if (!ok || !el.textContent.trim()) {
        // Escape HTML so WorkBuddy's XML tags (<instructions>, <tools>, etc.) are preserved verbatim
        el.innerHTML = "<p>" + escapeHtml(val).replace(/\n/g, "<br>") + "</p>";
      }
      el.dispatchEvent(new Event("input", { bubbles: true, cancelable: true, composed: true }));
      try {
        el.dispatchEvent(new InputEvent("input", {
          bubbles: true,
          cancelable: true,
          composed: true,
          inputType: "insertText",
          data: val
        }));
      } catch (e) {}
    }
  }

  async function submitPrompt(inputArea, netCapture) {
    closeAnyOpenDropdown();
    inputArea.focus();

    const didSubmit = () =>
      (netCapture && netCapture.started) || isInputCleared(inputArea) || isGenerating(netCapture);

    const candidateBtns = findAllCandidateSendButtons(inputArea);
    console.log("[Web2API Bridge] Candidate send buttons found:", candidateBtns.length);

    // Stage 1: Try clicking candidate send buttons
    for (const btn of candidateBtns) {
      if (didSubmit()) return true;
      try {
        btn.disabled = false;
        btn.removeAttribute("disabled");
        btn.setAttribute("aria-disabled", "false");
        btn.classList.remove("disabled", "mat-button-disabled", "mdc-button--disabled");
      } catch (e) {}

      simulateClick(btn);
      await new Promise((r) => setTimeout(r, 350));

      if (didSubmit()) {
        console.log("[Web2API Bridge] Prompt submitted via send button click");
        return true;
      }
    }

    // Stage 2: Dispatch Enter key events
    console.log("[Web2API Bridge] Dispatching Enter key...");
    inputArea.focus();
    const enterOpts = {
      key: "Enter",
      code: "Enter",
      keyCode: 13,
      which: 13,
      charCode: 13,
      bubbles: true,
      cancelable: true,
      composed: true,
      view: window
    };
    inputArea.dispatchEvent(new KeyboardEvent("keydown", enterOpts));
    inputArea.dispatchEvent(new KeyboardEvent("keypress", enterOpts));
    inputArea.dispatchEvent(new KeyboardEvent("keyup", enterOpts));

    if (inputArea.parentElement) {
      inputArea.parentElement.dispatchEvent(new KeyboardEvent("keydown", enterOpts));
      inputArea.parentElement.dispatchEvent(new KeyboardEvent("keyup", enterOpts));
    }

    await new Promise((r) => setTimeout(r, 350));
    if (didSubmit()) {
      console.log("[Web2API Bridge] Prompt submitted via Enter key");
      return true;
    }

    // Stage 3: Form requestSubmit fallback
    if (inputArea.form) {
      try {
        if (inputArea.form.requestSubmit) inputArea.form.requestSubmit();
        else inputArea.form.submit();
      } catch (e) {}
      await new Promise((r) => setTimeout(r, 350));
      if (didSubmit()) return true;
    }

    // Stage 4: Final click retry
    for (const btn of candidateBtns) {
      if (didSubmit()) return true;
      simulateClick(btn);
    }
    await new Promise((r) => setTimeout(r, 300));
    return didSubmit();
  }

  // ---------------------------------------------------------------------------
  // 3. Prompt Handling & Dual-Engine Stream Observation
  // ---------------------------------------------------------------------------

  function createNetCapture() {
    const listeners = [];
    return {
      started: false,
      activeStreamId: null,
      url: "",
      active: false,
      thinking: false,
      receivedText: false,
      accumulated: "",
      done: false,
      error: null,
      skipped: false,
      skippedReasons: [],
      onUpdate(fn) {
        listeners.push(fn);
      },
      notify() {
        for (const fn of listeners) {
          try { fn(); } catch (e) {}
        }
      }
    };
  }

  async function handlePromptRequest(reqId, prompt, model) {
    // A. Switch model dropdown if the requested model specifies a known family
    try {
      await ensureModelSelected(model);
    } catch (e) {
      console.warn("[Web2API Bridge] Error selecting model:", e);
    }

    // B. Locate Prompt Input Area
    const inputArea = await findChatInput(3500);

    // Snapshot exact pre-existing response elements BEFORE submitting
    const baselineMap = snapshotResponses(inputArea);

    // Register network capture BEFORE submitting so we never miss synchronous start/delta
    const netCapture = createNetCapture();
    activeNetCapture = netCapture;

    try {
      setInputValue(inputArea, prompt);
      await new Promise((r) => setTimeout(r, 200));

      const submitSuccess = await submitPrompt(inputArea, netCapture);
      console.log("[Web2API Bridge] submitPrompt result:", submitSuccess, "netStarted:", netCapture.started);

      // C. Watch for Streaming Response (Network primary, DOM fallback)
      await observeResponseStream(reqId, inputArea, prompt, baselineMap, netCapture);
    } finally {
      if (activeNetCapture === netCapture) {
        activeNetCapture = null;
      }
    }

    // D. Auto-Refresh page after response is delivered (if enabled)
    if (autoRefreshEnabled) {
      setStatus("busy", "Web2API: Refreshing page for next request...");
      await new Promise((r) => setTimeout(r, 600));
      resetOrRefreshPage();
    }
  }

  function observeResponseStream(reqId, inputArea, prompt, baselineMap, netCapture) {
    return new Promise((resolve) => {
      let sentText = "";
      let lastChangeTime = Date.now();
      let completed = false;
      let abortedByServer = false;
      let sawGenerating = false;
      let netDoneAt = 0;
      const startTime = Date.now();
      let chunkQueue = Promise.resolve();

      function enqueueChunk(text, isDone, err) {
        chunkQueue = chunkQueue.then(async () => {
          if (abortedByServer) return;
          const res = await sendChunk(reqId, text, isDone, err);
          if (res && res.gone) {
            console.log("[Web2API Bridge] Request cancelled by client (410 Gone), stopping observer.");
            abortedByServer = true;
            cleanup();
            resolve();
          }
        });
        return chunkQueue;
      }

      function emitDeltaFromFull(fullText, sourceTag) {
        if (!fullText) return;
        if (fullText.length > sentText.length && fullText.startsWith(sentText)) {
          const delta = fullText.slice(sentText.length);
          sentText = fullText;
          lastChangeTime = Date.now();
          setStatus("busy", `Web2API: Streaming (${sentText.length} chars) ${sourceTag}`);
          enqueueChunk(delta, false);
        } else if (sentText.length === 0 && fullText.length > 0) {
          sentText = fullText;
          lastChangeTime = Date.now();
          setStatus("busy", `Web2API: Streaming (${sentText.length} chars) ${sourceTag}`);
          enqueueChunk(fullText, false);
        }
      }

      function cleanup() {
        completed = true;
        clearInterval(checkInterval);
        clearTimeout(hardTimeout);
        observer.disconnect();
        currentActiveStream = null;
      }

      function finish(explicitError) {
        if (completed) return;
        cleanup();

        // Final flush from network or DOM if nothing was sent yet
        if (!sentText.trim() && !explicitError) {
          if (netCapture.receivedText && netCapture.accumulated.trim()) {
            emitDeltaFromFull(netCapture.accumulated, "⚡");
          } else {
            const finalDom = extractActiveDomResponseText(inputArea, prompt, baselineMap);
            if (finalDom && !isNonAnswerBoilerplate(finalDom, prompt)) {
              emitDeltaFromFull(finalDom, "🔍");
            }
          }
        }

        if (!sentText.trim()) {
          const errMsg = explicitError || "Model did not respond. Please verify the prompt was sent on the page or refresh.";
          console.warn("[Web2API Bridge] Completing with error:", errMsg);
          enqueueChunk("", true, errMsg).then(() => resolve()).catch(() => resolve());
          return;
        }

        console.log(
          `[Web2API Bridge] Response completed (${sentText.length} chars, mode=${netCapture.receivedText ? "network" : "dom"})`
        );
        enqueueChunk("", true).then(() => resolve()).catch(() => resolve());
      }

      function tick() {
        if (completed || abortedByServer) return;

        // 1. PRIMARY ENGINE: Transparent Network Stream Interception
        if (netCapture.receivedText) {
          emitDeltaFromFull(netCapture.accumulated, "⚡");
          if (netCapture.done) {
            finish();
          }
          return;
        }

        // If an intercepted network request is actively in flight (e.g. model thinking),
        // wait for it rather than scraping partial/header DOM text prematurely.
        if (netCapture.active) {
          sawGenerating = true;
          if (netCapture.thinking) {
            setStatus("busy", "Web2API: Thinking... ⚡");
          }
          return;
        }

        if (netCapture.done && !netDoneAt) {
          netDoneAt = Date.now();
        }

        // 2. FALLBACK ENGINE: Hardened Shadow-DOM Observer
        const domText = extractActiveDomResponseText(inputArea, prompt, baselineMap);
        const generating = isGenerating(netCapture);
        if (generating) {
          sawGenerating = true;
        }

        if (domText && !isNonAnswerBoilerplate(domText, prompt)) {
          emitDeltaFromFull(domText, "🔍");
        }

        const now = Date.now();
        const timeSinceChange = now - lastChangeTime;
        const timeSinceStart = now - startTime;

        // If network stream finished with no text (e.g. SKIPPED), allow DOM up to 6s to render fallback
        if (netDoneAt && sentText.length === 0 && !generating && now - netDoneAt > 6000) {
          finish();
          return;
        }

        if (sentText.length === 0) {
          // Wait up to 45s for generation to start producing text
          if (!generating && timeSinceStart > 45000) {
            finish();
          }
          return;
        }

        // Never finish while stop button / progress spinner / HTTP stream is active
        if (generating) {
          // Safety escape if spinner stays stuck forever after text completely stopped changing
          if (timeSinceChange > 8000) {
            finish();
          }
          return;
        }

        // Completion criteria for DOM fallback (never cuts off after 500ms!)
        if (sawGenerating && timeSinceChange > 1200) {
          finish();
          return;
        }
        if (!sawGenerating && timeSinceStart > 2500 && timeSinceChange > 2200) {
          finish();
          return;
        }
        if (timeSinceStart > 25000 && timeSinceChange > 1800) {
          finish();
          return;
        }
      }

      currentActiveStream = { finish: () => finish() };

      // React immediately to network stream events (zero polling latency!)
      netCapture.onUpdate(() => tick());

      const checkInterval = setInterval(tick, 150);

      const observer = new MutationObserver(() => {
        if (!netCapture.receivedText && !netCapture.active) {
          tick();
        }
      });

      observer.observe(document.body, {
        childList: true,
        subtree: true,
        characterData: true
      });

      // Absolute safety timeout: 150s
      const hardTimeout = setTimeout(() => {
        if (!completed) {
          finish();
        }
      }, 150000);
    });
  }

  // ---------------------------------------------------------------------------
  // 4. Server Polling, Heartbeat & Chunk Delivery
  // ---------------------------------------------------------------------------

  async function sendHeartbeat() {
    if (!activeReqId) return;
    try {
      await fetch(`${BRIDGE_URL}/bridge/heartbeat`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          tab: location.hostname,
          client_id: CLIENT_ID,
          busy: true
        })
      });
    } catch (e) {}
  }

  setInterval(sendHeartbeat, 10000);

  async function pollServer() {
    if (isPolling) return;
    isPolling = true;

    while (true) {
      try {
        const tabParam = encodeURIComponent(location.hostname);
        const clientParam = encodeURIComponent(CLIENT_ID);
        const res = await fetch(`${BRIDGE_URL}/bridge/poll?tab=${tabParam}&client=${clientParam}`, {
          method: "GET",
          headers: { Accept: "application/json" }
        });

        if (!res.ok) {
          setStatus("disconnected");
          await new Promise((r) => setTimeout(r, 2500));
          continue;
        }

        const data = await res.json();
        setStatus("connected");

        if (data.timeout) {
          continue;
        }

        if (data.id && data.prompt) {
          activeReqId = data.id;
          setStatus("busy", "Web2API: Answering request...");
          try {
            await handlePromptRequest(data.id, data.prompt, data.model);
          } catch (err) {
            console.error("[Web2API Bridge] Error processing prompt:", err);
            await sendChunk(data.id, "", true, (err && err.message) || "Failed to generate reply");
          } finally {
            activeReqId = null;
            setStatus("connected");
          }
        }
      } catch (err) {
        setStatus("disconnected");
        await new Promise((r) => setTimeout(r, 2500));
      }
    }
  }

  async function sendChunk(id, chunkText, isDone, err) {
    try {
      const res = await fetch(`${BRIDGE_URL}/bridge/chunk`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: id,
          text: chunkText,
          done: isDone,
          error: err || ""
        })
      });
      if (res.status === 410) {
        return { gone: true };
      }
      return { ok: res.ok };
    } catch (e) {
      console.warn("[Web2API Bridge] Failed to send chunk:", e);
      return { ok: false };
    }
  }

  // On Vertex AI Search / Gemini Enterprise, auto-select Hydrogen on startup
  if (
    location.hostname.includes("vertexaisearch.cloud.google.com") ||
    location.hostname.includes("business.gemini.google")
  ) {
    setTimeout(() => {
      ensureModelSelected("hydrogen").catch(() => {});
    }, 1500);
  }

  pollServer();
})(typeof window !== "undefined" ? window : globalThis);
