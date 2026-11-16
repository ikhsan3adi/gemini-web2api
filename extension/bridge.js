// Gemini Web2API Live Web Bridge Content Script
// Supports both gemini.google.com and vertexaisearch.cloud.google.com (Enterprise Vertex AI Search)

(function () {
  console.log("[Web2API Bridge] Initializing live web bridge on", location.hostname);

  const BRIDGE_URL = "http://localhost:8081";
  let isPolling = false;
  let activeReqId = null;
  let lastFocusedInput = null;

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

  // 1. Floating Status Widget
  const badge = document.createElement("div");
  badge.id = "web2api-bridge-badge";
  badge.style.cssText = `
    position: fixed;
    bottom: 20px;
    right: 20px;
    z-index: 999999;
    background: #111827;
    color: #f9fafb;
    padding: 8px 14px;
    border-radius: 20px;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    font-size: 12px;
    font-weight: 500;
    box-shadow: 0 4px 12px rgba(0,0,0,0.3);
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
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
  `;

  const text = document.createElement("span");
  text.textContent = "Web2API: Connecting...";

  badge.appendChild(dot);
  badge.appendChild(text);
  document.body.appendChild(badge);

  function setStatus(status, msg) {
    if (status === "connected") {
      dot.style.background = "#10b981";
      text.textContent = msg || `Web2API Bridge: Connected (:8081)`;
    } else if (status === "busy") {
      dot.style.background = "#f59e0b";
      text.textContent = msg || `Web2API: Generating reply...`;
    } else {
      dot.style.background = "#ef4444";
      text.textContent = msg || `Web2API: Disconnected (Start server)`;
    }
  }

  // 2. Deep Tree Search for Inputs (including Shadow DOM and iframes)
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

      // Traverse children looking for shadow roots
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

  async function findChatInput(maxWaitMs = 3000) {
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
      // 1. Check last user-focused element
      if (lastFocusedInput && isEditable(lastFocusedInput) && document.contains(lastFocusedInput)) {
        return lastFocusedInput;
      }

      // 2. Check current activeElement
      if (isEditable(document.activeElement)) {
        return document.activeElement;
      }

      // 3. Search via known selectors
      for (const sel of inputSelectors) {
        const found = queryDeep(sel);
        for (const el of found) {
          // Verify it is visible and not hidden
          const rect = el.getBoundingClientRect();
          if (rect.width > 0 && rect.height > 0 && !el.disabled && !el.readOnly) {
            return el;
          }
        }
      }

      await new Promise((r) => setTimeout(r, 200));
    }

    // Collect debug info for error reporting
    const allInputs = queryDeep("input, textarea, [contenteditable]");
    const tagList = allInputs.map((el) => `${el.tagName.toLowerCase()}[placeholder="${el.placeholder || ""}"]`).join(", ");
    throw new Error(`Could not find Gemini chat input box on page. (Found inputs: ${tagList || "none"}. Please click inside the chat box and retry)`);
  }

  function findSendButton(inputEl) {
    const btnSelectors = [
      'button[aria-label*="Send" i]',
      'button[aria-label*="发送" i]',
      'button[aria-label*="Submit" i]',
      'button[aria-label*="Search" i]',
      'button[mattooltip*="Send" i]',
      'button[mattooltip*="Search" i]',
      'button.send-button',
      'button[type="submit"]',
      'button:has(mat-icon[data-mat-icon-name="send"])',
      'button:has(mat-icon[data-mat-icon-name="arrow_forward"])',
      'button:has(mat-icon[data-mat-icon-name="search"])'
    ];

    // Check near input first
    if (inputEl) {
      const parentForm = inputEl.closest("form, mat-form-field, .input-area, .chat-box");
      if (parentForm) {
        for (const sel of btnSelectors) {
          const btn = parentForm.querySelector(sel);
          if (btn && !btn.disabled) return btn;
        }
      }
    }

    // Search globally across deep DOM
    for (const sel of btnSelectors) {
      const btns = queryDeep(sel);
      for (const b of btns) {
        if (!b.disabled) return b;
      }
    }

    return null;
  }

  // 3. Poll Server for Prompt Requests
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
          await new Promise((r) => setTimeout(r, 3000));
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
            await handlePromptRequest(data.id, data.prompt);
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
        await new Promise((r) => setTimeout(r, 3000));
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

  // 4. Web Page DOM Interaction
  async function handlePromptRequest(reqId, prompt) {
    // A. Locate Prompt Input Area with retry and deep search
    const inputArea = await findChatInput(3500);

    inputArea.focus();

    // Insert text simulating user interaction
    if (inputArea.tagName === "TEXTAREA" || inputArea.tagName === "INPUT") {
      inputArea.value = prompt;
      inputArea.dispatchEvent(new Event("input", { bubbles: true }));
      inputArea.dispatchEvent(new Event("change", { bubbles: true }));
    } else {
      inputArea.innerHTML = "";
      document.execCommand("selectAll", false, null);
      document.execCommand("insertText", false, prompt);
      inputArea.dispatchEvent(new Event("input", { bubbles: true }));
    }

    await new Promise((r) => setTimeout(r, 250));

    // B. Locate Send Button or trigger Enter
    const sendBtn = findSendButton(inputArea);

    if (sendBtn && !sendBtn.disabled) {
      sendBtn.click();
    } else {
      // Dispatch Enter key event
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

    await new Promise((r) => setTimeout(r, 600));

    // C. Watch for Streaming Response
    await observeResponseStream(reqId);
  }

  function observeResponseStream(reqId) {
    return new Promise((resolve, reject) => {
      let lastText = "";
      let lastChangeTime = Date.now();
      let completed = false;

      function findLatestResponseElement() {
        const candidateSelectors = [
          "model-response",
          "message-content",
          ".model-response-text",
          ".response-container",
          ".answer-content",
          ".search-result-summary",
          ".summary-content",
          ".markdown",
          "[data-test-id='model-turn']",
          "[data-test-id*='response']",
          "[data-test-id*='answer']"
        ];

        for (const sel of candidateSelectors) {
          const els = queryDeep(sel);
          if (els.length > 0) {
            return els[els.length - 1];
          }
        }
        return null;
      }

      function isGenerating() {
        const stopBtnSelectors = [
          'button[aria-label*="Stop" i]',
          'button[aria-label*="停止" i]',
          'button.stop-generating-button',
          'mat-icon[data-mat-icon-name="stop"]'
        ];
        for (const sel of stopBtnSelectors) {
          const btns = queryDeep(sel);
          if (btns.length > 0) return true;
        }
        return false;
      }

      const checkInterval = setInterval(async () => {
        if (completed) return;

        const resEl = findLatestResponseElement();
        if (resEl) {
          const currentText = resEl.innerText || resEl.textContent || "";
          if (currentText.length > lastText.length) {
            const delta = currentText.slice(lastText.length);
            lastText = currentText;
            lastChangeTime = Date.now();
            await sendChunk(reqId, delta, false);
          }
        }

        const timeSinceChange = Date.now() - lastChangeTime;
        const generating = isGenerating();

        // If stopped generating and text has settled
        if (lastText.length > 0 && !generating && timeSinceChange > 1200) {
          finish();
        } else if (lastText.length > 0 && timeSinceChange > 8000) {
          finish();
        }
      }, 150);

      function finish() {
        if (completed) return;
        completed = true;
        clearInterval(checkInterval);
        observer.disconnect();
        sendChunk(reqId, "", true).then(() => resolve()).catch(() => resolve());
      }

      const observer = new MutationObserver(() => {
        // Keeps interval active
      });

      observer.observe(document.body, {
        childList: true,
        subtree: true,
        characterData: true
      });

      setTimeout(() => {
        if (!completed) {
          finish();
        }
      }, 180000);
    });
  }

  // Start polling
  pollServer();
})();
