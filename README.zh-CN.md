<!-- markdown-translator:b53ef407123ad63d9fe15c7fb4ba82cef2f0cef506e0a6a5be39cac111c67001 -->
# Gemini-web2api

<p align="center">
  <img src="https://raw.githubusercontent.com/Sophomoresty/gemini-web2api/main/logo.png" width="200" alt="gemini-web2api logo">
</p>

[英语](README.md)|[中文文档](README.zh-CN.md)

[![CI](https://github.com/ikhsan3adi/gemini-web2api/actions/workflows/ci.yml/badge.svg)](https://github.com/ikhsan3adi/gemini-web2api/actions/workflows/ci.yml)

高性能 Go 代理将 Google Gemini 的 Web 界面转换为 OpenAI 兼容的 Anthropic Messages API。零成本、跨平台、单一静态二进制文件。

这是对原始版本的完全 Go 重写[Python gemini-web2api](https://github.com/Sophomoresty/gemini-web2api)。它具有巨大的并发吞吐量、最小的内存占用以及增强的功能，例如**TLS 模拟**用于WAF绕过。

## 主要特点

* **可选 API 密钥**: 没有授权时`api_keys`配置后为空，OpenAI 风格的承载身份验证。
* **兼容 OpenAI**：直接替代`/v1/chat/completions`,`/v1/models`， 和`/v1/responses`（法典 CLI）。
* **人择兼容**:`/v1/messages`和`/v1/messages/count_tokens`与 Claude Code 的 SSE 流式传输。
* **浏览器 Cookie 导入**: Cookie 编辑器风格`cookie.json`被自动检测到时`cookie_file`未设置。
* **有状态会话**：Gemini Web 线程的可选延续，具有上下文压缩和长期运行的翻转。
* **多式联运（视觉）**：完全支持发送图像（`image_url`/base64) 到 Gemini，使用 Scotty 可恢复上传协议本机内置图像压缩。
* **TLS 模拟**：内置支持通过模仿 Chrome/Edge TLS 指纹来绕过 Cloudflare/WAF`tls-client`.
* **工具调用**：全函数调用支持（OpenAI格式）。
* **多种型号**：Gemini 4 Carbon、Neon、Argon 和 Hydrogen，以及旧版 Flash、Thinking、Pro、Auto 和 Lite 型号。您帐户名册上的可用模型会在启动时自动注册。
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

如果您的系统上安装了 Go 1.26+：

```bash
go install github.com/ikhsan3adi/gemini-web2api@latest
```

这将编译二进制文件并将其直接放入您的`$GOPATH/bin`（或者`%USERPROFILE%\go\bin`）。从任何地方运行它：

```bash
gemini-web2api --port 8081
```

### 3. 从源代码构建

先决条件：Go 1.26+

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

对于 cookie 身份验证，请将您的浏览器 cookie 导出到`cookie.json`（看[如何获取cookies](#how-to-get-cookies)）并添加`-v ./cookie.json:/app/cookie.json:ro`到下面的命令。 Docker Compose 会自动安装它。

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

> **笔记**：如果您得到空回复（`content: null`）使用 Docker 的默认桥接网络，切换到主机网络：`docker run --network host ...`或添加`network_mode: host`在您的撰写文件中。这是由 Gemini 的上游拒绝来自某些 Docker NAT IP 范围的请求引起的。

## 客户端配置

### Cherry Studio / ChatBox / 任何 OpenAI 客户端

|领域 |价值|
| -------- | -------------------------------------------------------------------------------- |
|基本网址 |`http://localhost:8081/v1`|
| API 密钥 |任何`api_keys`价值来自`config.json`;如果没有配置的话，什么都可以|
|型号|`gemini-4-hydrogen`|

### 卷曲

#### bash/macOS/Linux

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-key" \
  -d '{"model":"gemini-4-hydrogen","messages":[{"role":"user","content":"Hello!"}]}'
```

#### PowerShell（Windows）

```powershell
curl.exe --% http://127.0.0.1:8081/v1/chat/completions -H "Content-Type: application/json" -H "Authorization: Bearer sk-your-key" -d "{\"model\":\"gemini-4-hydrogen\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}"
```

> 注意：在 Windows PowerShell 上，使用`curl.exe`和`--%`因此 PowerShell 不会重新解释 JSON 引用或curl 选项。

### OpenAI Python SDK

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8081/v1", api_key="sk-your-key")
resp = client.chat.completions.create(
    model="gemini-4-hydrogen",
    messages=[{"role": "user", "content": "Explain quantum computing"}]
)
print(resp.choices[0].message.content)
```

### 克劳德代码（人择消息 API）

```bash
export ANTHROPIC_BASE_URL=http://localhost:8081
export ANTHROPIC_API_KEY=sk-your-key
export ANTHROPIC_MODEL=gemini-4-hydrogen
claude
```

支持人择消息端点：

* `POST /v1/messages`: 非流式 JSON 和流式 SSE (`message_start`,`content_block_*`,`message_delta`,`message_stop`)
* `POST /v1/messages/count_tokens`：返回基于字符的标记估计（`len(prompt) / 4`)

与饼干和`session_enabled: true`，多轮 Claude Code 请求继续同一个 Gemini Web 线程。看[有状态会话](#4-stateful-sessions).

> **笔记**： 什么时候`stream: true`在启用工具定义的情况下发送（`tool_choice != "none"`)，服务器缓冲上游响应来解析`tool_call`在发出 Anthropic SSE 事件之前阻塞，而不是实时流式传输原始令牌。

### 双子座命令行界面

```bash
export GEMINI_API_KEY=none
export GOOGLE_GEMINI_BASE_URL=http://localhost:8081
gemini
```

支持 Google 本机 API 端点：

* `GET /v1beta/models`：列出型号
* `POST /v1beta/models/{model}:generateContent`：非流媒体
* `POST /v1beta/models/{model}:streamGenerateContent`：流媒体（上海证券交易所）

## 可用型号

从最强大/最新的前沿模型到轻量级和遗留别名。

|型号|等级 |描述 |输出|
| -------------------------------- | ----------- | ---------------------------------------------------------- | ---------------- |
|`gemini-4-carbon`|超|长视野主体推理（前沿） |**~20k+ 字符**|
|`carbon`|超| gemini-4-carbon 的别名 |**~20k+ 字符**|
|`gemini-4-neon`|专业/超|前沿推理与复杂编码 |**~20k+ 字符**|
|`neon`|专业/超| Gemini-4-neon 的别名 |**~20k+ 字符**|
|`gemini-4-argon`|专业|复杂任务的高效前沿推理 |**~20k+ 字符**|
|`argon`,`gemini-4`|专业| Gemini-4-argon 的别名 |**~20k+ 字符**|
|`gemini-4-hydrogen`|免费|日常编码和代理工作流程 |约 12k 个字符 |
|`hydrogen`|免费| Gemini-4-氢的别名 |约 12k 个字符 |
|`gemini-3.8-flash`|免费|传统的日常老虎机（现在提供氢）|约 12k 个字符 |
|`gemini-3.7-flash`|免费|传统全能模型 |约 12k 个字符 |
|`gemini-3.6-flash`|免费|默认后备模型|约 12k 个字符 |
|`gemini-3.5-flash`|免费| gemini-3.6-flash 的别名 |约 12k 个字符 |
|`gemini-3.1-pro`|专业| Legacy Pro 老虎机（现在在 Pro 上提供 Neon）|约 12k 个字符 |
|`gemini-3.5-flash-thinking`|免费|延伸思维，最长产出 |**约 20,000 个字符**|
|`gemini-3.5-flash-thinking-lite`|免费|适应性思维深度|约 15,000 个字符 |
|`gemini-auto`|免费|汽车选型|变化 |
|`gemini-flash-lite`|免费|最快的答案，轻量级 | 〜10k 个字符 |

启动时从您的帐户名册中发现的模型（例如上面未列出的新 Gemini 变体）会自动注册并显示在`/v1/models`;看[动态模型注册](#dynamic-model-registration).

Gemini 4 模型通过网络应用程序的层级槽进行路由：Hydrogen 映射到日常/快速槽，Neon 映射到 Pro 槽，Carbon 映射到顶级槽。在没有所需订阅的帐户上，上游可能会将请求路由到该插槽上可用的下一个模型（例如，Carbon 票证在非 Ultra 帐户上解析为 Argon）。

### 思考深度

附加`@think=N`任意模型名称来控制推理逻辑（对应Web UI Thinking级别子菜单）：

```text
gemini-4-argon@think=0              # High (deepest reasoning, default for thinking models)
gemini-4-argon@think=2              # Medium
gemini-4-argon@think=4              # Low (default for standard models)
```

### 模型检查头

每个 API 响应（JSON 和流式 SSE）都包含指示实际模型路由的 HTTP 响应标头：

* `X-Resolved-Model`：实际路由到的后端模型标识符（例如`gemini-4-neon`).
* `X-Model-Fallback: true`：每当请求未映射的模型并回退到默认模型时都包含在内。

## 高级功能（独家）

### 1.动态模型发现和上游路由头

当使用帐户 cookie 进行身份验证时，代理会动态查询 Gemini 的`otAQ7b`启动期间进行 RPC 以发现可用模型和帐户层容量。它附加所需的上游路由标头（`x-goog-ext-525001261-jspb`) 匹配 Web 应用程序行为，允许 Gemini 4 系列和旧层插槽正确路由。

#### 动态模型注册

发现的模型也会自动注册为从其上游标签派生的 API 模型名称：名册条目显示为`4 Hydrogen`变成`gemini-4-hydrogen`（也可称为`4-hydrogen`）。这些名字列出来了`/v1/models`和`/v1beta/models`并且无需更改代码即可在请求中使用，因此新推出的变体一旦出现在您的帐户名册中即可发挥作用。

* 静态条目（上表）始终优先于已发现的同名条目。
* 如果静态模型的上游路由票证发生轮换，则具有相同显示标签的名册条目将自动用于路由标头。
* Discovery 需要 cookie；匿名设置只能看到静态模型列表。
* `@think=N`适用于已发现的模型，但只有标准思维深度映射到上游标头；更高层次，例如`xhigh`不是合成的。
* 请求既不是静态的也不存在于名册中的模型会回退到默认模型：`X-Model-Fallback: true`.

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

### 4. 有状态会话

与饼干和`session_enabled: true`，代理保持 Gemini Web 线程状态（`cid`/`rid`/`rcid`）跨轮并仅发送新消息。一旦使用结束，超大对话就会被压缩`compress_threshold_ratio * max_context_tokens`，会话将转到一个新的线程，其中的摘要接近`session_rollover_ratio * max_context_tokens`或者在上游上下文错误之后。

* 发送`X-Session-Id`（或者`X-Conversation-Id`) 固定对话。标题限制为 128 个字符。
* 回应暴露`X-Gemini-Session-Id`,`X-Gemini-Session-Rollover`， 和`X-Gemini-Context-Compressed`.
* 自动匹配需要调用者凭证（`Authorization`,`x-api-key`,`x-goog-api-key`， 或者`?key=`），并且密钥的范围仅限于该凭证，因此单独的调用者永远不会共享线程。没有凭证的请求保持无状态。
* 调整行为`max_context_tokens`,`keep_recent_messages`,`session_rollover_ratio`,`compress_threshold_ratio`,`session_ttl_sec`， 和`max_sessions`.

