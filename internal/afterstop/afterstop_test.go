package afterstop

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoundedWriter(t *testing.T) {
	var buffer bytes.Buffer
	writer := &boundedWriter{out:&buffer, max:8}
	if n,err:=writer.Write([]byte("12345678"));err!=nil||n!=8 {t.Fatalf("write: %d %v",n,err)}
	if n,err:=writer.Write([]byte("9"));err!=errTooLarge||n!=0 {t.Fatalf("limit: %d %v",n,err)}
	if got:=buffer.String();got!="12345678" {t.Fatalf("bad contents: %q",got)}
}

func TestSessionViewReadyOnly(t *testing.T) {
	s:=&session{id:"abc",token:"private-token",path:"/tmp/abc",state:"recording",duration:10*time.Second}
	if url:=s.view().DownloadURL;url!="" {t.Fatalf("non-ready session exposed URL: %q",url)}
	s.state="ready"
	view:=s.view()
	if !strings.Contains(view.DownloadURL,"token=private-token") {t.Fatalf("missing capability URL: %q",view.DownloadURL)}
}

func TestAuthorize(t *testing.T) {
	m:=&manager{secret:strings.Repeat("a",32)}
	r:=httptest.NewRequest(http.MethodPost,"/api/luxe/poc4/start",nil)
	if m.authorized(r) {t.Fatal("no key should be denied")}
	r.Header.Set("X-Luxe-POC-Key",strings.Repeat("b",32))
	if m.authorized(r) {t.Fatal("wrong key should be denied")}
	r.Header.Set("X-Luxe-POC-Key",strings.Repeat("a",32))
	if !m.authorized(r) {t.Fatal("correct key rejected")}
}

func TestDownloadCapabilityAndRange(t *testing.T) {
	dir:=t.TempDir()
	path:=filepath.Join(dir,"test.mp4")
	content:=[]byte("ftyp_mock_data_for_poc2_testing")
	if err:=os.WriteFile(path,content,0600);err!=nil {t.Fatal(err)}
	m:=&manager{secret:strings.Repeat("x",32),dir:dir,sessions:map[string]*session{
		"abc":{id:"abc",token:"secret123",state:"ready",path:path,filename:"record.mp4",expires:time.Now().Add(time.Minute)},
	}}
	wrong:=httptest.NewRecorder()
	m.handle(wrong,httptest.NewRequest(http.MethodGet,"/api/luxe/poc4/download/abc?token=wrong",nil))
	if wrong.Code!=http.StatusNotFound {t.Fatalf("wrong token: %d",wrong.Code)}
	get:=httptest.NewRecorder()
	m.handle(get,httptest.NewRequest(http.MethodGet,"/api/luxe/poc4/download/abc?token=secret123",nil))
	if get.Code!=http.StatusOK {t.Fatalf("download: %d body=%s",get.Code,get.Body)}
	if !bytes.Equal(get.Body.Bytes(),content) {t.Fatal("bad data")}
	if !strings.Contains(get.Header().Get("Content-Disposition"),"attachment") {t.Fatal("missing attachment header")}
	r:=httptest.NewRequest(http.MethodGet,"/api/luxe/poc4/download/abc?token=secret123",nil)
	r.Header.Set("Range","bytes=0-3")
	rangeResponse:=httptest.NewRecorder()
	m.handle(rangeResponse,r)
	if rangeResponse.Code!=http.StatusPartialContent || rangeResponse.Body.String()!="ftyp" {
		t.Fatalf("range: %d %q",rangeResponse.Code,rangeResponse.Body.String())
	}
}

func TestPruneRemovesExpired(t *testing.T) {
	dir:=t.TempDir()
	path:=filepath.Join(dir,"old.mp4")
	if err:=os.WriteFile(path,[]byte("data"),0600);err!=nil {t.Fatal(err)}
	m:=&manager{sessions:map[string]*session{"old":{id:"old",state:"ready",path:path,expires:time.Now().Add(-time.Second)}}}
	m.pruneLocked(time.Now())
	if _,ok:=m.sessions["old"];ok {t.Fatal("expired session retained")}
	if _,err:=os.Stat(path);!os.IsNotExist(err) {t.Fatalf("expired file retained: %v",err)}
}

func TestThreeRecordingSlots(t *testing.T) {
	m:=&manager{activeIDs:make(map[string]string)}
	names:=[]string{"poc_camera_1","poc_camera_2","poc_camera_3"}
	for i,name:=range names {
		if len(m.activeIDs)>=maxActive {t.Fatalf("slot %d unavailable",i)}
		m.activeIDs[name]=name
	}
	if len(m.activeIDs)!=maxActive {t.Fatalf("expected %d active slots, got %d", maxActive,len(m.activeIDs))}
	if len(m.activeIDs)<maxActive {t.Fatal("fourth recording unexpectedly allowed")}
	delete(m.activeIDs,"poc_camera_2")
	if len(m.activeIDs)>=maxActive {t.Fatal("released recording slot not available")}
}

func TestPOC4TemporarySpaceGuard(t *testing.T) {
	testCases := []struct {
		name string
		committed int64
		active int
		free uint64
		want bool
	}{
		{"three-reservations-with-headroom",0,3,uint64(3*maxBytes+minDiskReserve),true},
		{"insufficient-free-space",0,3,uint64(3*maxBytes+minDiskReserve-1),false},
		{"over-total-budget",maxTotalBudget-3*maxBytes+1,3,uint64(maxTotalBudget+minDiskReserve),false},
		{"one-reservation",0,1,uint64(maxBytes+minDiskReserve),true},
		{"too-many-recorders",0,maxActive+1,uint64(maxTotalBudget+minDiskReserve),false},
	}
	for _,tc := range testCases {
		t.Run(tc.name,func(t *testing.T){
			got:=hasTemporarySpace(tc.committed,tc.active,tc.free)
			if got!=tc.want {t.Fatalf("got=%v want=%v",got,tc.want)}
		})
	}
}

func TestPOC4EarlyTermination(t *testing.T) {
	tests:=[]struct{
		name string
		elapsed time.Duration
		requested time.Duration
		manual bool
		want bool
	}{
		{"remote-disconnect",40*time.Second,60*time.Second,false,true},
		{"normal-timeout",59*time.Second,60*time.Second,false,false},
		{"manual-stop",20*time.Second,300*time.Second,true,false},
		{"short-video-early",2*time.Second,10*time.Second,false,true},
	}
	for _,tt:=range tests {
		t.Run(tt.name,func(t *testing.T){
			if got:=recordingEndedEarly(tt.elapsed,tt.requested,tt.manual);got!=tt.want {
				t.Fatalf("early=%v want=%v",got,tt.want)
			}
		})
	}
}
