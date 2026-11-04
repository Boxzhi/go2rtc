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
	m,err:=newManager(strings.Repeat("K",32),dir)
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
	r.Header.Set("X-Luxe-Record-Key",strings.Repeat("K",32))
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
	next,err:=newManager(strings.Repeat("K",32),m.dir)
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

func TestMissingKeyAndNoImplicitSave(t *testing.T){
	m,s:=fixture(t)
	r:=httptest.NewRequest("GET","/api/luxe/recordings/",nil)
	w:=httptest.NewRecorder();m.serve(w,r)
	if w.Code!=401{t.Fatal("listing without key")}
	if s.State!="ready"{t.Fatal("download state changed without explicit ack")}
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
