// Integration regression for the recording subclass loaded after the existing wall.
// This stubs only the browser/native interface, not Go server behavior.
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync('www/luxe-camera-wall-recording-rc.js','utf8');
const sources=['jinghuafu_keting_ptz_3k','jinghuafu_keting_fixed_3k','jinghuafu_canting_2k'];
function make(configuredSlug,ingressUrl='/api/hassio_ingress/abc/luxe-xiaomi-recorder-rc.html'){
  let now=Date.parse('2026-11-04T10:00:00Z'),next=0,native=[],shares=[],calls=[],timers=new Map(),tid=0;
  const db=new Map(),elements=new Map(),registry=new Map();
  const documentListeners=new Map(),windowListeners=new Map();
  let heartbeats=0,aborts=0;
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
  const document={
    body:{append(){}},createElement:elt,cookie:'',hidden:false,
    addEventListener:(event,fn)=>documentListeners.set(event,fn),
    removeEventListener:(event)=>documentListeners.delete(event),
  };
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
    assert.ok(path.startsWith('/api/hassio_ingress/abc/api/luxe/recordings/'),
      'recording request must use true Ingress mount, not HTML ingress_entry: '+path);
    if(path.endsWith('/start')&&method==='POST'){
      const request=JSON.parse(opts.body);
      if([...db.values()].some(v=>v.source===request.source&&v.state==='ready'))
        return response({error:'camera has pending video to save or discard'},409);
      // A new recording discards a previously presented temporary MP4.
      for(const [id,v] of db){
        if(v.source===request.source&&v.state==='presented')db.delete(id);
      }
      const id='id_'+(++next);
      const s={id,source:request.source,group:request.group,filename:id+'.mp4',
        state:'recording',started_at:new Date(now).toISOString(),bytes:0,video_codec:'H265',audio_codec:'Opus'};
      db.set(id,s);return response(s,202);
    }
    if(path.includes('/ack/')&&method==='POST'){
      const id=path.split('/').at(-1),action=JSON.parse(opts.body).action;
      if(action==='presented'){
        const rec=db.get(id);
        if(!rec||rec.state!=='ready')return response({error:'not ready'},409);
        rec.state='presented';
      }else db.delete(id);
      return response(null,204);
    }
    if(path.includes('/heartbeat/')&&method==='POST'){
      heartbeats++;
      return response(null,204);
    }
    if(path.includes('/abort/')&&method==='POST'){
      aborts++;
      db.delete(path.split('/').at(-1));
      return response(null,204);
    }
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
    window:{
      customCards:[],confirm:()=>true,
      addEventListener:(event,fn)=>windowListeners.set(event,fn),
      removeEventListener:(event)=>windowListeners.delete(event),
    },
    setInterval:()=>1,clearInterval(){},
    setTimeout(fn,delay){const id=++tid;timers.set(id,{at:now+delay,fn});return id},
    clearTimeout(id){timers.delete(id)}
  };
  ctx.fetch=mockFetch;
  vm.runInNewContext(source,ctx,{filename:'luxe-camera-wall-recording-rc.js'});
  const RC=registry.get('luxe-camera-wall-recording-rc');
  assert.ok(RC,'missing recording RC class');
  const card=new RC();
  card.setConfig({cameras:sources.map(url=>({url})),
    ...(configuredSlug===undefined?{}:{recording_addon_slug:configuredSlug})});
  card._rcKey='abcdefghijklmnopqrstuvwxyz012345';
  card._hass={callWS:async arg=>{
    if(arg.endpoint.includes('/info')){
      assert.equal(arg.endpoint,
        '/addons/local_luxe_xiaomi_record_rc/info',
        'Supervisor requires full local addon slug');
      return {ingress_url:ingressUrl};
    }
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
  return {
    card,db,native,shares,calls,complete,advance,
    background(){
      document.hidden=true;
      const fn=documentListeners.get('visibilitychange');
      assert.ok(fn,'visibility handler must be registered');
      fn();
    },
    foreground(){document.hidden=false;documentListeners.get('visibilitychange')?.()},
    get aborts(){return aborts},
    get heartbeats(){return heartbeats},
  };
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
  assert.ok(h.card._recordings.every(s=>s===null),
    'all record buttons must immediately reset when one batch share opens');
  assert.ok([...h.db.values()].every(s=>s.state==='presented'),
    'batch sharing must retain server MP4 in presented state');
}
async function staggered(){
  const h=make();
  await Promise.all(sources.map((_,i)=>h.card._startRecording(i)));
  const first=h.complete(0);
  await h.card._rcRefresh();await settle();h.advance(2500);await settle();
  assert.equal(h.native.length,1,'first ready must native-download once');
  assert.ok(h.card._rcRequested.has(first.id));
  assert.equal(h.card._recordings[0],null,'native download immediately resets first record icon');
  assert.equal(h.db.get(first.id)?.state,'presented','a native download must NOT delete MP4 immediately');
  h.complete(1);h.complete(2,600);
  await h.card._rcRefresh();await settle();
  assert.equal(h.native.length,1,'other recordings cannot open overlapping native sheets');
  assert.equal(h.card._rcBatch?.files?.length,2,'remaining two should be batched');
  h.card._rcShareBatch();await settle();
  assert.equal(h.shares.length,1);
  assert.equal(h.shares[0].length,2);
  assert.equal(h.card._recordings[1],null);
  assert.equal(h.card._recordings[2],null);
  assert.ok([...h.db.values()].every(s=>s.state==='presented'));
  // User starts another recording on the same camera: delete its previous
  // presented MP4 immediately, but retain the other two cameras' caches.
  await h.card._startRecording(0);
  assert.equal(h.db.has(first.id),false,'starting same camera must purge old MP4');
  assert.ok(h.card._recordings[0]?.state==='recording');
  assert.equal([...h.db.values()].filter(s=>s.state==='presented').length,2);
  // Navigating away must not request stop from server.
  await h.card._stopAllRecordings('navigation');
  assert.ok([...h.db.values()].some(s=>s.state==='presented'),
    'navigation cannot delete presented MP4s; backend retains them until TTL or same-camera restart');
}
async function explicitNewRecordingCanAutoDownload(){
  const h=make();
  await h.card._startRecording(0);
  h.complete(0);
  await h.card._rcRefresh();await settle();
  h.advance(2500);await settle();
  assert.equal(h.native.length,1,'first recording should start native WKDownload');
  h.advance(9000);
  await h.card._startRecording(1);
  h.complete(1);
  await h.card._rcRefresh();await settle();
  h.advance(2600);await settle();
  assert.equal(h.native.length,2,
    'after explicitly beginning a new recording group, its first completed file may auto-download');
  assert.equal(h.card._recordings[1],null,
    'after the second native download, record icon must reset automatically');
}
async function noReadyBadgeAndNoConfirmationUI(){
  const h=make();
  const recordingButton={innerHTML:'<svg>record</svg>',title:'',_rcOriginalHTML:'<svg>record</svg>',
    setAttribute(name,value){this[name]=value}};
  const status={hidden:false,style:{},querySelector(selector){
    if(selector==='.record-status-label')return {hidden:false,textContent:''};
    return null;
  }};
  const tile={
    querySelector(selector){
      if(selector==='.luxe-media-record')return recordingButton;
      if(selector==='.luxe-record-status')return status;
      if(selector==='.luxe-rc-confirm')throw Error('confirmation control must not be queried');
      return null;
    },
    classList:{toggle(){},remove(){}}
  };
  h.card._wall={children:[tile]};
  const s={
    id:'pending_video',source:sources[0],state:'ready',
    expires_at:new Date('2026-11-04T10:14:00Z').toISOString(),
    expiresAt:Date.parse('2026-11-04T10:14:00Z'),
    filename:'pending.mp4'
  };
  h.card._recordings[0]=s;
  h.card._syncRecordingUI(0);
  assert.equal(status.hidden,true,'ready state must not show top-right countdown');
  assert.match(recordingButton.innerHTML,/svg/,'ready state shows save icon');
  assert.doesNotMatch(recordingButton.title,/剩余|14:/,'button tooltip must not show TTL');
  h.card._recordings[0]=null;
  h.card._syncRecordingUI(0);
  assert.equal(recordingButton.innerHTML,'<svg>record</svg>',
    'after native download requested, icon immediately restores original record SVG');
  assert.equal(status.hidden,true,'idle must not display stale ready status');
}

async function backgroundDiscardsRecordingImmediately(){
  const h=make();
  h.card.connectedCallback();
  await h.card._startRecording(0);
  assert.equal(h.card._recordings[0].state,'recording');
  assert.ok(h.heartbeats>=1,'start must immediately renew its foreground lease');
  const id=h.card._recordings[0].id;
  h.background();
  await settle();
  assert.equal(h.card._recordings[0],null,'record icon must restore synchronously');
  assert.equal(h.aborts,1,'background should send exactly one abort request');
  assert.equal(h.db.has(id),false,'background abort must not produce a ready MP4');
  assert.equal(h.native.length,0,'background must never open a download sheet');
  assert.ok(h.calls.some(v=>v.msg==='录像已取消'));
  h.foreground();
  await h.card._rcRefresh();
  assert.equal(h.card._recordings[0],null,'refresh must not resurrect cancelled REC');
  await h.card._startRecording(0);
  assert.equal(h.card._recordings[0].state,'recording','subsequent foreground recording must work');
  h.card.disconnectedCallback();
}
async function backgroundDoesNotDiscardCompletedVideo(){
  const h=make();
  h.card.connectedCallback();
  await h.card._startRecording(0);
  const ready=h.complete(0);
  await h.card._rcRefresh();await settle();
  h.background();await settle();
  assert.equal(h.aborts,0,'already ready MP4 must survive background');
  assert.equal(h.db.get(ready.id).state,'ready');
}
async function localAddonSlugRegression(){
  const variants=[
    '/api/hassio_ingress/abc/luxe-xiaomi-recorder-rc.html',
    '/api/hassio_ingress/abc/',
    'https://ha.test/api/hassio_ingress/abc/luxe-xiaomi-recorder-rc.html'
  ];
  for (const configuredSlug of [undefined,'luxe_xiaomi_record_rc','local_luxe_xiaomi_record_rc']){
    for(const ingressUrl of variants){
      const h=make(configuredSlug,ingressUrl);
      assert.equal(h.card._rcAddonSlug,'local_luxe_xiaomi_record_rc');
      assert.equal(await h.card._rcIngressBase(),
        'https://ha.test/api/hassio_ingress/abc/',
        'must remove the HTML ingress_entry when building recorder API base');
      await h.card._startRecording(0);
      assert.equal(h.card._recordings[0].state,'recording');
    }
  }
}
async function test(){
 await localAddonSlugRegression();await backgroundDiscardsRecordingImmediately();await backgroundDoesNotDiscardCompletedVideo();await noReadyBadgeAndNoConfirmationUI();await closeGroup();await staggered();await explicitNewRecordingCanAutoDownload();
 console.log('PASS: RC subclass 3-video coalescing, record icon reset on native/share opening, new recording purges old MP4, single native popup per group, explicit new recording permits new download, remaining-two share, navigation independence, full local Supervisor slug, background discard and fresh foreground recovery');
}
test().catch(e=>{console.error(e);process.exitCode=1;});
