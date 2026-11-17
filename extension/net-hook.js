// Gemini Web2API Network Stream Interceptor (MAIN world, document_start)
// Transparently taps fetch() and XMLHttpRequest responses from Gemini Enterprise
// (Vertex AI Search / Discovery Engine widgetStreamAssist & streamAssist) and
// Consumer Gemini Web (BardFrontendService/StreamGenerate) without altering page behavior.

(function (globalScope) {
  "use strict";

  const MSG_KEY = "__web2api_net";

  function classifyEndpoint(url, method) {
    if (!url) return null;
    if (method && String(method).toUpperCase() !== "POST") return null;
    const u = String(url);

    if (u.includes("BardFrontendService/StreamGenerate")) {
      return "gemini_web";
    }

    if (
      /widgetStreamAssist/i.test(u) ||
      /[:/]streamAssist(\b|\?|$)/i.test(u) ||
      /widgetAssist(\b|\?|$)/i.test(u) ||
      /widgetAnswer(\b|\?|$)/i.test(u) ||
      /widgetConverse/i.test(u) ||
      /[:/]answer(\b|\?|$)/i.test(u) ||
      /[:/]converse(\b|\?|$)/i.test(u) ||
      /[:/]streamGenerateContent(\b|\?|$)/i.test(u) ||
      /[:/]generateContent(\b|\?|$)/i.test(u)
    ) {
      if (/googleapis\.com|google\.com/i.test(u) || u.startsWith("/")) {
        return "discovery_json";
      }
    }

    return null;
  }

  function classifyUploadEndpoint(url, method, body, contentType) {
    if (!url) return false;
    const m = method ? String(method).toUpperCase() : "POST";
    if (m !== "POST" && m !== "PUT") return false;
    const u = String(url);

    // Ignore localhost bridge traffic itself
    if (/^https?:\/\/(localhost|127\.0\.0\.1)(:\d+)?\//i.test(u)) return false;

    if (
      /widgetAddContextFile/i.test(u) ||
      /batchAddContextFile/i.test(u) ||
      /[:/]addContextFile(\b|\?|$)/i.test(u) ||
      /[:/]uploadContextFile(\b|\?|$)/i.test(u) ||
      /widgetAddSessionFile/i.test(u) ||
      /[:/]addSessionFile(\b|\?|$)/i.test(u) ||
      /[:/]uploadSessionFile(\b|\?|$)/i.test(u) ||
      /[:/]uploadFile(\b|\?|$)/i.test(u) ||
      /[:/]uploadMedia(\b|\?|$)/i.test(u) ||
      /fileUpload/i.test(u) ||
      /content-push\.clients6\.google\.com\/upload/i.test(u) ||
      /push\.clients6\.google\.com\/upload/i.test(u) ||
      (/googleapis\.com|google\.com/i.test(u) && /\/upload(\/|\?|$)/i.test(u))
    ) {
      return true;
    }

    // Body / Content-Type inspection catches any Google file upload endpoint even if renamed
    if (!classifyEndpoint(u, m) && (/googleapis\.com|google\.com/i.test(u) || u.startsWith("/"))) {
      const ct = String(contentType || "").toLowerCase();
      if (ct.includes("multipart/form-data")) return true;
      if (body) {
        try {
          if (typeof FormData !== "undefined" && body instanceof FormData) return true;
          if (typeof File !== "undefined" && body instanceof File) return true;
          if (typeof Blob !== "undefined" && body instanceof Blob && body.size > 0) return true;
          if (
            typeof body === "string" &&
            body.length > 200 &&
            /"(fileName|file_name|fileContent|file_content|sessionFile|contextFile|addContextFile)"/i.test(body)
          ) {
            return true;
          }
        } catch (e) {}
      }
    }

    return false;
  }

  // Incremental balanced-brace top-level JSON object extractor.
  // Handles streaming JSON arrays ([{...},{...}]), anti-XSSI prefixes (")]}'"),
  // NDJSON ({...}\n{...}), and SSE (data: {...}\n\n) without splitting on braces inside strings.
  function createJsonObjectExtractor(onObject) {
    let buf = "";
    let scanPos = 0;
    let depth = 0;
    let inString = false;
    let escape = false;
    let objStart = -1;

    return function feed(chunk) {
      if (!chunk) return;
      buf += chunk;

      for (let i = scanPos; i < buf.length; i++) {
        const ch = buf[i];

        if (depth === 0) {
          if (ch === "{") {
            objStart = i;
            depth = 1;
            inString = false;
            escape = false;
          }
          continue;
        }

        if (inString) {
          if (escape) {
            escape = false;
          } else if (ch === "\\") {
            escape = true;
          } else if (ch === '"') {
            inString = false;
          }
          continue;
        }

        if (ch === '"') {
          inString = true;
        } else if (ch === "{") {
          depth++;
        } else if (ch === "}") {
          depth--;
          if (depth === 0 && objStart !== -1) {
            const jsonStr = buf.slice(objStart, i + 1);
            objStart = -1;
            try {
              const parsed = JSON.parse(jsonStr);
              onObject(parsed);
            } catch (e) {
              // Ignore non-JSON brace fragments
            }
          }
        }
      }

      if (depth === 0) {
        buf = "";
        scanPos = 0;
      } else if (objStart > 0) {
        buf = buf.slice(objStart);
        scanPos = buf.length;
        objStart = 0;
      } else {
        scanPos = buf.length;
      }
    };
  }

  const RE_CODE_EXEC = /```(?:python|javascript|text)\?code_(?:reference|stdout)&code_event_index=\d+\n[\s\S]*?```\n?/g;
  const RE_CARD_CONTENT = /http:\/\/googleusercontent\.com\/card_content\/\d+\n?/g;
  const RE_DISCOVERY_ROUTING_SECTION = /(?:^|\n)[ \t]*(?:I am (?:transferring your request|checking the available tools and integrations)[^\n]*\r?\n)?[ \t]*\[\[\/?section(?::[^\]]*)?\]\][ \t]*\r?\n?/gi;
  const RE_DISCOVERY_STANDALONE_ROUTING = /^[ \t]*I am (?:transferring your request to [^\n]+|checking the available tools and integrations in this environment[^\n]*)[ \t]*$/i;

  function cleanBardText(text) {
    if (!text) return "";
    return text.replace(RE_CODE_EXEC, "").replace(RE_CARD_CONTENT, "");
  }

  function cleanDiscoveryText(text) {
    if (!text) return "";
    const raw = String(text);
    if (RE_DISCOVERY_STANDALONE_ROUTING.test(raw.trim())) {
      return "";
    }
    const cleaned = raw.replace(RE_DISCOVERY_ROUTING_SECTION, "");
    if (cleaned !== raw) {
      return cleaned.replace(/^\n+/, "");
    }
    return raw;
  }

  function extractBardTextsFromLine(line) {
    if (!line || !line.includes('"wrb.fr"')) return [];
    let arr;
    try {
      arr = JSON.parse(line);
    } catch (e) {
      return [];
    }
    if (!Array.isArray(arr) || arr.length === 0) return [];
    const first = arr[0];
    if (!Array.isArray(first) || first.length < 3 || typeof first[2] !== "string") return [];

    let inner;
    try {
      inner = JSON.parse(first[2]);
    } catch (e) {
      return [];
    }
    if (!Array.isArray(inner) || inner.length <= 4 || !Array.isArray(inner[4])) return [];

    const texts = [];
    for (const part of inner[4]) {
      if (Array.isArray(part) && part.length > 1 && Array.isArray(part[1])) {
        for (const t of part[1]) {
          if (typeof t === "string" && t.length > 0) {
            texts.push(t);
          }
        }
      }
    }
    return texts;
  }

  function createStreamProcessor(kind, url, emit) {
    const state = {
      kind,
      url,
      accumulated: "",
      hasText: false,
      failed: false,
      skipped: false,
      skippedReasons: []
    };

    function emitIncrementalOrDedup(chunkText, isFinalState) {
      if (!chunkText) return;
      const acc = state.accumulated;
      let delta = chunkText;

      if (acc.length > 0) {
        if (isFinalState && chunkText === acc) {
          return;
        }
        if ((isFinalState || acc.length >= 16) && chunkText.startsWith(acc)) {
          delta = chunkText.slice(acc.length);
          if (!delta) return;
        }
      }

      state.accumulated += delta;
      state.hasText = true;
      emit("delta", { text: delta });
    }

    function processDiscoveryObject(obj) {
      if (!obj || typeof obj !== "object") return;

      // 1. StreamAssistResponse (widgetStreamAssist or streamAssist or answerQuery)
      const sar = obj.streamAssistResponse || obj.stream_assist_response || obj;
      const answer = sar && (sar.answer || (sar.answerQueryResponse && sar.answerQueryResponse.answer));

      if (answer && typeof answer === "object") {
        const answerState = String(answer.state || "").toUpperCase();
        const isFinalState =
          answerState === "SUCCEEDED" ||
          answerState === "FAILED" ||
          answerState === "SKIPPED" ||
          Boolean(answer.name);

        if (Array.isArray(answer.replies) && answer.replies.length > 0) {
          let chunkAnswerText = "";
          let sawThought = false;

          for (const reply of answer.replies) {
            if (!reply || typeof reply !== "object") continue;
            const gc = reply.groundedContent || reply.grounded_content;
            if (!gc || typeof gc !== "object") continue;
            const content = gc.content;
            if (!content || typeof content !== "object") continue;

            const role = String(content.role || "").toLowerCase();
            if (role === "user") continue;

            if (content.thought === true || gc.thought === true || reply.thought === true) {
              sawThought = true;
              continue;
            }

            if (typeof content.text === "string" && content.text.length > 0) {
              const cleaned = cleanDiscoveryText(content.text);
              if (cleaned.length > 0) {
                chunkAnswerText += cleaned;
              } else {
                sawThought = true;
              }
            }
          }

          if (sawThought && !chunkAnswerText) {
            emit("thinking", {});
          }

          if (chunkAnswerText) {
            emitIncrementalOrDedup(chunkAnswerText, isFinalState);
          }
        }

        const answerText = answer.answerText || answer.answer_text;
        if (typeof answerText === "string" && answerText.length > 0) {
          const cleanedAnswer = cleanDiscoveryText(answerText);
          if (cleanedAnswer.length > 0) {
            emitIncrementalOrDedup(cleanedAnswer, true);
          }
        }

        if (answerState === "FAILED") {
          state.failed = true;
        }
        if (answerState === "SKIPPED") {
          state.skipped = true;
          state.skippedReasons = answer.assistSkippedReasons || answer.assist_skipped_reasons || [];
        }
        return;
      }

      // 2. ConverseConversation (reply.summary.summaryText)
      if (obj.reply && obj.reply.summary && typeof obj.reply.summary.summaryText === "string") {
        const sText = obj.reply.summary.summaryText;
        if (sText.length > 0) {
          emitIncrementalOrDedup(sText, true);
        }
        return;
      }

      // 3. Gemini generateContent / streamGenerateContent (candidates[].content.parts[])
      if (Array.isArray(obj.candidates) && obj.candidates.length > 0) {
        const cand = obj.candidates[0];
        const parts = cand && cand.content && Array.isArray(cand.content.parts) ? cand.content.parts : [];
        let chunkText = "";
        let sawThought = false;

        for (const p of parts) {
          if (!p || typeof p !== "object") continue;
          if (p.thought === true) {
            sawThought = true;
            continue;
          }
          if (typeof p.text === "string" && p.text.length > 0) {
            chunkText += p.text;
          }
        }

        if (sawThought && !chunkText) {
          emit("thinking", {});
        }
        if (chunkText) {
          const isFinal = Boolean(cand.finishReason);
          emitIncrementalOrDedup(chunkText, isFinal);
        }
      }
    }

    if (kind === "discovery_json") {
      const feedJson = createJsonObjectExtractor(processDiscoveryObject);
      return {
        state,
        feed(chunk) {
          feedJson(chunk);
        },
        finish() {
          if (!state.hasText && typeof console !== "undefined" && console.warn) {
            console.warn("[Web2API NetHook] Discovery stream finished with no extracted text:", url, {
              failed: state.failed,
              skipped: state.skipped,
              skippedReasons: state.skippedReasons
            });
          }
          emit("done", {
            hasText: state.hasText,
            failed: state.failed,
            skipped: state.skipped,
            skippedReasons: state.skippedReasons
          });
        }
      };
    }

    // Consumer Gemini Web (BardFrontendService/StreamGenerate)
    let lineBuf = "";

    function handleBardLine(line) {
      const texts = extractBardTextsFromLine(line);
      if (texts.length === 0) return;

      let best = "";
      for (const t of texts) {
        const cleaned = cleanBardText(t);
        if (cleaned.length > best.length) {
          best = cleaned;
        }
      }
      if (!best) return;

      if (best.length > state.accumulated.length) {
        if (best.startsWith(state.accumulated)) {
          const delta = best.slice(state.accumulated.length);
          state.accumulated = best;
          state.hasText = true;
          emit("delta", { text: delta });
        } else {
          state.accumulated = best;
          state.hasText = true;
          emit("replace", { text: best });
        }
      }
    }

    return {
      state,
      feed(chunk) {
        if (!chunk) return;
        lineBuf += chunk;
        let idx;
        while ((idx = lineBuf.indexOf("\n")) !== -1) {
          const line = lineBuf.slice(0, idx);
          lineBuf = lineBuf.slice(idx + 1);
          handleBardLine(line);
        }
      },
      finish() {
        if (lineBuf.trim()) {
          handleBardLine(lineBuf);
          lineBuf = "";
        }
        emit("done", {
          hasText: state.hasText,
          failed: state.failed,
          skipped: state.skipped,
          skippedReasons: state.skippedReasons
        });
      }
    };
  }

  // Export helpers for unit testing in Node.js without touching browser globals
  if (typeof module !== "undefined" && module.exports) {
    module.exports = {
      MSG_KEY,
      classifyEndpoint,
      classifyUploadEndpoint,
      createJsonObjectExtractor,
      extractBardTextsFromLine,
      cleanBardText,
      cleanDiscoveryText,
      createStreamProcessor
    };
    return;
  }

  // Browser MAIN world installation
  if (!globalScope || globalScope.__web2apiNetHookInstalled) return;
  globalScope.__web2apiNetHookInstalled = true;

  let streamCounter = 0;

  function postToBridge(type, streamId, url, extra) {
    const payload = Object.assign(
      {
        [MSG_KEY]: true,
        type,
        streamId,
        url,
        ts: Date.now()
      },
      extra || {}
    );
    try {
      if (globalScope.top && globalScope.top !== globalScope) {
        globalScope.top.postMessage(payload, "*");
      }
    } catch (e) {}
    try {
      globalScope.postMessage(payload, "*");
    } catch (e2) {}
  }

  function startStream(kind, url) {
    const streamId = `net_${Date.now()}_${++streamCounter}`;
    postToBridge("start", streamId, url, { kind });
    return {
      streamId,
      processor: createStreamProcessor(kind, url, (type, extra) => {
        postToBridge(type, streamId, url, extra);
      })
    };
  }

  // 1. Intercept window.fetch
  const origFetch = globalScope.fetch;
  function extractContentType(headers) {
    if (!headers) return "";
    try {
      if (typeof Headers !== "undefined" && headers instanceof Headers) {
        return headers.get("content-type") || "";
      }
      if (Array.isArray(headers)) {
        for (const pair of headers) {
          if (Array.isArray(pair) && String(pair[0] || "").toLowerCase() === "content-type") {
            return String(pair[1] || "");
          }
        }
      } else if (typeof headers === "object") {
        for (const k of Object.keys(headers)) {
          if (k.toLowerCase() === "content-type") return String(headers[k] || "");
        }
      }
    } catch (e) {}
    return "";
  }

  if (typeof origFetch === "function") {
    globalScope.fetch = function web2apiFetch(...args) {
      let url = "";
      let method = "GET";
      let body = null;
      let contentType = "";
      try {
        const input = args[0];
        const init = args[1] || {};
        if (typeof input === "string") {
          url = input;
        } else if (input && typeof input === "object" && "url" in input) {
          url = String(input.url || "");
        } else {
          url = String(input || "");
        }
        method = String(init.method || (input && typeof input === "object" && input.method) || "GET").toUpperCase();
        body = init.body !== undefined ? init.body : (input && typeof input === "object" ? input.body : null);
        contentType = extractContentType(init.headers) || extractContentType(input && typeof input === "object" ? input.headers : null);
      } catch (e) {}

      const kind = classifyEndpoint(url, method);
      const isUpload = !kind && classifyUploadEndpoint(url, method, body, contentType);
      const promise = origFetch.apply(this, args);

      if (isUpload) {
        const uploadId = `upl_${Date.now()}_${++streamCounter}`;
        postToBridge("upload_start", uploadId, url, {});
        promise.then((res) => {
          if (res && res.ok) {
            postToBridge("upload_done", uploadId, url, { status: res.status });
          } else {
            postToBridge("upload_error", uploadId, url, { status: res ? res.status : 0 });
          }
        }).catch((err) => {
          postToBridge("upload_error", uploadId, url, { error: String((err && err.message) || err) });
        });
        return promise;
      }

      if (!kind) {
        return promise;
      }

      const { streamId, processor } = startStream(kind, url);

      promise.then((res) => {
        if (!res || !res.ok) {
          const status = res ? res.status : 0;
          postToBridge("error", streamId, url, {
            error: `HTTP ${status || "error"}`,
            status,
            rateLimited: status === 429
          });
          return;
        }
        let cloned;
        try {
          cloned = res.clone();
        } catch (e) {
          postToBridge("error", streamId, url, { error: "Failed to clone response stream" });
          return;
        }

        if (cloned.body && typeof cloned.body.getReader === "function") {
          const reader = cloned.body.getReader();
          const decoder = new TextDecoder("utf-8");

          (async () => {
            try {
              while (true) {
                const { done, value } = await reader.read();
                if (done) {
                  const tail = decoder.decode();
                  if (tail) processor.feed(tail);
                  break;
                }
                if (value) {
                  const chunk = decoder.decode(value, { stream: true });
                  if (chunk) processor.feed(chunk);
                }
              }
              processor.finish();
            } catch (err) {
              postToBridge("error", streamId, url, { error: String((err && err.message) || err) });
            }
          })();
        } else {
          cloned.text().then((txt) => {
            if (txt) processor.feed(txt);
            processor.finish();
          }).catch((err) => {
            postToBridge("error", streamId, url, { error: String((err && err.message) || err) });
          });
        }
      }).catch((err) => {
        postToBridge("error", streamId, url, { error: String((err && err.message) || err) });
      });

      return promise;
    };
  }

  // 2. Intercept XMLHttpRequest
  const XHRProto = globalScope.XMLHttpRequest && globalScope.XMLHttpRequest.prototype;
  if (XHRProto && typeof XHRProto.open === "function" && typeof XHRProto.send === "function") {
    const origOpen = XHRProto.open;
    const origSend = XHRProto.send;
    const origSetRequestHeader = XHRProto.setRequestHeader;

    XHRProto.open = function web2apiXhrOpen(method, url, ...rest) {
      try {
        this.__w2aMethod = String(method || "GET").toUpperCase();
        this.__w2aUrl = String(url || "");
        this.__w2aContentType = "";
      } catch (e) {}
      return origOpen.call(this, method, url, ...rest);
    };

    if (typeof origSetRequestHeader === "function") {
      XHRProto.setRequestHeader = function web2apiXhrSetHeader(name, value) {
        try {
          if (String(name || "").toLowerCase() === "content-type") {
            this.__w2aContentType = String(value || "");
          }
        } catch (e) {}
        return origSetRequestHeader.call(this, name, value);
      };
    }

    XHRProto.send = function web2apiXhrSend(body) {
      try {
        const kind = classifyEndpoint(this.__w2aUrl, this.__w2aMethod);
        const isUpload = !kind && classifyUploadEndpoint(this.__w2aUrl, this.__w2aMethod, body, this.__w2aContentType);
        if (isUpload) {
          const uploadId = `upl_${Date.now()}_${++streamCounter}`;
          const url = this.__w2aUrl;
          postToBridge("upload_start", uploadId, url, {});
          let doneSent = false;
          const finishUpload = (ok, extra) => {
            if (doneSent) return;
            doneSent = true;
            postToBridge(ok ? "upload_done" : "upload_error", uploadId, url, extra || {});
          };
          this.addEventListener("load", () => {
            finishUpload(this.status >= 200 && this.status < 300, { status: this.status });
          });
          this.addEventListener("error", () => finishUpload(false, { error: "XHR upload error" }));
          this.addEventListener("abort", () => finishUpload(false, { error: "XHR upload aborted" }));
        } else if (kind) {
          const url = this.__w2aUrl;
          const streamObj = startStream(kind, url);
          let offset = 0;
          let finalized = false;

          const readIncremental = () => {
            try {
              if (this.status && (this.status < 200 || this.status >= 300)) return;
              if (this.responseType && this.responseType !== "" && this.responseType !== "text") return;
              const txt = this.responseText || "";
              if (txt.length > offset) {
                const delta = txt.slice(offset);
                offset = txt.length;
                streamObj.processor.feed(delta);
              }
            } catch (e) {}
          };

          this.addEventListener("progress", readIncremental);
          this.addEventListener("load", () => {
            if (finalized) return;
            finalized = true;
            try {
              if (this.status >= 200 && this.status < 300) {
                if (this.responseType === "arraybuffer" && this.response) {
                  const decoded = new TextDecoder("utf-8").decode(this.response);
                  if (decoded) streamObj.processor.feed(decoded);
                } else {
                  readIncremental();
                }
                streamObj.processor.finish();
              } else {
                postToBridge("error", streamObj.streamId, url, {
                  error: `HTTP ${this.status}`,
                  status: this.status,
                  rateLimited: this.status === 429
                });
              }
            } catch (e) {}
          });
          this.addEventListener("error", () => {
            if (finalized) return;
            finalized = true;
            postToBridge("error", streamObj.streamId, url, { error: "XHR network error" });
          });
          this.addEventListener("abort", () => {
            if (finalized) return;
            finalized = true;
            streamObj.processor.finish();
          });
        }
      } catch (e) {}
      return origSend.call(this, body);
    };
  }

  // 3. Armed one-shot file-input click trap for detached <input type="file"> upload buttons.
  // Only intercepts while explicitly armed by bridge.js (auto-expires in 8s); manual user clicks pass through untouched.
  let armedUpload = null;

  function buildDataTransferFromPayloads(files) {
    const dt = new DataTransfer();
    for (const f of files || []) {
      if (!f || !f.data_base64) continue;
      const binStr = atob(f.data_base64);
      const bytes = new Uint8Array(binStr.length);
      for (let i = 0; i < binStr.length; i++) {
        bytes[i] = binStr.charCodeAt(i);
      }
      const fileObj = new File([bytes], f.filename || "attachment.bin", {
        type: f.mime || "application/octet-stream",
        lastModified: Date.now()
      });
      dt.items.add(fileObj);
    }
    return dt;
  }

  globalScope.addEventListener("message", (event) => {
    const data = event && event.data;
    if (!data || typeof data !== "object") return;
    if (data.__web2api_upload_arm === true && Array.isArray(data.files) && data.files.length > 0) {
      armedUpload = {
        armId: String(data.armId || ""),
        files: data.files,
        expiresAt: Date.now() + 8000
      };
    } else if (data.__web2api_upload_disarm === true) {
      if (!data.armId || (armedUpload && armedUpload.armId === String(data.armId))) {
        armedUpload = null;
      }
    }
  });

  const InputProto = globalScope.HTMLInputElement && globalScope.HTMLInputElement.prototype;
  if (InputProto && typeof InputProto.click === "function") {
    const origInputClick = InputProto.click;
    InputProto.click = function web2apiInputClick() {
      try {
        if (
          this &&
          String(this.type || "").toLowerCase() === "file" &&
          armedUpload &&
          Date.now() < armedUpload.expiresAt
        ) {
          const activeArm = armedUpload;
          armedUpload = null; // One-shot disarm

          const dt = buildDataTransferFromPayloads(activeArm.files);
          if (dt.files.length > 0) {
            // Fire ONLY `change` on <input type="file"> (firing both `input` and `change`
            // causes frameworks listening to both events to attach duplicate copies).
            this.files = dt.files;
            this.dispatchEvent(new Event("change", { bubbles: true, composed: true }));
            postToBridge("upload_injected", activeArm.armId, "", {
              count: dt.files.length,
              method: "click_trap"
            });
            return;
          }
        }
      } catch (e) {
        armedUpload = null;
      }
      return origInputClick.apply(this, arguments);
    };
  }
})(typeof window !== "undefined" ? window : globalThis);
