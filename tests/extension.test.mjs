import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const netHook = require("../extension/net-hook.js");
const bridge = require("../extension/bridge.js");

// ---------------------------------------------------------------------------
// 1. manifest.json validation
// ---------------------------------------------------------------------------
test("manifest.json is valid MV3 with MAIN world net-hook before bridge", () => {
  const manifestPath = path.resolve("extension/manifest.json");
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));

  assert.equal(manifest.manifest_version, 3);
  assert.ok(Array.isArray(manifest.content_scripts));
  assert.equal(manifest.content_scripts.length, 2);

  const [hookScript, bridgeScript] = manifest.content_scripts;
  assert.deepEqual(hookScript.js, ["net-hook.js"]);
  assert.equal(hookScript.world, "MAIN");
  assert.equal(hookScript.run_at, "document_start");
  assert.equal(hookScript.all_frames, true);

  assert.deepEqual(bridgeScript.js, ["bridge.js"]);
  assert.equal(bridgeScript.run_at, "document_idle");
  assert.equal(bridgeScript.all_frames, false);
});

// ---------------------------------------------------------------------------
// 2. net-hook.js endpoint classification
// ---------------------------------------------------------------------------
test("classifyEndpoint matches Vertex AI Search, Gemini Enterprise, and Consumer Gemini POST streams", () => {
  assert.equal(
    netHook.classifyEndpoint(
      "https://content-discoveryengine.googleapis.com/v1alpha/locations/global/widgetStreamAssist",
      "POST"
    ),
    "discovery_json"
  );
  assert.equal(
    netHook.classifyEndpoint(
      "https://biz-discoveryengine.googleapis.com/v1alpha/locations/us/widgetStreamAssist?alt=json",
      "POST"
    ),
    "discovery_json"
  );
  assert.equal(
    netHook.classifyEndpoint(
      "https://discoveryengine.googleapis.com/v1/projects/p/locations/global/collections/default_collection/engines/e/assistants/default_assistant:streamAssist",
      "POST"
    ),
    "discovery_json"
  );
  assert.equal(
    netHook.classifyEndpoint(
      "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=xyz",
      "POST"
    ),
    "gemini_web"
  );

  // Reject GET requests and unrelated telemetry/list calls
  assert.equal(
    netHook.classifyEndpoint(
      "https://content-discoveryengine.googleapis.com/v1alpha/locations/global/widgetStreamAssist",
      "GET"
    ),
    null
  );
  assert.equal(
    netHook.classifyEndpoint(
      "https://vertexaisearch.cloud.google.com/api/telemetry/log",
      "POST"
    ),
    null
  );
  assert.equal(
    netHook.classifyEndpoint(
      "https://discoveryengine.googleapis.com/v1/projects/p/locations/global/engines/e/assistants",
      "POST"
    ),
    null
  );
});

// ---------------------------------------------------------------------------
// 3. net-hook.js incremental JSON extractor & streamAssist parser
// ---------------------------------------------------------------------------
test("createJsonObjectExtractor parses split chunked JSON array with XSSI prefix and braces inside strings", () => {
  const objects = [];
  const feed = netHook.createJsonObjectExtractor((obj) => objects.push(obj));

  const payload =
    ")]}'\n[\n" +
    JSON.stringify({ a: 'code { return "hi } there"; }' }) +
    ",\n" +
    JSON.stringify({ b: { nested: [1, 2, 3] }, escaped: 'quote \\" and \\\\ slash' }) +
    "\n]";

  // Feed in tiny arbitrary 7-char chunks to stress boundary splits
  for (let i = 0; i < payload.length; i += 7) {
    feed(payload.slice(i, i + 7));
  }

  assert.equal(objects.length, 2);
  assert.equal(objects[0].a, 'code { return "hi } there"; }');
  assert.equal(objects[1].escaped, 'quote \\" and \\\\ slash');
});

