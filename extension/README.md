# Gemini Web2API - Cookie Exporter Extension

A lightweight, zero-dependency Manifest V3 browser extension to export your session cookies and XSRF tokens directly into the `cookie.json` format expected by `gemini-web2api`.

Supports both:
- **Gemini Consumer Web**: `https://gemini.google.com`
- **Gemini Enterprise / Vertex AI Search**: `https://vertexaisearch.cloud.google.com`

---

## How to Install (Chrome, Edge, Brave, Opera)

1. Open your browser's extensions page:
   - **Chrome**: `chrome://extensions/`
   - **Edge**: `edge://extensions/`
   - **Brave**: `brave://extensions/`
2. Enable **"Developer mode"** (toggle in the top-right or left sidebar).
3. Click **"Load unpacked"**.
4. Select the `extension/` folder from this repository.
5. Pin the extension icon to your browser toolbar.

---

## How to Use

1. Log into your account on [gemini.google.com](https://gemini.google.com) or your enterprise portal [vertexaisearch.cloud.google.com](https://vertexaisearch.cloud.google.com/).
2. Click the **Gemini Cookie Exporter** extension icon in your toolbar.
3. Click **"Download cookie.json"** (or **"Copy as cookie.json"**).
4. Save the file in your `gemini-web2api` working directory.
5. Run `gemini-web2api`:
   ```bash
   ./gemini-web2api --cookie-file cookie.json
   ```
   *(For Enterprise, also pass `--base-url https://vertexaisearch.cloud.google.com`)*.

---

## Alternative 3rd-Party Extensions

If you prefer using established Chrome Web Store / Firefox Add-on extensions:

1. **Cookie-Editor** (Recommended):
   - Chrome Web Store: Search for `Cookie-Editor` (by cgagnier.ca)
   - Firefox Add-ons: Search for `Cookie-Editor`
   - Export format: Click "Export" → "Header String" or "JSON".
2. **EditThisCookie**:
   - Chrome Web Store: Search for `EditThisCookie`
   - Export format: Click the export icon to copy cookies to your clipboard.
