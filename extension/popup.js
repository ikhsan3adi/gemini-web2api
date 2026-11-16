document.addEventListener("DOMContentLoaded", async () => {
  const envBadge = document.getElementById("envBadge");
  const statusBox = document.getElementById("statusBox");
  const bridgeBox = document.getElementById("bridgeBox");
  const bridgeStatusText = document.getElementById("bridgeStatusText");
  const autoRefreshCheckbox = document.getElementById("autoRefreshCheckbox");
  const refreshTabBtn = document.getElementById("refreshTabBtn");
  const forceFinishBtn = document.getElementById("forceFinishBtn");
  const output = document.getElementById("output");
  const copyJsonBtn = document.getElementById("copyJsonBtn");
  const downloadJsonBtn = document.getElementById("downloadJsonBtn");
  const copyTxtBtn = document.getElementById("copyTxtBtn");

  // Load auto-refresh preference (default: false)
  if (chrome.storage && chrome.storage.local) {
    chrome.storage.local.get(["autoRefreshEnabled"], (res) => {
      autoRefreshCheckbox.checked = res.autoRefreshEnabled === true;
    });
  }

  autoRefreshCheckbox.addEventListener("change", async () => {
    const enabled = autoRefreshCheckbox.checked;
    if (chrome.storage && chrome.storage.local) {
      chrome.storage.local.set({ autoRefreshEnabled: enabled });
    }
    const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (activeTab && activeTab.id) {
      chrome.tabs.sendMessage(activeTab.id, { action: "setAutoRefresh", enabled }).catch(() => {});
    }
  });

  // Refresh active tab button
  refreshTabBtn.addEventListener("click", async () => {
    const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (activeTab && activeTab.id) {
      chrome.tabs.sendMessage(activeTab.id, { action: "refresh" }).catch(() => {
        chrome.tabs.reload(activeTab.id);
      });
      refreshTabBtn.textContent = "Refreshed!";
      setTimeout(() => { refreshTabBtn.textContent = "🔄 Refresh Page"; }, 1500);
    }
  });

  // Force finish button
  forceFinishBtn.addEventListener("click", async () => {
    const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (activeTab && activeTab.id) {
      chrome.tabs.sendMessage(activeTab.id, { action: "forceFinish" }).catch(() => {});
      forceFinishBtn.textContent = "Sent!";
      setTimeout(() => { forceFinishBtn.textContent = "⏹️ Force Finish"; }, 1500);
    }
  });

  // Check bridge server connectivity
  try {
    const bridgeRes = await fetch("http://localhost:8081/bridge/status");
    if (bridgeRes.ok) {
      const bData = await bridgeRes.json();
      if (bData.connected) {
        bridgeBox.className = "status success";
        bridgeStatusText.textContent = `Connected (${bData.tab || "tab active"})`;
      } else {
        bridgeBox.className = "status warn";
        bridgeStatusText.textContent = "Server running on :8081 (Open Gemini page to connect)";
      }
    } else {
      bridgeBox.className = "status error";
      bridgeStatusText.textContent = "Server returned error";
    }
  } catch (e) {
    bridgeBox.className = "status error";
    bridgeStatusText.textContent = "Server not reachable (Start gemini-web2api)";
  }

  let currentJson = "";
  let currentTxt = "";

  // 1. Identify active tab
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  const url = tab && tab.url ? tab.url : "";
  const isEnterprise = url.includes("vertexaisearch.cloud.google.com");
  const isGeminiWeb = url.includes("gemini.google.com");

  if (isEnterprise) {
    envBadge.textContent = "Enterprise";
  } else if (isGeminiWeb) {
    envBadge.textContent = "Consumer Web";
  } else {
    envBadge.textContent = "Other Domain";
  }

  // 2. Query relevant cookie domains
  const targetDomains = [
    "google.com",
    ".google.com",
    "gemini.google.com",
    "cloud.google.com",
    ".cloud.google.com",
    "vertexaisearch.cloud.google.com"
  ];

  const cookieMap = new Map();

  for (const domain of targetDomains) {
    try {
      const cookies = await chrome.cookies.getAll({ domain });
      for (const c of cookies) {
        cookieMap.set(c.name, c.value);
      }
    } catch (e) {
      console.warn("Failed to get cookies for domain:", domain, e);
    }
  }

  // Relevant keys for Gemini authentication
  const keyOrder = [
    "SID",
    "HSID",
    "SSID",
    "APISID",
    "SAPISID",
    "__Secure-1PSID",
    "__Secure-1PSIDTS",
    "__Secure-1PAPISID",
    "__Secure-3PSID",
    "__Secure-3PAPISID"
  ];

  const cookieParts = [];
  for (const k of keyOrder) {
    if (cookieMap.has(k)) {
      cookieParts.push(`${k}=${cookieMap.get(k)}`);
    }
  }

  const cookieString = cookieParts.join("; ");
  const sapisid = cookieMap.get("SAPISID") || "";

  // 3. Extract XSRF token (SNlM0e) and auth_user from active tab if possible
  let xsrfToken = "";
  let authUser = "";

  if (url) {
    const authMatch = url.match(/\/u\/(\d+)\//);
    if (authMatch) {
      authUser = authMatch[1];
    }
  }

  if (tab && tab.id && (isGeminiWeb || isEnterprise)) {
    try {
      const results = await chrome.scripting.executeScript({
        target: { tabId: tab.id },
        func: () => {
          const scripts = document.querySelectorAll("script");
          for (const s of scripts) {
            const m = s.textContent.match(/"SNlM0e":"([^"]+)"/);
            if (m) return m[1];
          }
          if (window.WIZ_global_data && window.WIZ_global_data.SNlM0e) {
            return window.WIZ_global_data.SNlM0e;
          }
          return "";
        }
      });
      if (results && results[0] && results[0].result) {
        xsrfToken = results[0].result;
      }
    } catch (e) {
      console.warn("Scripting extraction error:", e);
    }
  }

  if (!sapisid && cookieParts.length === 0) {
    statusBox.className = "status error";
    statusBox.textContent = "No Google session cookies found. Please make sure you are signed in on gemini.google.com or vertexaisearch.cloud.google.com.";
    return;
  }

  statusBox.className = "status success";
  statusBox.textContent = `Found ${cookieParts.length} session cookies` + (sapisid ? " (SAPISID present)" : "") + (xsrfToken ? " (XSRF token found)" : "");

  const exportObj = {
    cookie: cookieString,
    sapisid: sapisid
  };

  if (authUser) {
    exportObj.auth_user = authUser;
  }
  if (xsrfToken) {
    exportObj.xsrf_token = xsrfToken;
  }

  currentJson = JSON.stringify(exportObj, null, 2);
  currentTxt = cookieString;
  output.value = currentJson;

  // Copy JSON
  copyJsonBtn.addEventListener("click", async () => {
    await navigator.clipboard.writeText(currentJson);
    copyJsonBtn.textContent = "Copied cookie.json!";
    setTimeout(() => { copyJsonBtn.textContent = "Copy as cookie.json"; }, 2000);
  });

  // Download JSON
  downloadJsonBtn.addEventListener("click", () => {
    const blob = new Blob([currentJson], { type: "application/json" });
    const dlUrl = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = dlUrl;
    a.download = "cookie.json";
    a.click();
    URL.revokeObjectURL(dlUrl);
  });

  // Copy TXT
  copyTxtBtn.addEventListener("click", async () => {
    await navigator.clipboard.writeText(currentTxt);
    copyTxtBtn.textContent = "Copied cookie.txt!";
    setTimeout(() => { copyTxtBtn.textContent = "Copy as single-line cookie.txt"; }, 2000);
  });
});