test("widgetStreamAssist processor filters thought:true, streams answer deltas, and deduplicates final SUCCEEDED snapshot", () => {
  const events = [];
  const proc = netHook.createStreamProcessor(
    "discovery_json",
    "https://content-discoveryengine.googleapis.com/v1alpha/locations/global/widgetStreamAssist",
    (type, extra) => events.push({ type, ...extra })
  );

  const chunks = [
    {
      streamAssistResponse: {
        answer: {
          state: "IN_PROGRESS",
          replies: [
            {
              groundedContent: {
                content: {
                  role: "model",
                  thought: true,
                  text: "The user said greetings, I should reply politely."
                }
              }
            }
          ]
        }
      }
    },
    {
      streamAssistResponse: {
        answer: {
          state: "IN_PROGRESS",
          replies: [
            {
              groundedContent: {
                content: {
                  role: "model",
                  text: "Hello! "
                }
              }
            }
          ]
        }
      }
    },
    {
      streamAssistResponse: {
        answer: {
          state: "IN_PROGRESS",
          replies: [
            {
              groundedContent: {
                content: {
                  role: "model",
                  text: "How can I help you today?"
                }
              }
            }
          ]
        }
      }
    },
    // Final SUCCEEDED chunk repeating the full text (must NOT be duplicated!)
    {
      streamAssistResponse: {
        answer: {
          name: "projects/p/sessions/s/assistAnswers/a",
          state: "SUCCEEDED",
          replies: [
            {
              groundedContent: {
                content: {
                  role: "model",
                  text: "Hello! How can I help you today?"
                }
              }
            }
          ]
        }
      }
    }
  ];

  const streamText = "[\n" + chunks.map((c) => JSON.stringify(c)).join(",\n") + "\n]";
  // Split at arbitrary 13-byte boundaries
  for (let i = 0; i < streamText.length; i += 13) {
    proc.feed(streamText.slice(i, i + 13));
  }
  proc.finish();

  const thinkingEvents = events.filter((e) => e.type === "thinking");
  const deltaText = events
    .filter((e) => e.type === "delta")
    .map((e) => e.text)
    .join("");
  const doneEvents = events.filter((e) => e.type === "done");

  assert.equal(thinkingEvents.length, 1);
  assert.equal(deltaText, "Hello! How can I help you today?");
  assert.equal(doneEvents.length, 1);
  assert.equal(doneEvents[0].hasText, true);
  assert.equal(doneEvents[0].skipped, false);
});

test("streamAssist SKIPPED response reports hasText:false and skipped:true for DOM fallback", () => {
  const events = [];
  const proc = netHook.createStreamProcessor(
    "discovery_json",
    "https://content-discoveryengine.googleapis.com/v1alpha/locations/global/widgetStreamAssist",
    (type, extra) => events.push({ type, ...extra })
  );

  proc.feed(
    JSON.stringify([
      {
        streamAssistResponse: {
          answer: {
            state: "SKIPPED",
            assistSkippedReasons: ["NON_ASSIST_SEEKING_QUERY_IGNORED"]
          }
        }
      }
    ])
  );
  proc.finish();

  assert.equal(events.length, 1);
  assert.equal(events[0].type, "done");
  assert.equal(events[0].hasText, false);
  assert.equal(events[0].skipped, true);
});

test("Consumer Gemini StreamGenerate parses cumulative wrb.fr frames (including short replies) into incremental deltas", () => {
  const events = [];
  const proc = netHook.createStreamProcessor(
    "gemini_web",
    "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate",
    (type, extra) => events.push({ type, ...extra })
  );

  const mkFrame = (text) => {
    const inner = JSON.stringify([null, ["c_conv", "r_resp"], null, null, [["rc_1", [text]]]]);
    return JSON.stringify([["wrb.fr", null, inner]]);
  };

  proc.feed(")]}'\n\n");
  proc.feed(mkFrame("Hi!") + "\n");
  proc.feed(mkFrame("Hi! How can I assist you?") + "\n");
  proc.finish();

  const combined = events
    .filter((e) => e.type === "delta")
    .map((e) => e.text)
    .join("");

  assert.equal(combined, "Hi! How can I assist you?");
});

