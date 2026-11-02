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

self.onmessage = async (event) => {
  const data = event.data || {};
  if (data.type !== "write") return;

  let access = null;
  try {
    if (!self.isSecureContext || !navigator.storage || !navigator.storage.getDirectory) {
      throw new Error("当前浏览器不支持安全上下文中的 OPFS。");
    }
    if (!data.fileName || !/^luxe-opfs-poc-[abc]-[0-9a-f-]+\.bin$/.test(data.fileName)) {
      throw new Error("测试文件名无效。");
    }

    const sizeMiB = Math.max(1, Math.min(64, Number(data.sizeMiB) || 16));
    const totalBytes = sizeMiB * 1024 * 1024;
    const seed = Number(data.seed) & 0xFF;
    const root = await navigator.storage.getDirectory();
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

      const written = access.write(chunk, { at: offset });
      if (written !== length) {
        throw new Error("OPFS 短写入：" + written + "/" + length);
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
      crc32: ((crc ^ 0xFFFFFFFF) >>> 0).toString(16).padStart(8, "0")
    });
  } catch (error) {
    if (access) {
      try { access.close(); } catch (_) {}
    }
    self.postMessage({
      type: "error",
      fileName: data.fileName || "",
      message: error && error.message ? error.message : String(error)
    });
  }
};
