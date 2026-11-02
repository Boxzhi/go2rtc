"use strict";

const CHUNK_SIZE = 256 * 1024;
const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = (c & 1) ? (0xEDB88320 ^ (c >>> 1)) : (c >>> 1);
    table[n] = c >>> 0;
  }
  return table;
})();

function crcUpdate(crc, bytes) {
  for (let i = 0; i < bytes.length; i++) {
    crc = CRC_TABLE[(crc ^ bytes[i]) & 0xFF] ^ (crc >>> 8);
  }
  return crc >>> 0;
}

function crcHex(crc) {
  return ((crc ^ 0xFFFFFFFF) >>> 0).toString(16).padStart(8, "0");
}

async function postJSON(url) {
  const response = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    cache: "no-store"
  });
  const text = await response.text();
  let result = {};
  try { result = text ? JSON.parse(text) : {}; } catch (_) {}
  if (!response.ok) throw new Error(result.error || ("HTTP " + response.status));
  return result;
}

async function uploadOPFS(data) {
  let access = null;
  let failed = false;
  try {
    if (!self.isSecureContext || !navigator.storage || !navigator.storage.getDirectory) {
      throw new Error("当前环境无法访问 OPFS。");
    }
    const root = await navigator.storage.getDirectory();
    const handle = await root.getFileHandle(data.fileName);
    access = await handle.createSyncAccessHandle();
    const actualSize = access.getSize();
    if (actualSize !== data.sizeBytes) {
      throw new Error("OPFS 文件大小发生变化：" + actualSize + "/" + data.sizeBytes);
    }

    let offset = 0;
    let crc = 0xFFFFFFFF;
    while (offset < actualSize) {
      const length = Math.min(CHUNK_SIZE, actualSize - offset);
      const chunk = new Uint8Array(length);
      const count = access.read(chunk, { at: offset });
      if (count !== length) throw new Error("OPFS 分块读取长度异常：" + count + "/" + length);

      crc = crcUpdate(crc, chunk);
      const url = new URL(data.uploadURL, self.location.href);
      url.searchParams.set("offset", String(offset));
      const response = await fetch(url.href, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        redirect: "error",
        headers: { "Content-Type": "application/octet-stream" },
        body: chunk.buffer
      });
      const responseText = await response.text();
      let result = {};
      try { result = responseText ? JSON.parse(responseText) : {}; } catch (_) {}
      if (!response.ok) throw new Error(result.error || ("分块上传失败，HTTP " + response.status));
      if (Number(result.next_offset) !== offset + count) throw new Error("服务器确认的偏移量不一致。");

      offset += count;
      self.postMessage({
        type: "progress",
        fileName: data.fileName,
        uploadedBytes: offset,
        totalBytes: actualSize
      });
    }

    const actualCRC = crcHex(crc);
    if (actualCRC !== String(data.crc32).toLowerCase()) {
      throw new Error("OPFS CRC32 发生变化：" + actualCRC + "/" + data.crc32);
    }

    const complete = await postJSON(data.completeURL);
    if (!complete.upload_complete || complete.crc32 !== actualCRC) {
      throw new Error("服务器端 CRC32 校验未通过。");
    }

    self.postMessage({
      type: "complete",
      fileName: data.fileName,
      sizeBytes: actualSize,
      crc32: actualCRC
    });
  } catch (error) {
    failed = true;
    try { await postJSON(data.abortURL); } catch (_) {}
    self.postMessage({
      type: "error",
      fileName: data.fileName || "",
      message: error && error.message ? error.message : String(error)
    });
  } finally {
    if (access) {
      try { access.close(); } catch (_) {}
    }
    if (failed) self.close();
  }
}

self.onmessage = event => {
  const data = event.data || {};
  if (data.type === "upload") uploadOPFS(data);
};
