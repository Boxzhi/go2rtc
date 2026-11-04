// Regression: connecting/paused video must not expose native or custom
// controls until a playable frame exists. Uses fake DOM, not a real device.
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const js=fs.readFileSync('www/luxe-camera-wall.js','utf8');
function classList(){
 const set=new Set();
 return {add(...a){a.forEach(x=>set.add(x))},remove(...a){a.forEach(x=>set.delete(x))},
   contains:x=>set.has(x),toggle(x,on){on??=!set.has(x);if(on)set.add(x);else set.delete(x);return on}};
}
class El{
 constructor(tag='div'){
  this.tag=tag;this.classList=classList();this.children=[];this.listeners=new Map();
  this.style={};this.shadowRoot={querySelector:()=>null};
  this.isConnected=true;
 }
 attachShadow(){return this.shadowRoot}
 append(...a){this.children.push(...a)}
 appendChild(x){this.children.push(x)}
 remove(){this.removed=true}
 setAttribute(){}
 addEventListener(event,fn,opts){
  const a=this.listeners.get(event)||[];a.push(fn);this.listeners.set(event,a);
 }
 removeEventListener(event,fn){
  this.listeners.set(event,(this.listeners.get(event)||[]).filter(x=>x!==fn));
 }
 querySelector(s){
  return s==='.luxe-start-layer'?
    this.children.find(e=>e.classList?.contains('luxe-start-layer')&&!e.removed)||null:null;
 }
 contains(){return false}
 matches(){return false}
 getBoundingClientRect(){return {height:100,bottom:100,top:0}}
 click(path){
  const e={stopped:false,prevented:false,composedPath:()=>path,
   preventDefault(){this.prevented=true},stopImmediatePropagation(){this.stopped=true},
   stopPropagation(){this.stopped=true}};
  for(const fn of this.listeners.get('click')||[]){fn(e);if(e.stopped)break}
  return e;
 }
}
const registry=new Map();
vm.runInNewContext(js,{
 HTMLElement:El,window:{navigator:{userAgent:'Macintosh',platform:'MacIntel',maxTouchPoints:0},customCards:[]},
 navigator:{userAgent:'Macintosh'},document:{hidden:false,createElement:t=>new El(t)},
 customElements:{get:k=>registry.get(k),define:(k,v)=>registry.set(k,v)},
 console,setTimeout:()=>1,clearTimeout(){},setInterval:()=>1,clearInterval(){},
 performance:{now:()=>1}
},{filename:'www/luxe-camera-wall.js'});
const Wall=registry.get('luxe-camera-wall');assert.ok(Wall);
const wall=new Wall(),tile=new El(),camera=new El('webrtc-camera');
wall._wall={children:[tile]};
wall._cards=[camera,null,null];
wall._config={cameras:['jinghuafu_keting_ptz_3k','jinghuafu_keting_fixed_3k','jinghuafu_canting_2k'].map(url=>({url}))};
wall._syncRecordingUI=()=>{};
wall._alignVolumeWithScreenshot=()=>{};
wall._playStates[0]='connecting';
wall._syncMediaTools(0);
wall._bindControls(0,camera,tile);
wall._setControlsVisible(0,true,true);
assert.equal(tile.classList.contains('luxe-video-ready'),false);
assert.equal(tile.classList.contains('luxe-tools-visible'),false);
assert.equal(camera.classList.contains('luxe-controls-visible'),false);
const fullscreen={classList:{contains:n=>n==='fullscreen'},matches:()=>true};
assert.equal(tile.click([fullscreen]).stopped,true,'native click must be intercepted before playable video');
const central={classList:{contains:()=>false},matches:()=>false};
assert.equal(tile.click([central]).stopped,false,'central Play/Retry must remain usable');
wall._showStartLayer(0,'connecting');
wall._sessionTokens[0]=42;
wall._scheduleCameraHealthCheck=()=>{};
wall._cameraReady(0,camera,{paused:false,readyState:3,videoWidth:1920,videoHeight:1080},42);
assert.equal(tile.classList.contains('luxe-video-ready'),true);
assert.equal(camera.classList.contains('luxe-video-ready'),true);
wall._setControlsVisible(0,true,true);
assert.equal(camera.classList.contains('luxe-controls-visible'),true);
assert.equal(tile.classList.contains('luxe-tools-visible'),true);
assert.equal(tile.click([fullscreen]).stopped,false,'native fullscreen must work after first frame');
wall._playStates[0]='connecting';
wall._showStartLayer(0,'connecting');
wall._setControlsVisible(0,true);
assert.equal(tile.classList.contains('luxe-video-ready'),false);
assert.equal(tile.classList.contains('luxe-tools-visible'),false);
assert.equal(camera.classList.contains('luxe-controls-visible'),false);
assert.equal(tile.click([fullscreen]).stopped,true,'reconnect must block controls again');
for(const rule of [
 ':host(:not(.luxe-video-ready)) .controls',
 ':host(:not(.luxe-video-ready)) .controls *',
 '.tile:not(.luxe-video-ready) .luxe-media-tools .luxe-media-tool'
])assert.ok(js.includes(rule),'missing CSS rule: '+rule);
assert.match(js,/\.luxe-start-layer\.is-connecting\s*\{[^}]*pointer-events:auto/);
console.log('PASS: connecting & reconnecting controls hidden/inert; first frame unlocks; Play/Retry unchanged');
