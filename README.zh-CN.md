<!-- markdown-translator:62dba9eec309a74d88006d57d713a432a95bf4677b56668264e4322808769359 -->
# Gemini-web2api

<p align="center">
  <img src="https://raw.githubusercontent.com/Sophomoresty/gemini-web2api/main/logo.png" width="200" alt="gemini-web2api logo">
</p>

[英语](README.md)|[中文文档](README.zh-CN.md)

高性能 Go 代理将 Google Gemini 的 Web 界面转换为 OpenAI 兼容的 API。零成本、跨平台、单一静态二进制文件。

这是对原始版本的完全 Go 重写[Python gemini-web2api](https://github.com/Sophomoresty/gemini-web2api)。它具有巨大的并发吞吐量、最小的内存占用以及增强的功能，例如**TLS 模拟**用于WAF绕过。

## 主要特点

* **可选 API 密钥**: 没有授权时`api_keys`配置后为空，OpenAI 风格的承载身份验证。
* **兼容 OpenAI**：直接替代`/v1/chat/completions`,`/v1/models`， 和`/v1/responses`（法典 CLI）。
* **多式联运（视觉）**：完全支持发送图像（`image_url`/base64) 到 Gemini，使用 Scotty 可恢复上传协议本机内置图像压缩。
* **TLS 模拟**：内置支持通过模仿 Chrome/Edge TLS 指纹来绕过 Cloudflare/WAF`tls-client`🔥.
* **工具调用**：全函数调用支持（OpenAI格式）。
* **多种型号**：Flash (3.6)、Extend Thinking（20k+ 字符输出）、Pro、Auto、Lite。
* **思考深度**：可通过调整推理`@think=N`后缀（0=最深，4=最浅）。
* **网页搜索**：内置互联网访问（Gemini 的本机搜索）。
* **高性能**：纯 Go 静态二进制文件，具有高并发 SSE 流吞吐量和最小的内存占用。
* **跨平台**：零运行时依赖性，单个静态二进制文件。
* **法典 CLI**：响应API（`/v1/responses`）用于 OpenAI Codex 集成。
* **双子座命令行界面**：谷歌原生API（`/v1beta/models`）以实现 Gemini CLI 兼容性。

## 快速入门

### 1.下载预构建的二进制文件（推荐）

