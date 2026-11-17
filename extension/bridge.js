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

  // ---------------------------------------------------------------------------
  // Attachment & File Upload Helpers (pure / testable)
  // ---------------------------------------------------------------------------

  function base64ToUint8Array(b64) {
    if (!b64) return new Uint8Array(0);
    if (typeof atob === "function") {
      const bin = atob(b64);
      const out = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) {
        out[i] = bin.charCodeAt(i);
      }
      return out;
    }
    if (typeof Buffer !== "undefined") {
      return new Uint8Array(Buffer.from(b64, "base64"));
    }
    return new Uint8Array(0);
  }

  function deduplicateAttachments(attachments) {
    if (!Array.isArray(attachments) || attachments.length === 0) return [];
    const out = [];
    const seenPayload = new Set();
    const seenName = new Set();
    for (const att of attachments) {
      if (!att || typeof att !== "object" || !att.data_base64) continue;
      const b64 = String(att.data_base64);
      if (b64.length === 0) continue;
      const payloadKey = `${b64.length}:${b64.slice(0, 64)}:${b64.slice(-64)}`;
      if (seenPayload.has(payloadKey)) continue;
      const nameKey = String(att.filename || "").trim().toLowerCase();
      if (nameKey && seenName.has(nameKey)) continue;
      seenPayload.add(payloadKey);
      if (nameKey) seenName.add(nameKey);
      out.push(att);
    }
    return out;
  }

  function buildAttachedFileNote(attachments) {
    if (!Array.isArray(attachments) || attachments.length === 0) return "";
    const seen = new Set();
    const names = [];
    for (const a of attachments) {
      const name = a && a.filename ? String(a.filename).trim() : "";
      if (!name) continue;
      const key = name.toLowerCase();
      if (seen.has(key)) continue;
      seen.add(key);
      names.push(name);
    }
    if (names.length === 0) return "";
    return `[Directly attached file(s): ${names.join(", ")}. Read the attached file(s) natively in full — do NOT call local shell/python/file tools to inspect or extract them.]`;
  }

  function findComposerScope(inputArea, root) {
    if (inputArea && inputArea.nodeType === 1) {
      const container = closestDeep(
        inputArea,
        "form, [class*='composer'], [class*='input-area'], [class*='chat-input'], [class*='prompt-area'], [class*='input-box'], [class*='search-box'], [class*='query-input'], ucs-proactive-search-bar, ucs-search-bar, ucs-chat-input"
      );
      if (container) {
        const tag = String(container.tagName || "").toUpperCase();
        if (tag !== "BODY" && tag !== "HTML" && tag !== "MAIN") {
          return container;
        }
      }
      // Walk up a couple shadow-aware parents so sibling chip bars above the input are included
      let cur = inputArea;
      for (let i = 0; i < 4 && cur; i++) {
        const parent = cur.parentElement || (cur.getRootNode && cur.getRootNode().host) || null;
        if (!parent) break;
        const pTag = String(parent.tagName || "").toUpperCase();
        if (pTag === "BODY" || pTag === "HTML" || pTag === "MAIN") break;
        cur = parent;
      }
      if (cur && cur !== inputArea) return cur;
    }
    return root || (typeof document !== "undefined" ? document : null);
  }

  function walkElementsDeep(root, visitor) {
    if (!root) return;
    const stack = [root];
    const seen = new Set();
    while (stack.length > 0) {
      const node = stack.pop();
      if (!node || seen.has(node)) continue;
      seen.add(node);
      if (node.nodeType === 1) {
        if (isOwnExtensionElement(node)) continue;
        visitor(node);
        if (node.shadowRoot) {
          stack.push(node.shadowRoot);
        }
      }
      const children = node.children || node.childNodes;
      if (children && children.length) {
        for (let i = children.length - 1; i >= 0; i--) {
          const child = children[i];
          if (child && (child.nodeType === 1 || child.nodeType === 11)) {
            stack.push(child);
          }
        }
      }
    }
  }

  function countAttachmentChipsInDom(inputArea, root) {
    const scope = findComposerScope(inputArea, root);
    if (!scope) return { chipCount: 0, removeBtnCount: 0 };

    let chipCount = 0;
    let removeBtnCount = 0;
    const countedChips = [];

    walkElementsDeep(scope, (el) => {
      if (inputArea && (el === inputArea || containsDeep(inputArea, el))) return;
      const tag = String(el.tagName || "").toLowerCase();
      const cls = typeof el.className === "string" ? el.className.toLowerCase() : "";
      const testId = String((el.getAttribute && el.getAttribute("data-test-id")) || "").toLowerCase();
      const role = String((el.getAttribute && el.getAttribute("role")) || "").toLowerCase();
      const aria = String((el.getAttribute && el.getAttribute("aria-label")) || "").toLowerCase();
      const title = String((el.getAttribute && el.getAttribute("title")) || "").toLowerCase();
      const tooltip = String((el.getAttribute && el.getAttribute("data-tooltip")) || "").toLowerCase();

      // Count remove/delete attachment buttons inside the composer
      const isBtn = tag === "button" || role === "button" || tag.includes("icon-button") || tag.includes("button");
      const btnSignal = `${aria} ${title} ${tooltip}`;
      if (isBtn && /\b(remove|delete|detach|discard|clear file|cancel upload|close)\b/.test(btnSignal)) {
        // Avoid counting generic modal/menu close buttons outside file items unless labeled remove/delete/detach/file
        if (/\b(remove|delete|detach|discard|file|attachment|upload)\b/.test(btnSignal)) {
          removeBtnCount++;
        }
        return;
      }

      // Count attachment chip / file preview elements (including custom Lit elements like ucs-file-preview)
      const looksLikeChip =
        /(file|attach|upload)/.test(tag) ||
        tag.includes("chip") ||
        /\b(file-chip|file-card|file-preview|file-item|attachment-chip|attachment-card|attachment-item|uploaded-file|upload-chip|context-file)\b/.test(cls) ||
        /(file|attach|chip|upload)/.test(testId) ||
        role === "listitem";

      if (looksLikeChip && !isBtn && tag !== "input") {
        // Skip wrapper elements that already contain or sit inside a counted chip
        const alreadyNested = countedChips.some((prev) => containsDeep(prev, el) || containsDeep(el, prev));
        if (!alreadyNested) {
          const text = String(extractControlLabel(el) || "").trim();
          // Ignore empty hidden containers
          if (text.length > 0 && text.length <= 400) {
            countedChips.push(el);
            chipCount++;
          }
        }
      }
    });

    return { chipCount, removeBtnCount };
  }

  function matchesFilenameInText(text, filename) {
    if (!text || !filename) return false;
    if (text.includes(filename)) return true;
    // Handle middle/end truncation in chips, e.g. "very_long_report_2026.pdf" -> "very_long_...pdf" or "very_long_…"
    const dotIdx = filename.lastIndexOf(".");
    const stem = dotIdx > 0 ? filename.slice(0, dotIdx) : filename;
    const ext = dotIdx > 0 ? filename.slice(dotIdx) : "";
    if (stem.length >= 10) {
      const prefix = stem.slice(0, 8);
      if (text.includes(prefix) && (!ext || text.includes(ext) || text.includes("...") || text.includes("…"))) {
        return true;
      }
    }
    return false;
  }

  function hasAttachmentChipInDom(filenames, inputArea, root) {
    if (!Array.isArray(filenames) || filenames.length === 0) return false;
    const lowered = filenames
      .map((f) => String(f || "").trim().toLowerCase())
      .filter(Boolean);
    if (lowered.length === 0) return false;

    const searchRoot = findComposerScope(inputArea, root);
    if (!searchRoot) return false;

    let matched = false;
    walkElementsDeep(searchRoot, (el) => {
      if (matched) return;
      if (inputArea && (el === inputArea || containsDeep(inputArea, el))) return;

      const tag = String(el.tagName || "").toLowerCase();
      const cls = typeof el.className === "string" ? el.className.toLowerCase() : "";
      const testId = String((el.getAttribute && el.getAttribute("data-test-id")) || "").toLowerCase();
      const role = String((el.getAttribute && el.getAttribute("role")) || "").toLowerCase();
      const aria = String((el.getAttribute && el.getAttribute("aria-label")) || "").toLowerCase();
      const title = String((el.getAttribute && el.getAttribute("title")) || "").toLowerCase();

      const isCandidate =
        /(file|attach|chip|upload|preview)/.test(tag) ||
        /(file|attach|chip|upload|preview)/.test(cls) ||
        /(file|attach|chip|upload)/.test(testId) ||
        role === "listitem" ||
        aria.length > 0 ||
        title.length > 0;
      if (!isCandidate) return;

      const text = String(extractControlLabel(el) || "").trim().toLowerCase();
      if (text.length > 400) return;

      for (const name of lowered) {
        if (
          matchesFilenameInText(aria, name) ||
          matchesFilenameInText(title, name) ||
          matchesFilenameInText(text, name)
        ) {
          matched = true;
          return;
        }
      }
    });
    return matched;
  }

  function isSameConversationUrlTransition(prevUrlKey, nextUrlKey) {
    const normalizePath = (raw) => {
      const s = String(raw || "");
      const pathOnly = s.split(/[?#]/)[0];
      return pathOnly.replace(/\/+$/, "") || "/";
    };
    const prevPath = normalizePath(prevUrlKey);
    const nextPath = normalizePath(nextUrlKey);
    if (prevPath === nextPath) return true;
    // Initial turn session-ID assignment: e.g. /app -> /app/abc123 or
    // /home/cid/<cid> -> /home/cid/<cid>/r/session/<sid>
    if (prevPath !== "/" && nextPath.startsWith(prevPath + "/")) {
      return true;
    }
    return false;
  }

  function extractControlLabel(node) {
    if (!node) return "";
    if (node.nodeType === 3) return node.textContent || "";
    if (node.nodeType !== 1 && node.nodeType !== 11) return "";
    if (node.nodeType === 1) {
      if (isOwnExtensionElement(node)) return "";
      const tag = (node.tagName || "").toUpperCase();
      if (tag === "SCRIPT" || tag === "STYLE" || tag === "SVG" || tag === "NOSCRIPT") return "";
      if (tag === "SLOT" && typeof node.assignedNodes === "function") {
        const assigned = node.assignedNodes({ flatten: true });
        if (assigned && assigned.length > 0) {
          return assigned.map(extractControlLabel).join(" ");
        }
      }
      if (node.shadowRoot) {
        const srText = extractControlLabel(node.shadowRoot);
        if (srText.trim()) return srText;
      }
    }
    let out = "";
    const children = node.childNodes || [];
    for (let i = 0; i < children.length; i++) {
      const part = extractControlLabel(children[i]);
      if (part) out += (out ? " " : "") + part;
    }
    return out;
  }

  function isLinkOrPromoElement(el) {
    if (!el || el.nodeType !== 1) return false;
    const tag = String(el.tagName || "").toUpperCase();
    if (tag === "A") return true;
    const role = String((el.getAttribute && el.getAttribute("role")) || "").toLowerCase();
    if (role === "link") return true;
    if (el.getAttribute && (el.getAttribute("href") || el.getAttribute("target") === "_blank")) {
      return true;
    }
    if (
      closestDeep(
        el,
        'a, [href], [target="_blank"], [role="link"], [class*="banner"], [class*="promo"], [class*="announcement"], [class*="whats-new"], [class*="snackbar"], [class*="toast"], [role="alert"], [role="status"], header'
      )
    ) {
      return true;
    }
    // Reject wrapper containers around an external/navigation <a> link
    try {
      const childLinks = queryDeep('a[href], a[target="_blank"], [role="link"]', el);
      if (childLinks.length > 0) {
        return true;
      }
    } catch (e) {}
    return false;
  }

  function isRateLimitMessage(msg) {
    if (!msg) return false;
    const lower = String(msg).toLowerCase();
    return (
      lower.includes("http 429") ||
      lower.includes("too many requests") ||
      lower.includes("rate limit") ||
      lower.includes("rate_limit") ||
      lower.includes("quota exceeded") ||
      lower.includes("resource_exhausted") ||
      lower.includes("unusual traffic") ||
      lower.includes("try again later")
    );
  }

  function isElementVisible(el) {
    if (!el || isElementHidden(el)) return false;
    if (typeof window !== "undefined" && typeof window.getComputedStyle === "function") {
      try {
        const s = window.getComputedStyle(el);
        if (s && (s.display === "none" || s.visibility === "hidden" || parseFloat(s.opacity) === 0)) {
          return false;
        }
      } catch (e) {}
    }
    if (typeof el.getBoundingClientRect === "function") {
      const r = el.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) return false;
    }
    return true;
  }

  const ALL_MODEL_FAMILIES = ["hydrogen", "carbon", "argon", "flash", "pro", "auto"];
  const PROMO_LINK_TEXT_RE = /\b(introducing|learn more|read more|blog|what'?s new|announcement|explore now|discover)\b/i;

  function findModelOption(target, triggerEl, root) {
    if (!target) return null;
    const searchRoot = root || (typeof document !== "undefined" ? document : null);
    if (!searchRoot) return null;

    const competing = ALL_MODEL_FAMILIES.filter((f) => f !== target.family);
    const all = queryDeep("*", searchRoot);
    const matches = [];

    for (const el of all) {
      if (isOwnExtensionElement(el) || isNavigationOrSidebar(el) || isLinkOrPromoElement(el)) continue;
      if (triggerEl && (el === triggerEl || containsDeep(triggerEl, el) || containsDeep(el, triggerEl))) {
        continue;
      }
      if (!isElementVisible(el)) continue;

      const t = String(extractControlLabel(el) || el.innerText || el.textContent || "").trim();
      if (!t || t.length > 140) continue;
      const lower = t.toLowerCase();

      if (PROMO_LINK_TEXT_RE.test(lower)) continue;
      if (!target.keywords.some((kw) => lower.includes(kw))) continue;

      // Reject menu containers that list multiple competing models together
      if (competing.some((other) => new RegExp(`\\b${other}\\b`, "i").test(lower))) {
        continue;
      }

      const clickable = closestDeep(
        el,
        'button, [role="menuitem"], [role="menuitemradio"], [role="option"], [role="button"], li, mat-option, md-menu-item, mwc-list-item, [class*="option"], [class*="menu-item"]'
      );
      if (!clickable) continue;
      if (isOwnExtensionElement(clickable) || isLinkOrPromoElement(clickable) || !isElementVisible(clickable)) {
        continue;
      }
      if (triggerEl && (clickable === triggerEl || containsDeep(triggerEl, clickable) || containsDeep(clickable, triggerEl))) {
        continue;
      }

      const cText = String(extractControlLabel(clickable) || clickable.innerText || clickable.textContent || "").trim();
      if (!cText || cText.length > 160 || PROMO_LINK_TEXT_RE.test(cText)) continue;
      if (competing.some((other) => new RegExp(`\\b${other}\\b`, "i").test(cText.toLowerCase()))) {
        continue;
      }

      const cRole = String((clickable.getAttribute && clickable.getAttribute("role")) || "").toLowerCase();
      const cTag = String(clickable.tagName || "").toLowerCase();
      const hasMenuOptionRole =
        cRole === "menuitem" ||
        cRole === "menuitemradio" ||
        cRole === "option" ||
        cTag === "mat-option" ||
        cTag === "md-menu-item" ||
        cTag === "mwc-list-item";

      const inMenuOverlay = !!closestDeep(
        clickable,
        '[role="menu"], [role="listbox"], .cdk-overlay-pane, [class*="menu"], [class*="dropdown"], [class*="popup"], [class*="popover"], [class*="overlay"]'
      );

      // Strictly require an actual menu overlay or explicit menu-option role so
      // non-dropdown page banners or announcements can NEVER be clicked!
      if (!inMenuOverlay && !hasMenuOptionRole) continue;

      matches.push({ el: clickable, inMenuOverlay, hasMenuOptionRole, len: cText.length });
    }

    if (matches.length === 0) return null;

    matches.sort((a, b) => {
      if (a.inMenuOverlay !== b.inMenuOverlay) return a.inMenuOverlay ? -1 : 1;
      if (a.hasMenuOptionRole !== b.hasMenuOptionRole) return a.hasMenuOptionRole ? -1 : 1;
      return a.len - b.len;
    });

    return matches[0].el;
  }

  function findModelTriggerElement(root) {
    const searchRoot = root || (typeof document !== "undefined" ? document : null);
    if (!searchRoot) return null;

    // Strategy 1: Look near the mic icon (In Vertex AI Search, model selector is right next to mic)
    const micIcons = queryDeep('mat-icon, [data-mat-icon-name="mic"], button[aria-label*="mic" i], [data-icon*="mic"]', searchRoot);
    for (const mic of micIcons) {
      if (isOwnExtensionElement(mic) || isNavigationOrSidebar(mic) || isLinkOrPromoElement(mic)) continue;
      const t = String(extractControlLabel(mic) || mic.innerText || mic.textContent || "").trim().toLowerCase();
      const n = String((mic.getAttribute && mic.getAttribute("data-mat-icon-name")) || "").toLowerCase();
      if (t === "mic" || n === "mic") {
        const container = closestDeep(mic, ".bottom-bar, form, discovery-engine-chat, div") || mic.parentElement;
        if (container) {
          const candidates = queryDeep('button, [role="button"], [role="combobox"], [aria-haspopup], mat-select', container);
          for (const b of candidates) {
            if (isOwnExtensionElement(b) || isLinkOrPromoElement(b) || containsDeep(b, mic) || containsDeep(mic, b)) continue;
            const txt = String(extractControlLabel(b) || b.innerText || b.textContent || "").trim().toLowerCase();
            if (/^(auto|hydrogen|gemini|carbon|argon|flash|pro)(\s|$)/i.test(txt) && isElementVisible(b)) {
              return b;
            }
          }
        }
      }
    }

    // Strategy 2: Explicit menu trigger / combobox / button matching model name
    const candidates = queryDeep(
      'button, [role="button"], [role="combobox"], [aria-haspopup], mat-select, .mat-mdc-menu-trigger, [class*="model-selector"], [class*="model-picker"]',
      searchRoot
    );
    for (const el of candidates) {
      if (isOwnExtensionElement(el) || isNavigationOrSidebar(el) || isLinkOrPromoElement(el) || !isElementVisible(el)) continue;
      const t = String(extractControlLabel(el) || el.innerText || el.textContent || "").trim();
      if (/^(auto|hydrogen|gemini(?:\s*\d+(?:\.\d+)?)?(?:\s*(?:hydrogen|carbon|argon|flash|pro))?|carbon|argon|flash|pro)(\s+(keyboard_arrow_down|expand_more|arrow_drop_down|[\u25bc\u25bd\u2228\u2304v\u25be]))?$/i.test(t)) {
        return el;
      }
    }

    // Strategy 3: Dropdown arrow icon near model trigger text
    const arrowIcons = queryDeep("mat-icon, md-icon, svg, [data-icon]", searchRoot);
    for (const icon of arrowIcons) {
      if (isOwnExtensionElement(icon) || isNavigationOrSidebar(icon) || isLinkOrPromoElement(icon)) continue;
      const iconTxt = String(extractControlLabel(icon) || icon.innerText || icon.textContent || "").trim().toLowerCase();
      const iconName = String((icon.getAttribute && icon.getAttribute("data-mat-icon-name")) || "").toLowerCase();
      if (
        iconTxt === "keyboard_arrow_down" || iconTxt === "expand_more" || iconTxt === "arrow_drop_down" ||
        iconName.includes("arrow_down") || iconName.includes("expand_more")
      ) {
        const btn = closestDeep(icon, 'button, [role="button"], [role="combobox"], [aria-haspopup], mat-select') || icon.parentElement;
        if (btn && !isOwnExtensionElement(btn) && !isLinkOrPromoElement(btn) && isElementVisible(btn) && !isNavigationOrSidebar(btn)) {
          const btnTxt = String(extractControlLabel(btn) || btn.innerText || btn.textContent || "").trim().toLowerCase();
          if (/^(auto|hydrogen|gemini|carbon|argon|flash|pro)/i.test(btnTxt)) {
            return btn;
          }
        }
      }
    }

    return null;
  }

  function findAttachButton(inputArea, root) {
    const searchRoot = root || (typeof document !== "undefined" ? document : null);
    if (!searchRoot) return null;

    const buttons = queryDeep('button, [role="button"], md-icon-button, mwc-icon-button', searchRoot);
    const inputRect =
      inputArea && typeof inputArea.getBoundingClientRect === "function"
        ? inputArea.getBoundingClientRect()
        : null;

    let bestBtn = null;
    let bestScore = -1;

    for (const btn of buttons) {
      if (isOwnExtensionElement(btn) || isLinkOrPromoElement(btn)) continue;
      if (btn.disabled || (btn.getAttribute && btn.getAttribute("aria-disabled") === "true")) continue;
      const rect = typeof btn.getBoundingClientRect === "function" ? btn.getBoundingClientRect() : { width: 24, height: 24, top: 0, left: 0 };
      if (rect.width === 0 && rect.height === 0) continue;

      const aria = String((btn.getAttribute && btn.getAttribute("aria-label")) || "").toLowerCase();
      const title = String((btn.getAttribute && btn.getAttribute("title")) || "").toLowerCase();
      const testId = String((btn.getAttribute && btn.getAttribute("data-test-id")) || "").toLowerCase();
      const text = String(extractControlLabel(btn) || "").trim().toLowerCase();
      const combined = `${aria} ${title} ${testId} ${text}`;

      // Exclude send, mic, stop, new chat, menu, or model dropdown buttons
      if (
        /\b(send|submit|stop|microphone|voice|audio|new chat|close|delete|remove|sign out|account|settings)\b/.test(combined)
      ) {
        continue;
      }

      let score = 0;
      if (/\b(upload|attach|add file|add files|upload file|upload files|add context|add image|paperclip|attach_file|upload_file)\b/.test(combined)) {
        score += 100;
      } else if (/\b(add|more)\b/.test(combined) && (text === "+" || text === "add" || aria.includes("add"))) {
        score += 70;
      } else if (text === "+" || text === "add" || text === "attach_file" || text === "upload_file") {
        score += 60;
      }

      if (score <= 0) continue;

      // Prefer buttons visually close to the chat input bar
      if (inputRect && inputRect.height > 0 && rect.top > 0) {
        const dy = Math.abs(rect.top - inputRect.top);
        if (dy < 160) score += 50;
        else if (dy > 350) score -= 40;
      }

      if (score > bestScore) {
        bestScore = score;
        bestBtn = btn;
      }
    }

    return bestBtn;
  }

  function findUploadMenuOption(root, excludeEl) {
    const searchRoot = root || (typeof document !== "undefined" ? document : null);
    if (!searchRoot) return null;

    const items = queryDeep(
      '[role="menuitem"], [role="option"], [role="button"], button, li, md-menu-item, mat-list-item, [class*="menu-item"]',
      searchRoot
    );

    let bestItem = null;
    let bestScore = -1;

    for (const el of items) {
      if (isOwnExtensionElement(el) || isLinkOrPromoElement(el)) continue;
      if (excludeEl && (el === excludeEl || containsDeep(excludeEl, el))) continue;
      if (typeof el.getBoundingClientRect === "function") {
        const r = el.getBoundingClientRect();
        if (r.width === 0 && r.height === 0) continue;
      }
      const role = String((el.getAttribute && el.getAttribute("role")) || "").toLowerCase();
      const tag = String(el.tagName || "").toLowerCase();
      const aria = String((el.getAttribute && el.getAttribute("aria-label")) || "").toLowerCase();
      const text = String(extractControlLabel(el) || "").trim().toLowerCase();
      if (!text && !aria) continue;
      if (text.length > 120) continue;
      const combined = `${aria} ${text}`;

      // Avoid Google Drive / Cloud / URL pickers when we want local file upload
      if (/\b(google drive|from drive|drive|cloud storage|url|link|camera|photo library)\b/.test(combined)) {
        continue;
      }

      let score = 0;
      if (/\b(upload files?|upload from (?:computer|device)|from (?:computer|device)|local files?)\b/.test(combined)) {
        score = 120;
      } else if (/\badd files?\b/.test(combined)) {
        score = 80;
      } else if (/\bupload\b/.test(combined)) {
        score = 70;
      } else if (/\b(attach|choose file|browse)\b/.test(combined)) {
        score = 50;
      }

      if (score <= 0) continue;

      if (role === "menuitem" || role === "option" || tag === "li" || tag.includes("menu-item") || tag.includes("list-item")) {
        score += 40;
      }

      if (score > bestScore) {
        bestScore = score;
        bestItem = el;
      }
    }

    return bestItem;
  }

  function isExternalLinkElement(el) {
    if (!el || el.nodeType !== 1) return false;
    const target = String((el.getAttribute && el.getAttribute("target")) || "").toLowerCase();
    if (target === "_blank") return true;
    const href = String((el.getAttribute && el.getAttribute("href")) || "").trim();
    if (!href) return false;
    if (/^https?:\/\//i.test(href)) {
      try {
        if (typeof location !== "undefined" && location && location.hostname) {
          const parsed = new URL(href);
          if (parsed.hostname && parsed.hostname !== location.hostname) {
            return true;
          }
        } else {
          // Outside browser (unit tests): reject known external promo links
          if (/blog\.google|support\.google/i.test(href)) return true;
        }
      } catch (e) {
        return true;
      }
    }
    return false;
  }

  function findNewChatButton(root) {
    const searchRoot = root || (typeof document !== "undefined" ? document : null);
    if (!searchRoot) return null;

    const candidates = queryDeep('button, a, [role="button"], [role="menuitem"]', searchRoot);
    let best = null;
    let bestScore = 0;

    for (const el of candidates) {
      if (isOwnExtensionElement(el)) continue;
      if (el.disabled || (el.getAttribute && el.getAttribute("aria-disabled") === "true")) continue;
      if (isExternalLinkElement(el)) continue;
      if (closestDeep(el, "message-content, model-response, [class*='promo'], [class*='announcement'], [class*='banner']")) {
        continue;
      }

      const aria = String((el.getAttribute && el.getAttribute("aria-label")) || "").trim().toLowerCase();
      const title = String((el.getAttribute && el.getAttribute("title")) || "").trim().toLowerCase();
      const tooltip = String((el.getAttribute && el.getAttribute("data-tooltip")) || "").trim().toLowerCase();
      const testId = String((el.getAttribute && el.getAttribute("data-test-id")) || "").trim().toLowerCase();
      const text = String(extractControlLabel(el) || "").trim().toLowerCase();
      const combined = `${aria} ${title} ${tooltip} ${testId} ${text}`.trim();

      if (!combined || text.length > 60) continue;
      if (/\b(delete|remove|rename|share|export|pin|archive|settings|help|sign out|logout)\b/.test(combined)) {
        continue;
      }

      let score = 0;
      if (
        /^(?:start\s+a?\s*)?new\s+(?:chat|conversation)$/.test(aria) ||
        /^(?:start\s+a?\s*)?new\s+(?:chat|conversation)$/.test(title) ||
        /^(?:start\s+a?\s*)?new\s+(?:chat|conversation)$/.test(text) ||
        /^(?:新对话|新建对话|开启新对话|新聊天|开始新对话)$/.test(aria) ||
        /^(?:新对话|新建对话|开启新对话|新聊天|开始新对话)$/.test(text) ||
        testId.includes("new-chat") ||
        testId.includes("new_chat") ||
        testId.includes("new-conversation")
      ) {
        score = 130;
      } else if (/\bnew\s+(?:chat|conversation)\b/.test(aria) || /\bnew\s+(?:chat|conversation)\b/.test(title) || /\bnew\s+(?:chat|conversation)\b/.test(tooltip)) {
        score = 100;
      } else if (/\bnew\s+(?:chat|conversation)\b/.test(text) || /(?:新对话|新建对话|开启新对话|新聊天)/.test(combined)) {
        score = 80;
      } else if (
        (/\bedit_square\b/.test(text) || /\badd_comment\b/.test(text)) &&
        closestDeep(el, "nav, aside, header, [class*='side'], [class*='rail'], [class*='drawer'], [class*='nav']")
      ) {
        score = 60;
      }

      if (score <= 0) continue;

      if (closestDeep(el, "nav, aside, header, [class*='side'], [class*='rail'], [class*='drawer'], [class*='nav']")) {
        score += 15;
      }

      if (score > bestScore) {
        bestScore = score;
        best = el;
      }
    }

    return best;
  }

  function findSidebarToggleButton(root) {
    const searchRoot = root || (typeof document !== "undefined" ? document : null);
    if (!searchRoot) return null;

    const candidates = queryDeep('button, [role="button"]', searchRoot);
    for (const el of candidates) {
      if (isOwnExtensionElement(el)) continue;
      if (el.disabled || (el.getAttribute && el.getAttribute("aria-disabled") === "true")) continue;
      const aria = String((el.getAttribute && el.getAttribute("aria-label")) || "").trim().toLowerCase();
      const title = String((el.getAttribute && el.getAttribute("title")) || "").trim().toLowerCase();
      const combined = `${aria} ${title}`.trim();
      if (/\b(main menu|open menu|expand menu|open sidebar|expand sidebar|navigation menu|toggle sidebar|show sidebar)\b/.test(combined)) {
        return el;
      }
    }
    return null;
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
      extractActiveDomResponseText,
      base64ToUint8Array,
      deduplicateAttachments,
      buildAttachedFileNote,
      countAttachmentChipsInDom,
      hasAttachmentChipInDom,
      isSameConversationUrlTransition,
      isLinkOrPromoElement,
      isRateLimitMessage,
      findModelOption,
      findModelTriggerElement,
      findAttachButton,
      findUploadMenuOption,
      findNewChatButton,
      findSidebarToggleButton
    };
    return;
  }

  // ---------------------------------------------------------------------------
  // Live Browser Bridge Runtime
  // ---------------------------------------------------------------------------

  if (!globalScope || globalScope.__web2apiBridgeInstalled) return;
  globalScope.__web2apiBridgeInstalled = true;

  const CLIENT_ID = `tab_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
  console.log("[Web2API Bridge] Initializing live web bridge on", location.hostname, "client:", CLIENT_ID);

  let isPolling = false;
  let activeReqId = null;
  let lastFocusedInput = null;
  let autoRefreshEnabled = false;
  let currentActiveStream = null;
  let activeNetCapture = null;
  let convGen = 1;
  let lastConvUrlKey = `${location.pathname}${location.search}`;
  let hadResponsesInConv = false;
  let hadDomResponsesInConv = false;
  let preferredUploadMethod = null;

  const uploadTracker = {
    inFlightIds: new Set(),
    startedCount: 0,
    doneCount: 0,
    errorCount: 0,
    injectedArmIds: new Set()
  };

  function syncConversationGeneration() {
    const curUrlKey = `${location.pathname}${location.search}`;
    if (curUrlKey !== lastConvUrlKey) {
      const sameConv = isSameConversationUrlTransition(lastConvUrlKey, curUrlKey);
      lastConvUrlKey = curUrlKey;
      // Only treat URL navigation as a fresh conversation if we already saw replies
      // AND the URL moved away from this conversation (not initial session-ID assignment
      // like /home/cid/<id> -> /home/cid/<id>/r/session/<sid> or query params like ?pli=1).
      if (hadResponsesInConv && !sameConv) {
        convGen++;
        hadResponsesInConv = false;
        hadDomResponsesInConv = false;
        console.log("[Web2API Bridge] Conversation URL changed to new chat -> conv_gen:", convGen);
      }
      return;
    }

    // Detect SPA "New Chat" reset ONLY if DOM responses were previously visible in this chat
    // (avoids false New Chat resets on Vertex AI Search when replies come via net-hook.js
    // and DOM selectors don't match custom Shadow DOM response components).
    if (hadResponsesInConv && hadDomResponsesInConv && !activeReqId) {
      try {
        const curResponses = findResponseElements(lastFocusedInput, "");
        if (curResponses.length === 0) {
          convGen++;
          hadResponsesInConv = false;
          hadDomResponsesInConv = false;
          console.log("[Web2API Bridge] Detected New Chat reset -> conv_gen:", convGen);
        }
      } catch (e) {}
    }
  }

  // Listen for transparent network stream & upload events from net-hook.js (MAIN world)
  window.addEventListener("message", (event) => {
    const data = event && event.data;
    if (!data || data[NET_MSG_KEY] !== true) return;

    const { type, streamId, url } = data;

    // Upload telemetry & click-trap confirmation (active before prompt submission)
    if (type === "upload_start") {
      if (streamId) uploadTracker.inFlightIds.add(streamId);
      uploadTracker.startedCount++;
      console.log("[Web2API Bridge] File upload started:", url);
      return;
    }
    if (type === "upload_done") {
      if (streamId) uploadTracker.inFlightIds.delete(streamId);
      uploadTracker.doneCount++;
      console.log("[Web2API Bridge] File upload completed:", url);
      return;
    }
    if (type === "upload_error") {
      if (streamId) uploadTracker.inFlightIds.delete(streamId);
      uploadTracker.errorCount++;
      console.warn("[Web2API Bridge] File upload error:", data.error || data.status, url);
      return;
    }
    if (type === "upload_injected") {
      if (streamId) uploadTracker.injectedArmIds.add(streamId);
      console.log("[Web2API Bridge] File input injected via", data.method, "count:", data.count);
      return;
    }

    const cap = activeNetCapture;
    if (!cap) return;

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
      cap.rateLimited = Boolean(data.rateLimited || isRateLimitMessage(data.error));
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

  async function ensureModelSelected(requestedModel) {
    const target = resolveModelKeywords(requestedModel);
    if (!target) return false;

    const trigger = findModelTriggerElement();
    if (!trigger) {
      // Never blindly click elements on the page when no model dropdown trigger exists
      return false;
    }

    const currentText = String(extractControlLabel(trigger) || trigger.innerText || trigger.textContent || "").trim().toLowerCase();
    if (target.keywords.some((kw) => currentText.includes(kw))) {
      console.log("[Web2API Bridge] Target model already selected:", currentText);
      if (target.family === "hydrogen") {
        modelBtn.textContent = "✅ Hydrogen";
      }
      return true;
    }

    // 1. If a dropdown menu overlay is already open with the option visible, click it
    let item = findModelOption(target, trigger);
    if (item && isVisible(item) && !isLinkOrPromoElement(item)) {
      console.log("[Web2API Bridge] Dropdown already open, clicking model option:", item);
      simulateClick(item);
      await new Promise((r) => setTimeout(r, 250));
      if (target.family === "hydrogen") modelBtn.textContent = "✅ Hydrogen";
      return true;
    }

    // 2. Open dropdown via verified model trigger button (never an <a> link or banner)
    console.log("[Web2API Bridge] Opening model dropdown via trigger:", trigger);
    simulateClick(trigger);
    await new Promise((r) => setTimeout(r, 250));

    const start = Date.now();
    while (Date.now() - start < 2500) {
      item = findModelOption(target, trigger);
      if (item && isVisible(item) && !isLinkOrPromoElement(item)) {
        console.log("[Web2API Bridge] Found model menu option, clicking:", item);
        simulateClick(item);
        await new Promise((r) => setTimeout(r, 250));
        if (target.family === "hydrogen") modelBtn.textContent = "✅ Hydrogen";
        return true;
      }
      await new Promise((r) => setTimeout(r, 100));
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
  // 2b. Direct Multi-Tier File Upload (PDFs, Office Docs, Images)
  // ---------------------------------------------------------------------------

  function buildDataTransferFromAttachments(attachments) {
    const dt = new DataTransfer();
    for (const att of attachments || []) {
      if (!att || !att.data_base64) continue;
      const bytes = base64ToUint8Array(att.data_base64);
      if (bytes.length === 0) continue;
      const file = new File([bytes], att.filename || "attachment.bin", {
        type: att.mime || "application/octet-stream",
        lastModified: Date.now()
      });
      dt.items.add(file);
    }
    return dt;
  }

  function findBestFileInput(inputArea) {
    const inputs = queryDeep('input[type="file"]').filter((el) => !isOwnExtensionElement(el));
    if (inputs.length === 0) return null;

    let best = inputs[0];
    let bestScore = -1;
    const composerForm = inputArea ? closestDeep(inputArea, "form") : null;

    for (const inp of inputs) {
      let score = 10;
      if (composerForm && containsDeep(composerForm, inp)) score += 60;
      if (inp.multiple) score += 20;
      const accept = String(inp.getAttribute("accept") || "").toLowerCase();
      if (!accept || accept.includes("pdf") || accept.includes("*")) score += 15;
      if (score > bestScore) {
        bestScore = score;
        best = inp;
      }
    }
    return best;
  }

  function checkUploadTriggerSignal(filenames, inputArea, snap) {
    if (uploadTracker.startedCount > snap.startedCount) {
      return "network";
    }
    if (snap.armId && uploadTracker.injectedArmIds.has(snap.armId)) {
      return "injected";
    }
    const curChips = countAttachmentChipsInDom(inputArea);
    if (
      curChips.chipCount > snap.beforeChips.chipCount ||
      curChips.removeBtnCount > snap.beforeChips.removeBtnCount
    ) {
      return "chip_count";
    }
    if (!snap.beforeMatchedChip && hasAttachmentChipInDom(filenames, inputArea)) {
      return "chip";
    }
    return null;
  }

  async function waitForUploadTrigger(filenames, inputArea, snap, timeoutMs) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      const sig = checkUploadTriggerSignal(filenames, inputArea, snap);
      if (sig) return sig;
      await new Promise((r) => setTimeout(r, 90));
    }
    return null;
  }

  async function waitForUploadsReady(filenames, inputArea, snap) {
    // Allow brief window after injection for upload POST request to kick off
    const kickDeadline = Date.now() + 1800;
    while (
      Date.now() < kickDeadline &&
      uploadTracker.startedCount === snap.startedCount &&
      !checkUploadTriggerSignal(filenames, inputArea, snap)
    ) {
      await new Promise((r) => setTimeout(r, 100));
    }

    // If network upload started, wait until all in-flight uploads finish (up to 35s for large PDFs)
    if (uploadTracker.startedCount > snap.startedCount) {
      const netDeadline = Date.now() + 35000;
      while (Date.now() < netDeadline) {
        const finished = uploadTracker.doneCount > snap.doneCount || uploadTracker.errorCount > snap.errorCount;
        if (uploadTracker.inFlightIds.size === 0 && finished) {
          break;
        }
        await new Promise((r) => setTimeout(r, 150));
      }
      if (uploadTracker.errorCount > snap.errorCount && uploadTracker.doneCount === snap.doneCount) {
        return false;
      }
    }

    // Wait briefly for UI chip / send button to settle
    const chipDeadline = Date.now() + 3000;
    while (Date.now() < chipDeadline) {
      const curChips = countAttachmentChipsInDom(inputArea);
      if (
        curChips.chipCount > snap.beforeChips.chipCount ||
        curChips.removeBtnCount > snap.beforeChips.removeBtnCount ||
        (!snap.beforeMatchedChip && hasAttachmentChipInDom(filenames, inputArea))
      ) {
        break;
      }
      await new Promise((r) => setTimeout(r, 120));
    }
    await new Promise((r) => setTimeout(r, 450));
    return true;
  }

  async function uploadAttachmentsToPage(attachments, inputArea) {
    const uniqueAttachments = deduplicateAttachments(attachments);
    if (uniqueAttachments.length === 0) {
      return { ok: false, method: "none" };
    }

    const filenames = uniqueAttachments.map((a) => (a && a.filename) || "attachment");
    const dt = buildDataTransferFromAttachments(uniqueAttachments);
    if (dt.files.length === 0) {
      return { ok: false, method: "empty" };
    }

    const snap = {
      startedCount: uploadTracker.startedCount,
      doneCount: uploadTracker.doneCount,
      errorCount: uploadTracker.errorCount,
      beforeChips: countAttachmentChipsInDom(inputArea),
      beforeMatchedChip: hasAttachmentChipInDom(filenames, inputArea),
      armId: null
    };

    // Prioritize whichever upload tier already succeeded on this site to avoid probing
    // failing tiers (and prevent any chance of double-uploading across tiers).
    const defaultTiers = ["file_input", "drag_drop", "click_trap"];
    const tierOrder = preferredUploadMethod
      ? [preferredUploadMethod, ...defaultTiers.filter((t) => t !== preferredUploadMethod)]
      : defaultTiers;

    for (const tier of tierOrder) {
      // Safety abort: if a prior attempt already started uploading or added a chip, NEVER try another tier!
      if (checkUploadTriggerSignal(filenames, inputArea, snap)) {
        const ready = await waitForUploadsReady(filenames, inputArea, snap);
        return { ok: ready, method: preferredUploadMethod || "detected" };
      }

      if (tier === "file_input") {
        // Tier 1: Direct assignment to an existing <input type="file"> in DOM / Shadow DOM
        const existingInput = findBestFileInput(inputArea);
        if (!existingInput) continue;
        try {
          // Clear previous value so re-uploading an updated file with the same name still fires `change`
          try { existingInput.value = ""; } catch (e) {}
          const freshDt = buildDataTransferFromAttachments(uniqueAttachments);
          existingInput.files = freshDt.files;
          // Dispatch ONLY `change` (firing both `input` and `change` triggers duplicate uploads
          // on Angular/Lit components that listen to both events).
          existingInput.dispatchEvent(new Event("change", { bubbles: true, composed: true }));

          const triggered = await waitForUploadTrigger(filenames, inputArea, snap, 1600);
          if (triggered) {
            const ready = await waitForUploadsReady(filenames, inputArea, snap);
            if (ready) {
              preferredUploadMethod = "file_input";
              console.log("[Web2API Bridge] Uploaded attachment(s) via Tier 1 (<input type=file>, signal:", triggered + ")");
              return { ok: true, method: "file_input" };
            }
            // If the page already accepted the files (chip or upload started), never cascade into Tier 2/3
            // to avoid duplicating files.
            return { ok: false, method: "file_input_error" };
          }
          // Tier 1 produced zero signal — clear input so a dormant FileList cannot double-submit later
          try { existingInput.value = ""; } catch (e) {}
        } catch (e) {
          console.warn("[Web2API Bridge] Tier 1 file input error:", e);
        }
      } else if (tier === "drag_drop") {
        // Tier 2: Single synthetic Drag-and-Drop onto ONE target (bubbles naturally to composer/form;
        // dispatching on both inputArea AND container caused duplicate drop handling!).
        try {
          const dropTarget =
            inputArea ||
            closestDeep(
              inputArea,
              "form, [class*='composer'], [class*='input-area'], [class*='chat-input'], [class*='prompt'], main"
            );
          if (!dropTarget) continue;

          const freshDt = buildDataTransferFromAttachments(uniqueAttachments);
          for (const evName of ["dragenter", "dragover", "drop"]) {
            let ev;
            try {
              ev = new DragEvent(evName, {
                bubbles: true,
                cancelable: true,
                composed: true,
                dataTransfer: freshDt
              });
            } catch (err) {
              ev = new Event(evName, { bubbles: true, cancelable: true, composed: true });
              Object.defineProperty(ev, "dataTransfer", { value: freshDt });
            }
            dropTarget.dispatchEvent(ev);
          }

          const triggered = await waitForUploadTrigger(filenames, inputArea, snap, 1600);
          if (triggered) {
            const ready = await waitForUploadsReady(filenames, inputArea, snap);
            if (ready) {
              preferredUploadMethod = "drag_drop";
              console.log("[Web2API Bridge] Uploaded attachment(s) via Tier 2 (drag-and-drop, signal:", triggered + ")");
              return { ok: true, method: "drag_drop" };
            }
            return { ok: false, method: "drag_drop_error" };
          }
        } catch (e) {
          console.warn("[Web2API Bridge] Tier 2 drag-drop error:", e);
        }
      } else if (tier === "click_trap") {
        // Tier 3: Armed MAIN-world click trap + clicking the page's '+' / Attach / Upload button
        const attachBtn = findAttachButton(inputArea);
        if (!attachBtn) continue;
        const armId = `arm_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`;
        snap.armId = armId;
        try {
          window.postMessage(
            {
              __web2api_upload_arm: true,
              armId,
              files: uniqueAttachments
            },
            "*"
          );
          await new Promise((r) => setTimeout(r, 60));

          simulateClick(attachBtn);
          let triggered = await waitForUploadTrigger(filenames, inputArea, snap, 700);

          if (!triggered) {
            // Clicking '+' likely opened a popover menu ("Upload files" / "Upload from computer")
            const menuOpt = findUploadMenuOption(document, attachBtn);
            if (menuOpt) {
              simulateClick(menuOpt);
              triggered = await waitForUploadTrigger(filenames, inputArea, snap, 1200);
            }
          }

          if (triggered) {
            const ready = await waitForUploadsReady(filenames, inputArea, snap);
            if (ready) {
              preferredUploadMethod = "click_trap";
              console.log("[Web2API Bridge] Uploaded attachment(s) via Tier 3 (attach button + click trap, signal:", triggered + ")");
              return { ok: true, method: "click_trap" };
            }
            return { ok: false, method: "click_trap_error" };
          }
        } catch (e) {
          console.warn("[Web2API Bridge] Tier 3 click-trap upload error:", e);
        } finally {
          window.postMessage({ __web2api_upload_disarm: true, armId }, "*");
          closeAnyOpenDropdown();
        }
      }
    }

    console.warn("[Web2API Bridge] All file upload tiers did not confirm upload for:", filenames.join(", "));
    return { ok: false, method: "failed" };
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
      rateLimited: false,
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

  async function startFreshChatOnPage() {
    const beforeUrl = `${location.pathname}${location.search}`;
    let beforeResponses = 0;
    try {
      beforeResponses = findResponseElements(lastFocusedInput, "").length;
    } catch (e) {}

    let newChatBtn = findNewChatButton(document);
    if (!newChatBtn) {
      const toggleBtn = findSidebarToggleButton(document);
      if (toggleBtn) {
        simulateClick(toggleBtn);
        await new Promise((r) => setTimeout(r, 320));
        newChatBtn = findNewChatButton(document);
      }
    }

    let triggered = false;
    if (newChatBtn) {
      console.log("[Web2API Bridge] Starting fresh Gemini chat via New Chat button...");
      simulateClick(newChatBtn);
      triggered = true;
    } else if (location.hostname.includes("gemini.google.com")) {
      // Fallback: Gemini Web's built-in Ctrl/Cmd+Shift+O New Chat shortcut
      console.log("[Web2API Bridge] Starting fresh Gemini chat via Shift+O shortcut...");
      const isMac = /mac/i.test(navigator.platform || "");
      const evOpts = {
        key: "O",
        code: "KeyO",
        shiftKey: true,
        ctrlKey: !isMac,
        metaKey: isMac,
        bubbles: true,
        cancelable: true
      };
      document.dispatchEvent(new KeyboardEvent("keydown", evOpts));
      document.dispatchEvent(new KeyboardEvent("keyup", evOpts));
      triggered = true;
    }

    if (triggered) {
      const deadline = Date.now() + 2400;
      const prevInput = lastFocusedInput;
      while (Date.now() < deadline) {
        const curUrl = `${location.pathname}${location.search}`;
        if (curUrl !== beforeUrl) break;
        if (prevInput && !containsDeep(document, prevInput)) break;
        try {
          if (beforeResponses > 0 && findResponseElements(null, "").length < beforeResponses) {
            break;
          }
        } catch (e) {}
        await new Promise((r) => setTimeout(r, 100));
      }
      await new Promise((r) => setTimeout(r, 350));
    } else {
      console.warn("[Web2API Bridge] Could not locate New Chat button on page; continuing on current view.");
    }

    // Reset local input/URL trackers so the new chat starts cleanly without
    // bumping convGen mid-request (the server resets session state via resetSession).
    lastFocusedInput = null;
    lastConvUrlKey = `${location.pathname}${location.search}`;
    hadResponsesInConv = false;
    hadDomResponsesInConv = false;
    return triggered;
  }

  async function handlePromptRequest(reqId, prompt, model, attachments, newChat) {
    // 0. If WorkBuddy/client started a brand-new chat, open a fresh Gemini chat first
    // so previous conversation memory & files never bleed into the new chat!
    if (newChat) {
      setStatus("busy", "Web2API: Starting fresh chat... ✨");
      try {
        await startFreshChatOnPage();
      } catch (e) {
        console.warn("[Web2API Bridge] Error starting fresh chat:", e);
      }
    }

    // A. Switch model dropdown if the requested model specifies a known family
    try {
      await ensureModelSelected(model);
    } catch (e) {
      console.warn("[Web2API Bridge] Error selecting model:", e);
    }

    // B. Locate Prompt Input Area
    const inputArea = await findChatInput(4000);

    // B2. Directly upload any attached PDFs/documents/images to the Gemini page
    let effectivePrompt = prompt;
    const uniqueAttachments = deduplicateAttachments(attachments);
    if (uniqueAttachments.length > 0) {
      const label = uniqueAttachments.map((a) => (a && a.filename) || "file").join(", ");
      setStatus("busy", `Web2API: Uploading ${label}... 📎`);
      try {
        const uploadRes = await uploadAttachmentsToPage(uniqueAttachments, inputArea);
        if (uploadRes && uploadRes.ok) {
          const note = buildAttachedFileNote(uniqueAttachments);
          if (note) {
            effectivePrompt = `${note}\n\n${prompt}`;
          }
          setStatus("busy", `Web2API: Attached ${label} ✓`);
        } else {
          console.warn("[Web2API Bridge] Proceeding with text-only prompt after upload fallback.");
        }
      } catch (e) {
        console.warn("[Web2API Bridge] Attachment upload threw error:", e);
      }
    }

    // Snapshot exact pre-existing response elements BEFORE submitting
    const baselineMap = snapshotResponses(inputArea);

    // Register network capture BEFORE submitting so we never miss synchronous start/delta
    const netCapture = createNetCapture();
    activeNetCapture = netCapture;

    try {
      setInputValue(inputArea, effectivePrompt);
      // Small humanized jitter before clicking Send to avoid robotic fixed timing
      await new Promise((r) => setTimeout(r, 220 + Math.floor(Math.random() * 280)));

      const submitSuccess = await submitPrompt(inputArea, netCapture);
      console.log("[Web2API Bridge] submitPrompt result:", submitSuccess, "netStarted:", netCapture.started);

      // C. Watch for Streaming Response (Network primary, DOM fallback)
      await observeResponseStream(reqId, inputArea, effectivePrompt, baselineMap, netCapture);
      lastConvUrlKey = `${location.pathname}${location.search}`;
      hadResponsesInConv = true;
      try {
        if (findResponseElements(inputArea, "").length > 0) {
          hadDomResponsesInConv = true;
        }
      } catch (e) {}
    } finally {
      if (activeNetCapture === netCapture) {
        activeNetCapture = null;
      }
    }

    // D. Auto-Refresh page after response is delivered (if enabled)
    if (autoRefreshEnabled) {
      convGen++;
      hadResponsesInConv = false;
      hadDomResponsesInConv = false;
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

      function enqueueChunk(text, isDone, err, rateLimited) {
        chunkQueue = chunkQueue.then(async () => {
          if (abortedByServer) return;
          const res = await sendChunk(reqId, text, isDone, err, rateLimited);
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

      function finish(explicitError, explicitRateLimited) {
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
          const errMsg = explicitError || netCapture.error || "Model did not respond. Please verify the prompt was sent on the page or refresh.";
          const rateLimited = Boolean(explicitRateLimited || netCapture.rateLimited || isRateLimitMessage(errMsg));
          if (rateLimited) {
            setStatus("disconnected", "Web2API: Rate limit detected — cooling down 🛡️");
          }
          console.warn("[Web2API Bridge] Completing with error:", errMsg, "rateLimited:", rateLimited);
          enqueueChunk("", true, errMsg, rateLimited).then(() => resolve()).catch(() => resolve());
          return;
        }

        console.log(
          `[Web2API Bridge] Response completed (${sentText.length} chars, mode=${netCapture.receivedText ? "network" : "dom"})`
        );
        enqueueChunk("", true, "", false).then(() => resolve()).catch(() => resolve());
      }

      function tick() {
        if (completed || abortedByServer) return;

        // Immediately trip circuit breaker if Google returned HTTP 429 / rate limit
        if (netCapture.rateLimited && !netCapture.receivedText) {
          finish(netCapture.error || "HTTP 429 rate limited", true);
          return;
        }

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
          conv_gen: String(convGen),
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
        syncConversationGeneration();
        const tabParam = encodeURIComponent(location.hostname);
        const clientParam = encodeURIComponent(CLIENT_ID);
        const convParam = encodeURIComponent(String(convGen));
        const res = await fetch(
          `${BRIDGE_URL}/bridge/poll?tab=${tabParam}&client=${clientParam}&conv_gen=${convParam}`,
          {
            method: "GET",
            headers: { Accept: "application/json" }
          }
        );

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
            await handlePromptRequest(data.id, data.prompt, data.model, data.attachments, Boolean(data.new_chat));
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

  async function sendChunk(id, chunkText, isDone, err, rateLimited) {
    try {
      const payload = {
        id: id,
        text: chunkText,
        done: isDone,
        error: err || ""
      };
      if (rateLimited) {
        payload.rate_limited = true;
      }
      const res = await fetch(`${BRIDGE_URL}/bridge/chunk`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
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

  // Note: Model switching happens on-demand right before submitting a request (or via the
  // badge button), never via unsolicited page-load clicks that could trigger banners/links.
  pollServer();
})(typeof window !== "undefined" ? window : globalThis);
