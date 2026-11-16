// Gemini Web2API Live Web Bridge Content Script
// Interacts directly with open gemini.google.com and vertexaisearch.cloud.google.com tabs.

(function () {
  console.log("[Web2API Bridge] Initializing live web bridge on", location.hostname);

  const BRIDGE_URL = "http://localhost:8081";
  let isPolling = false;
  let activeReqId = null;

  // 1. Create Floating Status Widget
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

  // 2. Poll Server for Prompt Requests
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
          // Normal poll timeout, continue loop
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

  // 3. Web Page DOM Interaction
  async function handlePromptRequest(reqId, prompt) {
    // A. Locate Prompt Input Area
    const inputArea =
      document.querySelector('rich-textarea div[contenteditable="true"]') ||
      document.querySelector('div[contenteditable="true"][role="textbox"]') ||
      document.querySelector('div[contenteditable="true"]') ||
      document.querySelector('textarea');

    if (!inputArea) {
      throw new Error("Could not find Gemini chat input box on page");
    }

    inputArea.focus();

    // Insert text simulating user paste/type
    if (inputArea.tagName === "TEXTAREA") {
      inputArea.value = prompt;
      inputArea.dispatchEvent(new Event("input", { bubbles: true }));
      inputArea.dispatchEvent(new Event("change", { bubbles: true }));
    } else {
      inputArea.innerHTML = "";
      document.execCommand("insertText", false, prompt);
      inputArea.dispatchEvent(new Event("input", { bubbles: true }));
    }

    await new Promise((r) => setTimeout(r, 200));

    // B. Locate Send Button
    const sendBtn =
      document.querySelector('button[aria-label*="Send message"]') ||
      document.querySelector('button[aria-label*="发送"]') ||
      document.querySelector('button[mattooltip*="Send"]') ||
      document.querySelector('button.send-button') ||
      document.querySelector('button[aria-label*="Submit"]') ||
      document.querySelector('button:has(mat-icon[data-mat-icon-name="send"])');

    if (sendBtn && !sendBtn.disabled) {
      sendBtn.click();
    } else {
      // Fallback: Dispatch Enter key
      const enterEvt = new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        keyCode: 13,
        which: 13,
        bubbles: true,
        cancelable: true
      });
      inputArea.dispatchEvent(enterEvt);
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

      // Select container or body
      const targetNode = document.querySelector("main") || document.body;

      function findLatestResponseElement() {
        const candidates = document.querySelectorAll(
          "model-response, message-content, .model-response-text, .response-container, [data-test-id='model-turn']"
        );
        if (candidates.length > 0) {
          return candidates[candidates.length - 1];
        }
        return null;
      }

      function isGenerating() {
        const stopBtn =
          document.querySelector('button[aria-label*="Stop"]') ||
          document.querySelector('button[aria-label*="停止"]') ||
          document.querySelector('button.stop-generating-button');
        return !!stopBtn;
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

        // Check completion conditions
        const timeSinceChange = Date.now() - lastChangeTime;
        const generating = isGenerating();

        // If stopped generating and text has settled, or text has settled for 4 seconds after initial output
        if (lastText.length > 0 && !generating && timeSinceChange > 1200) {
          finish();
        } else if (lastText.length > 0 && timeSinceChange > 8000) {
          // Timeout safety
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
        // Observer keeps interval loop responsive
      });

      observer.observe(targetNode, {
        childList: true,
        subtree: true,
        characterData: true
      });

      // Overall safety timeout: 180 seconds
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