从以下地址下载适用于您的操作系统（Linux、macOS、Windows）的预编译二进制文件[发布](https://github.com/ikhsan3adi/gemini-web2api/releases)页：

**Linux / macOS：**

```bash
chmod +x gemini-web2api
./gemini-web2api --port 8081
```

**Windows（PowerShell /命令提示符）：**

```powershell
.\gemini-web2api.exe --port 8081
```

### 2.通过Go工具链安装

如果您的系统上安装了 Go 1.22+：

```bash
go install github.com/ikhsan3adi/gemini-web2api@latest
```

这将编译二进制文件并将其直接放入您的`$GOPATH/bin`（或者`%USERPROFILE%\go\bin`）。从任何地方运行它：

```bash
gemini-web2api --port 8081
```

### 3. 从源代码构建

先决条件：Go 1.22+

```bash
git clone https://github.com/ikhsan3adi/gemini-web2api.git
cd gemini-web2api
```

构建后，运行可执行文件：

**Linux / macOS：**

```bash
go build -o gemini-web2api .
./gemini-web2api --port 8081
```

**Windows（PowerShell /命令提示符）：**

```powershell
go build -o gemini-web2api.exe .
.\gemini-web2api.exe --port 8081
```

服务器开始于`http://localhost:8081/v1`.

### 4.通过Docker运行

在通过 Docker 运行之前，复制示例配置文件：

```bash
cp config.example.json config.json
```

#### 选项 A：运行预构建的 GHCR 映像（无需 git 克隆）

```bash
docker run -d --name gemini-web2api -p 8081:8081 ghcr.io/ikhsan3adi/gemini-web2api:latest
```

#### 选项 B：本地构建并运行

```bash
docker build -t gemini-web2api .
docker run -d --name gemini-web2api -p 8081:8081 -v ./config.json:/app/config.json gemini-web2api
```

#### 选项 C：Docker Compose

```bash
docker compose up -d
```

> **笔记**：如果您得到空回复（`content: null`）使用 Docker 的默认桥接网络，切换到主机网络：`docker run --network host ...`或添加`network_mode: host`在您的撰写文件中。这是由 Gemini 的上游拒绝来自某些 Docker NAT IP 范围的请求造成的。

## 客户端配置

### Cherry Studio / ChatBox / 任何 OpenAI 客户端

|领域 |价值|
| -------- | -------------------------------------------------------------------------------- |
|基本网址 |`http://localhost:8081/v1`|
| API 密钥 |任何`api_keys`价值来自`config.json`;如果没有配置的话，什么都可以|
|型号|`gemini-3.5-flash-thinking`|

### 卷曲

#### bash/macOS/Linux

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-key" \
  -d '{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"Hello!"}]}'
```

#### PowerShell（Windows）

```powershell
curl.exe --% http://127.0.0.1:8081/v1/chat/completions -H "Content-Type: application/json" -H "Authorization: Bearer sk-your-key" -d "{\"model\":\"gemini-3.5-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}"
```

> 注意：在 Windows PowerShell 上，使用`curl.exe`和`--%`因此 PowerShell 不会重新解释 JSON 引用或curl 选项。

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

### 双子座命令行界面

```bash
export GEMINI_API_KEY=none
export GOOGLE_GEMINI_BASE_URL=http://localhost:8081
gemini
```

支持 Google 本机 API 端点：

* `GET /v1beta/models`— 列出型号
* `POST /v1beta/models/{model}:generateContent`— 非流媒体
* `POST /v1beta/models/{model}:streamGenerateContent`— 流媒体（上交所）

## 可用型号

|型号|描述 |输出|
| -------------------------------- | ------------------------------------------------------------------ | --------------- |
|`gemini-4-argon`|复杂任务的前沿模型（需要 Pro）|**~20k+ 字符**|
|`argon`,`gemini-4`| Gemini-4-argon 的别名 |**~20k+ 字符**|
|`gemini-3.8-flash`|最新全能机型（Gemini 3.8 Flash）|约 12k 个字符 |
|`gemini-3.7-flash`|全能机型（Gemini 3.7 Flash）|约 12k 个字符 |
|`gemini-3.6-flash`|全能机型（Gemini 3.6 Flash）|约 12k 个字符 |
|`gemini-3.5-flash`| gemini-3.6-flash 的别名 |约 12k 个字符 |
|`gemini-3.5-flash-thinking`|延伸思维，最长产出 |**约 20,000 个字符**|
|`gemini-3.5-flash-thinking-lite`|适应性思维深度|约 15,000 个字符 |
|`gemini-3.1-pro`|高级数学和代码（需要 Pro cookie）|约 12k 个字符 |
|`gemini-auto`|汽车选型|变化 |
|`gemini-flash-lite`|最快的答案，轻量级 | 〜10k 个字符 |

### 思考深度

附加`@think=N`任意模型名称来控制推理逻辑（对应Web UI Thinking级别子菜单）：

```text
gemini-4-argon@think=0              # High (deepest reasoning, default for thinking models)
gemini-4-argon@think=2              # Medium
gemini-4-argon@think=4              # Low (default for standard models)
```

### 模型检查头

每个 API 响应（JSON 和流式 SSE）都包含指示实际模型路由的 HTTP 响应标头：

* `X-Resolved-Model`：实际路由到的后端模型标识符（例如`gemini-4-argon`).
* `X-Model-Fallback: true`：每当请求未映射的模型并回退到默认模型时都包含在内。

## 高级功能（独家）

### 1.动态模型发现和上游路由头

当使用帐户 cookie 进行身份验证时，代理会动态查询 Gemini 的`otAQ7b`启动期间进行 RPC 以发现可用模型和帐户层容量。它附加所需的上游路由标头（`x-goog-ext-525001261-jspb`）匹配 Web 应用程序行为，允许 Gemini 4 Argon 等模型正确路由。

### 2. 视觉/多模式上传

与忽略图像的 Python 版本不同，此 Go 端口主动拦截`image_url`有效负载（base64 编码和外部 HTTP URL）。
它启动后台 Scotty 可恢复上传会话，将 base64 图像转换为 Google 内部 WIZ blob，并将 WIZ 图像引用直接传递给 Gemini。
只需发送 OpenAI 标准多模式有效负载，它就会起作用！

### 3. TLS 模拟（绕过 WAF）

如果您在 Google 使用 403 Forbidden 阻止 IP 的数据中心（如 AWS/DigitalOcean）中运行此工具，您可以模拟真实浏览器的 TLS 签名：

```bash
./gemini-web2api --impersonate chrome_120
```

或者在`config.json`:`"impersonate": "chrome_120"`。支持的配置文件包括`chrome_112`到`chrome_130`,`edge`， 和`safari`.

## 可选：Pro 和 Argon 的 Cookie

匿名访问适用于 Flash 模型，但是`gemini-4-argon`和`gemini-3.1-pro`需要经过身份验证**谷歌人工智能专业版/高级版**帐户会话。如果没有身份验证，上游会将请求路由到 Flash。

```bash
./gemini-web2api --cookie-file gemini-auth.json
```

### 如何获取cookies

1. 打开 Chrome，转到[gemini.google.com](https://gemini.google.com)并使用 a 登录**双子座高级版**谷歌帐户
2. 打开 DevTools (F12) → 应用程序 → Cookies →`https://gemini.google.com`
3. 复制这些 cookie 值：`SID`,`HSID`,`SSID`,`APISID`,`SAPISID`,`__Secure-1PSID`
4. 创造`cookie.txt`以这种格式：

```text
SID=your_sid; HSID=your_hsid; SSID=your_ssid; APISID=your_apisid; SAPISID=your_sapisid; __Secure-1PSID=your_1psid
```

或者如果您知道自己的具体情况，请使用 JSON 格式`SAPISID`散列需求：

```json
{ "cookie": "SID=xxx; HSID=xxx; ...", "sapisid": "your_sapisid_value" }
```

**替代方案（浏览器扩展）**：使用任何“导出 Cookie”扩展程序导出 Cookie`gemini.google.com`Netscape 格式，然后转换为上面的单行格式。

### 经过身份验证的帐户路径和 XSRF 令牌

如果登录的 Gemini 页面 URL 包含帐户索引，例如：

```text
https://gemini.google.com/u/1/app/...
```

放`auth_user`到该索引。经过身份验证的 Web 请求可能还需要页面 XSRF 令牌。在渲染的 Gemini 页面源代码中，此令牌公开为`SNlM0e`;将其传递为`xsrf_token`在`config.json`。服务器将其作为`at`表单字段。

例子：

```json
{
  "cookie_file": "/app/cookie.txt",
  "auth_user": "1",
  "xsrf_token": "AOOh0P...",
  "gemini_bl": "boq_assistant-bard-web-server_YYYYMMDD.xx_p0"
}
```

如果经过身份验证的请求返回 HTTP 400`xsrf`错误，刷新Gemini Web，更新`xsrf_token`，并确保`auth_user`匹配`/u/<index>/`浏览器 URL 的一部分。

专业路由需要**双子座高级版**（付费订阅）。免费的 Google 帐户 cookie 将进行身份验证，但会默默地退回到 Flash。

## 配置

创造`config.json`在同一目录中：

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

什么时候`api_keys`是空的`[]`，身份验证已禁用。设置按键后，`/v1/*`端点需要`Authorization: Bearer <key>`.

## 代理人

如果您无法访问`gemini.google.com`直接配置代理：

**方法一：CLI 参数**

```bash
# Linux / macOS
./gemini-web2api --proxy http://127.0.0.1:7890

# Windows (PowerShell)
.\gemini-web2api.exe --proxy http://127.0.0.1:7890
```

**方法2：config.json**

```json
{ "proxy": "http://127.0.0.1:7890" }
```

**方法三：环境变量**

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

## 局限性

* **不是真正的 Pro/Ultra**：没有付费订阅 cookie，`gemini-3.1-pro`路由到相同的 Flash 模型。 “Pro”标签是 UI 首选项，而不是后端模型开关。
* **仅单圈**：每个请求都是一个独立的对话。通过在提示中包含先前的消息来模拟多轮上下文。
* **速率限制**：Google 可能会限制高频请求。服务器会自动重试，但持续的大量使用可能会被阻止。

## 执照

我的许可证。