test("streamGenerateContent filters thought:true parts and emits model answer deltas", () => {
  const events = [];
  const proc = netHook.createStreamProcessor(
    "discovery_json",
    "https://discoveryengine.googleapis.com/v1/projects/p/locations/global:streamGenerateContent",
    (type, extra) => events.push({ type, ...extra })
  );

  const payload = JSON.stringify([
    { candidates: [{ content: { role: "model", parts: [{ thought: true, text: "Internal reasoning" }] } }] },
    { candidates: [{ content: { role: "model", parts: [{ text: "Hello " }] } }] },
    { candidates: [{ content: { role: "model", parts: [{ text: "world!" }] }, finishReason: "STOP" }] }
  ]);
  proc.feed(payload);
  proc.finish();

  assert.equal(events.filter((e) => e.type === "thinking").length, 1);
  assert.equal(events.filter((e) => e.type === "delta").map((e) => e.text).join(""), "Hello world!");
});

// ---------------------------------------------------------------------------
// 4. bridge.js text sanitization & "Gemini 4 Hydrogen" regression tests
// ---------------------------------------------------------------------------
test("cleanResponseText rejects standalone 'Gemini 4 Hydrogen' and strips leading model header while preserving real mentions", () => {
  // Exact user-reported bug: model badge alone must become empty!
  assert.equal(bridge.cleanResponseText("Gemini 4 Hydrogen"), "");
  assert.equal(bridge.isNonAnswerBoilerplate("Gemini 4 Hydrogen", "greetings"), true);

  // Leading model header above real reply must strip ONLY the header
  assert.equal(
    bridge.cleanResponseText("Gemini 4 Hydrogen\n\nHello! How can I help you today?\nthumb_up\nthumb_down\ncontent_copy"),
    "Hello! How can I help you today?"
  );

  // Real answer mentioning the model name in a sentence must be preserved verbatim!
  const realMention = "I am Gemini 4 Hydrogen, ready to help you with your project.";
  assert.equal(bridge.cleanResponseText(realMention), realMention);
  assert.equal(bridge.isNonAnswerBoilerplate(realMention, "who are you?"), false);

  // Lit markers & disclaimers stripped
  assert.equal(
    bridge.cleanResponseText("<!--?lit$203076504$-->?lit$203076504$\nHello!\nGenerative AI may display inaccurate information."),
    "Hello!"
  );

  // Thinking-only placeholder rejected
  assert.equal(bridge.cleanResponseText("Thinking..."), "");
  assert.equal(bridge.isNonAnswerBoilerplate("Show thinking", "greetings"), true);

  // User prompt echo rejected
  assert.equal(bridge.isNonAnswerBoilerplate("greetings", "# Tool Use\n\ngreetings"), true);
  assert.equal(bridge.isNonAnswerBoilerplate("Hello there!", "# Tool Use\n\ngreetings"), false);
});

test("escapeHtml preserves WorkBuddy XML tags in contenteditable fallback", () => {
  const xmlPrompt = "<instructions>Use <tool>read_file</tool> & check</instructions>";
  assert.equal(
    bridge.escapeHtml(xmlPrompt),
    "&lt;instructions&gt;Use &lt;tool&gt;read_file&lt;/tool&gt; &amp; check&lt;/instructions&gt;"
  );
});

test("resolveModelKeywords respects requested model family instead of hardcoding hydrogen", () => {
  assert.equal(bridge.resolveModelKeywords("gemini-4-hydrogen").family, "hydrogen");
  assert.equal(bridge.resolveModelKeywords("gemini-4-argon").family, "argon");
  assert.equal(bridge.resolveModelKeywords("gemini-3.6-flash").family, "flash");
  assert.equal(bridge.resolveModelKeywords("gemini-3.1-pro").family, "pro");
  assert.equal(bridge.resolveModelKeywords("gemini-auto").family, "auto");
  assert.equal(bridge.resolveModelKeywords("unknown-custom-id"), null);
});

