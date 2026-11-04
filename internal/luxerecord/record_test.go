package luxerecord

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*manager,*session) {
	t.Helper()
	dir:=t.TempDir()
	m,err:=newManager(dir)
	if err!=nil{t.Fatal(err)}
	id:="testsession_1234567890123"
	now:=time.Now().UTC()
	s:=&session{
		ID:id,Source:"jinghuafu_canting_2k",Group:"group1",
		Token:"token_123456789012345678901234",
		Filename:"luxe_jinghuafu_canting_2k_20261104_110000_test.mp4",
		State:"ready",Started:now.Add(-10*time.Second),
		ReadyAt:now,ExpiresAt:now.Add(retention),
		Duration:10*time.Second,VideoCodec:"H265",AudioCodec:"FLAC",
	}
	data:=make([]byte,4096)
	copy(data[4:],"ftyp")
	if err:=os.WriteFile(m.filePath(id),data,0600);err!=nil{t.Fatal(err)}
	s.Bytes=int64(len(data))
	m.sessions[id]=s
	if err:=m.saveManifestLocked(s);err!=nil{t.Fatal(err)}
	return m,s
}
func reqJSON(method,target string,body any)*http.Request{
	var payload bytes.Buffer
	if body!=nil {_=json.NewEncoder(&payload).Encode(body)}
	r:=httptest.NewRequest(method,target,&payload)
	r.RemoteAddr="172.30.32.2:4242"
	return r
}

func TestRecordingRetentionStartsAtReady(t *testing.T) {
	m,s:=fixture(t)
	if got:=s.ExpiresAt.Sub(s.ReadyAt);got!=15*time.Minute{
		t.Fatalf("TTL=%v",got)
	}
	m.pruneLocked(s.ExpiresAt.Add(-time.Nanosecond))
	if m.sessions[s.ID]==nil{t.Fatal("removed before its own expiry")}
	m.pruneLocked(s.ExpiresAt)
	if m.sessions[s.ID]!=nil{t.Fatal("not revoked at expiry")}
	if _,err:=os.Stat(m.filePath(s.ID));!os.IsNotExist(err){t.Fatalf("video still on disk: %v",err)}
	if _,err:=os.Stat(m.metaPath(s.ID));!os.IsNotExist(err){t.Fatalf("metadata still on disk: %v",err)}
}

func TestRecordingRestoresValidMP4AcrossRestart(t *testing.T) {
	m,s:=fixture(t)
	_ = os.WriteFile(filepath.Join(m.dir,"broken.mp4.part"),[]byte("partial"),0600)
	next,err:=newManager(m.dir)
	if err!=nil{t.Fatal(err)}
	got:=next.sessions[s.ID]
	if got==nil||got.State!="ready"||got.ExpiresAt!=s.ExpiresAt ||
		got.Bytes!=s.Bytes||got.Token!=s.Token{
		t.Fatalf("restored session mismatch: %+v",got)
	}
	if _,err:=os.Stat(filepath.Join(m.dir,"broken.mp4.part"));!os.IsNotExist(err){
		t.Fatal("orphaned partial MP4 retained after restart")
	}
	response:=httptest.NewRecorder()
	next.serve(response,reqJSON(http.MethodGet,"/api/luxe/recordings/",nil))
	if response.Code!=200 || !strings.Contains(response.Body.String(),s.ID){
		t.Fatalf("list after restart: %d %s",response.Code,response.Body.String())
	}
}

func TestSaveAndDiscardExplicitAck(t *testing.T) {
	for _,action:=range []string{"saved","discard"}{
		t.Run(action,func(t *testing.T){
			m,s:=fixture(t)
			r:=reqJSON(http.MethodPost,"/api/luxe/recordings/ack/"+s.ID,map[string]string{"action":action})
			w:=httptest.NewRecorder()
			m.serve(w,r)
			if w.Code!=204{t.Fatalf("ack: %d %s",w.Code,w.Body.String())}
			if _,ok:=m.sessions[s.ID];ok{t.Fatal("ack did not release camera")}
			if _,err:=os.Stat(m.filePath(s.ID));!os.IsNotExist(err){t.Fatal("ack did not delete video")}
			again:=httptest.NewRecorder()
			m.serve(again,reqJSON(http.MethodPost,"/api/luxe/recordings/ack/"+s.ID,map[string]string{"action":action}))
			if again.Code!=204{t.Fatal("ack must be idempotent")}
		})
	}
}

func TestInvalidAckDoesNotDeleteReadyMP4(t *testing.T){
	m,s:=fixture(t)
	w:=httptest.NewRecorder()
	m.serve(w,reqJSON(http.MethodPost,"/api/luxe/recordings/ack/"+s.ID,map[string]string{"action":"close"}))
	if w.Code!=400{t.Fatalf("invalid ack: %d",w.Code)}
	if _,err:=os.Stat(m.filePath(s.ID));err!=nil{t.Fatal("invalid ack deleted video")}
}

