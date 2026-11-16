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

  function cleanBardText(text) {
    if (!text) return "";
    return text.replace(RE_CODE_EXEC, "").replace(RE_CARD_CONTENT, "");
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

            if (content.thought === true) {
              sawThought = true;
              continue;
            }

            if (typeof content.text === "string" && content.text.length > 0) {
              chunkAnswerText += content.text;
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
          emitIncrementalOrDedup(answerText, true);
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
      createJsonObjectExtractor,
      extractBardTextsFromLine,
      cleanBardText,
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
      (globalScope.top || globalScope).postMessage(payload, "*");
    } catch (e) {
      try {
        globalScope.postMessage(payload, "*");
      } catch (e2) {}
    }
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
  if (typeof origFetch === "function") {
    globalScope.fetch = function web2apiFetch(...args) {
      let url = "";
      let method = "GET";
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
      } catch (e) {}

      const kind = classifyEndpoint(url, method);
      const promise = origFetch.apply(this, args);

      if (!kind) {
        return promise;
      }

      const { streamId, processor } = startStream(kind, url);

      promise.then((res) => {
        if (!res || !res.ok) {
          postToBridge("error", streamId, url, { error: `HTTP ${res ? res.status : "error"}` });
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

    XHRProto.open = function web2apiXhrOpen(method, url, ...rest) {
      try {
        this.__w2aMethod = String(method || "GET").toUpperCase();
        this.__w2aUrl = String(url || "");
      } catch (e) {}
      return origOpen.call(this, method, url, ...rest);
    };

    XHRProto.send = function web2apiXhrSend(body) {
      try {
        const kind = classifyEndpoint(this.__w2aUrl, this.__w2aMethod);
        if (kind) {
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
                postToBridge("error", streamObj.streamId, url, { error: `HTTP ${this.status}` });
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
})(typeof window !== "undefined" ? window : globalThis);