// ---------------------------------------------------------------------------
// 5. Realistic Mock Shadow DOM + <slot> tests for bridge.js
// ---------------------------------------------------------------------------
function createMockDOM() {
  class MockNode {
    constructor(nodeType) {
      this.nodeType = nodeType;
      this.parentNode = null;
      this.parentElement = null;
      this.childNodes = [];
    }
    getRootNode() {
      let c = this;
      while (c.parentNode) c = c.parentNode;
      return c;
    }
  }

  class MockText extends MockNode {
    constructor(text) {
      super(3);
      this.textContent = text;
    }
  }

  class MockShadowRoot extends MockNode {
    constructor(host) {
      super(11);
      this.host = host;
    }
    get children() {
      return this.childNodes.filter((n) => n.nodeType === 1);
    }
    appendChild(child) {
      child.parentNode = this;
      child.parentElement = null;
      this.childNodes.push(child);
      return child;
    }
    querySelectorAll(sel) {
      return querySelectorAllMock(this, sel);
    }
  }

  class MockElement extends MockNode {
    constructor(tagName, attrs = {}) {
      super(1);
      this.tagName = tagName.toUpperCase();
      this.attrs = {};
      for (const [k, v] of Object.entries(attrs)) {
        this.attrs[k.toLowerCase()] = String(v);
      }
      this.id = this.attrs.id || "";
      this.className = this.attrs.class || "";
      this.shadowRoot = null;
      this.hidden = "hidden" in this.attrs;
      this.isContentEditable = this.attrs.contenteditable === "true";
      this._assignedNodes = [];
    }
    get children() {
      return this.childNodes.filter((n) => n.nodeType === 1);
    }
    getAttribute(k) {
      const key = k.toLowerCase();
      return key in this.attrs ? this.attrs[key] : null;
    }
    hasAttribute(k) {
      return k.toLowerCase() in this.attrs;
    }
    appendChild(child) {
      child.parentNode = this;
      child.parentElement = this;
      this.childNodes.push(child);
      return child;
    }
    attachShadow() {
      this.shadowRoot = new MockShadowRoot(this);
      return this.shadowRoot;
    }
    assignedNodes() {
      return this._assignedNodes;
    }
    matches(selectorList) {
      const parts = selectorList.split(",").map((s) => s.trim()).filter(Boolean);
      return parts.some((sel) => matchesSimpleSelector(this, sel));
    }
    querySelectorAll(sel) {
      return querySelectorAllMock(this, sel);
    }
  }

  function matchesSimpleSelector(el, sel) {
    if (el.nodeType !== 1) return false;
    if (sel === "*") return true;

    // Handle :not(...) clauses
    let work = sel;
    const notClauses = [];
    work = work.replace(/:not\(([^)]+)\)/g, (_, inner) => {
      notClauses.push(inner.trim());
      return "";
    });
    for (const nc of notClauses) {
      if (matchesSimpleSelector(el, nc)) return false;
    }

    // Extract attribute selectors [attr], [attr='val'], [attr*='val']
    const attrChecks = [];
    work = work.replace(/\[([^\]=*]+)(?:(\*?=)['"]?([^'"\]]+)['"]?)?\]/g, (_, name, op, val) => {
      attrChecks.push({ name: name.trim().toLowerCase(), op, val });
      return "";
    });

    // Extract class selectors
    const classChecks = [];
    work = work.replace(/\.([a-zA-Z0-9_-]+)/g, (_, cls) => {
      classChecks.push(cls);
      return "";
    });

    // Remaining token is optional tag name
    const tagCheck = work.trim();
    if (tagCheck && el.tagName !== tagCheck.toUpperCase()) {
      return false;
    }

    const elClasses = el.className.split(/\s+/).filter(Boolean);
    for (const cls of classChecks) {
      if (!elClasses.includes(cls)) return false;
    }

    for (const ac of attrChecks) {
      const actual = ac.name === "class" ? el.className : el.getAttribute(ac.name);
      if (!ac.op) {
        if (actual === null) return false;
      } else if (ac.op === "=") {
        if (actual !== ac.val) return false;
      } else if (ac.op === "*=") {
        if (actual === null || !actual.includes(ac.val)) return false;
      }
    }

    return true;
  }

  function querySelectorAllMock(root, selectorList) {
    const out = [];
    const stack = [...(root.children || [])].reverse();
    while (stack.length > 0) {
      const el = stack.pop();
      if (el.matches(selectorList)) out.push(el);
      const kids = el.children || [];
      for (let i = kids.length - 1; i >= 0; i--) {
        stack.push(kids[i]);
      }
    }
    return out;
  }

  class MockDocument extends MockNode {
    constructor() {
      super(9);
      this.body = new MockElement("body");
      this.body.parentNode = this;
      this.childNodes.push(this.body);
    }
    get children() {
      return [this.body];
    }
    querySelectorAll(sel) {
      return querySelectorAllMock(this, sel);
    }
  }

  return {
    doc: new MockDocument(),
    el: (tag, attrs) => new MockElement(tag, attrs),
    txt: (s) => new MockText(s)
  };
}