func TestAuthorizedRangeAndExpiredDownload(t *testing.T){
	m,s:=fixture(t)
	wrong:=httptest.NewRecorder()
	m.serve(wrong,httptest.NewRequest("GET","/api/luxe/recordings/file/"+s.ID+"?token=no",nil))
	if wrong.Code!=404{t.Fatalf("bad token: %d",wrong.Code)}

	r:=httptest.NewRequest("GET","/api/luxe/recordings/file/"+s.ID+"?token="+s.Token,nil)
	r.RemoteAddr="172.30.32.2:4242"
	r.Header.Set("Range","bytes=4-7")
	ok:=httptest.NewRecorder()
	m.serve(ok,r)
	if ok.Code!=206||ok.Body.String()!="ftyp"{t.Fatalf("Range: %d %q",ok.Code,ok.Body.String())}
	if !strings.Contains(ok.Header().Get("Content-Disposition"),"attachment"){t.Fatal("not an attachment")}

	m.mu.Lock()
	s.ExpiresAt=time.Now().Add(-time.Second)
	m.mu.Unlock()
	expired:=httptest.NewRecorder()
	m.serve(expired,httptest.NewRequest("GET","/api/luxe/recordings/file/"+s.ID+"?token="+s.Token,nil))
	if expired.Code!=404{t.Fatalf("expired capability accepted: %d",expired.Code)}
	if _,err:=os.Stat(m.filePath(s.ID));!os.IsNotExist(err){t.Fatal("expired file retained")}
}

func TestIngressOnlyWithoutSecondSecret(t *testing.T){
	m,s:=fixture(t)
	tests:=[]struct{
		name,remote string
		forged bool
		want int
	}{
		{"direct-lan","192.168.1.23:52321",false,http.StatusForbidden},
		{"direct-with-forged-user-header","192.168.1.23:52321",true,http.StatusForbidden},
		{"direct-loopback","127.0.0.1:11220",false,http.StatusForbidden},
		{"valid-supervisor-ingress","172.30.32.2:4242",false,http.StatusOK},
		{"valid-supervisor-ingress-no-user-header","172.30.32.2:4242",true,http.StatusOK},
		{"invalid-forwarded-source","172.30.32.44:4242",true,http.StatusForbidden},
	}
	for _,tt:=range tests{
		t.Run(tt.name,func(t *testing.T){
			r:=httptest.NewRequest(http.MethodGet,"/api/luxe/recordings/",nil)
			r.RemoteAddr=tt.remote
			if tt.forged {
				r.Header.Set("X-Remote-User-Id","spoofed-user")
				r.Header.Set("X-Forwarded-For","172.30.32.2")
				r.Header.Set("X-Luxe-Record-Key",strings.Repeat("K",32))
			}
			w:=httptest.NewRecorder()
			m.serve(w,r)
			if w.Code!=tt.want{t.Fatalf("response %d; want %d; body=%s",w.Code,tt.want,w.Body.String())}
		})
	}
	if s.State!="ready"{t.Fatal("unauthorized access changed ready session")}
}

func TestMP4DownloadAlsoIngressOnly(t *testing.T){
	m,s:=fixture(t)
	for _,remote:=range []string{"127.0.0.1:1234","192.168.1.7:9090"}{
		r:=httptest.NewRequest(http.MethodGet,"/api/luxe/recordings/file/"+s.ID+"?token="+s.Token,nil)
		r.RemoteAddr=remote
		w:=httptest.NewRecorder()
		m.serve(w,r)
		if w.Code!=http.StatusForbidden{t.Fatalf("download exposed on %s: %d",remote,w.Code)}
	}
}

func TestLimitedMP4Writer(t *testing.T){
	var buf bytes.Buffer
	w:=&limitedWriter{out:&buf,max:5}
	if n,err:=w.Write([]byte("12345"));n!=5||err!=nil{t.Fatalf("Write: %d, %v",n,err)}
	if n,err:=w.Write([]byte("6"));n!=0||err!=errFileLimit{t.Fatal("limit not enforced")}
}

func TestExpiredAndFailedNotListedAsReady(t *testing.T){
	m,s:=fixture(t)
	s.State="failed"
	views:=s.view()
	if views.DownloadURL!=""{t.Fatal("failed recording exposed download URL")}
	s.State="ready"
	s.ExpiresAt=time.Now().Add(-time.Second)
	w:=httptest.NewRecorder()
	m.serve(w,reqJSON("GET","/api/luxe/recordings/",nil))
	if strings.Contains(w.Body.String(),s.ID){t.Fatalf("expired session listed: %s",w.Body.String())}
}


