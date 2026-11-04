// Integration regression for the recording subclass loaded after the existing wall.
// This stubs only the browser/native interface, not Go server behavior.
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync('www/luxe-camera-wall-recording-rc.js','utf8');
const sources=['jinghuafu_keting_ptz_3k','jinghuafu_keting_fixed_3k','jinghuafu_canting_2k'];
function make(){
  let now=Date.parse('2026-11-04T10:00:00Z'),next=0,native=[],shares=[],calls=[],timers=new Map(),tid=0;
  const db=new Map(),elements=new Map(),registry=new Map();
  class FakeDate extends Date{static now(){return now;}}
  class FakeBase{
    constructor(){
      this._recordings=[null,null,null];
      this._playStates=['playing','playing','playing'];
      this.isConnected=true;
      this.shadowRoot={querySelector:()=>null,append:()=>{}};
      this._wall=null;
      this._config={cameras:sources.map(url=>({url}))};
    }
    setConfig(c){this._config=c}
    connectedCallback(){}
    disconnectedCallback(){return this._stopAllRecordings('navigation')}
    _syncRecordingUI(){}
    _createMediaTools(){}
    _cameraLabel(index){return sources[index]}
    _mediaFeedback(index,msg){calls.push({index,msg})}
  }
  registry.set('luxe-camera-wall',FakeBase);
  const customElements={
    get:name=>registry.get(name),
    define:(name,cls)=>registry.set(name,cls),
    whenDefined:async name=>name
  };
  function elt(tag){
    return {tag,href:'',download:'',style:{},children:[],append(...a){this.children.push(...a)},remove(){},
      click(){if(tag==='a')native.push(this.href)},addEventListener(){},setAttribute(){}};
  }
  const document={body:{append(){}},createElement:elt,cookie:''};
  const media=Buffer.alloc(2048);media.write('ftyp',4,'ascii');
  const blob=new Blob([media],{type:'video/mp4'});
  class FakeFile{
    constructor(chunks,name,opts){this.name=name;this.type=opts.type;this.size=chunks.reduce((n,v)=>n+v.size,0)}
  }
  function response(data,code=200){
    return {ok:code>=200&&code<300,status:code,
      json:async()=>data,blob:async()=>blob};
  }
  async function mockFetch(raw,opts={}){
    const url=new URL(raw),path=url.pathname,method=opts.method||'GET';
    if(path.endsWith('/start')&&method==='POST'){
      const request=JSON.parse(opts.body);
      if([...db.values()].some(v=>v.source===request.source&&v.state==='ready'))
        return response({error:'camera has pending video to save or discard'},409);
      const id='id_'+(++next);
      const s={id,source:request.source,group:request.group,filename:id+'.mp4',
        state:'recording',started_at:new Date(now).toISOString(),bytes:0,video_codec:'H265',audio_codec:'Opus'};
      db.set(id,s);return response(s,202);
    }
    if(path.includes('/ack/')&&method==='POST'){db.delete(path.split('/').at(-1));return response(null,204)}
    if(path.includes('/stop/')&&method==='POST'){
      const s=db.get(path.split('/').at(-1));s.state='stopping';return response(s);
    }
    if(path.includes('/file/'))return response({},200);
    if(path.endsWith('/api/luxe/recordings/')&&method==='GET'){
      return response({sessions:[...db.values()]});
    }
    throw Error('Unmocked API '+method+' '+path);
  }
  const navigator={canShare:({files})=>files.length>=2,
    share:({files})=>{shares.push(files.map(f=>f.name));return Promise.resolve();}};
  const location={origin:'https://ha.test',protocol:'https:',href:'https://ha.test/lovelace/test'};
  const ctx={console,customElements,document,navigator,location,File:FakeFile,Blob,URL,Uint8Array,
    Date:FakeDate,Promise,
    window:{customCards:[],confirm:()=>true},
    setInterval:()=>1,clearInterval(){},
    setTimeout(fn,delay){const id=++tid;timers.set(id,{at:now+delay,fn});return id},
    clearTimeout(id){timers.delete(id)}
  };
  ctx.fetch=mockFetch;
  vm.runInNewContext(source,ctx,{filename:'luxe-camera-wall-recording-rc.js'});
  const RC=registry.get('luxe-camera-wall-recording-rc');
  assert.ok(RC,'missing recording RC class');
  const card=new RC();
  card._rcKey='abcdefghijklmnopqrstuvwxyz012345';
  card._rcAddonSlug='luxe_xiaomi_record_rc';
  card._hass={callWS:async arg=>{
    if(arg.endpoint.includes('/info'))return {ingress_url:'/api/hassio_ingress/abc/'};
    return {session:'session123'};
  }};
  function complete(i,offset=0){
    const session=[...db.values()].find(s=>s.source===sources[i]&&s.state==='recording');
    assert.ok(session,'expected recording session');
    session.state='ready';session.bytes=2048;
    session.ready_at=new Date(now+offset).toISOString();
    session.expires_at=new Date(now+offset+900000).toISOString();
    session.download_url='api/luxe/recordings/file/'+session.id+'?token=test';
    return session;
  }
  function advance(ms){
    now+=ms;
    for(const [id,t] of [...timers.entries()]){
      if(t.at<=now){timers.delete(id);t.fn();}
    }
  }
  return {card,db,native,shares,calls,complete,advance};
}
async function settle(){for(let i=0;i<25;i++)await new Promise(resolve=>setImmediate(resolve));}
async function closeGroup(){
  const h=make();
  await Promise.all(sources.map((_,i)=>h.card._startRecording(i)));
  assert.equal(h.card._rcExpected.size,3);
  h.complete(0,0);h.complete(1,300);h.complete(2,700);
  await h.card._rcRefresh();await settle();
  h.advance(2500);await settle();
  assert.equal(h.native.length,0,'group-ready recordings should not auto-open native WKDownload');
  assert.equal(h.card._rcBatch?.files?.length,3,'must prepare 3 MP4s for one user share');
  h.card._rcShareBatch();await settle();
  assert.equal(h.shares.length,1);
  assert.equal(h.shares[0].length,3);
  assert.equal(h.card._rcRequested.size,3);
}
async function staggered(){
  const h=make();
  await Promise.all(sources.map((_,i)=>h.card._startRecording(i)));
  const first=h.complete(0);
  await h.card._rcRefresh();await settle();h.advance(2500);await settle();
  assert.equal(h.native.length,1,'first ready must native-download once');
  assert.ok(h.card._rcRequested.has(first.id));
  h.complete(1);h.complete(2,600);
  await h.card._rcRefresh();await settle();
  assert.equal(h.native.length,1,'other recordings cannot open overlapping native sheets');
  assert.equal(h.card._rcBatch?.files?.length,2,'remaining two should be batched');
  h.card._rcShareBatch();await settle();
  assert.equal(h.shares.length,1);
  assert.equal(h.shares[0].length,2);
  h.card._recordings[0].state='failed';
  await h.card._startRecording(0).catch(()=>{});
  assert.ok(h.calls.some(c=>/录像启动失败/.test(c.msg)),'failed active source should show error until ack');
  // Navigating away must not request stop from server.
  await h.card._stopAllRecordings('navigation');
  assert.ok([...h.db.values()].some(s=>s.state==='ready'));
}
async function test(){
 await closeGroup();await staggered();
 console.log('PASS: RC subclass 3-video coalescing, staggered single-native download, remaining-two share, navigation independence');
}
test().catch(e=>{console.error(e);process.exitCode=1;});
