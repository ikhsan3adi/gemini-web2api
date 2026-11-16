// Gemini Web2API Live Web Bridge Content Script
// Supports both gemini.google.com and vertexaisearch.cloud.google.com (Enterprise Vertex AI Search)

(function () {
  console.log("[Web2API Bridge] Initializing live web bridge on", location.hostname);

  const BRIDGE_URL = "http://localhost:8081";
  let isPolling = false;
  let activeReqId = null;
  let lastFocusedInput = null;
  let autoRefreshEnabled = false; // Default false to prevent aborting back-to-back follow-up requests
  let currentActiveStream = null;

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
    if (isEditable(e.target)) {
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

  // 1. In-Page Floating Control Widget
  const badgeContainer = document.createElement("div");
  badgeContainer.id = "web2api-bridge-badge";
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
      // Force complete current stream with whatever is on page before reloading
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
      statusText.textContent = msg || `Web2API: Ready (:8081)`;
    } else if (status === "busy") {
      dot.style.background = "#f59e0b";
      statusText.textContent = msg || `Web2API: Generating...`;
    } else {
      dot.style.background = "#ef4444";
      statusText.textContent = msg || `Web2API: Disconnected`;
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

  // 2. Deep Tree Search for Inputs & Elements (Shadow DOM and iframes)
  function queryDeep(selector, root = document) {
    const results = [];

    function search(node) {
      if (!node) return;
      if (node.querySelectorAll) {
        try {
          const matched = node.querySelectorAll(selector);
          matched.forEach((m) => results.push(m));
        } catch (e) {}
      }

      // Traverse shadow roots
      const children = node.children || [];
      for (let i = 0; i < children.length; i++) {
        const child = children[i];
        if (child.shadowRoot) {
          search(child.shadowRoot);
        }
        search(child);
      }

      // Check iframes
      if (node.tagName === "IFRAME") {
        try {
          if (node.contentDocument) {
            search(node.contentDocument);
          }
        } catch (e) {}
      }
    }

    search(root);
    return results;
  }

  function extractDeepText(node) {
    if (!node) return "";
    if (node.nodeType === Node.TEXT_NODE) {
      return node.textContent || "";
    }

    // Try innerText first
    let t = "";
    try {
      t = node.innerText || "";
    } catch (e) {}
    if (t && t.trim()) return t;

    // If node has shadowRoot, traverse into shadowRoot
    if (node.shadowRoot) {
      const st = extractDeepText(node.shadowRoot);
      if (st && st.trim()) return st;
    }

    // If node has childNodes, traverse recursively
    const children = node.childNodes || [];
    let combined = "";
    for (let i = 0; i < children.length; i++) {
      combined += extractDeepText(children[i]);
    }
    if (combined && combined.trim()) return combined;

    return node.textContent || "";
  }

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
      'mat-form-field textarea',
      'mat-form-field input',
      'textarea[matinput]',
      'input[matinput]',
      '[role="searchbox"]',
      '[role="textbox"]',
      'textarea',
      'input[type="text"]:not([readonly])',
      'input[type="search"]:not([readonly])',
      'input:not([type]):not([readonly])'
    ];

    while (Date.now() - start < maxWaitMs) {
      // 1. Check last focused element
      if (lastFocusedInput && isEditable(lastFocusedInput) && document.contains(lastFocusedInput)) {
        return lastFocusedInput;
      }

      // 2. Check activeElement
      if (isEditable(document.activeElement)) {
        return document.activeElement;
      }

      // 3. Search selectors
      for (const sel of inputSelectors) {
        const found = queryDeep(sel);
        for (const el of found) {
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

  function isNavigationOrSidebar(el, inputEl) {
    if (!el) return false;
    // If element is inside the same input container, bottom bar, or chat box, it is NOT navigation!
    if (inputEl) {
      const chatContainer = inputEl.closest("form, .bottom-bar, .input-area, .chat-box, .input-container, discovery-engine-chat, mat-form-field");
      if (chatContainer && chatContainer.contains(el)) {
        return false;
      }
    }
    return !!el.closest("nav, aside.left-nav, mat-sidenav.sidebar-nav, .side-nav, [role='navigation'], [aria-label*='sidebar' i]");
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

  function findModelTriggerElement() {
    // Strategy 1: Look next to the mic icon (In Vertex AI Search, Auto is right next to mic!)
    const micIcons = queryDeep('mat-icon, [data-mat-icon-name="mic"], button[aria-label*="mic" i], [data-icon*="mic"]');
    for (const mic of micIcons) {
      if (isNavigationOrSidebar(mic)) continue;
      const t = (mic.innerText || mic.textContent || "").trim().toLowerCase();
      const n = (mic.getAttribute("data-mat-icon-name") || "").toLowerCase();
      if (t === "mic" || n === "mic") {
        const micBtn = mic.closest('button, [role="button"]') || mic;
        let sib = micBtn.previousElementSibling;
        while (sib) {
          const sText = (sib.innerText || sib.textContent || "").trim().toLowerCase();
          if (/auto|hydrogen|gemini|carbon|argon|flash|pro/i.test(sText)) {
            return sib.closest('button, [role="button"], [aria-haspopup], mat-select, div') || sib;
          }
          sib = sib.previousElementSibling;
        }
      }
    }

    // Strategy 2: Explicit Angular Material / MDC menu trigger or button with exact "Auto" or model name
    const candidates = queryDeep('button, [role="button"], [role="combobox"], [aria-haspopup], mat-select, .mat-mdc-menu-trigger, [class*="model-selector"], [class*="model-picker"]');
    for (const el of candidates) {
      if (isNavigationOrSidebar(el)) continue;
      const t = (el.innerText || el.textContent || "").trim();
      // Strictly match "Auto", "Auto keyboard_arrow_down", "Auto ∨", "Hydrogen", etc.
      // Must NOT match broad containers like .bottom-bar that have multiple buttons!
      if (/^(auto|hydrogen|gemini|gemini\s*4\s*hydrogen|carbon|argon|flash|pro)(\s+(keyboard_arrow_down|expand_more|arrow_drop_down|[\u25bc\u25bd\u2228\u2304v\u25be]))?$/i.test(t)) {
        return el;
      }
    }

    // Strategy 3: Look for dropdown arrow icon near text "Auto"
    const arrowIcons = queryDeep('mat-icon, svg, [data-icon]');
    for (const icon of arrowIcons) {
      if (isNavigationOrSidebar(icon)) continue;
      const iconTxt = (icon.innerText || icon.textContent || "").trim().toLowerCase();
      const iconName = (icon.getAttribute("data-mat-icon-name") || "").toLowerCase();
      if (
        iconTxt === "keyboard_arrow_down" || iconTxt === "expand_more" || iconTxt === "arrow_drop_down" ||
        iconName.includes("arrow_down") || iconName.includes("expand_more")
      ) {
        const btn = icon.closest('button, [role="button"], [aria-haspopup], mat-select') || icon.parentElement;
        if (btn && isVisible(btn) && !isNavigationOrSidebar(btn)) {
          const btnTxt = (btn.innerText || btn.textContent || "").trim().toLowerCase();
          if (/^(auto|hydrogen|gemini|carbon|argon|flash|pro)/i.test(btnTxt)) {
            return btn;
          }
        }
      }
    }

    // Strategy 4: Leaf element with exact text "Auto"
    const allEls = queryDeep('button, [role="button"], span, div');
    for (const el of allEls) {
      if (isNavigationOrSidebar(el)) continue;
      if (el.children.length <= 1) {
        const t = (el.innerText || el.textContent || "").trim();
        if (/^auto$/i.test(t)) {
          return el.closest('button, [role="button"], [aria-haspopup], mat-select') || el;
        }
      }
    }

    return null;
  }

  function findModelOptionInDOM(targetKeywords, excludeEl) {
    const allEls = queryDeep("*");
    for (const el of allEls) {
      if (isNavigationOrSidebar(el)) continue;
      // Skip large container elements; match leaf or near-leaf menu items
      if (el.children.length > 3) continue;

      const txt = (el.innerText || el.textContent || "").trim();
      if (!txt) continue;

      const lower = txt.toLowerCase();
      if (targetKeywords.some((kw) => lower.includes(kw))) {
        const clickable = el.closest("button, [role='button'], [role='menuitem'], [role='option'], li, div") || el;
        if (excludeEl && (clickable === excludeEl || excludeEl.contains(clickable))) {
          continue;
        }
        if (isVisible(clickable)) {
          return clickable;
        }
      }
    }
    return null;
  }

  // Automatically switch the model dropdown (e.g. "Auto ∨") to Gemini 4 Hydrogen
  async function ensureModelSelected(requestedModel) {
    if (!requestedModel) requestedModel = "hydrogen";
    const targetKeywords = ["hydrogen", "gemini 4 hydrogen", "gemini-4-hydrogen", "4 hydrogen"];
    if (/flash/i.test(requestedModel)) {
      targetKeywords.length = 0;
      targetKeywords.push("flash");
    } else if (/pro/i.test(requestedModel)) {
      targetKeywords.length = 0;
      targetKeywords.push("pro");
    }

    const trigger = findModelTriggerElement();
    if (trigger) {
      const currentText = (trigger.innerText || trigger.textContent || "").trim().toLowerCase();
      if (targetKeywords.some((kw) => currentText.includes(kw))) {
        console.log("[Web2API Bridge] Hydrogen model is already selected on trigger:", currentText);
        modelBtn.textContent = "✅ Hydrogen";
        return true;
      }
    }

    // Check if dropdown is already open right now (e.g. from user click or previous attempt)
    let option = findModelOptionInDOM(targetKeywords, trigger);
    if (option) {
      console.log("[Web2API Bridge] Menu already open, clicking model option directly:", option);
      simulateClick(option);
      await new Promise((r) => setTimeout(r, 200));
      modelBtn.textContent = "✅ Gemini 4 Hydrogen";
      return true;
    }

    if (!trigger) {
      console.warn("[Web2API Bridge] Could not find model selector dropdown trigger");
      return false;
    }

    console.log("[Web2API Bridge] Triggering model dropdown via trigger element:", trigger);
    simulateClick(trigger);

    // Poll for menu overlay or items up to 3000ms
    const start = Date.now();
    while (Date.now() - start < 3000) {
      await new Promise((r) => setTimeout(r, 100));
      option = findModelOptionInDOM(targetKeywords, trigger);
      if (option) {
        console.log("[Web2API Bridge] Found model menu option, clicking:", option);
        simulateClick(option);
        await new Promise((r) => setTimeout(r, 250));
        modelBtn.textContent = "✅ Gemini 4 Hydrogen";
        return true;
      }
    }

    console.warn("[Web2API Bridge] Target model not found in dropdown");
    // Click outside to close menu if not matched
    document.body.click();
    return false;
  }

  function isNotSendButton(btn) {
    if (!btn) return true;
    const t = (btn.innerText || btn.textContent || "").trim().toLowerCase();
    const aria = (btn.getAttribute("aria-label") || "").toLowerCase();
    const tip = (btn.getAttribute("mattooltip") || "").toLowerCase();
    if (/auto|hydrogen|carbon|argon|flash|pro/i.test(t) || /auto|hydrogen/i.test(aria) || /auto|hydrogen/i.test(tip)) return true;
    if (t === "mic" || aria.includes("mic") || tip.includes("mic")) return true;
    if (t.includes("keyboard_arrow_down") && /auto|hydrogen/i.test(t)) return true;
    return false;
  }

  function findAllCandidateSendButtons(inputEl) {
    const candidates = [];

    // Priority 1: Match icons with text or name "arrow_upward", "send", "arrow_forward"
    // (Vertex AI Search send button uses <mat-icon>arrow_upward</mat-icon>)
    const iconCandidates = queryDeep('mat-icon, svg, [data-icon], .material-icons, span, i');
    for (const icon of iconCandidates) {
      if (isNavigationOrSidebar(icon, inputEl)) continue;
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
        const btn = icon.closest('button, [role="button"]') || icon.parentElement;
        if (btn && isVisible(btn) && !isNotSendButton(btn) && !candidates.includes(btn)) {
          candidates.push(btn);
        }
      }
    }

    // Priority 2: In Vertex AI Search, send button is right next to the mic button
    const micCandidates = queryDeep('mat-icon, button[aria-label*="mic" i], button[mattooltip*="mic" i]');
    for (const mic of micCandidates) {
      if (isNavigationOrSidebar(mic, inputEl)) continue;
      const text = (mic.innerText || mic.textContent || "").trim().toLowerCase();
      const name = (mic.getAttribute("data-mat-icon-name") || "").toLowerCase();
      if (text === "mic" || name === "mic" || mic.getAttribute("aria-label")?.toLowerCase().includes("mic")) {
        const micBtn = mic.closest('button, [role="button"]') || mic;
        let sib = micBtn.nextElementSibling;
        while (sib) {
          const btn = (sib.tagName === "BUTTON" || sib.getAttribute("role") === "button")
            ? sib
            : sib.querySelector('button, [role="button"]');
          if (btn && isVisible(btn) && !isNotSendButton(btn) && !candidates.includes(btn)) {
            candidates.push(btn);
          }
          sib = sib.nextElementSibling;
        }
      }
    }

    // Priority 3: Within the input container or bottom bar, find any button that isn't Auto or mic
    if (inputEl) {
      const container = inputEl.closest("form, .bottom-bar, .input-area, .chat-box, .input-container, discovery-engine-chat, mat-form-field") || inputEl.parentElement;
      if (container) {
        const btns = queryDeep('button, [role="button"]', container);
        for (const b of btns) {
          if (isVisible(b) && !isNotSendButton(b) && !candidates.includes(b)) {
            candidates.push(b);
          }
        }
      }
    }

    // Priority 4: Standard button selectors across the page
    const btnSelectors = [
      'button[aria-label*="Send" i]',
      'button[aria-label*="发送" i]',
      'button[aria-label*="Submit" i]',
      'button[mattooltip*="Send" i]',
      'button[mattooltip*="Submit" i]',
      'button.send-button',
      'button[type="submit"]'
    ];
    for (const sel of btnSelectors) {
      const btns = queryDeep(sel);
      for (const b of btns) {
        if (!isNavigationOrSidebar(b, inputEl) && isVisible(b) && !isNotSendButton(b) && !candidates.includes(b)) {
          candidates.push(b);
        }
      }
    }

    return candidates;
  }

  function findSendButton(inputEl) {
    const all = findAllCandidateSendButtons(inputEl);
    return all.length > 0 ? all[0] : null;
  }

  function isSendButtonReady(inputEl) {
    const btns = findAllCandidateSendButtons(inputEl);
    return btns.length > 0;
  }

  async function submitPrompt(inputArea) {
    const candidateBtns = findAllCandidateSendButtons(inputArea);
    console.log("[Web2API Bridge] Candidate send buttons found:", candidateBtns.length, candidateBtns);

    // Stage 1: Try clicking candidate buttons with disabled attribute clearing
    for (const btn of candidateBtns) {
      console.log("[Web2API Bridge] Attempting click on candidate send button:", btn);
      try {
        btn.disabled = false;
        btn.removeAttribute("disabled");
        btn.setAttribute("aria-disabled", "false");
        btn.classList.remove("disabled", "mat-button-disabled", "mdc-button--disabled");
      } catch (e) {}

      simulateClick(btn);
      const icon = btn.querySelector("mat-icon, svg, span, i");
      if (icon) simulateClick(icon);

      await new Promise((r) => setTimeout(r, 250));

      const remaining = (inputArea.value || inputArea.textContent || "").trim();
      if (!remaining || isGenerating()) {
        console.log("[Web2API Bridge] Prompt submitted successfully via send button click!");
        return true;
      }
    }

    // Stage 2: Dispatch Enter key events (composed: true for shadow DOM)
    console.log("[Web2API Bridge] Buttons did not clear input, dispatching Enter key...");
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

    inputArea.focus();
    inputArea.dispatchEvent(new KeyboardEvent("keydown", enterOpts));
    inputArea.dispatchEvent(new KeyboardEvent("keypress", enterOpts));
    inputArea.dispatchEvent(new KeyboardEvent("keyup", enterOpts));

    // Also dispatch on parent element and shadow host if inside shadow DOM
    if (inputArea.parentElement) {
      inputArea.parentElement.dispatchEvent(new KeyboardEvent("keydown", enterOpts));
      inputArea.parentElement.dispatchEvent(new KeyboardEvent("keyup", enterOpts));
    }
    const rootNode = inputArea.getRootNode();
    if (rootNode && rootNode.host) {
      rootNode.host.dispatchEvent(new KeyboardEvent("keydown", enterOpts));
      rootNode.host.dispatchEvent(new KeyboardEvent("keyup", enterOpts));
    }

    await new Promise((r) => setTimeout(r, 300));
    const remainingAfterEnter = (inputArea.value || inputArea.textContent || "").trim();
    if (!remainingAfterEnter || isGenerating()) {
      console.log("[Web2API Bridge] Prompt submitted successfully via Enter key!");
      return true;
    }

    // Stage 3: Form requestSubmit / submit
    if (inputArea.form) {
      console.log("[Web2API Bridge] Attempting form.requestSubmit()...");
      try {
        if (inputArea.form.requestSubmit) inputArea.form.requestSubmit();
        else inputArea.form.submit();
      } catch (e) {}
    }

    // Stage 4: Secondary click retry on all candidate buttons
    for (const btn of candidateBtns) {
      simulateClick(btn);
    }

    await new Promise((r) => setTimeout(r, 300));
    return !(inputArea.value || inputArea.textContent || "").trim() || isGenerating();
  }

  function isGenerating() {
    // Only check VISIBLE stop buttons
    const stopBtnSelectors = [
      'button[aria-label*="Stop" i]',
      'button[aria-label*="停止" i]',
      'button.stop-generating-button',
      'button[mattooltip*="Stop" i]'
    ];
    for (const sel of stopBtnSelectors) {
      const btns = queryDeep(sel);
      for (const b of btns) {
        if (isVisible(b)) return true;
      }
    }

    // Check visible stop icon inside a visible button
    const stopIcons = queryDeep('mat-icon[data-mat-icon-name="stop"]');
    for (const icon of stopIcons) {
      if (isVisible(icon)) {
        const parentBtn = icon.closest("button");
        if (parentBtn && isVisible(parentBtn)) {
          return true;
        }
      }
    }

    return false;
  }

  function cleanResponseText(rawText) {
    if (!rawText) return "";

    // 1. Strip Lit / Polymer template markers like <!--?lit$203076504$--> or ?lit$203076504$ or lit$12345$
    let text = rawText
      .replace(/<\!--\??lit\$[0-9a-zA-Z]+\$-->/g, "")
      .replace(/\??lit\$[0-9a-zA-Z]+\$/g, "");

    // 2. Strip disclaimer lines
    text = text.replace(/Generative AI (may display inaccurate information|is experimental).*$/gim, "");

    // 3. Strip standalone UI control labels
    const uiTerms = [
      "auto",
      "keyboard_arrow_down",
      "arrow_upward",
      "arrow_downward",
      "arrow_forward",
      "expand_more",
      "arrow_drop_down",
      "mic",
      "send",
      "stop",
      "thumb_up",
      "thumb_down",
      "content_copy",
      "more_vert",
      "share",
      "refresh"
    ];

    const lines = text.split("\n");
    const filtered = lines.filter((l) => {
      const trimmed = l.trim().toLowerCase();
      if (!trimmed) return true;
      return !uiTerms.includes(trimmed);
    });

    return filtered.join("\n").trim();
  }

  function isResponseElement(el) {
    if (!el) return false;
    if (isNavigationOrSidebar(el)) return false;

    // Reject anything inside form, bottom bar, input area, or user queries
    if (el.closest("form, .bottom-bar, .input-area, .chat-box-input, .input-container, mat-form-field, rich-textarea, user-query, .user-query, [user], discovery-engine-input")) {
      return false;
    }

    // Reject if it contains input controls or buttons (A real model response will never contain a textarea, input, or button)
    if (el.querySelector("textarea, input, mat-form-field, rich-textarea, button, [role='button']")) {
      return false;
    }

    // Reject if its text after cleaning is empty (e.g. only UI chrome or template markers)
    const raw = extractDeepText(el);
    const cleaned = cleanResponseText(raw);
    if (!cleaned || cleaned.length === 0) {
      return false;
    }

    return true;
  }

  function findResponseElements() {
    const candidateSelectors = [
      // Vertex AI Search & Enterprise Google Cloud Discovery Engine
      "discovery-engine-formatted-text",
      "discovery-engine-markdown",
      "discovery-engine-message:not([user]):not(.user-message)",
      "discovery-engine-chat-message:not([user]):not(.user-message)",
      "discovery-engine-answer-content",
      "discovery-engine-answer",
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
      "[data-test-id*='bot']",
      // Consumer Gemini Web
      "message-content",
      ".model-response-text",
      ".markdown",
      "model-response"
    ];

    for (const sel of candidateSelectors) {
      try {
        const els = queryDeep(sel);
        const filtered = els.filter(isResponseElement);
        if (filtered.length > 0) {
          return filtered;
        }
      } catch (e) {}
    }

    // Dynamic fallback
    const fallbackEls = queryDeep(".message-content, [class*='agent'], [class*='bot'], [class*='answer'], [class*='markdown'], [role='article']");
    const valid = fallbackEls.filter(isResponseElement);
    if (valid.length > 0) {
      return valid;
    }

    return [];
  }

  function setInputValue(el, val) {
    el.focus();
    if (el.tagName === "TEXTAREA" || el.tagName === "INPUT") {
      // 1. Dispatch beforeinput
      try {
        el.dispatchEvent(new InputEvent("beforeinput", {
          bubbles: true,
          cancelable: true,
          composed: true,
          inputType: "insertFromPaste",
          data: val
        }));
      } catch (e) {}

      // 2. Set value via prototype setter and direct assignment
      const proto = el.tagName === "TEXTAREA" ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
      const setMethod = Object.getOwnPropertyDescriptor(proto, "value")?.set;
      if (setMethod) {
        setMethod.call(el, val);
      } else {
        el.value = val;
      }

      // 3. Dispatch input and change events
      el.dispatchEvent(new Event("input", { bubbles: true, cancelable: true, composed: true }));
      try {
        el.dispatchEvent(new InputEvent("input", { bubbles: true, cancelable: true, composed: true, inputType: "insertFromPaste", data: val }));
      } catch (e) {}
      el.dispatchEvent(new Event("change", { bubbles: true, cancelable: true, composed: true }));

      // 4. Dispatch keydown/keyup so any reactive key listeners enable the send button
      el.dispatchEvent(new KeyboardEvent("keydown", { key: " ", code: "Space", bubbles: true, composed: true }));
      el.dispatchEvent(new KeyboardEvent("keyup", { key: " ", code: "Space", bubbles: true, composed: true }));
    } else {
      el.focus();
      document.execCommand("selectAll", false, null);
      document.execCommand("delete", false, null);
      let ok = false;
      try {
        ok = document.execCommand("insertText", false, val);
      } catch (e) {}
      if (!ok || !el.textContent.trim()) {
        el.innerHTML = "<p>" + val.replace(/\n/g, "<br>") + "</p>";
      }
      el.dispatchEvent(new Event("input", { bubbles: true, cancelable: true, composed: true }));
      try {
        el.dispatchEvent(new InputEvent("input", { bubbles: true, cancelable: true, composed: true, inputType: "insertText", data: val }));
      } catch (e) {}
      el.dispatchEvent(new KeyboardEvent("keydown", { key: " ", code: "Space", bubbles: true, composed: true }));
      el.dispatchEvent(new KeyboardEvent("keyup", { key: " ", code: "Space", bubbles: true, composed: true }));
    }
  }

  // 3. Web Page DOM Interaction & Prompt Dispatch
  async function handlePromptRequest(reqId, prompt, model) {
    // A. Ensure Gemini 4 Hydrogen or requested model is selected in web dropdown
    try {
      await ensureModelSelected(model || "hydrogen");
    } catch (e) {
      console.warn("[Web2API Bridge] Error selecting model:", e);
    }

    // B. Locate Prompt Input Area
    const inputArea = await findChatInput(3500);

    // Snapshot existing model response count before submitting
    const baselineResponses = findResponseElements();
    const baselineCount = baselineResponses.length;
    const baselineLastText = baselineCount > 0 ? cleanResponseText(extractDeepText(baselineResponses[baselineCount - 1])) : "";

    // Insert text simulating user interaction
    setInputValue(inputArea, prompt);

    await new Promise((r) => setTimeout(r, 200));

    // Submit prompt using multi-stage dispatcher
    const submitSuccess = await submitPrompt(inputArea);
    console.log("[Web2API Bridge] submitPrompt result:", submitSuccess);

    await new Promise((r) => setTimeout(r, 400));

    // C. Watch for Streaming Response
    await observeResponseStream(reqId, inputArea, baselineCount, baselineLastText);

    // D. Auto-Refresh page after response is delivered (if enabled)
    if (autoRefreshEnabled) {
      setStatus("busy", "Web2API: Refreshing page for next request...");
      await new Promise((r) => setTimeout(r, 600));
      resetOrRefreshPage();
    }
  }

  function observeResponseStream(reqId, inputArea, baselineCount, baselineLastText) {
    return new Promise((resolve) => {
      let lastText = "";
      let lastChangeTime = Date.now();
      let completed = false;
      let hasStarted = false;
      const startTime = Date.now();

      function getActiveResponseText() {
        const currentResponses = findResponseElements();
        if (currentResponses.length === 0) return "";

        let targetEl = currentResponses[currentResponses.length - 1];
        let rawText = extractDeepText(targetEl);
        let fullText = cleanResponseText(rawText);

        // If turn count has not increased, check if new text was appended to existing element
        if (currentResponses.length === baselineCount && baselineLastText) {
          if (fullText.startsWith(baselineLastText)) {
            return fullText.slice(baselineLastText.length).trimStart();
          }
        }
        return fullText;
      }

      function finish() {
        if (completed) return;
        completed = true;
        clearInterval(checkInterval);
        observer.disconnect();
        currentActiveStream = null;

        // If no text was captured via stream, do one final aggressive scrape
        if (!lastText.trim()) {
          const finalScrape = getActiveResponseText();
          if (finalScrape.trim()) {
            lastText = finalScrape;
            sendChunk(reqId, finalScrape, false);
          }
        }

        if (!lastText.trim()) {
          console.warn("[Web2API Bridge] No model response captured within timeout");
          sendChunk(reqId, "", true, "Model did not respond. Please verify the prompt was sent on the page or refresh.").then(() => resolve()).catch(() => resolve());
          return;
        }

        sendChunk(reqId, "", true).then(() => resolve()).catch(() => resolve());
      }

      currentActiveStream = { finish };

      const checkInterval = setInterval(async () => {
        if (completed) return;

        const currentText = getActiveResponseText();
        const generating = isGenerating();
        if (generating || currentText.length > 0) {
          hasStarted = true;
        }

        if (currentText.length > lastText.length) {
          const delta = currentText.slice(lastText.length);
          lastText = currentText;
          lastChangeTime = Date.now();
          setStatus("busy", `Web2API: Streaming (${lastText.length} chars)...`);
          await sendChunk(reqId, delta, false);
        }

        const timeSinceChange = Date.now() - lastChangeTime;
        const timeSinceStart = Date.now() - startTime;
        const sendReady = isSendButtonReady(inputArea);

        // Do not finish before generation has started (unless timeout after 25s)
        if (!hasStarted && timeSinceStart < 25000) {
          return;
        }

        // Completion criteria:
        // 1. Text has been captured and send button has re-enabled with no stop button
        if (lastText.length > 0 && !generating && sendReady && timeSinceChange > 500) {
          finish();
          return;
        }

        // 2. Text has been captured, generation stopped, and text settled
        if (lastText.length > 0 && !generating && timeSinceChange > 1000) {
          finish();
          return;
        }

        // 3. Text has settled for 2.5 seconds even if stop button state is ambiguous
        if (lastText.length > 0 && timeSinceChange > 2500) {
          finish();
          return;
        }

        // 4. Safety fallback: if 20s elapsed and we have text, finish
        if (lastText.length > 0 && timeSinceStart > 20000 && timeSinceChange > 1500) {
          finish();
          return;
        }
      }, 150);

      const observer = new MutationObserver(() => {
        // Keeps loop active on DOM mutations
      });

      observer.observe(document.body, {
        childList: true,
        subtree: true,
        characterData: true
      });

      // Absolute timeout: 90s max (not 180s)
      setTimeout(() => {
        if (!completed) {
          finish();
        }
      }, 90000);
    });
  }

  // 4. Poll Server for Prompt Requests
  async function pollServer() {
    if (isPolling) return;
    isPolling = true;

    while (true) {
      try {
        const tabParam = encodeURIComponent(location.hostname);
        const res = await fetch(`${BRIDGE_URL}/bridge/poll?tab=${tabParam}`, {
          method: "GET",
          headers: { "Accept": "application/json" }
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
          setStatus("busy", `Web2API: Answering request...`);
          try {
            await handlePromptRequest(data.id, data.prompt, data.model);
          } catch (err) {
            console.error("[Web2API Bridge] Error processing prompt:", err);
            await sendChunk(data.id, "", true, err.message || "Failed to generate reply");
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

  // Helper: Post generated chunk back to gemini-web2api
  async function sendChunk(id, chunkText, isDone, err) {
    try {
      await fetch(`${BRIDGE_URL}/bridge/chunk`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: id,
          text: chunkText,
          done: isDone,
          error: err || ""
        })
      });
    } catch (e) {
      console.warn("[Web2API Bridge] Failed to send chunk:", e);
    }
  }

  // Automatically switch dropdown to Hydrogen on tab startup
  setTimeout(() => {
    ensureModelSelected("hydrogen").catch(() => {});
  }, 1500);

  // Start polling
  pollServer();
})();
