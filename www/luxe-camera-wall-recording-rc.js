/* Luxe Xiaomi Camera · integrated server recording RC2.
 * Standalone test subclass. Load AFTER original luxe-camera-wall.js.
 * Production <luxe-camera-wall> is not modified.
 */
(() => {
  'use strict';
  const TAG='luxe-camera-wall-recording-rc';
  const ICON_SAVE='<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v11m-4-4 4 4 4-4M4 16v4h16v-4"/></svg>';
  const SHARE_LIMIT=16*1048576, SHARE_TOTAL=32*1048576;
  const errorText=e=>String(e?.message||e).slice(0,110);
  const valid=s=>s && s.state==='ready' && Number.isFinite(Date.parse(s.expires_at)) && Date.now()<Date.parse(s.expires_at);
  const minsec=ms=>{
    const s=Math.max(0,Math.ceil(ms/1000));
    return String(Math.floor(s/60)).padStart(2,'0')+':'+String(s%60).padStart(2,'0');
  };
  const css=[
    '.tile.luxe-rc-ready .luxe-media-record { opacity:1!important; visibility:visible!important;pointer-events:auto!important;color:#a8e0ff; }',
    '.tile.luxe-rc-ready .luxe-media-snapshot { display:none; }',
    '.luxe-rc-confirm {border:0;background:rgba(20,80,99,.9);color:white;border-radius:7px;font-size:12px;min-width:44px;min-height:27px;padding:4px 6px;cursor:pointer;}',
    '.luxe-rc-confirm[hidden] {display:none!important;}',
    '.luxe-rc-ackbox {position:absolute;right:7px;top:calc(50% + 46px);z-index:12;}',
    '.luxe-rc-group {position:absolute;left:50%;top:9px;transform:translateX(-50%);z-index:30;border:0;border-radius:10px;padding:10px 14px;background:rgba(5,104,131,.96);color:white;font-weight:700;white-space:nowrap;cursor:pointer;}',
    '.luxe-rc-group[hidden] {display:none!important;}',
    '.luxe-rc-keyback {position:fixed;inset:0;background:rgba(3,10,18,.75);display:grid;place-items:center;z-index:9999;padding:16px;box-sizing:border-box;}',
    '.luxe-rc-keypanel {background:#172433;color:#f7fafe;border:1px solid #809aad;border-radius:14px;box-sizing:border-box;padding:20px;width:min(360px,100%);font:14px/1.5 -apple-system,BlinkMacSystemFont,sans-serif;}',
    '.luxe-rc-keypanel input {box-sizing:border-box;width:100%;font-size:16px;border:1px solid #a4b9cd;background:#0b1625;color:white;border-radius:8px;padding:10px;margin:10px 0;}',
    '.luxe-rc-keypanel button {background:#2768af;color:white;border:0;padding:9px 14px;border-radius:8px;margin-right:8px;}'
  ].join('\n');
  async function askKey(card){
    if(card._rcKey)return card._rcKey;
    if(card._rcKeyPending)return card._rcKeyPending;
    card._rcKeyPending=new Promise(resolve=>{
      const back=document.createElement('div');
      back.className='luxe-rc-keyback';
      const panel=document.createElement('div');
      panel.className='luxe-rc-keypanel';
      const heading=document.createElement('strong');
      heading.textContent='录像 RC 测试密钥';
      const help=document.createElement('p');
      help.textContent='请输入独立 RC Add-on 的 secret（至少 24 字符）。仅保留在本卡片内存，不写入仪表盘配置。';
      const inp=document.createElement('input');
      inp.type='password';inp.autocomplete='off';inp.placeholder='RC Add-on secret';
      const ok=document.createElement('button');ok.textContent='确认';
      const cancel=document.createElement('button');cancel.textContent='取消';
      panel.append(heading,help,inp,ok,cancel);back.append(panel);
      card.shadowRoot.append(back);
      const done=key=>{back.remove();resolve(key);};
      ok.addEventListener('click',()=>{
        const key=inp.value.trim();
        if(key.length<24){inp.setCustomValidity('密钥长度不得少于24字符');inp.reportValidity();return;}
        card._rcKey=key;done(key);
        void card._rcRefresh(true);
      });
      inp.addEventListener('keydown',e=>{if(e.key==='Enter')ok.click();});
      cancel.addEventListener('click',()=>done(''));
      inp.focus();
    }).finally(()=>{card._rcKeyPending=null;});
    return card._rcKeyPending;
  }

  function register(Base){
    if(customElements.get(TAG))return;
    class RecordingWallRC extends Base {
      constructor(){
        super();
        this._rcKey='';
        this._rcIngressPromise=null;
        this._rcPoll=null;
        this._rcBusy=[false,false,false];
        this._rcRequested=new Set();
        this._rcReadySeen=new Set();
        this._rcSessions=[null,null,null];
        this._rcGroup='';
        this._rcGroupAt=0;
        this._rcExpected=new Set();
        this._rcCoalesceTimer=null;
        this._rcAutoDone=false;
        this._rcBatch=null;
        this._rcBatchLoading=false;
        this._rcBatchFailed='';
        this._rcGeneration=0;
      }
      setConfig(config){
        super.setConfig(config);
        this._rcAddonSlug=String(config.recording_addon_slug||'luxe_xiaomi_record_rc');
        if(this.isConnected)this._rcSetup();
      }
      connectedCallback(){
        super.connectedCallback();
        this._rcSetup();
      }
      disconnectedCallback(){
        this._rcStopPolling();
        clearTimeout(this._rcCoalesceTimer);
        this._rcCoalesceTimer=null;
        this._rcGeneration++;
        // Parent calls _stopAllRecordings. This override makes it a no-op:
        // server-side recording MUST survive HA navigation and app sleep.
        super.disconnectedCallback();
      }
      async _stopAllRecordings(){return true;}
      _createMediaTools(index,tile){
        super._createMediaTools(index,tile);
        const button=tile.querySelector('.luxe-media-record');
        if(button)button._rcOriginalHTML=button.innerHTML;
        const area=document.createElement('div');area.className='luxe-rc-ackbox';
        const confirm=document.createElement('button');
        confirm.className='luxe-rc-confirm';
        confirm.textContent='已保存';confirm.hidden=true;
        confirm.addEventListener('click',event=>{
          event.preventDefault();event.stopPropagation();void this._rcConfirmSaved(index);
        });
        area.append(confirm);tile.append(area);
      }
      _rcSetup(){
        if(!this.isConnected||!this._wall)return;
        let style=this.shadowRoot.querySelector('style[data-rc-record]');
        if(!style){
          style=document.createElement('style');style.dataset.rcRecord='';
          style.textContent=css;this.shadowRoot.append(style);
        }
        let button=this.shadowRoot.querySelector('.luxe-rc-group');
        if(!button){
          button=document.createElement('button');
          button.className='luxe-rc-group';
          button.hidden=true;
          button.type='button';
          button.addEventListener('click',()=>this._rcShareBatch());
          this.shadowRoot.querySelector('ha-card')?.append(button);
        }
        this._rcShareButton=button;
        this._rcStartPolling();
        for(let i=0;i<3;i++)this._syncRecordingUI(i);
      }
      _rcStopPolling(){
        if(this._rcPoll){clearInterval(this._rcPoll);this._rcPoll=null;}
      }
      _rcStartPolling(){
        if(this._rcPoll)return;
        this._rcPoll=setInterval(()=>void this._rcRefresh(),1600);
        if(this._rcKey)void this._rcRefresh(true);
      }
      async _rcIngressBase(){
        if(this._rcIngressPromise)return this._rcIngressPromise;
        this._rcIngressPromise=(async()=>{
          const hass=this._hass;
          if(!hass?.callWS)throw Error('需要在管理员 HA 仪表盘中使用本测试卡片');
          const raw=await hass.callWS({
            type:'supervisor/api',
            endpoint:'/addons/'+this._rcAddonSlug+'/info',
            method:'get',
          });
          if(!raw?.ingress_url)throw Error('无法获取 RC Add-on Ingress 地址');
          const session=await hass.callWS({
            type:'supervisor/api',endpoint:'/ingress/session',method:'post',
          });
          if(!session?.session)throw Error('无法建立 HA Ingress 会话');
          const secure=location.protocol==='https:'?';Secure':'';
          document.cookie='ingress_session='+session.session+
            ';path=/api/hassio_ingress/;SameSite=Strict'+secure;
          const root=new URL(raw.ingress_url,location.origin);
          return location.origin+root.pathname.replace(/\/+$/,'')+'/';
        })().catch(e=>{this._rcIngressPromise=null;throw e;});
        return this._rcIngressPromise;
      }
      async _rcRequest(route,method='GET',payload){
        const key=await askKey(this);
        if(!key)throw Error('未设置录像 RC 测试密钥');
        const base=await this._rcIngressBase();
        const headers={'X-Luxe-Record-Key':key};
        if(payload!==undefined)headers['Content-Type']='application/json';
        const response=await fetch(new URL('api/luxe/recordings/'+route,base),{
          method,credentials:'same-origin',cache:'no-store',headers,
          ...(payload!==undefined?{body:JSON.stringify(payload)}:{}),
        });
        if(response.status===204)return null;
        const data=await response.json().catch(()=>({error:'服务器响应不是 JSON'}));
        if(!response.ok)throw Error(data.error||'HTTP '+response.status);
        return data;
      }
      _rcSource(index){
        return String(this._config?.cameras?.[index]?.url||'').trim();
      }
      async _startRecording(index){
        const s=this._recordings?.[index];
        if(s?.state==='ready'){
          this._rcNativeSave(index);return;
        }
        if(s||this._rcBusy[index])return;
        const source=this._rcSource(index);
        if(!/^[A-Za-z0-9_-]{1,100}$/.test(source)){
          this._mediaFeedback(index,'录像源必须是 go2rtc 流名称');return;
        }
        this._rcBusy[index]=true;
        this._recordings[index]={state:'starting',startedAt:Date.now()};
        this._syncRecordingUI(index);
        try{
          const now=Date.now();
          if(!this._rcGroup || now-this._rcGroupAt>8000 ||
              (this._rcAutoDone && this._rcExpected.size>0)){
            this._rcGroup='g_'+now.toString(36)+'_'+Math.random().toString(36).slice(2,7);
            this._rcGroupAt=now;
            this._rcExpected=new Set();
            this._rcAutoDone=false;
            this._rcReadySeen.clear();
          }
          const result=await this._rcRequest('start','POST',{
            source,group:this._rcGroup,duration_seconds:1800,
          });
          this._rcExpected.add(source);
          this._recordings[index]=this._rcMapState(result);
          this._mediaFeedback(index,'服务器已开始录像');
          void this._rcRefresh();
        }catch(e){
          this._recordings[index]=null;
          this._mediaFeedback(index,'录像启动失败：'+errorText(e));
        }finally{
          this._rcBusy[index]=false;this._syncRecordingUI(index);
        }
      }
      async _stopRecording(index){
        const state=this._recordings?.[index];
        if(!state||state.state!=='recording'||this._rcBusy[index])return false;
        this._rcBusy[index]=true;
        this._recordings[index]={...state,state:'stopping'};
        this._syncRecordingUI(index);
        try{
          await this._rcRequest('stop/'+encodeURIComponent(state.id),'POST');
          this._mediaFeedback(index,'服务端正在封装 MP4');
          await this._rcRefresh();
          return true;
        }catch(e){
          this._mediaFeedback(index,'停止失败：'+errorText(e));
          return false;
        }finally{
          this._rcBusy[index]=false;
          void this._rcRefresh();
        }
      }
      _rcMapState(s){
        if(!s)return null;
        return {
          ...s,
          startedAt:Date.parse(s.started_at)||Date.now(),
          expiresAt:Date.parse(s.expires_at)||0,
          downloadRequested:this._rcRequested.has(s.id),
        };
      }
      async _rcRefresh(recovery=false){
        if(!this.isConnected||!this._rcKey||this._rcRefreshing)return;
        this._rcRefreshing=true;
        const generation=this._rcGeneration;
        try{
          const response=await this._rcRequest('');
          if(generation!==this._rcGeneration||!this.isConnected)return;
          const sessions=response.sessions||[];
          const newly=[];
          for(let i=0;i<3;i++){
            if(this._rcBusy[i])continue;
            const source=this._rcSource(i);
            const records=sessions.filter(s=>s.source===source);
            records.sort((a,b)=>Date.parse(b.started_at)-Date.parse(a.started_at));
            const selected=records[0]||null;
            if(selected?.state==='ready'&&!this._rcReadySeen.has(selected.id)){
              newly.push(selected);
              this._rcReadySeen.add(selected.id);
            }
            this._recordings[i]=this._rcMapState(selected);
            this._syncRecordingUI(i);
          }
          if(recovery){
            this._rcAutoDone=true; // Never auto-download old files on restore.
          }else if(newly.length){
            this._rcOnReady(newly);
          }
          void this._rcPrepareBatch(sessions);
          this._rcShowBatch();
        }catch(e){
          // Keep last known state rather than reset recording buttons on HTTP failure.
          if(!this._rcErrorAt || Date.now()-this._rcErrorAt>20000){
            this._rcErrorAt=Date.now();
            console.warn('Luxe recording RC status sync:',errorText(e));
          }
        }finally{this._rcRefreshing=false;}
      }
      _rcOnReady(newly){
        if(this._rcAutoDone||!this._rcGroup)return;
        const candidates=this._recordings.filter(s=>s?.state==='ready'&&s.group===this._rcGroup);
        if(!candidates.length)return;
        if(this._rcCoalesceTimer)return;
        // 2s merge window on server ready timestamp; timer is never used
        // to infer native sheet closure.
        const earliest=Math.min(...candidates.map(s=>Date.parse(s.ready_at)));
        const wait=Math.max(0,2000-(Date.now()-earliest));
        this._rcCoalesceTimer=setTimeout(()=>{
          this._rcCoalesceTimer=null;
          if(this._rcAutoDone||!this.isConnected)return;
          const ready=this._recordings.filter(s=>valid(s)&&s.group===this._rcGroup);
          if(!ready.length)return;
          const times=ready.map(s=>Date.parse(s.ready_at));
          const allExpected=this._rcExpected.size>=2&&
            [...this._rcExpected].every(source=>ready.some(s=>s.source===source));
          const close=allExpected && Math.max(...times)-Math.min(...times)<=2000;
          this._rcAutoDone=true;
          if(close&&ready.every(s=>s.bytes<=SHARE_LIMIT)){
            this._mediaFeedback(0,'多路录像已完成，请点击「保存多份」');
            void this._rcPrepareBatch();
          }else{
            const first=[...ready].sort((a,b)=>Date.parse(a.ready_at)-Date.parse(b.ready_at))[0];
            const i=this._config.cameras.findIndex(c=>c.url===first.source);
            if(i>=0)this._rcNativeSave(i);
          }
        },wait);
      }
      async _rcNativeSave(index){
        const s=this._recordings[index];
        if(!valid(s)){this._mediaFeedback(index,'录像已过期，不能下载');void this._rcRefresh();return;}
        try{
          const base=await this._rcIngressBase();
          const u=new URL(s.download_url,base);
          if(u.origin!==location.origin)throw Error('下载链接非同源');
          const a=document.createElement('a');
          a.href=u.href;a.download=s.filename;a.style.display='none';
          document.body.append(a);a.click();a.remove();
          this._rcRequested.add(s.id);
          s.downloadRequested=true;
          this._syncRecordingUI(index);
          this._mediaFeedback(index,'已请求下载；保存后请点「已保存」');
        }catch(e){this._mediaFeedback(index,'下载失败：'+errorText(e));}
      }
      async _rcConfirmSaved(index){
        const s=this._recordings?.[index];
        if(!valid(s)||!this._rcRequested.has(s.id))return;
        if(!window.confirm('确认该录像已保存到 iPhone「文件」？若取消下载，请选择取消并重新保存。'))return;
        this._rcBusy[index]=true;
        try{
          await this._rcRequest('ack/'+encodeURIComponent(s.id),'POST',{action:'saved'});
          this._rcRequested.delete(s.id);
          this._recordings[index]=null;
          this._syncRecordingUI(index);
          this._mediaFeedback(index,'已确认保存，恢复录制按钮');
          void this._rcRefresh();
        }catch(e){this._mediaFeedback(index,'确认失败：'+errorText(e));}
        finally{this._rcBusy[index]=false;}
      }
      _syncRecordingUI(index){
        super._syncRecordingUI(index);
        const tile=this._wall?.children[index];
        if(!tile)return;
        const s=this._recordings?.[index];
        const button=tile.querySelector('.luxe-media-record');
        const status=tile.querySelector('.luxe-record-status');
        const ack=tile.querySelector('.luxe-rc-confirm');
        const ready=valid(s);
        tile.classList.toggle('luxe-rc-ready',ready);
        if(button){
          if(!button._rcOriginalHTML)button._rcOriginalHTML=button.innerHTML;
          button.innerHTML=ready?ICON_SAVE:button._rcOriginalHTML;
          button.title=ready?'保存录像（剩余 '+minsec(s.expiresAt-Date.now())+'）':
            s?.state==='recording'?'停止录像':'开始录像';
          button.setAttribute('aria-label',button.title+' '+this._cameraLabel(index));
        }
        if(ack)ack.hidden=!(ready&&this._rcRequested.has(s.id));
        if(status){
          if(ready){
            status.hidden=false;
            const label=status.querySelector('.record-status-label');
            const dot=status.querySelector('.record-dot');
            const spinner=status.querySelector('.record-status-spinner');
            const timer=status.querySelector('.record-timer');
            if(label){label.hidden=false;label.textContent='待保存 '+minsec(s.expiresAt-Date.now());}
            if(dot)dot.hidden=true;
            if(spinner)spinner.hidden=true;
            if(timer)timer.hidden=true;
          }else if(s?.state==='recording'){
            const timer=status.querySelector('.record-timer');
            if(timer)timer.textContent=minsec(Date.now()-s.startedAt);
          }else if(s?.state==='finalizing'){
            status.hidden=false;
            const label=status.querySelector('.record-status-label');
            if(label){label.hidden=false;label.textContent='正在封装';}
          }
        }
        if(ready){
          // Preserve screenshot and live playback state. Only the record state
          // styling changes to show a persistent clickable Save button.
          tile.classList.remove('luxe-recording');
          this._cards?.[index]?.classList.remove('luxe-recording');
        }
      }
      async _rcPrepareBatch(){
        if(this._rcBatchLoading||!this.isConnected)return;
        const ready=this._recordings.filter(s=>valid(s)&&!this._rcRequested.has(s.id));
        const group=ready.filter(s=>s.group===this._rcGroup);
        const selected=group.length>=2?group:ready;
        if(selected.length<2||selected.some(s=>s.bytes<1024||s.bytes>SHARE_LIMIT)||
          selected.reduce((t,s)=>t+s.bytes,0)>SHARE_TOTAL){
          this._rcBatch=null;this._rcShowBatch();return;
        }
        const signature=selected.map(s=>s.id).sort().join('|');
        if(this._rcBatch?.signature===signature||this._rcBatchFailed===signature)return;
        this._rcBatchLoading=true;
        try{
          const base=await this._rcIngressBase();
          const files=[];
          for(const s of selected){
            const u=new URL(s.download_url,base);
            if(u.origin!==location.origin||!valid(s))throw Error('下载 URL 已失效');
            const response=await fetch(u.href,{cache:'no-store',credentials:'same-origin'});
            if(!response.ok)throw Error('HTTP '+response.status);
            const blob=await response.blob();
            const hdr=new Uint8Array(await blob.slice(4,8).arrayBuffer());
            if(blob.size!==s.bytes||blob.size>SHARE_LIMIT||
               String.fromCharCode(...hdr)!=='ftyp')throw Error('MP4 数据校验失败');
            files.push(new File([blob],s.filename,{type:'video/mp4'}));
          }
          if(!navigator.canShare?.({files})||!navigator.share)throw Error('HA App 不支持多文件分享');
          const latest=this._recordings.filter(s=>valid(s)&&!this._rcRequested.has(s.id));
          const actual=(latest.filter(s=>s.group===this._rcGroup).length>=2?
            latest.filter(s=>s.group===this._rcGroup):latest).map(s=>s.id).sort().join('|');
          if(actual!==signature)return;
          this._rcBatch={signature,files,ids:selected.map(s=>s.id)};
        }catch(e){
          this._rcBatchFailed=signature;
          console.warn('Luxe RC batch share (individual download remains):',errorText(e));
        }finally{this._rcBatchLoading=false;this._rcShowBatch();}
      }
      _rcShowBatch(){
        const btn=this._rcShareButton;
        if(!btn)return;
        const batch=this._rcBatch;
        const usable=!!(batch&&batch.ids.every(id=>this._recordings.some(s=>valid(s)&&s.id===id&&!this._rcRequested.has(id))));
        btn.hidden=!usable;
        if(usable)btn.textContent='保存 '+batch.files.length+' 份录像';
      }
      _rcShareBatch(){
        const batch=this._rcBatch;
        if(!batch)return;
        // Must be directly triggered by click to retain Web Share user gesture.
        try{
          const promise=navigator.share({files:batch.files});
          for(const id of batch.ids)this._rcRequested.add(id);
          for(let i=0;i<3;i++)this._syncRecordingUI(i);
          Promise.resolve(promise).then(
            ()=>this._mediaFeedback(0,'分享面板已返回，请分别确认已保存'),
            e=>{
              for(const id of batch.ids)this._rcRequested.delete(id);
              for(let i=0;i<3;i++)this._syncRecordingUI(i);
              console.warn('Luxe RC share cancelled:',errorText(e));
            }
          );
        }catch(e){this._mediaFeedback(0,'打开分享失败：'+errorText(e));}
      }
    }
    customElements.define(TAG,RecordingWallRC);
    window.customCards=window.customCards||[];
    window.customCards.push({type:TAG,name:'Luxe Xiaomi Camera · 录像 RC',description:'继承正式监控墙，仅替换测试服务端录像与15分钟缓存',preview:false});
  }
  const original=customElements.get('luxe-camera-wall');
  if(original)register(original);
  else customElements.whenDefined('luxe-camera-wall').then(()=>register(customElements.get('luxe-camera-wall')));
})();
