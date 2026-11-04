const fs=require("node:fs");
const vm=require("node:vm");
const assert=require("node:assert/strict");
const html=fs.readFileSync("www/luxe-xiaomi-recorder-rc.html","utf8");
const script=html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script,"test HTML must embed executable JS");
const names=["jinghuafu_keting_ptz_3k","jinghuafu_keting_fixed_3k","jinghuafu_canting_2k"];

function harness(){
  let clock=Date.parse("2026-11-04T04:00:00Z");
  let nextID=0,nextTimer=0;
  const nodes=new Map(), intervals=[],timers=new Map(),native=[],shared=[],sessions=new Map(),errors=[];
  function element(tag="div"){
    return {
      tag,textContent:"",innerHTML:"",value:"",disabled:false,children:[],listeners:{},
      className:"",style:{},scrollHeight:0,scrollTop:0,
      append(...children){this.children.push(...children)},
      appendChild(c){this.children.push(c)},
      replaceChildren(...children){this.children=[...children]},
      addEventListener(type,listener){this.listeners[type]=listener},
      remove(){},
      click(){if(this.tag==="a")native.push(this.href);else this.listeners.click?.();}
    };
  }
  const document={
    hidden:false,
    body:element("body"),
    createElement(tag){return element(tag)},
    getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id)},
    addEventListener(){}
  };
  const $=id=>document.getElementById(id);
  $("secret").value="abcdefghijklmnopqrstuvwxyz012345";
  $("duration").value="10";
  class TestDate extends Date{
    constructor(...args){if(args.length)super(...args);else super(clock)}
    static now(){return clock}
  }
  class TestFile{
    constructor(blobs,name,opts){this.name=name;this.size=blobs.reduce((v,b)=>v+b.size,0);this.type=opts.type;}
  }
  const media=Buffer.alloc(2048,1);media.write("ftyp",4,"ascii");
  const blob=new Blob([media],{type:"video/mp4"});
  function response(data,code=200){
    return {ok:code>=200&&code<300,status:code,
      json:async()=>data,blob:async()=>blob};
  }
  async function fetchMock(href,options={}){
    const url=new URL(href),p=url.pathname,method=options.method||"GET";
    if(p.endsWith("/api/luxe/recordings/")&&method==="GET"){
      return response({sessions:[...sessions.values()],retention_seconds:900,max_duration_seconds:1800});
    }
    if(p.endsWith("/start")&&method==="POST"){
      const req=JSON.parse(options.body);
      const id="id"+(++nextID);
      const value={id,source:req.source,group:req.group,filename:id+".mp4",state:"recording",
        bytes:0,duration_seconds:req.duration_seconds,video_codec:"H265",audio_codec:"FLAC"};
      sessions.set(id,value);
      return response(value,202);
    }
    if(p.includes("/ack/")&&method==="POST"){
      const id=p.split("/").at(-1);sessions.delete(id);
      return {status:204,ok:true,json:async()=>null};
    }
    if(p.includes("/stop/")&&method==="POST"){
      const id=p.split("/").at(-1);
      if(sessions.has(id))sessions.get(id).state="stopping";
      return response(sessions.get(id));
    }
    if(p.includes("/file/"))return response({},200);
    errors.push("unexpected "+method+" "+p);return response({error:"Unknown"},404);
  }
  const navigator={
    canShare({files}){return files.length>=2&&files.every(x=>x.size>0);},
    share({files}){shared.push(files.map(x=>x.name));return Promise.resolve();}
  };
  const ctx={
    document,navigator,File:TestFile,Blob,Uint8Array,Date:TestDate,URL,console,Promise,String,
    confirm(){return true},
    fetch:fetchMock,
    location:{href:"https://homeassistant.test/api/hassio_ingress/token/luxe-xiaomi-recorder-rc.html",
      origin:"https://homeassistant.test"},
    setInterval(fn){intervals.push(fn);return intervals.length},
    clearInterval(){},
    setTimeout(fn,delay){const id=++nextTimer;timers.set(id,{when:clock+delay,fn});return id},
    clearTimeout(id){timers.delete(id)},
    window:{addEventListener(){}}
  };
  vm.runInNewContext(script,ctx,{filename:"luxe-xiaomi-recorder-rc.html"});
  function advance(ms){
    clock+=ms;
    for(const [id,{when,fn}] of [...timers.entries()]) if(when<=clock){timers.delete(id);fn();}
  }
  function finish(index,offset=0){
    const rec=[...sessions.values()].find(x=>x.source===names[index]);
    assert.ok(rec,"camera must be active");
    rec.state="ready";rec.ready_at=new Date(clock+offset).toISOString();
    rec.expires_at=new Date(clock+offset+15*60*1000).toISOString();
    rec.bytes=2048;
    rec.download_url="api/luxe/recordings/file/"+rec.id+"?token=test";
  }
  function renderButton(index) {
    return $("cameras").children[index]?.children[1]?.children[0];
  }
  async function poll(){await intervals[0]();}
  return {$,sessions,native,shared,errors,finish,advance,renderButton,
    startAll:()=>$("startAll").click(),poll};
}
async function settle(){
  for(let i=0;i<24;i++)await new Promise(resolve=>setImmediate(resolve));
}

async function allThreeClose(){
  const h=harness();
  h.startAll();await settle();
  // All three finalize close together, with measured server timestamps.
  h.finish(0,0);h.finish(1,500);h.finish(2,1200);
  await h.poll();await settle();
  assert.equal(h.native.length,0,"coalesced three-camera files must not open any WKDownload");
  assert.equal(h.$("batch").disabled,false,"one combined user share must be enabled");
  h.$("batch").click();await settle();
  assert.equal(h.shared.length,1,"one user gesture should open share");
  assert.equal(h.shared[0].length,3,"one share must include three independent MP4");
}

async function staggeredCompletion(){
  const h=harness();
  h.startAll();await settle();
  h.finish(0);
  await h.poll();await settle();
  h.advance(2500);await settle();
  assert.equal(h.native.length,1,"first completed file automatically invokes native WKDownload");
  h.finish(1);h.finish(2,1200);
  await h.poll();await settle();
  assert.equal(h.native.length,1,"later ready cameras must not open other native download sheets");
  assert.match(h.renderButton(1).textContent,/保存/,"second camera should show Save");
  assert.match(h.renderButton(2).textContent,/保存/,"third camera should show Save");
  assert.equal(h.$("batch").disabled,false,"remaining two can be shared after prefetch");
  h.$("batch").click();await settle();
  assert.equal(h.shared.length,1);
  assert.equal(h.shared[0].length,2,"already native-downloaded first file must be excluded");
  assert.equal(h.errors.length,0);
}
async function main(){
  await allThreeClose();
  await staggeredCompletion();
  console.log("PASS: close-completion coalesces three MP4 into one share; staggered completion triggers only one WKDownload and offers per-camera Save + remaining-two share");
}
main().catch(e=>{console.error(e);process.exitCode=1;});
