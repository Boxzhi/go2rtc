"use strict";

const CHUNK_SIZE = 256 * 1024;
const FLUSH_INTERVAL = 1024 * 1024;
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

function validFileName(value) {
  return /^luxe-opfs-poc-[abc]-[0-9a-f-]+\.bin$/.test(value || "");
}

async function openAccess(fileName) {
  if (!self.isSecureContext || !navigator.storage || !navigator.storage.getDirectory) {
    throw new Error("当前浏览器不支持安全上下文中的 OPFS。");
  }
  if (!validFileName(fileName)) throw new Error("OPFS 测试文件名无效。");
  const root = await navigator.storage.getDirectory();
  const fileHandle = await root.getFileHandle(fileName);
  const access = await fileHandle.createSyncAccessHandle();
  return { root, fileHandle, access };
}

async function writeSample(data) {
  let access = null;
  let root = null;
  try {
    if (!self.isSecureContext || !navigator.storage || !navigator.storage.getDirectory) {
      throw new Error("当前浏览器不支持安全上下文中的 OPFS。");
    }
    if (!validFileName(data.fileName)) throw new Error("测试文件名无效。");

    const sizeMiB = Math.max(1, Math.min(64, Number(data.sizeMiB) || 16));
    const totalBytes = sizeMiB * 1024 * 1024;
    const seed = Number(data.seed) & 0xFF;
    root = await navigator.storage.getDirectory();
    const fileHandle = await root.getFileHandle(data.fileName, { create: true });
    access = await fileHandle.createSyncAccessHandle();
    access.truncate(0);

    let offset = 0;
    let crc = 0xFFFFFFFF;
    let lastFlush = 0;

    while (offset < totalBytes) {
      const length = Math.min(CHUNK_SIZE, totalBytes - offset);
      const chunk = new Uint8Array(length);
      for (let i = 0; i < length; i++) {
        const absolute = offset + i;
        chunk[i] = (absolute * 31 + (absolute >>> 8) + seed) & 0xFF;
      }

      let written = 0;
      while (written < length) {
        const count = access.write(chunk.subarray(written), { at: offset + written });
        if (!count) throw new Error("OPFS 写入返回 0 字节。");
        written += count;
      }

      crc = crcUpdate(crc, chunk);
      offset += written;

      if (offset - lastFlush >= FLUSH_INTERVAL || offset === totalBytes) {
        access.flush();
        lastFlush = offset;
      }

      self.postMessage({
        type: "progress",
        fileName: data.fileName,
        writtenBytes: offset,
        totalBytes
      });
      await new Promise(resolve => setTimeout(resolve, 0));
    }

    const actualSize = access.getSize();
    access.flush();
    access.close();
    access = null;

    if (actualSize !== totalBytes) {
      throw new Error("文件大小校验失败：" + actualSize + "/" + totalBytes);
    }

    self.postMessage({
      type: "complete",
      fileName: data.fileName,
      sizeBytes: actualSize,
      crc32: crcHex(crc)
    });
  } catch (error) {
    if (access) {
      try { access.close(); } catch (_) {}
    }
    if (root && validFileName(data.fileName)) {
      try { await root.removeEntry(data.fileName); } catch (_) {}
    }
    self.postMessage({
      type: "error",
      fileName: data.fileName || "",
      message: error && error.message ? error.message : String(error)
    });
  }
}

async function statSample(data) {
  let access = null;
  try {
    const opened = await openAccess(data.fileName);
    access = opened.access;
    const sizeBytes = access.getSize();
    let offset = 0;
    let crc = 0xFFFFFFFF;
    while (offset < sizeBytes) {
      const buffer = new Uint8Array(Math.min(CHUNK_SIZE, sizeBytes - offset));
      const count = access.read(buffer, { at: offset });
      if (!count) throw new Error("读取 OPFS 文件时提前结束。");
      crc = crcUpdate(crc, buffer.subarray(0, count));
      offset += count;
      await new Promise(resolve => setTimeout(resolve, 0));
    }
    access.close();
    access = null;
    self.postMessage({
      type: "stat-complete",
      fileName: data.fileName,
      sizeBytes,
      crc32: crcHex(crc)
    });
  } catch (error) {
    if (access) {
      try { access.close(); } catch (_) {}
    }
    self.postMessage({
      type: "stat-error",
      fileName: data.fileName || "",
      message: error && error.message ? error.message : String(error)
    });
  }
}

async function streamSample(data) {
  const port = data.port;
  let access = null;
  let closed = false;
  let offset = 0;
  let crc = 0xFFFFFFFF;

  const closeAccess = () => {
    if (closed) return;
    closed = true;
    if (access) {
      try { access.close(); } catch (_) {}
      access = null;
    }
  };

  const fail = message => {
    closeAccess();
    try { port.postMessage({ type: "error", message }); } catch (_) {}
    try { port.close(); } catch (_) {}
    self.close();
  };

  try {
    const opened = await openAccess(data.fileName);
    access = opened.access;
    const totalBytes = access.getSize();
    port.postMessage({ type: "ready", sizeBytes: totalBytes });

    port.onmessage = event => {
      const command = event.data || {};
      if (command.type === "cancel") {
        closeAccess();
        try { port.postMessage({ type: "cancelled" }); } catch (_) {}
        try { port.close(); } catch (_) {}
        self.close();
        return;
      }
      if (command.type !== "pull") return;

      try {
        if (offset >= totalBytes) {
          closeAccess();
          port.postMessage({
            type: "done",
            sizeBytes: totalBytes,
            crc32: crcHex(crc)
          });
          port.close();
          self.close();
          return;
        }

        const buffer = new Uint8Array(Math.min(CHUNK_SIZE, totalBytes - offset));
        const count = access.read(buffer, { at: offset });
        if (!count) throw new Error("OPFS 读取提前结束，offset=" + offset);

        const chunk = count === buffer.length ? buffer : buffer.slice(0, count);
        crc = crcUpdate(crc, chunk);
        offset += count;
        port.postMessage({ type: "chunk", buffer: chunk.buffer, bytes: count }, [chunk.buffer]);
      } catch (error) {
        fail(error && error.message ? error.message : String(error));
      }
    };
    port.start();
  } catch (error) {
    fail(error && error.message ? error.message : String(error));
  }
}

self.onmessage = event => {
  const data = event.data || {};
  if (data.type === "write") {
    writeSample(data);
  } else if (data.type === "stat") {
    statSample(data);
  } else if (data.type === "read") {
    streamSample(data);
  }
};
