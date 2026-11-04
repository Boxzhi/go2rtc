// Luxe Camera Wall v1.1.2: real event sequence and timer-driven regression.
// No actual network, WebRTC connection, or camera hardware required.
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync('www/luxe-camera-wall.js','utf8');
let seq=1;
const timeouts=new Map(),intervals=new Map(),elements=new Map();
const setTimeoutFake=(fn,delay=0)=>{const id=seq++;timeouts.set(id,{fn,delay});return id};
const clearTimeoutFake=id=>timeouts.delete(id);
const setIntervalFake=(fn,delay=0)=>{const id=seq++;intervals.set(id,{fn,delay});return id};
const clearIntervalFake=id=>intervals.delete(id);
function classes(){
  const values=new Set();
  return {contains:name=>values.has(name),
    add:(...a)=>a.forEach(n=>values.add(n)),
    remove:(...a)=>a.forEach(n=>values.delete(n)),
    toggle:(n,on)=>{if(on===undefined)on=!values.has(n);if(on)values.add(n);else values.delete(n);return on}};
}
class El{
  constructor(tag='div'){
    this.tag=tag;this.children=[];this.style={};this.dataset={};
    this.isConnected=true;this.classList=classes();this.listeners=new Map();
    this.shadowRoot={querySelector:()=>null,replaceChildren(){}};
  }
  attachShadow(){return this.shadowRoot}
  addEventListener(event,fn){
    if(!this.listeners.has(event))this.listeners.set(event,new Set());
    this.listeners.get(event).add(fn);
  }
  removeEventListener(event,fn){this.listeners.get(event)?.delete(fn)}
  emit(event){for(const fn of this.listeners.get(event)||[])fn()}
  remove(){}
  append(...a){this.children.push(...a)}
  appendChild(x){this.children.push(x)}
  setAttribute(){}
  getBoundingClientRect(){return {height:100,top:0,bottom:100}}
}
class Video extends El {
  constructor(){
    super('video');this.paused=true;this.readyState=0;this.videoWidth=0;
    this.videoHeight=0;this.muted=true;this.currentTime=0;this.decoded=0;
    this.play=()=>{this.paused=false;return Promise.resolve()};
  }
  getVideoPlaybackQuality(){return {totalVideoFrames:this.decoded}}
}
const registry=new Map();
const document={hidden:false,createElement:name=>new El(name)};
vm.runInNewContext(source,{
  HTMLElement:El,window:{navigator:{userAgent:'Macintosh',platform:'MacIntel',maxTouchPoints:0},customCards:[]},
  navigator:{userAgent:'Macintosh'},document,
  customElements:{get:k=>registry.get(k),define:(k,v)=>registry.set(k,v)},
  setTimeout:setTimeoutFake,clearTimeout:clearTimeoutFake,
  setInterval:setIntervalFake,clearInterval:clearIntervalFake,
  console,performance:{now:()=>1}
},{filename:'luxe-camera-wall.js'});
const Wall=registry.get('luxe-camera-wall');
assert.ok(Wall);
const urls=['jinghuafu_keting_ptz_3k','jinghuafu_keting_fixed_3k','jinghuafu_canting_2k'];
function make(){
  const card=new Wall(),tile=new El('tile'),video=new Video();
  const camera=new El('webrtc-camera');
  camera.video=video;camera.wsURL='wss://example.invalid/api/webrtc/ws';
  camera.pc={connectionState:'connected',iceConnectionState:'connected'};
  camera.shadowRoot={querySelector:(name)=>name==='video'?video:name==='.mode'?{textContent:'RTC'}:null};
  card._cards=[camera,null,null];card._wall={children:[tile]};
  card._config={cameras:urls.map(url=>({url}))};
  card._generation=4;card._sessionTokens=[77,0,0];
  card._playStates=['connecting','idle','idle'];
  card._desiredPlaying=[true,false,false];card._pageVisible=true;
  card._decorateControls=()=>{};
  card._decoratePause=()=>{};
  card._sampleRatio=()=>{};
  card._alignVolumeWithScreenshot=()=>{};
  card._syncMediaTools=()=>{};
  card._syncRecordingUI=()=>{};
  const overlay=[];
  card._showStartLayer=(i,state)=>overlay.push(state);
  return {card,tile,camera,video,overlay};
}
function clearClock(){timeouts.clear();intervals.clear();}
function runTimeout(delay){
  const entry=[...timeouts].find(([,v])=>v.delay===delay);
  assert.ok(entry,'expected timeout '+delay);
  timeouts.delete(entry[0]);entry[1].fn();
}
function runPoll(delay=500){
  const entry=[...intervals.values()].find(v=>v.delay===delay);
  assert.ok(entry,'missing '+delay+'ms poll');
  entry.fn();
}
async function settle(){await Promise.resolve();await Promise.resolve();await Promise.resolve()}
async function tests(){
  // A native 'playing' event can occur before dimensions are populated,
  // then never fire again. The poll of the same HTMLVideoElement must settle it.
  clearClock();
  {
    const {card,camera,video}=make();
    card._scheduleCameraHealthCheck=()=>{};
    card._watchVideo(0,camera,4,77);
    assert.equal(card._playStates[0],'connecting');
    video.readyState=2;video.paused=false;
    video.videoWidth=2960;video.videoHeight=1666;
    runPoll();
    assert.equal(card._playStates[0],'playing',
      'no-event playable frame must be caught by stable video poll');
    assert.equal(card._healthStalls[0],0);
    card._unwatchVideo(0);
  }
  // Loaded-data with the first decodable frame must also count as ready.
  clearClock();
  {
    const {card,camera,video}=make();
    card._scheduleCameraHealthCheck=()=>{};
    card._watchVideo(0,camera,4,77);
    video.paused=false;video.readyState=2;video.videoWidth=1920;video.videoHeight=1080;
    video.emit('loadeddata');
    assert.equal(card._playStates[0],'playing',
      'loadeddata must call onReady, not only onDimension');
    assert.equal(video.listeners.get('canplay')?.size,1);
    card._unwatchVideo(0);
    assert.equal(video.listeners.get('canplay')?.size,0,
      'new canplay listeners must not leak after disconnect');
    assert.equal(video.listeners.get('pause')?.size,0,
      'pause listener must not leak after disconnect');
  }
  // A rejected muted autoplay is NOT a lost camera connection. Keep
  // existing RTC alive and let a user gesture resume it.
  clearClock();
  {
    const {card,camera,video,overlay}=make();
    video.readyState=3;video.videoWidth=1920;video.videoHeight=1080;
    video.play=()=>Promise.reject(Object.assign(new Error('blocked'),
      {name:'NotAllowedError'}));
    card._scheduleCameraHealthCheck=()=>{};
    card._watchVideo(0,camera,4,77);
    await settle();
    assert.equal(card._playStates[0],'gesture',
      'NotAllowedError should display the central resume button');
    assert.ok(overlay.includes('gesture'));
    assert.equal(card._cards[0],camera,'keep established RTC connection');
    assert.equal(card._connectTimers[0],null);
    video.play=()=>{video.paused=false;return Promise.resolve()};
    card._handleStartButton(0);
    await settle();
    assert.equal(card._playStates[0],'playing','user gesture should finish playback');
    assert.equal(card._cards[0],camera);
    card._unwatchVideo(0);
  }
  // Signaling errors are distinct from missing frame events; do not wait 45s.
  clearClock();
  {
    const {card,camera}=make();
    let errorText='';
    card._failCamera=(i,msg)=>{errorText=msg};
    camera.shadowRoot.querySelector=(name)=>name==='.mode'?
      {textContent:'error'}:name==='video'?camera.video:null;
    card._checkCameraSignaling(0,camera,77);
    assert.match(errorText,/信令失败/);
  }
  // Timeout stage classification is based on the current transport state,
  // not a URL or signed HA token.
  clearClock();
  {
    const {card,camera,video}=make();
    camera.wsURL='';camera.pc=null;
    assert.equal(card._connectionStage(0),'HA 授权签名');
    camera.wsURL='ws://example.invalid';camera.pc={connectionState:'checking',iceConnectionState:'checking'};
    assert.equal(card._connectionStage(0),'RTC 协商');
    camera.pc={connectionState:'connected',iceConnectionState:'connected'};
    assert.equal(card._connectionStage(0),'等待首帧');
    video.paused=true;video.readyState=2;video.videoWidth=1920;
    assert.equal(card._connectionStage(0),'浏览器播放限制');
  }
  // One non-advancing frame window must not trigger reconnection, but two
  // consecutive failed samples must still recover a stalled player.
  clearClock();
  {
    const {card,camera,video}=make();
    video.paused=false;video.readyState=4;video.videoWidth=1920;video.videoHeight=1080;
    card._playStates[0]='playing';card._videos[0]=video;
    let restarts=0;
    card._restartCameraAfterHealthFailure=()=>{restarts++};
    card._scheduleCameraHealthCheck(0);
    runTimeout(4500);
    assert.equal(restarts,0,'one stall must not destroy healthy RTC');
    assert.equal(card._healthStalls[0],1);
    runTimeout(3500);
    assert.equal(restarts,1,'persistent stall should restart');
  }
  // Real ICE transport failures remain immediate, without waiting a second
  // sample; a progressing stream resets stale streak state.
  clearClock();
  {
    const {card,camera,video}=make();
    video.paused=false;video.readyState=4;video.videoWidth=1920;video.videoHeight=1080;
    card._playStates[0]='playing';card._videos[0]=video;
    card._healthStalls[0]=1;
    let restarts=0;
    card._restartCameraAfterHealthFailure=()=>{restarts++};
    card._scheduleCameraHealthCheck(0);
    video.decoded=12;video.currentTime=1;
    runTimeout(4500);
    assert.equal(card._healthStalls[0],0,'frame progress must reset stall streak');
    assert.equal(restarts,0);
    camera.pc.connectionState='failed';
    card._scheduleCameraHealthCheck(0);
    runTimeout(4500);
    assert.equal(restarts,1,'definitive RTC failure must restart promptly');
  }
  console.log('PASS: missed first frame, loadeddata/canplay cleanup, autoplay gesture, signaling error, connection stages, health recovery');
}
tests().catch(e=>{console.error(e);process.exitCode=1});
