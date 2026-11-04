// Node-only black-box simulation of the HAOS Ingress test page.
// It deliberately does not pretend to test native iOS downloads: it verifies
// the JS does not start overlapping downloads when later cameras become ready.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const html = fs.readFileSync("www/luxe-event-driven-download-queue.html", "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, "Inline test script was not found");

function createHarness() {
  const ids = new Map();
  const nativeDownloads = [];
  const shareCalls = [];
  const cameraState = new Map([
    ["poc_camera_1", "recording"],
    ["poc_camera_2", "recording"],
    ["poc_camera_3", "recording"]
  ]);
  const sourceForID = new Map();
  const timers = [];

  function element(tag = "div") {
    return {
      tag,
      textContent: "",
      value: "",
      href: "",
      disabled: false,
      children: [],
      listeners: {},
      scrollHeight: 0,
      append(...items) { this.children.push(...items); },
      appendChild(item) { this.children.push(item); },
      replaceChildren(...items) { this.children = [...items]; },
      remove() {},
      addEventListener(name, callback) { this.listeners[name] = callback; },
      click() {
        if (this.tag === "a") nativeDownloads.push(this.href);
        else this.listeners.click?.();
      }
    };
  }
  const document = {
    hidden: false,
    body: element("body"),
    createElement: tag => element(tag),
    getElementById(id) {
      if (!ids.has(id)) ids.set(id, element());
      return ids.get(id);
    },
    addEventListener() {}
  };
  document.getElementById("key").value = "0123456789abcdef0123456789abcdef";

  const media = Buffer.alloc(2048, 0);
  media.write("ftyp", 4, "ascii");
  const nativeBlob = new Blob([media], { type: "video/mp4" });
  class MockFile {
    constructor(blobs, name, opts) {
      this.name = name; this.type = opts.type;
      this.size = blobs.reduce((n, b) => n + b.size, 0);
    }
  }
  function httpResponse(data, success = true) {
    return {
      ok: success,
      status: success ? 200 : 404,
      json: async () => data,
      blob: async () => nativeBlob
    };
  }
  async function fetchMock(href, options={}) {
    const path = new URL(href).pathname;
    if (path.endsWith("/start")) {
      const { source } = JSON.parse(options.body);
      sourceForID.set("session-" + source, source);
      return httpResponse({
        id:"session-"+source,source,filename:source+".mp4",
        state:"recording",bytes:0,video_codec:"H265",audio_codec:"OPUS"
      });
    }
    const status = path.match(/\/status\/(session-[a-z0-9_]+)$/);
    if (status) {
      const source = sourceForID.get(status[1]);
      assert.ok(source, "Unknown session ID");
      const state = cameraState.get(source);
      return httpResponse({
        id:status[1],source,filename:source+".mp4",
        state,bytes:state==="ready"?2048:0,
        video_codec:"H265",audio_codec:"OPUS",
        download_url:state==="ready"?"api/luxe/poc3/download/"+status[1]+"?token=test":undefined
      });
    }
    if (path.includes("/download/")) return httpResponse({},true);
    throw Error("Unknown URL " + path);
  }

  const navigator = {
    canShare: ({files}) => files.length===2,
    share: ({files}) => {
      shareCalls.push(files.map(f=>f.name));
      return Promise.resolve();
    }
  };
  const context = {
    document, navigator, Blob, File:MockFile, fetch:fetchMock,
    URL, Date, console, Promise, Uint8Array, String,
    location:{href:"https://homeassistant.test/api/hassio_ingress/token/luxe-event-driven-download-queue.html"},
    setInterval(fn) { timers.push(fn); return timers.length; },
    clearInterval() {},
    setTimeout() { return 1; },
    window:{addEventListener() {}}
  };
  vm.runInNewContext(script,context,{filename:"luxe-event-driven-download-queue.html"});
  const get=id=>document.getElementById(id);
  const click=id=>get(id).click();
  const ready=source=>cameraState.set(source,"ready");
  return {click,ready,get,nativeDownloads,shareCalls};
}
async function settle() {
  // Yield repeatedly so simulated fetch, status and File/Blob promises settle.
  for (let i=0;i<25;i++) await new Promise(resolve=>setImmediate(resolve));
}
async function run() {
  const h=createHarness();
  h.click("start");
  await settle();
  h.ready("poc_camera_1");
  h.click("sync"); await settle();
  if(h.nativeDownloads.length!==1) console.error("DEBUG FIRST DOWNLOAD:",h.get("log").textContent,h.get("summary").textContent);
  assert.equal(h.nativeDownloads.length,1,"first ready recording must automatically download");
  h.ready("poc_camera_2"); h.ready("poc_camera_3");
  h.click("sync"); await settle();
  assert.equal(h.nativeDownloads.length,1,"second/third must not interrupt the open native sheet");
  assert.equal(h.get("shareRest").disabled,false,"remaining two small MP4 should be shareable");
  h.click("shareRest"); await settle();
  assert.equal(h.shareCalls.length,1,"remaining two shared in one native action");
  assert.equal(h.shareCalls[0].length,2,"batch share includes exactly two MP4s");
  assert.equal(h.nativeDownloads.length,1,"batch share should not create a second WKDownload");
  assert.equal(h.get("finishShare").disabled,false,"must require explicit user confirmation");
  h.click("finishShare");await settle();
  assert.equal(h.nativeDownloads.length,1);

  const j=createHarness();
  j.click("start");await settle();
  j.ready("poc_camera_1");j.click("sync");await settle();
  j.ready("poc_camera_2");j.click("sync");await settle();
  assert.equal(j.nativeDownloads.length,1);
  j.click("confirm");await settle();
  assert.equal(j.nativeDownloads.length,2,"user confirmation may advance to second WKDownload");
  j.ready("poc_camera_3");j.click("sync");await settle();
  assert.equal(j.nativeDownloads.length,2,"third must wait while second sheet is active");
  j.click("confirm");await settle();
  assert.equal(j.nativeDownloads.length,3,"confirming second starts third exactly once");
  console.log("PASS: native sheet overlap isolation, two-file share, explicit confirmation, delayed readiness");
}
run().catch(error=>{console.error(error);process.exitCode=1;});
