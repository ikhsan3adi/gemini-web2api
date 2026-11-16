// Gemini Web2API Live Web Bridge Content Script
// Supports both gemini.google.com and vertexaisearch.cloud.google.com (Enterprise Vertex AI Search)

(function () {
  console.log("[Web2API Bridge] Initializing live web bridge on", location.hostname);

  const BRIDGE_URL = "http://localhost:8081";
  let isPolling = false;
  let activeReqId = null;
  let lastFocusedInput = null;
  let autoRefreshEnabled = true;
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
    modelBtn.textContent = ok ? "✅ Hydrogen" : "⚡ Hydrogen";
    setTimeout(() => { modelBtn.textContent = "⚡ Hydrogen"; }, 2000);
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

  function isNavigationOrSidebar(el) {
    if (!el) return false;
    return !!el.closest("nav, aside, mat-sidenav, .sidebar, .side-nav, .nav, .menu, header, [role='navigation'], [aria-label*='sidebar' i]");
  }

  // Automatically switch the model dropdown (e.g. "Auto ∨") to Gemini 4 Hydrogen
  async function ensureModelSelected(requestedModel) {
    if (!requestedModel) requestedModel = "hydrogen";
    const targetKeywords = ["hydrogen", "gemini 4 hydrogen", "gemini-4-hydrogen"];
    if (/flash/i.test(requestedModel)) {
      targetKeywords.length = 0;
      targetKeywords.push("flash");
    } else if (/pro/i.test(requestedModel)) {
      targetKeywords.length = 0;
      targetKeywords.push("pro");
    }

    // Find candidate model dropdown button near the chat input (excluding navigation / sidebar)
    const allBtns = queryDeep("button, [role='button'], mat-select, .mat-mdc-menu-trigger");
    const candidateDropdowns = allBtns.filter((btn) => {
      if (isNavigationOrSidebar(btn)) return false;
      const text = (btn.innerText || btn.textContent || "").trim();
      return /auto|hydrogen|gemini|flash|pro/i.test(text);
    });

    for (const dropdown of candidateDropdowns) {
      const currentText = (dropdown.innerText || dropdown.textContent || "").trim().toLowerCase();
      // If already Hydrogen, we are good!
      if (targetKeywords.some((kw) => currentText.includes(kw))) {
        console.log("[Web2API Bridge] Model already selected:", currentText);
        return true;
      }

      // If it says "Auto" or other model, click to open dropdown
      if (/auto|gemini|flash|pro/i.test(currentText)) {
        console.log("[Web2API Bridge] Clicking model dropdown (current text:", currentText, ")");
        dropdown.click();
        await new Promise((r) => setTimeout(r, 300));

        // Find menu item for Hydrogen
        const menuItems = queryDeep("[role='menuitem'], [role='option'], .mat-mdc-menu-item, mat-list-item, button, span, div");
        for (const item of menuItems) {
          if (isNavigationOrSidebar(item)) continue;
          const itemText = (item.innerText || item.textContent || "").trim().toLowerCase();
          if (targetKeywords.some((kw) => itemText.includes(kw))) {
            console.log("[Web2API Bridge] Found and clicking model menu item:", itemText);
            item.click();
            await new Promise((r) => setTimeout(r, 300));
            return true;
          }
        }

        // Click outside if not found
        document.body.click();
        await new Promise((r) => setTimeout(r, 150));
      }
    }
    return false;
  }

  function findSendButton(inputEl) {
    // Strictly matching send/submit/arrow buttons; NEVER matching sidebar "Search" button
    const btnSelectors = [
      'button[aria-label*="Send" i]',
      'button[aria-label*="发送" i]',
      'button[aria-label*="Submit" i]',
      'button[mattooltip*="Send" i]',
      'button[mattooltip*="Submit" i]',
      'button.send-button',
      'button[type="submit"]',
      'button:has(mat-icon[data-mat-icon-name="send"])',
      'button:has(mat-icon[data-mat-icon-name="arrow_forward"])',
      'button:has(mat-icon[data-mat-icon-name="arrow_upward"])',
      'button:has(svg[data-icon="send"])',
      'button:has(svg[data-icon="arrow-up"])'
    ];

    // Priority 1: Check inside or immediately adjacent to chat input container
    if (inputEl) {
      const parentForm = inputEl.closest("form, mat-form-field, .input-area, .chat-box, .input-container, discovery-engine-chat, .bottom-bar, .input-wrapper") || inputEl.parentElement;
      if (parentForm) {
        for (const sel of btnSelectors) {
          const btn = parentForm.querySelector(sel);
          if (btn && isVisible(btn) && !btn.disabled && !isNavigationOrSidebar(btn)) return btn;
        }
      }
    }

    // Priority 2: Deep search across page, strictly excluding sidebar / navigation
    for (const sel of btnSelectors) {
      const btns = queryDeep(sel);
      for (const b of btns) {
        if (!isNavigationOrSidebar(b) && isVisible(b) && !b.disabled && b.getAttribute("aria-disabled") !== "true") {
          return b;
        }
      }
    }

    return null;
  }

  function isSendButtonReady(inputEl) {
    const btn = findSendButton(inputEl);
    return !!btn;
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

  function findResponseElements() {
    const candidateSelectors = [
      // Vertex AI Search & Enterprise Google Cloud Discovery Engine
      "discovery-engine-formatted-text",
      "discovery-engine-markdown",
      "discovery-engine-message:not([user]):not(.user-message)",
      "discovery-engine-chat-message:not([user]):not(.user-message)",
      "discovery-engine-answer",
      "discovery-engine-answer-content",
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
      "[data-test-id*='message']",
      // Consumer Gemini Web
      "message-content",
      ".model-response-text",
      ".markdown",
      ".response-container",
      "model-response"
    ];

    for (const sel of candidateSelectors) {
      try {
        const els = queryDeep(sel);
        const filtered = els.filter((el) => {
          if (el.closest("user-query, .user-query, rich-textarea, form, .input-area, mat-form-field")) {
            return false;
          }
          return true;
        });
        if (filtered.length > 0) {
          return filtered;
        }
      } catch (e) {}
    }

    // Dynamic fallback: look for any container that has text inside chat area
    const fallbackEls = queryDeep("[class*='agent'], [class*='bot'], [class*='response'], [class*='answer'], [class*='summary'], [class*='markdown'], [role='article']");
    const validFallbacks = fallbackEls.filter((el) => {
      if (el.closest("user-query, .user-query, rich-textarea, form, .input-area, mat-form-field")) {
        return false;
      }
      const t = extractDeepText(el);
      return t && t.trim().length > 0;
    });
    if (validFallbacks.length > 0) {
      return validFallbacks;
    }

    return [];
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
    const baselineLastText = baselineCount > 0 ? extractDeepText(baselineResponses[baselineCount - 1]) : "";

    inputArea.focus();

    // Insert text simulating user interaction
    if (inputArea.tagName === "TEXTAREA" || inputArea.tagName === "INPUT") {
      inputArea.value = prompt;
      inputArea.dispatchEvent(new Event("input", { bubbles: true }));
      inputArea.dispatchEvent(new Event("change", { bubbles: true }));
    } else {
      inputArea.focus();
      inputArea.innerHTML = "";
      try {
        const ok = document.execCommand("insertText", false, prompt);
        if (!ok || !inputArea.textContent.trim()) {
          inputArea.textContent = prompt;
        }
      } catch (e) {
        inputArea.textContent = prompt;
      }
      inputArea.dispatchEvent(new Event("input", { bubbles: true }));
    }

    await new Promise((r) => setTimeout(r, 200));

    // B. Locate Send Button or trigger Enter
    const sendBtn = findSendButton(inputArea);

    if (sendBtn && !sendBtn.disabled && sendBtn.getAttribute("aria-disabled") !== "true") {
      sendBtn.click();
    } else {
      const enterDown = new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        keyCode: 13,
        which: 13,
        bubbles: true,
        cancelable: true
      });
      inputArea.dispatchEvent(enterDown);

      const enterUp = new KeyboardEvent("keyup", {
        key: "Enter",
        code: "Enter",
        keyCode: 13,
        which: 13,
        bubbles: true,
        cancelable: true
      });
      inputArea.dispatchEvent(enterUp);
    }

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
      const startTime = Date.now();

      function getActiveResponseText() {
        const currentResponses = findResponseElements();
        if (currentResponses.length === 0) return "";

        let targetEl = currentResponses[currentResponses.length - 1];
        let fullText = extractDeepText(targetEl);

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

        sendChunk(reqId, "", true).then(() => resolve()).catch(() => resolve());
      }

      currentActiveStream = { finish };

      const checkInterval = setInterval(async () => {
        if (completed) return;

        const currentText = getActiveResponseText();
        if (currentText.length > lastText.length) {
          const delta = currentText.slice(lastText.length);
          lastText = currentText;
          lastChangeTime = Date.now();
          setStatus("busy", `Web2API: Streaming (${lastText.length} chars)...`);
          await sendChunk(reqId, delta, false);
        }

        const timeSinceChange = Date.now() - lastChangeTime;
        const timeSinceStart = Date.now() - startTime;
        const generating = isGenerating();
        const sendReady = isSendButtonReady(inputArea);

        // Completion criteria:
        // 1. Text has been captured and send button has re-enabled with no stop button
        if (lastText.length > 0 && !generating && sendReady && timeSinceChange > 400) {
          finish();
          return;
        }

        // 2. Text has been captured, generation stopped, and text settled
        if (lastText.length > 0 && !generating && timeSinceChange > 800) {
          finish();
          return;
        }

        // 3. Text has settled for 2 seconds even if stop button state is ambiguous
        if (lastText.length > 0 && timeSinceChange > 2000) {
          finish();
          return;
        }

        // 4. Safety fallback: if 15s elapsed and we have text, finish
        if (lastText.length > 0 && timeSinceStart > 15000 && timeSinceChange > 1000) {
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

  // Start polling
  pollServer();
})();
