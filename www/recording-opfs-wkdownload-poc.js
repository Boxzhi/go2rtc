(() => {
  "use strict";

  const MiB = 1024 * 1024;
  const list = document.getElementById("file-list");
  const status = document.getElementById("global-status");
  const refreshButton = document.getElementById("refresh-files");
  const tasks = new Map();
  const workers = new Map();
  const API = new URL("./api/luxe/recording-poc/relay", location.href);

  function formatBytes(value) {
    if (value >= MiB) return (value / MiB).toFixed(1) + " MiB";
    return (value / 1024).toFixed(1) + " KiB";
  }

  async function requestJSON(url, options) {
    const response = await fetch(url, Object.assign({
      credentials: "same-origin",
      cache: "no-store",
      redirect: "error"
    }, options || {}));
    const body = await response.text();
    let data = {};
    try { data = body ? JSON.parse(body) : {}; } catch (_) {}
    if (!response.ok) throw new Error(data.error || ("HTTP " + response.status));
    return data;
  }

  function statFile(fileName) {
    return new Promise((resolve, reject) => {
      const worker = new Worker(new URL("./recording-opfs-worker.js", location.href));
      const finish = () => worker.terminate();
      worker.onmessage = event => {
        const message = event.data || {};
        if (message.type === "stat-complete") {
          finish();
          resolve({ sizeBytes: message.sizeBytes, crc32: message.crc32 });
        } else if (message.type === "stat-error") {
          finish();
          reject(new Error(message.message || "无法读取 OPFS 文件"));
        }
      };
      worker.onerror = event => {
        finish();
        reject(new Error(event.message || "OPFS Worker 异常"));
      };
      worker.postMessage({ type: "stat", fileName });
    });
  }

  function setCardStatus(record, message) {
    record.statusLine.textContent = message;
  }

  function updateButtons(record) {
    record.prepareButton.disabled = !!record.preparing || !!record.task || !!record.uploading;
    record.startButton.disabled = !record.task || !!record.uploading || !!record.finished;
    record.abortButton.disabled = !record.task || !!record.finished;
    record.cleanupButton.disabled = !record.finished || !!record.uploading;
  }

  async function prepare(record) {
    record.preparing = true;
    updateButtons(record);
    setCardStatus(record, "正在向 go2rtc 创建独立流式中转任务…");
    try {
      const task = await requestJSON(API.href, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          filename: record.fileName,
          size_bytes: record.sizeBytes,
          crc32: record.crc32
        })
      });
      record.task = task;
      setCardStatus(record,
        "任务已创建 · " + task.session_id + " · 256 KiB 分块 · 等待点击开始原生下载");
    } catch (error) {
      setCardStatus(record, "创建任务失败：" + error.message);
    } finally {
      record.preparing = false;
      updateButtons(record);
    }
  }

  async function start(record) {
    if (!record.task || record.uploading || record.finished) return;

    // Match the already-tested WKDownload POC: a same-window, user-initiated
    // attachment link with a download attribute. WKDownload owns the response,
    // so the current Ingress page remains available for the OPFS uploader.
    const anchor = document.createElement("a");
    anchor.href = new URL(record.task.download_url, location.origin).href;
    anchor.download = record.task.filename || record.fileName;
    anchor.hidden = true;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();

    record.uploading = true;
    updateButtons(record);
    setCardStatus(record, "已触发附件下载，正在等待 go2rtc 确认 WKDownload 请求…");

    try {
      const deadline = Date.now() + 15000;
      let accepted = false;
      while (Date.now() < deadline) {
        const state = await requestJSON(record.task.status_url);
        if (state.error) throw new Error(state.error);
        if (state.download_started) {
          accepted = true;
          break;
        }
        await new Promise(resolve => setTimeout(resolve, 300));
      }
      if (!accepted) throw new Error("15 秒内 go2rtc 未收到下载请求。请检查原生下载界面后重试。");
    } catch (error) {
      record.uploading = false;
      setCardStatus(record, "下载请求未确认：" + error.message + "。尚未开始上传，OPFS 文件保留。");
      updateButtons(record);
      return;
    }

    setCardStatus(record, "WKDownload 已接入。Dedicated Worker 正在从 OPFS 分块上传…");
    const worker = new Worker(new URL("./recording-opfs-relay-worker.js", location.href));
    workers.set(record.fileName, worker);
    worker.onmessage = event => {
      const message = event.data || {};
      if (message.type === "progress") {
        setCardStatus(record,
          "OPFS → go2rtc → WKDownload · " + formatBytes(message.uploadedBytes) +
          " / " + formatBytes(message.totalBytes) +
          "（" + Math.floor(message.uploadedBytes / message.totalBytes * 100) + "%）");
        return;
      }
      if (message.type === "complete") {
        record.uploading = false;
        record.uploadComplete = true;
        setCardStatus(record,
          "上传完成 · 服务端 CRC32 校验通过 · " + formatBytes(message.sizeBytes) +
          " · CRC32 " + message.crc32 + " · 正在确认下载响应完成…");
        updateButtons(record);
        pollStatus(record);
        worker.terminate();
        workers.delete(record.fileName);
        return;
      }
      if (message.type === "error") {
        record.uploading = false;
        setCardStatus(record, "中转失败：" + message.message + "。OPFS 文件保留。");
        updateButtons(record);
        worker.terminate();
        workers.delete(record.fileName);
      }
    };
    worker.onerror = event => {
      record.uploading = false;
      setCardStatus(record, "中转 Worker 异常：" + (event.message || "未知错误"));
      updateButtons(record);
      worker.terminate();
      workers.delete(record.fileName);
    };
    worker.postMessage({
      type: "upload",
      fileName: record.fileName,
      sizeBytes: record.sizeBytes,
      crc32: record.crc32,
      uploadURL: record.task.upload_url,
      completeURL: record.task.complete_url,
      abortURL: record.task.abort_url
    });
  }

  async function pollStatus(record) {
    if (!record.task || record.finished) return;
    const started = Date.now();
    while (Date.now() - started < 60000) {
      try {
        const data = await requestJSON(record.task.status_url);
        if (data.error) throw new Error(data.error);
        if (data.download_complete && data.upload_complete &&
            Number(data.downloaded_bytes) === record.sizeBytes) {
          record.finished = true;
          setCardStatus(record,
            "go2rtc 已完整输出下载流 · " + formatBytes(data.downloaded_bytes) +
            " · CRC32 " + data.crc32 +
            "。请确认 iOS 下载管理器显示完成，再清理 OPFS。");
          updateButtons(record);
          return;
        }
        if (data.download_started) {
          setCardStatus(record,
            "WKDownload 已接入 · go2rtc 已输出 " + formatBytes(data.downloaded_bytes) +
            " / " + formatBytes(record.sizeBytes));
        }
      } catch (error) {
        setCardStatus(record, "查询中转状态失败：" + error.message);
        return;
      }
      await new Promise(resolve => setTimeout(resolve, 800));
    }
    setCardStatus(record, "尚未收到下载完成确认。请检查 iOS 下载管理器；OPFS 文件仍保留。");
  }

  async function abort(record) {
    if (!record.task || record.finished) return;
    try {
      await requestJSON(record.task.abort_url, { method: "POST" });
      const worker = workers.get(record.fileName);
      if (worker) worker.terminate();
      workers.delete(record.fileName);
      record.uploading = false;
      record.task = null;
      setCardStatus(record, "任务已中止。OPFS 临时文件保留，可重新创建任务。");
    } catch (error) {
      setCardStatus(record, "中止任务失败：" + error.message);
    }
    updateButtons(record);
  }

  async function cleanup(record) {
    if (!record.finished || record.uploading) return;
    try {
      const root = await navigator.storage.getDirectory();
      await root.removeEntry(record.fileName);
      setCardStatus(record, "OPFS 临时文件已删除。下载管理器中的文件不受影响。");
      record.fileName = "";
      record.cleanupButton.disabled = true;
      record.prepareButton.disabled = true;
    } catch (error) {
      setCardStatus(record, "清理失败：" + error.message);
    }
  }

  function makeCard(fileName, metadata) {
    const card = document.createElement("section");
    card.className = "file";
    const title = document.createElement("h2");
    title.textContent = "OPFS 文件 " + fileName;
    const detail = document.createElement("p");
    detail.className = "muted";
    detail.textContent = formatBytes(metadata.sizeBytes) + " · CRC32 " + metadata.crc32;

    const actions = document.createElement("div");
    actions.className = "actions";
    const prepareButton = document.createElement("button");
    prepareButton.textContent = "创建中转任务";
    const startButton = document.createElement("button");
    startButton.textContent = "开始原生下载";
    startButton.className = "secondary";
    startButton.disabled = true;
    const abortButton = document.createElement("button");
    abortButton.textContent = "中止任务";
    abortButton.className = "danger";
    abortButton.disabled = true;
    const cleanupButton = document.createElement("button");
    cleanupButton.textContent = "清理 OPFS";
    cleanupButton.className = "danger";
    cleanupButton.disabled = true;
    actions.append(prepareButton, startButton, abortButton, cleanupButton);

    const statusLine = document.createElement("p");
    statusLine.className = "file-status status";
    statusLine.textContent = "OPFS 文件已就绪，等待创建中转任务。";
    card.append(title, detail, actions, statusLine);
    list.appendChild(card);

    const record = {
      fileName,
      sizeBytes: metadata.sizeBytes,
      crc32: metadata.crc32,
      card,
      statusLine,
      prepareButton,
      startButton,
      abortButton,
      cleanupButton,
      preparing: false,
      uploading: false,
      finished: false,
      task: null
    };
    tasks.set(fileName, record);
    prepareButton.addEventListener("click", () => prepare(record));
    startButton.addEventListener("click", () => start(record));
    abortButton.addEventListener("click", () => abort(record));
    cleanupButton.addEventListener("click", () => cleanup(record));
    updateButtons(record);
  }

  async function scanFiles() {
    list.replaceChildren();
    tasks.clear();
    status.textContent = "正在扫描当前设备 OPFS…";
    if (!isSecureContext || !navigator.storage || !navigator.storage.getDirectory ||
        typeof Worker === "undefined") {
      status.textContent = "当前环境不支持 OPFS 或 Dedicated Worker。";
      return;
    }
    try {
      const root = await navigator.storage.getDirectory();
      const names = [];
      for await (const [name] of root.entries()) {
        if (/^luxe-opfs-poc-[abc]-[0-9a-f-]+\.bin$/.test(name)) names.push(name);
      }
      names.sort();
      for (const name of names) {
        const metadata = await statFile(name);
        makeCard(name, metadata);
      }
      status.textContent = names.length
        ? "发现 " + names.length + " 个 OPFS 测试文件。Service Worker 不参与本方案。"
        : "没有找到 OPFS 测试文件。请先返回 OPFS POC 写入 A、B、C。";
    } catch (error) {
      status.textContent = "扫描 OPFS 失败：" + error.message;
    }
  }

  refreshButton.addEventListener("click", scanFiles);
  scanFiles();
})();