test("Shadow DOM + <slot> Vertex AI Search simulation NEVER returns 'Gemini 4 Hydrogen' or duplicates and extracts real slotted answer", () => {
  const { doc, el, txt } = createMockDOM();

  // 1. Add our own floating badge with "Gemini 4 Hydrogen" button
  const badge = el("div", { id: bridge.BADGE_ID });
  const badgeBtn = el("button", { id: "web2api-model-btn" });
  badgeBtn.appendChild(txt("✅ Gemini 4 Hydrogen"));
  badge.appendChild(badgeBtn);
  doc.body.appendChild(badge);

  // 2. Add Vertex AI Search chat shell with Shadow DOM containing bottom-controls & model picker
  const chatApp = el("ucs-chat-app");
  const chatShadow = chatApp.attachShadow();
  doc.body.appendChild(chatApp);

  // Bottom bar with class matching substring "bot" (bottom-controls) AND model selector text "Gemini 4 Hydrogen"
  const bottomControls = el("div", { class: "bottom-controls chat-bottom" });
  const textarea = el("textarea", { placeholder: "Ask anything" });
  const modelChip = el("div", { class: "active-model-chip" });
  modelChip.appendChild(txt("Gemini 4 Hydrogen"));
  const sendBtn = el("button");
  const sendIcon = el("mat-icon");
  sendIcon.appendChild(txt("arrow_upward"));
  sendBtn.appendChild(sendIcon);
  bottomControls.appendChild(textarea);
  bottomControls.appendChild(modelChip);
  bottomControls.appendChild(sendBtn);
  chatShadow.appendChild(bottomControls);

  // BEFORE any model response exists: findResponseElements MUST return [] (NOT bottomControls / "Gemini 4 Hydrogen"!)
  const beforeResponses = bridge.findResponseElements(textarea, "greetings", doc);
  assert.equal(beforeResponses.length, 0);

  const baselineMap = bridge.snapshotResponses(textarea, doc);
  assert.equal(baselineMap.size, 0);

  // 3. Turn 1 starts: outer turn renders ONLY header "Gemini 4 Hydrogen" before answer arrives
  const turn1 = el("discovery-engine-answer");
  const turn1Shadow = turn1.attachShadow();
  const header1 = el("div", { class: "turn-header" });
  header1.appendChild(txt("Gemini 4 Hydrogen"));
  const slot1 = el("slot");
  turn1Shadow.appendChild(header1);
  turn1Shadow.appendChild(slot1);
  chatShadow.appendChild(turn1);

  // While only "Gemini 4 Hydrogen" header is visible, active DOM text MUST be ""!
  assert.equal(bridge.extractActiveDomResponseText(textarea, "greetings", baselineMap, doc), "");

  // 4. Actual answer streams into slotted <ucs-text-markdown> + action buttons (thumb_up, content_copy)
  const md1 = el("ucs-text-markdown");
  const md1Shadow = md1.attachShadow();
  const p1 = el("p");
  p1.appendChild(txt("Hello! How can I help you today?"));
  const actionBtn = el("button");
  const thumbIcon = el("mat-icon");
  thumbIcon.appendChild(txt("thumb_up"));
  actionBtn.appendChild(thumbIcon);
  md1Shadow.appendChild(p1);
  md1Shadow.appendChild(actionBtn);
  turn1.appendChild(md1);
  slot1._assignedNodes = [md1];

  const extracted1 = bridge.extractActiveDomResponseText(textarea, "greetings", baselineMap, doc);
  assert.equal(extracted1, "Hello! How can I help you today?");

  // 5. Verify queryDeep returns ZERO duplicates for deeply nested elements
  const foundMds = bridge.queryDeep("ucs-text-markdown", doc);
  assert.equal(foundMds.length, 1);

  // Verify own badge is NEVER returned by queryDeep
  const allButtons = bridge.queryDeep("button", doc);
  assert.ok(allButtons.every((b) => b.id !== "web2api-model-btn"));

  // 6. Turn 2: snapshot includes Turn 1 -> MUST NOT re-return Turn 1 as Turn 2's reply!
  const baselineTurn2 = bridge.snapshotResponses(textarea, doc);
  assert.equal(baselineTurn2.size, 1);
  assert.equal(bridge.extractActiveDomResponseText(textarea, "second question", baselineTurn2, doc), "");

  // Turn 2 answer arrives
  const md2 = el("ucs-text-markdown");
  const p2 = el("p");
  p2.appendChild(txt("Here is the second answer."));
  md2.appendChild(p2);
  chatShadow.appendChild(md2);

  assert.equal(
    bridge.extractActiveDomResponseText(textarea, "second question", baselineTurn2, doc),
    "Here is the second answer."
  );
});

