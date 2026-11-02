(() => {
  "use strict";

  const MiB = 1024 * 1024;
  const rows = [...document.querySelectorAll("[data-file]")];
  const recoveryList = document.getElementById("recovery-list");
  const envStatus = document.getElementById("env-status");
  const quotaStatus = document.getElementById("quota-status");
  const swStatus = document.getElementById("sw-status");
  const globalStatus = document.getElementById("global-status");
  const sizeSelect = document.getElementById("size-mib");
  const workers = new Map();
  const downloadWorkers = new Map();
  const records = new Map();
  let serviceWorkerRegistration = null;
  let recoveryScanned = false;
  let recoveredIndex = 0;

  function formatBytes(value) {
    if (value >= MiB) return (value / MiB).toFixed(1) + " MiB";
    return (value / 1024).toFixed(1) + " KiB";
  }

  function setRowStatus(row, message) {
    row.querySelector(".file-status").textContent = message;
  }

  function updateRowButtons(row, record) {
    const state = record || {};
    row.querySelector('[data-action="write"]').disabled = !!state.writing || !!state.fileName;
    row.querySelector('[data-action="download"]').disabled = !state.ready || !!state.downloading;
    row.querySelector('[data-action="cleanup"]').disabled = !state.fileName || !!state.writing || !!state.downloading;
  }

  function makeFileName(id) {
    const suffix = crypto.randomUUID
      ? crypto.randomUUID()
      : Date.now().toString(16) + "-" + Math.random().toString(16).slice(2);
    return "luxe-opfs-poc-" + id + "-" + suffix + ".bin";
  }

  async function refreshQuota() {
    if (!navigator.storage || !navigator.storage.estimate) {
      quotaStatus.textContent = "浏览器未提供 StorageManager.estimate()。";
      return;
    }
    try {
      const estimate = await navigator.storage.estimate();
      const usage = Number(estimate.usage) || 0;
      const quota = Number(estimate.quota) || 0;
      const free = Math.max(0, quota - usage);
      quotaStatus.textContent = quota
        ? "已用 " + formatBytes(usage) + " / 配额 " + formatBytes(quota) + " · 估算可用 " + formatBytes(free)
        : "浏览器未返回有效存储配额。";
      if (navigator.storage.persisted) {
        const persisted = await navigator.storage.persisted();
        quotaStatus.textContent += persisted ? " · 已获持久化保护" : " · 当前未获持久化保护";
      }
    } catch (error) {
      quotaStatus.textContent = "读取存储配额失败：" + error.message;
    }
  }

  function waitForController(timeoutMs) {
    if (navigator.serviceWorker.controller) return Promise.resolve(true);
    return new Promise(resolve => {
      let done = false;
      const finish = value => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        navigator.serviceWorker.removeEventListener("controllerchange", changed);
        resolve(value);
      };
      const changed = () => finish(!!navigator.serviceWorker.controller);
      const timer = setTimeout(() => finish(false), timeoutMs);
      navigator.serviceWorker.addEventListener("controllerchange", changed, { once: true });
    });
  }

  async function registerServiceWorker() {
    if (!window.isSecureContext) throw new Error("当前页面不是安全上下文，Service Worker 无法注册。");
    if (!("serviceWorker" in navigator)) throw new Error("当前浏览器或 HA App WebView 不支持 Service Worker。");
    if (!navigator.storage || !navigator.storage.getDirectory) throw new Error("当前浏览器不支持 OPFS。");

    const scriptURL = new URL("./recording-opfs-sw.js", location.href);
    const scopePath = new URL("./", location.href).pathname;
    serviceWorkerRegistration = await navigator.serviceWorker.register(scriptURL, {
      scope: scopePath,
      updateViaCache: "none"
    });
    await serviceWorkerRegistration.update().catch(() => {});
    await navigator.serviceWorker.ready;
    const controlled = await waitForController(8000);
    if (!controlled) {
      throw new Error("Service Worker 已注册，但当前页面尚未受其控制。请刷新本测试页后重试。");
    }
    swStatus.textContent = "Service Worker 已注册并控制当前 Ingress 页面。";
    return true;
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
          reject(new Error(message.message || "无法读取遗留 OPFS 文件"));
        }
      };
      worker.onerror = event => {
        finish();
        reject(new Error(event.message || "OPFS stat Worker 异常"));
      };
      worker.postMessage({ type: "stat", fileName });
    });
  }

  function makeRecoveredRow(fileName, metadata) {
    recoveredIndex += 1;
    const recordId = "recovered-" + recoveredIndex;
    const row = document.createElement("section");
    row.className = "file";
    row.dataset.file = recordId;
    row.dataset.recovered = "true";

    const head = document.createElement("div");
    head.className = "file-head";
    const title = document.createElement("h2");
    title.textContent = "恢复的临时文件 " + recoveredIndex;
    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = "OPFS 恢复";
    head.append(title, tag);

    const description = document.createElement("p");
    description.className = "muted";
    description.textContent = fileName + " · " + formatBytes(metadata.sizeBytes);

    const actions = document.createElement("div");
    actions.className = "actions";
    actions.innerHTML = '<button data-action="write" disabled>写入测试文件</button><button data-action="download" class="secondary">下载文件</button><button data-action="cleanup" class="danger">清理临时文件</button>';

    const status = document.createElement("p");
    status.className = "file-status status";
    status.textContent = "发现上次测试遗留的 OPFS 文件，可重新下载或清理。";

    row.append(head, description, actions, status);
    recoveryList.appendChild(row);

    const record = {
      fileName,
      row,
      writing: false,
      ready: true,
      downloading: false,
      streamComplete: false,
      sizeBytes: metadata.sizeBytes,
      crc32: metadata.crc32,
      recovered: true
    };
    records.set(recordId, record);
    bindRow(row);
    updateRowButtons(row, record);
  }

  async function recoverExistingFiles() {
    if (recoveryScanned) return;
    recoveryScanned = true;
    if (!navigator.storage || !navigator.storage.getDirectory) return;

    try {
      const root = await navigator.storage.getDirectory();
      for await (const [name] of root.entries()) {
        if (!/^luxe-opfs-poc-[abc]-[0-9a-f-]+\.bin$/.test(name)) continue;
        const alreadyKnown = [...records.values()].some(record => record.fileName === name);
        if (alreadyKnown) continue;
        const metadata = await statFile(name);
        makeRecoveredRow(name, metadata);
      }
      if (recoveryList.childElementCount) {
        document.getElementById("recovery-panel").hidden = false;
      }
    } catch (error) {
      globalStatus.textContent += " · 扫描遗留文件失败：" + error.message;
    }
  }

  async function inspectEnvironment() {
    const secure = window.isSecureContext;
    const opfs = !!(navigator.storage && navigator.storage.getDirectory);
    const worker = typeof Worker !== "undefined";
    const sw = "serviceWorker" in navigator;
    envStatus.textContent = [
      "安全上下文：" + (secure ? "支持" : "不支持"),
      "OPFS：" + (opfs ? "可用" : "不可用"),
      "Dedicated Worker：" + (worker ? "可用" : "不可用"),
      "Service Worker：" + (sw ? "可用" : "不可用"),
      "OPFS 读取：" + "由 Dedicated Worker 执行，不使用 File / Blob"
    ].join(" · ");
    await refreshQuota();

    if (secure && opfs && worker && sw) {
      globalStatus.textContent = "基础 API 检测通过，正在注册 Service Worker…";
      try {
        await registerServiceWorker();
        globalStatus.textContent = "环境检测通过。OPFS 写入、Worker 分块读取和下载服务已就绪。";
        await recoverExistingFiles();
      } catch (error) {
        globalStatus.textContent = "环境检测未通过：" + error.message;
        swStatus.textContent = "Service Worker：注册或控制失败。";
      }
    } else {
      globalStatus.textContent = "当前环境缺少必要 API，暂不能进行完整测试。";
    }
  }

  function startWrite(row) {
    const id = row.dataset.file;
    const previous = records.get(id);
    if (previous && previous.fileName) {
      setRowStatus(row, "请先清理当前 OPFS 临时文件，再开始新的写入。");
      return;
    }

    const fileName = makeFileName(id);
    const record = {
      fileName,
      row,
      writing: true,
      ready: false,
      downloading: false,
      streamComplete: false,
      sizeBytes: 0,
      crc32: ""
    };
    records.set(id, record);
    updateRowButtons(row, record);
    setRowStatus(row, "正在创建 OPFS 文件…");

    const worker = new Worker(new URL("./recording-opfs-worker.js", location.href));
    workers.set(id, worker);
    worker.onmessage = event => {
      const message = event.data || {};
      if (message.type === "progress") {
        const percent = Math.floor(message.writtenBytes / message.totalBytes * 100);
        setRowStatus(row, "正在写入 " + formatBytes(message.writtenBytes) + " / " +
          formatBytes(message.totalBytes) + "（" + percent + "%）");
        return;
      }
      if (message.type === "complete") {
        record.writing = false;
        record.ready = true;
        record.sizeBytes = message.sizeBytes;
        record.crc32 = message.crc32;
        setRowStatus(row, "OPFS 写入完成 · " + formatBytes(record.sizeBytes) +
          " · CRC32 " + record.crc32 + " · 等待下载");
        worker.terminate();
        workers.delete(id);
        updateRowButtons(row, record);
        refreshQuota();
        return;
      }
      if (message.type === "error") {
        record.writing = false;
        record.ready = false;
        setRowStatus(row, "写入失败：" + message.message);
        worker.terminate();
        workers.delete(id);
        updateRowButtons(row, record);
        refreshQuota();
      }
    };
    worker.onerror = error => {
      record.writing = false;
      setRowStatus(row, "Worker 异常：" + (error.message || "未知错误"));
      worker.terminate();
      workers.delete(id);
      updateRowButtons(row, record);
    };
    worker.postMessage({
      type: "write",
      fileName,
      sizeMiB: Number(sizeSelect.value) || 16,
      seed: id.charCodeAt(0)
    });
  }

  function startDownload(row) {
    const id = row.dataset.file;
    const record = records.get(id);
    if (!record || !record.ready) throw new Error("请先完成 OPFS 写入。");
    if (!navigator.serviceWorker.controller) throw new Error("当前页面没有受 Service Worker 控制，请先重新检测环境或刷新页面。");

    const downloadURL = new URL("./__luxe_opfs_download__/" + encodeURIComponent(record.fileName), location.href);
    downloadURL.searchParams.set("size", String(record.sizeBytes));
    downloadURL.searchParams.set("crc32", record.crc32);

    const anchor = document.createElement("a");
    anchor.href = downloadURL.href;
    anchor.target = "_blank";
    anchor.rel = "noopener";
    anchor.hidden = true;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();

    record.downloading = true;
    updateRowButtons(row, record);
    setRowStatus(row, "已发起浏览器原生下载。OPFS 文件仍保留，等待下载流校验。");
  }

  async function cleanupFile(row) {
    const id = row.dataset.file;
    const record = records.get(id);
    if (!record || !record.fileName) return;
    if (record.downloading) throw new Error("下载流仍在进行，暂不能清理。");

    const root = await navigator.storage.getDirectory();
    await root.removeEntry(record.fileName);
    records.delete(id);
    if (row.dataset.recovered === "true") {
      row.remove();
      if (!recoveryList.childElementCount) document.getElementById("recovery-panel").hidden = true;
    } else {
      setRowStatus(row, "OPFS 临时文件已删除。");
      updateRowButtons(row, {});
    }
    await refreshQuota();
  }

  function handleServiceWorkerMessage(message) {
    if (!message || !message.fileName) return;
    const record = [...records.values()].find(item => item.fileName === message.fileName);
    if (!record || !record.row) return;
    const row = record.row;

    if (message.type === "download-stream-progress") {
      setRowStatus(row, "浏览器正在接收 " + formatBytes(message.bytes) + " / " +
        formatBytes(message.totalBytes) + "（OPFS 分块读取）");
      return;
    }

    if (message.type === "download-stream-complete") {
      record.downloading = false;
      record.streamComplete = true;
      setRowStatus(row, "浏览器已完整读取下载流 · CRC32 校验通过 · " +
        formatBytes(message.sizeBytes) + "。请确认下载管理器中已保存文件，再清理 OPFS。");
    } else if (message.type === "download-stream-cancelled") {
      record.downloading = false;
      setRowStatus(row, "下载流被取消。OPFS 文件已保留，可以重新下载。");
    } else if (message.type === "download-stream-error") {
      record.downloading = false;
      setRowStatus(row, "下载流错误：" + message.message + "。OPFS 文件已保留。");
    } else {
      return;
    }
    const readerWorker = downloadWorkers.get(message.fileName);
    if (readerWorker) {
      readerWorker.terminate();
      downloadWorkers.delete(message.fileName);
    }
    updateRowButtons(row, record);
  }

  function handleWorkerDownloadRequest(event) {
    const message = event.data || {};
    if (message.type !== "LUXE_OPFS_DOWNLOAD_REQUEST" || !event.ports || !event.ports[0]) return false;
    const port = event.ports[0];
    try {
      const worker = new Worker(new URL("./recording-opfs-worker.js", location.href));
      downloadWorkers.set(message.fileName, worker);
      worker.postMessage({ type: "read", fileName: message.fileName, port }, [port]);
      return true;
    } catch (error) {
      try { port.postMessage({ type: "error", message: error.message || String(error) }); } catch (_) {}
      try { port.close(); } catch (_) {}
      setGlobalError("无法启动 OPFS 读取 Worker：" + error.message);
      return true;
    }
  }

  function setGlobalError(message) {
    globalStatus.textContent = message;
  }

  function bindRow(row) {
    const writeButton = row.querySelector('[data-action="write"]');
    if (writeButton) writeButton.addEventListener("click", () => startWrite(row));
    row.querySelector('[data-action="download"]').addEventListener("click", () => {
      try { startDownload(row); }
      catch (error) { setRowStatus(row, "下载失败：" + error.message); }
    });
    row.querySelector('[data-action="cleanup"]').addEventListener("click", async () => {
      try { await cleanupFile(row); }
      catch (error) { setRowStatus(row, "清理失败：" + error.message); }
    });
  }

  for (const row of rows) bindRow(row);

  document.getElementById("write-all").addEventListener("click", () => {
    for (const row of rows) startWrite(row);
  });
  document.getElementById("detect").addEventListener("click", inspectEnvironment);

  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.addEventListener("message", event => {
      if (!handleWorkerDownloadRequest(event)) {
        handleServiceWorkerMessage(event.data);
      }
    });
  }

  inspectEnvironment();
})();
