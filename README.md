# gemini-web2api

<p align="center">
  <img src="https://raw.githubusercontent.com/Sophomoresty/gemini-web2api/main/logo.png" width="200" alt="gemini-web2api logo">
</p>

[English](README.md) | [中文文档](README.zh-CN.md)

High-performance Go proxy converting Google Gemini's web interface into an OpenAI-compatible API. Zero cost, cross-platform, single static binary.

This is a complete Go rewrite of the original [Python gemini-web2api](https://github.com/Sophomoresty/gemini-web2api). It features massive concurrency throughput, minimal memory footprint, and enhanced capabilities such as **TLS Impersonation** for WAF bypass.

## Key Features

- **Optional API Keys**: No auth when `api_keys` is empty, OpenAI-style Bearer auth when configured.
- **OpenAI Compatible**: Drop-in replacement for `/v1/chat/completions`, `/v1/models`, and `/v1/responses` (Codex CLI).
- **Multimodal (Vision)**: Fully supports sending images (`image_url` / base64) to Gemini using Scotty Resumable Upload protocol natively with built-in image compression.
- **TLS Impersonation**: Built-in support to bypass Cloudflare/WAF by mimicking Chrome/Edge TLS fingerprints using `tls-client` 🔥.
- **Tool Calling**: Full function calling support (OpenAI format).
- **Multiple Models**: Flash (3.6), Extended Thinking (20k+ char output), Pro, Auto, Lite.
- **Thinking Depth**: Adjustable reasoning via `@think=N` suffix (0=deepest, 4=shallowest).
- **Web Search**: Built-in internet access (Gemini's native search).
- **High Performance**: Pure Go static binary with high concurrent SSE streaming throughput and minimal memory footprint.
- **Cross-Platform**: Zero runtime dependencies, single static binary.
- **Codex CLI**: Responses API (`/v1/responses`) for OpenAI Codex integration.
- **Gemini CLI**: Google native API (`/v1beta/models`) for Gemini CLI compatibility.

## Quick Start

### 1. Download Pre-built Binary (Recommended)

Download the pre-compiled binary for your OS (Linux, macOS, Windows) from the [Releases](https://github.com/ikhsan3adi/gemini-web2api/releases) page:

**Linux / macOS:**

```bash
chmod +x gemini-web2api
./gemini-web2api --port 8081
```

**Windows (PowerShell / Command Prompt):**

```powershell
.\gemini-web2api.exe --port 8081
```

### 2. Install via Go Toolchain

If you have Go 1.22+ installed on your system:

```bash
go install github.com/ikhsan3adi/gemini-web2api@latest
```

This compiles and places the binary directly into your `$GOPATH/bin` (or `%USERPROFILE%\go\bin`). Run it from anywhere:

```bash
gemini-web2api --port 8081
```

### 3. Build from Source

Prerequisites: Go 1.22+

```bash
git clone https://github.com/ikhsan3adi/gemini-web2api.git
cd gemini-web2api
```

After building, run the executable:

**Linux / macOS:**

```bash
go build -o gemini-web2api .
./gemini-web2api --port 8081
```

**Windows (PowerShell / Command Prompt):**

```powershell
go build -o gemini-web2api.exe .
.\gemini-web2api.exe --port 8081
```

Server starts at `http://localhost:8081/v1`.

### 4. Run via Docker

Before running via Docker, copy the example config file:

```bash
cp config.example.json config.json
```

#### Option A: Run pre-built GHCR Image (No git clone needed)

```bash
docker run -d --name gemini-web2api -p 8081:8081 ghcr.io/ikhsan3adi/gemini-web2api:latest
```

#### Option B: Build and run locally

```bash
docker build -t gemini-web2api .
docker run -d --name gemini-web2api -p 8081:8081 -v ./config.json:/app/config.json gemini-web2api
```

#### Option C: Docker Compose

```bash
docker compose up -d
```

> **Note**: If you get empty responses (`content: null`) with Docker's default bridge network, switch to host networking: `docker run --network host ...` or add `network_mode: host` in your compose file. This is caused by Gemini's upstream rejecting requests from certain Docker NAT IP ranges.

## Client Configuration

### Cherry Studio / ChatBox / any OpenAI client

| Field    | Value                                                               |
| -------- | ------------------------------------------------------------------- |
| Base URL | `http://localhost:8081/v1`                                          |
| API Key  | any `api_keys` value from `config.json`; anything if not configured |
| Model    | `gemini-3.5-flash-thinking`                                         |

### curl

#### bash / macOS / Linux

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-key" \
  -d '{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"Hello!"}]}'
```

#### PowerShell (Windows)

```powershell
curl.exe --% http://127.0.0.1:8081/v1/chat/completions -H "Content-Type: application/json" -H "Authorization: Bearer sk-your-key" -d "{\"model\":\"gemini-3.5-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}"
```

> Note: On Windows PowerShell, use `curl.exe` and `--%` so PowerShell does not reinterpret JSON quoting or curl options.

### OpenAI Python SDK

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8081/v1", api_key="sk-your-key")
resp = client.chat.completions.create(
    model="gemini-3.5-flash-thinking",
    messages=[{"role": "user", "content": "Explain quantum computing"}]
)
print(resp.choices[0].message.content)
```

### Gemini CLI

```bash
export GEMINI_API_KEY=none
export GOOGLE_GEMINI_BASE_URL=http://localhost:8081
gemini
```

Supports Google native API endpoints:

- `GET /v1beta/models` — list models
- `POST /v1beta/models/{model}:generateContent` — non-streaming
- `POST /v1beta/models/{model}:streamGenerateContent` — streaming (SSE)

## Available Models

| Model                            | Description                                  | Output          |
| -------------------------------- | -------------------------------------------- | --------------- |
| `gemini-4-argon`                 | Frontier model for complex tasks (needs Pro) | **~20k+ chars** |
| `argon`, `gemini-4`              | Aliases for gemini-4-argon                   | **~20k+ chars** |
| `gemini-3.8-flash`               | Latest all-around model (Gemini 3.8 Flash)   | ~12k chars      |
| `gemini-3.7-flash`               | All-around model (Gemini 3.7 Flash)          | ~12k chars      |
| `gemini-3.6-flash`               | All-around model (Gemini 3.6 Flash)          | ~12k chars      |
| `gemini-3.5-flash`               | Alias for gemini-3.6-flash                   | ~12k chars      |
| `gemini-3.5-flash-thinking`      | Extended thinking, longest output            | **~20k chars**  |
| `gemini-3.5-flash-thinking-lite` | Adaptive thinking depth                      | ~15k chars      |
| `gemini-3.1-pro`                 | Advanced math & code (needs Pro cookie)      | ~12k chars      |
| `gemini-auto`                    | Auto model selection                         | varies          |
| `gemini-flash-lite`              | Fastest answers, lightweight                 | ~10k chars      |

### Thinking Depth

Append `@think=N` to any model name to control reasoning logic (corresponds to the web UI Thinking level submenu):

```text
gemini-4-argon@think=0              # High (deepest reasoning, default for thinking models)
gemini-4-argon@think=2              # Medium
gemini-4-argon@think=4              # Low (default for standard models)
```

### Model Inspection Headers

Every API response (both JSON and streaming SSE) includes HTTP response headers indicating actual model routing:
- `X-Resolved-Model`: The backend model identifier actually routed to (e.g. `gemini-4-argon`).
- `X-Model-Fallback: true`: Included whenever an unmapped model was requested and fell back to the default model.

## Advanced Features (Go Exclusive)

### 1. Dynamic Model Discovery & Upstream Routing Headers

When authenticated with account cookies, the proxy dynamically queries Gemini's `otAQ7b` RPC during startup to discover available models and account tier capacity. It attaches the required upstream routing headers (`x-goog-ext-525001261-jspb`) matching web application behavior, allowing models like Gemini 4 Argon to route properly.

### 2. Vision / Multimodal Uploads

Unlike the Python version which ignores images, this Go port actively intercepts `image_url` payloads (both base64 encoded and external HTTP URLs).
It initiates a background Scotty Resumable Upload session, converts the base64 image into Google internal WIZ blobs, and passes the WIZ image references directly to Gemini.
Just send OpenAI standard multimodal payloads and it will work!

### 3. TLS Impersonation (Bypass WAF)

If you run this tool in datacenters (like AWS/DigitalOcean) where Google blocks the IP with a 403 Forbidden, you can impersonate a real browser's TLS signature:

```bash
./gemini-web2api --impersonate chrome_120
```

Or in `config.json`: `"impersonate": "chrome_120"`. Supported profiles include `chrome_112` to `chrome_130`, `edge`, and `safari`.

## Optional: Cookie for Pro and Argon

Anonymous access works for Flash models, but `gemini-4-argon` and `gemini-3.1-pro` require an authenticated **Google AI Pro / Advanced** account session. Without authentication, upstream routes requests to Flash.

```bash
./gemini-web2api --cookie-file gemini-auth.json
```

### How to get cookies

1. Open Chrome, go to [gemini.google.com](https://gemini.google.com) and sign in with a **Gemini Advanced** Google account
2. Open DevTools (F12) → Application → Cookies → `https://gemini.google.com`
3. Copy these cookie values: `SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`
4. Create `cookie.txt` in this format:

```text
SID=your_sid; HSID=your_hsid; SSID=your_ssid; APISID=your_apisid; SAPISID=your_sapisid; __Secure-1PSID=your_1psid
```

Or use JSON format if you know your specific `SAPISID` hashing needs:

```json
{ "cookie": "SID=xxx; HSID=xxx; ...", "sapisid": "your_sapisid_value" }
```

**Alternative (browser extension)**: Use any "Export Cookies" extension to export cookies for `gemini.google.com` in Netscape format, then convert to the single-line format above.

### Authenticated account path and XSRF token

If the signed-in Gemini page URL contains an account index, such as:

```text
https://gemini.google.com/u/1/app/...
```

set `auth_user` to that index. Authenticated web requests may also require the page XSRF token. In the rendered Gemini page source, this token is exposed as `SNlM0e`; pass it as `xsrf_token` in `config.json`. The server sends it as the `at` form field.

Example:

```json
{
  "cookie_file": "/app/cookie.txt",
  "auth_user": "1",
  "xsrf_token": "AOOh0P...",
  "gemini_bl": "boq_assistant-bard-web-server_YYYYMMDD.xx_p0"
}
```

If authenticated requests return HTTP 400 with an `xsrf` error, refresh Gemini Web, update `xsrf_token`, and make sure `auth_user` matches the `/u/<index>/` part of the browser URL.

Pro routing requires **Gemini Advanced** (paid subscription). A free Google account cookie will authenticate but silently fall back to Flash.

## Configuration

Create `config.json` in the same directory:

```json
{
  "port": 8081,
  "host": "0.0.0.0",
  "retry_attempts": 3,
  "retry_delay_sec": 2,
  "request_timeout_sec": 180,
  "gemini_bl": "boq_assistant-bard-web-server_20260716.08_p0",
  "auth_user": null,
  "xsrf_token": null,
  "api_keys": ["sk-your-key"],
  "cookie_file": null,
  "proxy": null,
  "impersonate": null,
  "log_requests": true
}
```

When `api_keys` is empty `[]`, authentication is disabled. When keys are set, `/v1/*` endpoints require `Authorization: Bearer <key>`.

## Proxy

If you cannot access `gemini.google.com` directly, configure a proxy:

**Method 1: CLI argument**

```bash
# Linux / macOS
./gemini-web2api --proxy http://127.0.0.1:7890

# Windows (PowerShell)
.\gemini-web2api.exe --proxy http://127.0.0.1:7890
```

**Method 2: config.json**

```json
{ "proxy": "http://127.0.0.1:7890" }
```

**Method 3: Environment variable**

```bash
# Linux / macOS (bash)
export HTTPS_PROXY=http://127.0.0.1:7890
./gemini-web2api

# Windows (PowerShell)
$env:HTTPS_PROXY="http://127.0.0.1:7890"
.\gemini-web2api.exe

# Windows (Command Prompt)
set HTTPS_PROXY=http://127.0.0.1:7890
gemini-web2api.exe
```

## Limitations

- **Not real Pro/Ultra**: Without a paid subscription cookie, `gemini-3.1-pro` routes to the same Flash model. The "Pro" label is a UI preference, not a backend model switch.
- **Single-turn only**: Each request is an independent conversation. Multi-turn context is simulated by including previous messages in the prompt.
- **Rate limits**: Google may throttle high-frequency requests. The server retries automatically but sustained heavy use may be blocked.

## License

MIT License.