func TestPresentedAckHidesButRetainsMP4For15Minutes(t *testing.T) {
	m,s:=fixture(t)
	originalDeadline:=s.ExpiresAt
	w:=httptest.NewRecorder()
	m.serve(w,reqJSON(http.MethodPost,"/api/luxe/recordings/ack/"+s.ID,
		map[string]string{"action":"presented"}))
	if w.Code!=http.StatusNoContent {
		t.Fatalf("presented ack: %d %s",w.Code,w.Body.String())
	}
	if s.State!="presented" {t.Fatalf("state=%s, want presented",s.State)}
	if !s.ExpiresAt.Equal(originalDeadline) {t.Fatal("presentation must not extend TTL")}
	if _,err:=os.Stat(m.filePath(s.ID));err!=nil{t.Fatalf("presented MP4 deleted early: %v",err)}
	list:=httptest.NewRecorder()
	m.serve(list,reqJSON(http.MethodGet,"/api/luxe/recordings/",nil))
	if list.Code!=http.StatusOK || !strings.Contains(list.Body.String(),`"presented"`) {
		t.Fatalf("presented metadata missing: %d %s",list.Code,list.Body.String())
	}
	if strings.Contains(list.Body.String(),`"download_url"`) {
		t.Fatal("presented session must not expose new Save entrypoint")
	}
	// Existing native downloads may issue Range requests after sheet opens;
	// do not revoke a previously issued capability until TTL or next recording.
	rangeRequest:=httptest.NewRequest("GET",
		"/api/luxe/recordings/file/"+s.ID+"?token="+s.Token,nil)
	rangeRequest.RemoteAddr="172.30.32.2:4242"
	rangeRequest.Header.Set("Range","bytes=4-7")
	partial:=httptest.NewRecorder()
	m.serve(partial,rangeRequest)
	if partial.Code!=http.StatusPartialContent||partial.Body.String()!="ftyp" {
		t.Fatalf("existing capability not usable during TTL: %d %q",partial.Code,partial.Body.String())
	}
	again:=httptest.NewRecorder()
	m.serve(again,reqJSON(http.MethodPost,"/api/luxe/recordings/ack/"+s.ID,
		map[string]string{"action":"presented"}))
	if again.Code!=http.StatusNoContent {t.Fatalf("presented must be idempotent: %d",again.Code)}
	m.pruneLocked(originalDeadline)
	if m.sessions[s.ID]!=nil{t.Fatal("presented cache not deleted at original 15m expiry")}
	if _,err:=os.Stat(m.filePath(s.ID));!os.IsNotExist(err){
		t.Fatal("presented MP4 still present after TTL")
	}
}

func TestNewSameCameraRecordingPurgesPresentedCacheOnly(t *testing.T) {
	m,s:=fixture(t)
	s.State="presented"
	if err:=m.saveManifestLocked(s);err!=nil {t.Fatal(err)}
	other:=*s
	other.ID="other_session_9876543210"
	other.Source="jinghuafu_keting_fixed_3k"
	other.Token="other_capability_9876543210"
	other.stop=nil;other.stream=nil;other.consumer=nil
	m.sessions[other.ID]=&other
	fileData,err:=os.ReadFile(m.filePath(s.ID))
	if err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(m.filePath(other.ID),fileData,0600);err!=nil{t.Fatal(err)}
	if err:=m.saveManifestLocked(&other);err!=nil{t.Fatal(err)}

	m.purgePresentedForSourceLocked(s.Source)
	if _,found:=m.sessions[s.ID];found{t.Fatal("same camera session not purged")}
	if _,err:=os.Stat(m.filePath(s.ID));!os.IsNotExist(err){t.Fatal("old MP4 still on disk")}
	if _,err:=os.Stat(m.metaPath(s.ID));!os.IsNotExist(err){t.Fatal("old session manifest still on disk")}
	if _,found:=m.sessions[other.ID];!found{t.Fatal("different camera cache was incorrectly purged")}
	if _,err:=os.Stat(m.filePath(other.ID));err!=nil{t.Fatal("other camera MP4 removed")}

	// User never clicked Save on this session. Its not-yet-presented video
	// must remain ready and may still block a same-camera replacement.
	other.State="ready"
	m.purgePresentedForSourceLocked(other.Source)
	if _,found:=m.sessions[other.ID];!found{t.Fatal("unpresented ready file should not be discarded")}
}

func TestPresentedSessionRestoresHiddenAfterRestart(t *testing.T) {
	m,s:=fixture(t)
	s.State="presented"
	if err:=m.saveManifestLocked(s);err!=nil{t.Fatal(err)}
	next,err:=newManager(m.dir)
	if err!=nil{t.Fatal(err)}
	recovered:=next.sessions[s.ID]
	if recovered==nil||recovered.State!="presented"{t.Fatalf("restored wrong state: %+v",recovered)}
	if _,err:=os.Stat(next.filePath(s.ID));err!=nil{t.Fatal("restart should retain presented MP4 until TTL")}
}