## 可选：Pro / Ultra 型号的 Cookie

匿名访问涵盖免费层插槽（`gemini-4-hydrogen`和旧版 Flash/Lite），目前解析为 Gemini 4 Hydrogen。`gemini-4-argon`需要**谷歌人工智能专业版**,`gemini-4-neon`需要**专业/超**， 和`gemini-4-carbon`需要**极端主义者**。如果没有授权 cookie，请求仍然会成功，但每个层槽都会解析为帐户上可用的最佳模型。

```bash
./gemini-web2api --cookie-file gemini-auth.json
```

什么时候`cookie_file`未设置，服务器寻找`./cookie.json`首先，然后`~/.config/gemini-web2api/cookie.json`.

### 如何获取cookies

1. 打开 Chrome，转到[gemini.google.com](https://gemini.google.com)并登录（**人工智能专业版/超级版**仅 Pro/Ultra-tier 型号需要订阅）
2. 打开 DevTools (F12) → 应用程序 → Cookies →`https://gemini.google.com`
3. 复制这些 cookie 值：`SID`,`HSID`,`SSID`,`APISID`,`SAPISID`,`__Secure-1PSID`
4. 创造`cookie.txt`以这种格式：

```text
SID=your_sid; HSID=your_hsid; SSID=your_ssid; APISID=your_apisid; SAPISID=your_sapisid; __Secure-1PSID=your_1psid
```

5. 或者使用浏览器扩展（例如 Cookie-Editor）将 cookie 导出为 JSON 数组，并将文件另存为`cookie.json`。服务器仅保留 Google 域条目。模板可在`cookie.example.json`.

如果您愿意设置，单对象 JSON 格式仍然有效`SAPISID`明确地：

```json
{ "cookie": "SID=xxx; HSID=xxx; ...", "sapisid": "your_sapisid_value" }
```

**替代方案（浏览器扩展）**：使用任何“导出 Cookie”扩展程序导出 Cookie`gemini.google.com`Netscape 格式，然后转换为上面的单行格式。

### 经过身份验证的帐户路径和 XSRF 令牌

配置cookie后，服务器会获取当前的`bl`和`SNlM0e`来自登录的 XSRF 令牌`/app`页面，并在上游 HTTP 400 或 405 后刷新一次。显式值`config.json`被用作起点。

如果登录的 Gemini 页面 URL 包含帐户索引，例如：

```text
https://gemini.google.com/u/1/app/...
```

放`auth_user`到该索引。要自行固定令牌，请复制`SNlM0e`从渲染的页面源并将其设置为`xsrf_token`;服务器将其发送为`at`表单字段。

例子：

```json
{
  "cookie_file": "./cookie.json",
  "auth_user": "1",
  "xsrf_token": "AOOh0P...",
  "gemini_bl": "boq_assistant-bard-web-server_YYYYMMDD.xx_p0"
}
```

`cookie_file`当`cookie.json`被自动检测到；`xsrf_token`和`gemini_bl`是可选的，因为它们是从页面中发现的。如果经过身份验证的请求不断返回 HTTP 400`xsrf`错误，刷新Gemini Web，更新`xsrf_token`手动，并确保`auth_user`匹配`/u/<index>/`浏览器 URL 的一部分。

型号可用性取决于您的帐户级别。免费帐户 cookie 通常会进行身份验证，但 Pro/Ultra 老虎机解析为免费层模型（例如，日常老虎机提供 Gemini 4 Hydrogen）。

## 配置

创造`config.json`在同一目录中：

```json
{
  "port": 8081,
  "host": "0.0.0.0",
  "retry_attempts": 3,
  "retry_delay_sec": 2,
  "request_timeout_sec": 180,
  "gemini_bl": "boq_gemini-web-uiserver_20261120.09_p0",
  "auth_user": null,
  "xsrf_token": null,
  "api_keys": ["sk-your-key"],
  "cookie_file": null,
  "proxy": null,
  "impersonate": null,
  "log_requests": true,
  "session_enabled": true,
  "max_context_tokens": 1000000,
  "session_rollover_ratio": 0.85,
  "compress_threshold_ratio": 0.60,
  "keep_recent_messages": 6,
  "session_ttl_sec": 3600,
  "max_sessions": 1024
}
```

什么时候`api_keys`是空的`[]`，身份验证已禁用。设置按键后，`/v1/*`端点需要`Authorization: Bearer <key>`（或者`x-api-key`).

什么时候`session_enabled`是`true`并加载 cookie，代理继续 Gemini Web 线程（`cid`/`rid`/`rcid`），压缩旧的/大的转弯过去`compress_threshold_ratio`，然后转到一个新的会话，其中包含过去的压缩摘要`session_rollover_ratio`（或对上游上下文错误做出反应）。浏览器导出的 cookie 数组（`cookie.json`， 看`cookie.example.json`) 受支持并在存在时自动检测。

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

* **层相关路由**：Gemini Web 应用程序通过层槽而不是直接模型切换来公开模型。如果没有授权 cookie，Pro/Ultra 型号名称将解析为同一插槽上可用的最佳型号。
* **会话连续性和代币估计**：与饼干和`session_enabled: true`，多回合上下文使用 Gemini Web 线程（`cid`/`rid`/`rcid`) 具有自动上下文压缩和翻转功能；匿名设置保持单轮。令牌计入`usage`和`/v1/messages/count_tokens`是基于字符的估计（`len / 4`).
* **速率限制**：Google 可能会限制高频请求。服务器会自动重试，但持续的大量使用可能会被阻止。

## 执照

我的许可证。