// ---------------------------------------------------------------------------
// 6. Direct File Upload telemetry & DOM selectors tests
// ---------------------------------------------------------------------------

test("classifyUploadEndpoint matches Gemini Enterprise & Consumer file upload APIs", () => {
  assert.equal(
    netHook.classifyUploadEndpoint(
      "https://content-discoveryengine.googleapis.com/v1alpha/locations/global/widgetAddContextFile",
      "POST"
    ),
    true
  );
  assert.equal(
    netHook.classifyUploadEndpoint(
      "https://discoveryengine.googleapis.com/v1alpha/projects/123/locations/global/collections/default_collection/engines/eng/assistants/default_assistant:addContextFile",
      "POST"
    ),
    true
  );
  assert.equal(
    netHook.classifyUploadEndpoint(
      "https://content-push.clients6.google.com/upload/",
      "POST"
    ),
    true
  );
  // GET requests and regular stream endpoints must NOT be classified as uploads
  assert.equal(
    netHook.classifyUploadEndpoint(
      "https://content-discoveryengine.googleapis.com/v1alpha/locations/global/widgetAddContextFile",
      "GET"
    ),
    false
  );
  assert.equal(
    netHook.classifyUploadEndpoint(
      "https://content-discoveryengine.googleapis.com/v1alpha/locations/global/widgetStreamAssist",
      "POST"
    ),
    false
  );
});

test("buildAttachedFileNote and base64ToUint8Array handle PDF attachments", () => {
  const bytes = bridge.base64ToUint8Array("JVBERi0xLjQK");
  assert.equal(Buffer.from(bytes).toString("utf8"), "%PDF-1.4\n");

  const note = bridge.buildAttachedFileNote([
    { filename: "report.pdf" },
    { filename: "slides.pptx" }
  ]);
  assert.ok(note.includes("report.pdf, slides.pptx"));
  assert.ok(note.includes("do NOT call local shell/python/file tools"));
  assert.equal(bridge.buildAttachedFileNote([]), "");
});

