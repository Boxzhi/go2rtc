(() => {
  "use strict";

  const apiBase = window.location.pathname.replace(/recording-poc\.html$/, "").replace(/\/$/, "") + "/api/luxe/recordings";
  const authStatus = document.getElementById("auth-status");
  const rows = [...document.querySelectorAll(".camera")];

  function getHass() {
    const roots = [window, window.parent, window.top];
    for (const root of roots) {
      try {
        const host = root.document && root.document.querySelector("home-assistant");
        if (host && host.hass) return host.hass;
        if (root.hass) return root.hass;
      } catch (_) {}
    }
    return null;
  }

  function getToken() {
    const hass = getHass();
    return hass && hass.auth && hass.auth.data && hass.auth.data.access_token
      ? hass.auth.data.access_token
      : "";
  }

  function setStatus(row, message) {
    row.querySelector(".status").textContent = message;
  }

  async function apiRequest(path, options = {}) {
    const token = getToken();
    if (!token) throw new Error("无法读取 Home Assistant access token。请从 Home Assistant 的 Add-on Ingress 面板打开本页面。");

    const headers = Object.assign({}, options.headers || {}, {
      Authorization: "Bearer " + token
    });
    const response = await fetch(apiBase + path, Object.assign({
      credentials: "same-origin",
      cache: "no-store"
    }, options, { headers }));

    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || ("HTTP " + response.status));
    return body;
  }

  function filenameFor(row) {
    const input = row.querySelector(".filename");
    const source = row.querySelector(".source").value.trim();
    const stamp = new Date().toISOString().replace(/[-:T]/g, "").slice(0, 14);
    let name = input.value.trim();
    if (!name) name = source + "-" + stamp + ".mp4";
    if (!name.toLowerCase().endsWith(".mp4")) name += ".mp4";
    input.value = name;
    return name;
  }

  async function createSession(row) {
    const source = row.querySelector(".source").value.trim();
    if (!source) throw new Error("请填写 go2rtc Stream 名称。");

    const result = await apiRequest("/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source, filename: filenameFor(row) })
    });

    row.dataset.sessionId = result.session_id;
    row.dataset.downloadUrl = result.download_url;
    row.dataset.filename = result.filename;
    row.querySelector('[data-action="download"]').disabled = false;
    row.querySelector('[data-action="stop"]').disabled = false;
    setStatus(row, "任务已创建。点击「开始下载」后，浏览器将直接接收 MP4 文件流。");
  }

  function startNativeDownload(row) {
    const downloadUrl = row.dataset.downloadUrl;
    if (!downloadUrl) throw new Error("请先创建任务。");

    // Deliberately use a normal browser navigation. The MP4 response is never
    // fetched into JavaScript, converted to Blob, or written to OPFS.
    const link = document.createElement("a");
    link.href = downloadUrl;
    link.target = "_blank";
    link.rel = "noopener";
    link.download = row.dataset.filename || "";
    link.style.display = "none";
    document.body.appendChild(link);
    link.click();
    link.remove();

    setStatus(row, "已提交原生下载请求；正在等待摄像机关键帧并录像。");
  }

  async function stopSession(row) {
    const id = row.dataset.sessionId;
    if (!id) return;
    await apiRequest("/" + encodeURIComponent(id) + "/stop", { method: "POST" });
    setStatus(row, "已发送停止请求。");
  }

  async function refreshRow(row) {
    const id = row.dataset.sessionId;
    if (!id) return;
    try {
      const status = await apiRequest("/" + encodeURIComponent(id));
      const labels = {
        pending: "等待浏览器开始下载",
        starting: "正在连接摄像机 / 等待初始化",
        recording: "REC 正在录像并下载",
        stopping: "正在停止并收尾",
        stopped: "录像已停止",
        interrupted: "下载连接中断",
        failed: "录像失败：" + (status.error || "未知错误"),
        expired: "任务已过期"
      };
      setStatus(row, labels[status.state] || status.state);
      if (["stopped", "interrupted", "failed", "expired"].includes(status.state)) {
        row.querySelector('[data-action="download"]').disabled = true;
        row.querySelector('[data-action="stop"]').disabled = true;
      }
    } catch (error) {
      setStatus(row, "状态查询失败：" + error.message);
    }
  }

  for (const row of rows) {
    row.querySelector('[data-action="create"]').addEventListener("click", async () => {
      const button = row.querySelector('[data-action="create"]');
      button.disabled = true;
      try {
        await createSession(row);
      } catch (error) {
        setStatus(row, "创建失败：" + error.message);
      } finally {
        button.disabled = false;
      }
    });

    row.querySelector('[data-action="download"]').addEventListener("click", () => {
      try {
        startNativeDownload(row);
      } catch (error) {
        setStatus(row, "下载失败：" + error.message);
      }
    });

    row.querySelector('[data-action="stop"]').addEventListener("click", async () => {
      const button = row.querySelector('[data-action="stop"]');
      button.disabled = true;
      try {
        await stopSession(row);
      } catch (error) {
        setStatus(row, "停止失败：" + error.message);
        button.disabled = false;
      }
    });
  }

  const token = getToken();
  authStatus.textContent = token
    ? "Home Assistant 授权已就绪。可分别创建三路任务，并独立启动三路下载。"
    : "未检测到 Home Assistant 授权。请通过 Add-on Ingress 面板打开本页面。";

  window.setInterval(() => {
    // Poll all sessions concurrently; there is no media-download queue.
    Promise.all(rows.map(refreshRow));
  }, 1000);
})();
