"use strict";

const DOWNLOAD_PREFIX = "__luxe_opfs_download__/";
const CHANNEL_NAME = "luxe-opfs-poc";

self.addEventListener("install", event => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener("activate", event => {
  event.waitUntil(self.clients.claim());
});

function notify(message) {
  try {
    const channel = new BroadcastChannel(CHANNEL_NAME);
    channel.postMessage(message);
    channel.close();
  } catch (_) {}
}

function safeFileName(value) {
  return /^luxe-opfs-poc-[abc]-[0-9a-f-]+\.bin$/.test(value);
}

async function serveOPFSFile(fileName) {
  if (!safeFileName(fileName)) {
    return new Response("Invalid test file name", { status: 400 });
  }

  let fileHandle;
  let file;
  try {
    const root = await navigator.storage.getDirectory();
    fileHandle = await root.getFileHandle(fileName);
    file = await fileHandle.getFile();
  } catch (_) {
    return new Response("OPFS test file not found", { status: 404 });
  }

  const reader = file.stream().getReader();
  let finished = false;

  const body = new ReadableStream({
    async pull(controller) {
      try {
        const result = await reader.read();
        if (result.done) {
          finished = true;
          controller.close();
          notify({
            type: "download-stream-complete",
            fileName,
            sizeBytes: file.size
          });
          return;
        }
        controller.enqueue(result.value);
      } catch (error) {
        controller.error(error);
        notify({
          type: "download-stream-error",
          fileName,
          message: error && error.message ? error.message : String(error)
        });
      }
    },
    async cancel(reason) {
      if (!finished) {
        try { await reader.cancel(reason); } catch (_) {}
        notify({
          type: "download-stream-cancelled",
          fileName
        });
      }
    }
  });

  const headers = new Headers({
    "Content-Type": "application/octet-stream",
    "Content-Length": String(file.size),
    "Content-Disposition": 'attachment; filename="' + fileName + '"',
    "Cache-Control": "no-store, no-cache, must-revalidate",
    "Pragma": "no-cache",
    "X-Content-Type-Options": "nosniff"
  });

  return new Response(body, { status: 200, headers });
}

self.addEventListener("fetch", event => {
  if (event.request.method !== "GET") return;

  const requestURL = new URL(event.request.url);
  const scopeURL = new URL(self.registration.scope);
  if (requestURL.origin !== scopeURL.origin) return;
  if (!requestURL.pathname.startsWith(scopeURL.pathname)) return;

  const relativePath = requestURL.pathname.slice(scopeURL.pathname.length);
  if (!relativePath.startsWith(DOWNLOAD_PREFIX)) return;

  event.respondWith(serveOPFSFile(decodeURIComponent(relativePath.slice(DOWNLOAD_PREFIX.length))));
});