test("Shadow DOM upload helpers locate attach button, menu option, and attachment chip", () => {
  const { doc, el, txt } = createMockDOM();

  const chatHost = el("ucs-chat-app");
  const shadow = chatHost.attachShadow();
  doc.body.appendChild(chatHost);

  const textarea = el("textarea");
  const sendBtn = el("button", { "aria-label": "Send message" });
  sendBtn.appendChild(txt("Send"));
  const attachBtn = el("button", { "aria-label": "Add files" });
  attachBtn.appendChild(txt("+"));

  shadow.appendChild(textarea);
  shadow.appendChild(attachBtn);
  shadow.appendChild(sendBtn);

  // Must pick the attach/add-files button, never Send
  const foundAttach = bridge.findAttachButton(textarea, doc);
  assert.equal(foundAttach, attachBtn);

  // Upload popover menu with both Google Drive and Upload files
  const driveItem = el("div", { role: "menuitem" });
  driveItem.appendChild(txt("Add from Google Drive"));
  const uploadItem = el("div", { role: "menuitem" });
  uploadItem.appendChild(txt("Upload files"));
  shadow.appendChild(driveItem);
  shadow.appendChild(uploadItem);

  const foundMenu = bridge.findUploadMenuOption(doc);
  assert.equal(foundMenu, uploadItem);

  // Before upload, no attachment chip exists
  assert.equal(bridge.hasAttachmentChipInDom(["quarterly_report.pdf"], textarea, doc), false);

  // After upload, chip renders filename in DOM
  const chip = el("div", { class: "file-attachment-chip" });
  chip.appendChild(txt("quarterly_report.pdf"));
  shadow.appendChild(chip);

  assert.equal(bridge.hasAttachmentChipInDom(["quarterly_report.pdf"], textarea, doc), true);
});

// ---------------------------------------------------------------------------
// 7. Regression tests: never click blog.google announcement links + rate-limit detection
// ---------------------------------------------------------------------------

test("findModelOption and findModelTriggerElement NEVER click blog.google announcement links or promo banners", () => {
  const { doc, el, txt } = createMockDOM();

  const target = bridge.resolveModelKeywords("gemini-4-hydrogen");

  // 1. Exact announcement link on Gemini Enterprise that used to pop open a new tab
  const promoBanner = el("div", { class: "announcement-banner" });
  const blogLink = el("a", {
    href: "https://blog.google/innovation-and-ai/models-and-research/gemini-models/introducing-gemini-4-hydrogen/",
    target: "_blank"
  });
  const blogSpan = el("span");
  blogSpan.appendChild(txt("Gemini 4 Hydrogen"));
  blogLink.appendChild(blogSpan);
  promoBanner.appendChild(blogLink);
  doc.body.appendChild(promoBanner);

  // 2. Standalone external link without banner wrapper
  const bareLink = el("a", {
    href: "https://blog.google/innovation-and-ai/models-and-research/gemini-models/introducing-gemini-4-hydrogen/",
    target: "_blank",
    role: "button"
  });
  bareLink.appendChild(txt("Gemini 4 Hydrogen"));
  doc.body.appendChild(bareLink);

  // 3. Non-menu page card containing "Introducing Gemini 4 Hydrogen"
  const promoCard = el("button");
  promoCard.appendChild(txt("Introducing Gemini 4 Hydrogen - Learn more"));
  doc.body.appendChild(promoCard);

  assert.equal(bridge.isLinkOrPromoElement(blogLink), true);
  assert.equal(bridge.isLinkOrPromoElement(blogSpan), true);
  assert.equal(bridge.isLinkOrPromoElement(bareLink), true);

  // With NO open dropdown menu, neither trigger nor option may match any of the above!
  assert.equal(bridge.findModelTriggerElement(doc), null);
  assert.equal(bridge.findModelOption(target, null, doc), null);

  // 4. Once a genuine dropdown overlay menu opens with a real menuitem, select ONLY that menuitem
  const overlay = el("div", { class: "cdk-overlay-pane", role: "menu" });
  const realOption = el("button", { role: "menuitem" });
  realOption.appendChild(txt("Gemini 4 Hydrogen"));
  overlay.appendChild(realOption);
  doc.body.appendChild(overlay);

  assert.equal(bridge.findModelOption(target, null, doc), realOption);
});

test("isRateLimitMessage detects Google 429 / quota / traffic warnings", () => {
  assert.equal(bridge.isRateLimitMessage("HTTP 429"), true);
  assert.equal(bridge.isRateLimitMessage("Too many requests, please try again later"), true);
  assert.equal(bridge.isRateLimitMessage("RESOURCE_EXHAUSTED: quota exceeded"), true);
  assert.equal(bridge.isRateLimitMessage("Unusual traffic detected"), true);
  assert.equal(bridge.isRateLimitMessage("HTTP 500 internal error"), false);
  assert.equal(bridge.isRateLimitMessage(""), false);
});


