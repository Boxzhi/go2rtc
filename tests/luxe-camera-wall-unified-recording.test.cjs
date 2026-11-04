// Production integration regression: exactly one card JS, real LuxeCameraWall
// constructor + server recorder, mocked HA Supervisor ingress and recording API.
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync('www/luxe-camera-wall.js','utf8');
const sources=['jinghuafu_keting_ptz_3k','jinghuafu_keting_fixed_3k','jinghuafu_canting_2k'];
const registry=new Map(),requests=[],saved=[];
const sessions=new Map();let idSeq=0;
function classes(){const values=new Set();return{
  contains:n=>values.has(n),add:(...names)=>names.forEach(v=>values.add(v)),
  remove:(...names)=>names.forEach(v=>values.delete(v)),
  toggle:(name,enabled)=>{if(enabled)values.add(name);else values.delete(name)}
};}
class FakeElement {
  constructor(tag='div'){
    this.isConnected=false;this._tag=tag;this.children=[];
    this.dataset={};this.style={};this.classList=classes();
    this.shadowRoot={querySelector:()=>null,append(){}};
  }
  attachShadow(){return this.shadowRoot}
  remove(){}
  setAttribute(){}
  addEventListener(){}
  removeEventListener(){}
  append(...a){this.children.push(...a)}
  click(){if(this._tag==='a')saved.push(this.href)}
}
const document={
  hidden:false,cookie:'',body:{append(){}},
  createElement:tag=>new FakeElement(tag),
  addEventListener(){},removeEventListener(){}
};
const location={origin:'https://ha.example',protocol:'https:',href:'https://ha.example/lovelace/home'};
function response(data,code=200){return {
  status:code,ok:code>=200&&code<300,
  json:async()=>data,
  headers:{get:()=> 'application/json'}
};}
async function fetchMock(href,opts={}){
  const path=new URL(href).pathname,method=opts.method||'GET';
  requests.push({path,method,headers:opts.headers||{},body:opts.body});
  assert.ok(path.startsWith('/api/hassio_ingress/official/api/luxe/recordings/'),
    'must route through production HA ingress: '+path);
  if(path.endsWith('/start')&&method==='POST'){
    const body=JSON.parse(opts.body);
    const id='session'+(++idSeq);
    for(const [oldId,x] of sessions)
      if(x.source===body.source&&x.state==='presented')sessions.delete(oldId);
    const value={id,source:body.source,group:body.group,state:'recording',
      started_at:new Date().toISOString(),filename:'sample'+id+'.mp4'};
    sessions.set(id,value);return response(value,202);
  }
  if(path.includes('/heartbeat/'))return response(null,204);
  if(path.includes('/abort/')){
    sessions.delete(path.split('/').at(-1));return response(null,204);
  }
  if(path.includes('/stop/')){
    const rec=sessions.get(path.split('/').at(-1));rec.state='stopping';
    return response(rec);
  }
  if(path.includes('/ack/')&&method==='POST'){
    const rec=sessions.get(path.split('/').at(-1));
    if(JSON.parse(opts.body).action==='presented')rec.state='presented';
    else sessions.delete(rec.id);
    return response(null,204);
  }
  if(path.endsWith('/api/luxe/recordings/')&&method==='GET')
    return response({sessions:[...sessions.values()]});
  throw Error('Unexpected '+method+' '+path);
}
const window={navigator:{platform:'MacIntel',userAgent:'Macintosh',maxTouchPoints:0},
  customCards:[],addEventListener(){},removeEventListener(){}};
vm.runInNewContext(source,{
  HTMLElement:FakeElement,document,window,navigator:window.navigator,location,
  customElements:{get:name=>registry.get(name),define:(name,klass)=>{
    assert.equal(name,'luxe-camera-wall','must have just one unified custom card');
    assert.ok(!registry.has(name),'duplicate custom card definition');
    registry.set(name,klass);
  }},
  fetch:fetchMock,URL,Blob,File:class{},Uint8Array,console,
  setTimeout:()=>1,clearTimeout(){},setInterval:()=>1,clearInterval(){},
  performance:{now:()=>1}
},{filename:'luxe-camera-wall.js'});
const Wall=registry.get('luxe-camera-wall');
assert.ok(Wall);
assert.equal(window.customCards.length,1,'single card metadata required');
assert.equal(typeof Wall.calculateLayout,'function');
assert.equal(typeof Wall.prototype._rcRefresh,'function','recorder methods should be intrinsic');
assert.equal(typeof Wall.prototype._rcAbortOnBackground,'function');
assert.doesNotMatch(source,/customElements\.define\(['"]luxe-camera-wall-recording-rc/);

async function main(){
  const card=new Wall();
  card.setConfig({cameras:sources.map(url=>({url}))});
  assert.equal(card._rcAddonSlug,'fee2ccf4_luxe_go2rtc');
  card._rcKey='long-production-secret-0123456789-abcdef';
  card._hass={callWS:async arg=>{
    if(arg.endpoint.endsWith('/info')){
      assert.equal(arg.endpoint,'/addons/fee2ccf4_luxe_go2rtc/info');
      return {ingress_url:'/api/hassio_ingress/official/index.html'};
    }
    return {session:'test-ingress-session'};
  }};
  card.isConnected=true;
  card._mediaFeedback=()=>{};
  await card._startRecording(0);
  const first=card._recordings[0];
  assert.equal(first.state,'recording');
  assert.ok(requests.some(r=>r.path.endsWith('/heartbeat/'+first.id)));
  card._rcAbortOnBackground('visibilitychange');
  assert.equal(card._recordings[0],null,'background immediately restores record icon');
  await new Promise(resolve=>setImmediate(resolve));
  assert.ok(!sessions.has(first.id),'background abort removes server recording');

  await card._startRecording(0);
  const s=card._recordings[0];
  assert.equal(s.state,'recording');
  s.state='ready';
  s.bytes=2048;s.filename='sample.mp4';
  s.ready_at=new Date().toISOString();
  s.expires_at=new Date(Date.now()+900000).toISOString();
  s.expiresAt=Date.parse(s.expires_at);
  s.download_url='api/luxe/recordings/file/'+s.id+'?token=hidden';
  sessions.set(s.id,{...s,state:'ready'});
  await card._rcNativeSave(0);
  assert.equal(saved.length,1,'native download invoked once');
  assert.equal(card._recordings[0],null,'record icon reset on file sheet invocation');
  assert.equal(sessions.get(s.id).state,'presented','video retained until TTL or next record');
  await card._startRecording(0);
  assert.equal(sessions.has(s.id),false,'new same-camera recording drops previously presented MP4');
  assert.equal(card._recordings[0].state,'recording');
  assert.ok(requests.some(r=>r.path.endsWith('/start')));
  console.log('PASS single-file official add-on ingress, recording, background-abort, download, presented cache, new recording');
}
main().catch(e=>{console.error(e);process.exitCode=1;});
