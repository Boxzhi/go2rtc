"use strict";

const DOWNLOAD_PREFIX = "__luxe_opfs_download__/";

self.addEventListener("install", event => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener("activate", event => {
  event.waitUntil(self.clients.claim());
});

async function findSourceClient(event) {
  // For a navigation download opened in a new tab, clientId can be empty and
  // resultingClientId belongs to the download tab. Prefer the initiating page.
  if (event.clientId) {
    const source = await self.clients.get(event.clientId);
    if (source) return source;
  }

  const clients = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  const candidates = clients.filter(client =>
    client.id !== event.resultingClientId &&
    client.url.startsWith(self.registration.scope)
  );
  return candidates.find(client => client.url.includes("recording-opfs-poc.html")) ||
    candidates[0] || null;
}

function notify(client, type, fileName, detail) {
  try {
    client.postMessage(Object.assign({ type, fileName }, detail || {}));
  } catch (_) {}
}

async function serveOPFSFile(event, fileName, finishLifetime) {
  if (!/^luxe-opfs-poc-[abc]-[0-9a-f-]+\.bin$/.test(fileName)) {
    finishLifetime();
    return new Response("Invalid OPFS test file name", { status: 400 });
  }

  const requestURL = new URL(event.request.url);
  const expectedSize = Number(requestURL.searchParams.get("size"));
  const expectedCRC = String(requestURL.searchParams.get("crc32") || "").toLowerCase();
  if (!Number.isSafeInteger(expectedSize) || expectedSize <= 0 || !/^[a-f0-9]{8}$/.test(expectedCRC)) {
    finishLifetime();
    return new Response("Missing or invalid OPFS file metadata", { status: 400 });
  }

  let client;
  try {
    client = await findSourceClient(event);
  } catch (_) {
    client = null;
  }
  if (!client) {
    finishLifetime();
    return new Response("The OPFS POC page is no longer available", { status: 410 });
  }

  const channel = new MessageChannel();
  let pendingPullResolve = null;
  let finished = false;
  let sentBytes = 0;
  let handshakeTimer = null;

  const finish = () => {
    if (finished) return;
    finished = true;
    if (pendingPullResolve) {
      const resolve = pendingPullResolve;
      pendingPullResolve = null;
      resolve();
    }
    if (handshakeTimer !== null) clearTimeout(handshakeTimer);
    try { channel.port1.close(); } catch (_) {}
    finishLifetime();
  };

  const body = new ReadableStream({
    start(controller) {
      handshakeTimer = setTimeout(() => {
        if (finished) return;
        try { channel.port1.postMessage({ type: "cancel" }); } catch (_) {}
        controller.error(new Error("OPFS 读取 Worker 启动超时"));
        notify(client, "download-stream-error", fileName, { message: "OPFS 读取 Worker 启动超时" });
        finish();
      }, 10000);
      channel.port1.onmessage = messageEvent => {
        const data = messageEvent.data || {};

        if (data.type === "ready") {
          if (handshakeTimer !== null) {
            clearTimeout(handshakeTimer);
            handshakeTimer = null;
          }
          if (Number(data.sizeBytes) !== expectedSize) {
            try { channel.port1.postMessage({ type: "cancel" }); } catch (_) {}
            controller.error(new Error("OPFS 文件大小与下载请求不一致"));
            notify(client, "download-stream-error", fileName, { message: "OPFS 文件大小不一致" });
            finish();
          }
          return;
        }

        if (data.type === "chunk") {
          const bytes = new Uint8Array(data.buffer);
          const count = Number(data.bytes) || bytes.byteLength;
          if (count !== bytes.byteLength) {
            try { channel.port1.postMessage({ type: "cancel" }); } catch (_) {}
            controller.error(new Error("OPFS 分块长度不一致"));
            notify(client, "download-stream-error", fileName, { message: "OPFS 分块长度不一致" });
            finish();
            return;
          }
          sentBytes += count;
          controller.enqueue(bytes);
          notify(client, "download-stream-progress", fileName, {
            bytes: sentBytes,
            totalBytes: expectedSize
          });
          if (pendingPullResolve) {
            const resolve = pendingPullResolve;
            pendingPullResolve = null;
            resolve();
          }
          return;
        }

        if (data.type === "done") {
          const valid = Number(data.sizeBytes) === expectedSize &&
            String(data.crc32).toLowerCase() === expectedCRC &&
            sentBytes === expectedSize;
          if (valid) {
            controller.close();
            notify(client, "download-stream-complete", fileName, {
              sizeBytes: sentBytes,
              crc32: data.crc32,
              checksumVerified: true
            });
          } else {
            controller.error(new Error("OPFS 下载流 CRC32 或文件大小校验失败"));
            notify(client, "download-stream-error", fileName, {
              message: "CRC32 或文件大小校验失败",
              bytes: sentBytes,
              totalBytes: expectedSize
            });
          }
          finish();
          return;
        }

        if (data.type === "error") {
          controller.error(new Error(data.message || "OPFS 文件读取失败"));
          notify(client, "download-stream-error", fileName, { message: data.message || "OPFS 文件读取失败" });
          finish();
          return;
        }

        if (data.type === "cancelled") {
          notify(client, "download-stream-cancelled", fileName, {
            bytes: sentBytes,
            totalBytes: expectedSize
          });
          finish();
        }
      };
      channel.port1.start();
    },
    pull() {
      if (finished) return;
      return new Promise(resolve => {
        pendingPullResolve = resolve;
        try {
          channel.port1.postMessage({ type: "pull" });
        } catch (error) {
          resolve();
          notify(client, "download-stream-error", fileName, {
            message: error && error.message ? error.message : String(error)
          });
          finish();
        }
      });
    },
    cancel(reason) {
      try { channel.port1.postMessage({ type: "cancel", reason: String(reason || "") }); } catch (_) {}
      notify(client, "download-stream-cancelled", fileName, { bytes: sentBytes, totalBytes: expectedSize });
      finish();
    }
  });

  try {
    client.postMessage({ type: "LUXE_OPFS_DOWNLOAD_REQUEST", fileName }, [channel.port2]);
  } catch (error) {
    finish();
    return new Response("Could not connect to the OPFS reader worker", { status: 503 });
  }

  return new Response(body, {
    status: 200,
    headers: {
      "Content-Type": "application/octet-stream",
      "Content-Length": String(expectedSize),
      "Content-Disposition": 'attachment; filename="' + fileName + '"',
      "Cache-Control": "no-store, no-cache, must-revalidate",
      "Pragma": "no-cache",
      "X-Content-Type-Options": "nosniff"
    }
  });
}

self.addEventListener("fetch", event => {
  if (event.request.method !== "GET") return;

  const requestURL = new URL(event.request.url);
  const scopeURL = new URL(self.registration.scope);
  if (requestURL.origin !== scopeURL.origin || !requestURL.pathname.startsWith(scopeURL.pathname)) return;

  const relativePath = requestURL.pathname.slice(scopeURL.pathname.length);
  if (!relativePath.startsWith(DOWNLOAD_PREFIX)) return;

  const fileName = decodeURIComponent(relativePath.slice(DOWNLOAD_PREFIX.length));
  let finishLifetime;
  const lifetime = new Promise(resolve => { finishLifetime = resolve; });
  event.waitUntil(lifetime);
  event.respondWith(serveOPFSFile(event, fileName, finishLifetime));
});