func TestBackgroundAbortImmediatelyRevokesRecording(t *testing.T) {
	m,_:=fixture(t)
	id:="foreground_lease_test"
	now:=time.Now()
	s:=&session{
		ID:id,Source:"jinghuafu_keting_ptz_3k",State:"recording",
		Started:now,lastHeartbeat:now,stop:make(chan struct{}),
	}
	m.sessions[id]=s
	m.active[s.Source]=id
	w:=httptest.NewRecorder()
	m.serve(w,reqJSON(http.MethodPost,"/api/luxe/recordings/abort/"+id,nil))
	if w.Code!=http.StatusNoContent {t.Fatalf("abort code %d",w.Code)}
	if s.State!="aborting" {t.Fatalf("state=%s, expected aborting",s.State)}
	select {
	case <-s.stop:
	default:
		t.Fatal("background abort did not close producer stop channel immediately")
	}
	retry:=httptest.NewRecorder()
	m.serve(retry,reqJSON(http.MethodPost,"/api/luxe/recordings/abort/"+id,nil))
	if retry.Code!=http.StatusNoContent {t.Fatalf("repeat abort status %d",retry.Code)}
	lease:=httptest.NewRecorder()
	m.serve(lease,reqJSON(http.MethodPost,"/api/luxe/recordings/heartbeat/"+id,nil))
	if lease.Code!=http.StatusConflict {t.Fatalf("aborted recording accepted a heartbeat: %d",lease.Code)}
	if _,err:=os.Stat(m.filePath(id));err==nil {
		t.Fatal("test should not produce a finalized MP4 when aborting")
	}
}

func TestForegroundLeaseExpiryAbortsWithoutClientEvent(t *testing.T) {
	m,_:=fixture(t)
	now:=time.Now()
	id:="suspended_webview_lease"
	s:=&session{ID:id,Source:"jinghuafu_keting_fixed_3k",State:"recording",
		Started:now.Add(-time.Minute),lastHeartbeat:now.Add(-heartbeatTimeout-time.Millisecond),
		stop:make(chan struct{})}
	m.sessions[id]=s
	m.active[s.Source]=id
	m.expireForegroundLeasesLocked(now)
	if s.State!="aborting" {t.Fatalf("stale lease state=%s",s.State)}
	select {
	case <-s.stop:
	default:t.Fatal("expired foreground lease did not stop producer")
	}
}

func TestForegroundHeartbeatRenewalAvoidsFalseCancellation(t *testing.T) {
	m,_:=fixture(t)
	now:=time.Now()
	id:="active_foreground_lease"
	s:=&session{ID:id,Source:"jinghuafu_keting_ptz_3k",State:"recording",
		Started:now.Add(-time.Minute),lastHeartbeat:now.Add(-10*time.Second),
		stop:make(chan struct{})}
	m.sessions[id]=s
	w:=httptest.NewRecorder()
	m.serve(w,reqJSON(http.MethodPost,"/api/luxe/recordings/heartbeat/"+id,nil))
	if w.Code!=http.StatusNoContent {t.Fatalf("heartbeat status %d",w.Code)}
	if s.lastHeartbeat.Before(now) {t.Fatal("heartbeat timestamp not renewed")}
	m.expireForegroundLeasesLocked(time.Now())
	if s.State!="recording" {t.Fatal("healthy foreground lease was aborted")}
}

func TestBackgroundAbortRaceWithReadyDiscardsMP4(t *testing.T) {
	m,s:=fixture(t)
	w:=httptest.NewRecorder()
	m.serve(w,reqJSON(http.MethodPost,"/api/luxe/recordings/abort/"+s.ID,nil))
	if w.Code!=http.StatusNoContent {t.Fatalf("abort ready status %d",w.Code)}
	if m.sessions[s.ID]!=nil {t.Fatal("background abort raced with finalization but left a ready MP4")}
	if _,err:=os.Stat(m.filePath(s.ID));!os.IsNotExist(err) {t.Fatal("race-discarded MP4 still stored")}
}

func TestBackgroundNeverTouchesPresentedMP4(t *testing.T) {
	m,s:=fixture(t)
	s.State="presented"
	w:=httptest.NewRecorder()
	m.serve(w,reqJSON(http.MethodPost,"/api/luxe/recordings/abort/"+s.ID,nil))
	if w.Code!=http.StatusNoContent {t.Fatalf("abort presented status %d",w.Code)}
	if s.State!="presented" {t.Fatal("previously presented recording must retain its original TTL")}
	if _,err:=os.Stat(m.filePath(s.ID));err!=nil {t.Fatal("previously presented MP4 was unexpectedly deleted")}
}
