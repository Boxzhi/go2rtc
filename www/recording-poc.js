(() => {
  "use strict";

  const apiBase = window.location.pathname.replace(/recording-poc\.html$/, "").replace(/\/$/, "") + "/api/luxe/recordings";
  const authStatus = document.getElementById("auth-status");
  const rows = [...document.querySelectorAll(".camera")];

  function setStatus(row, message) {
    row.querySelector(".status").textContent = message;
  }

  async function apiRequest(path, options = {}) {
    // The Supervisor authenticates the Ingress request and supplies
    // X-Remote-User-Id to go2rtc. No access token is exposed to this page.
    const response = await fetch(apiBase + path, Object.assign({
      credentials: "same-origin",
      cache: "no-store"
    }, options));

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

  // Query the authenticated API instead of reaching into the parent HA
  // frontend (which is unreliable in iOS HA App / sandboxed Ingress frames).
  authStatus.textContent = "正在验证 Ingress 用户身份…";
  apiRequest("").then(() => {
    authStatus.textContent = "Ingress 用户身份验证成功。可以独立创建三路录像任务。";
  }).catch(error => {
    authStatus.textContent = "Ingress 验证失败：" + error.message + "。请从 HA Add-on Ingress 打开测试页。";
  });

  window.setInterval(() => {
    // Poll all sessions concurrently; there is no media-download queue.
    Promise.all(rows.map(refreshRow));
  }, 1000);
})();
