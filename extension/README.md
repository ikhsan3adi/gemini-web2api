# Gemini Web2API - Live Web Bridge & Cookie Plugin

This browser extension allows `gemini-web2api` to interact directly with your open browser tab on **Gemini Enterprise** (`vertexaisearch.cloud.google.com`) or **Gemini Web** (`gemini.google.com`).

---

## 🛡️ Anti-Ban Live Web Bridge Mode (Recommended)

In this mode, you **do not use or export cookies**. Instead:
1. You keep your Gemini Enterprise / Web tab open in Chrome or Edge.
2. The extension's content script runs inside the open web page.
3. When `gemini-web2api` receives an API request (e.g. from WorkBuddy or an OpenAI client), it sends the prompt to the browser tab.
4. The extension enters the prompt into the real web page, clicks send, observes Gemini's response as it streams, and returns the tokens in real time back to `gemini-web2api`.

**Why this prevents bans**:
- Google's servers only see traffic coming from your real, authentic browser tab with genuine mouse movements, TLS signatures, and DOM rendering.
- No headless HTTP requests or cookie extraction scripts to trigger Cloudflare or Google bot detection.

---

## How to Install (Chrome, Edge, Brave, Opera)

1. Open your browser's extensions page:
   - **Chrome**: `chrome://extensions/`
   - **Edge**: `edge://extensions/`
   - **Brave**: `brave://extensions/`
2. Enable **"Developer mode"** (toggle in the top-right corner).
3. Click **"Load unpacked"**.
4. Select the `extension/` folder from this repository.

---

## How to Run with the Live Web Bridge

1. Start `gemini-web2api`:
   ```bash
   ./gemini-web2api --port 8081
   ```
2. Open your browser tab:
   - For Enterprise: `https://vertexaisearch.cloud.google.com/`
   - For Consumer: `https://gemini.google.com/`
3. Look at the bottom-right corner of the page:
   - You will see a small badge: `🟢 Web2API Bridge: Connected (:8081)`
4. Send requests to `http://localhost:8081/v1/chat/completions` (from WorkBuddy, Cursor, curl, etc.).
   - The badge will pulse: `⚡ Web2API: Generating reply...`
   - The web page will type the prompt and stream the reply directly back to your API client!

---

## Fallback Mode: Cookie Export

If you ever want headless background operation without an open browser window, you can still click the extension icon on your toolbar and click **"Download cookie.json"**.
