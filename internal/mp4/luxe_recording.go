package mp4

// Luxe direct-to-client recording proof of concept.
// No media bytes are retained on the server; each download owns one HTTP response.
import (
 "errors"
 "mime"
 "net/http"
 "regexp"
 "strconv"
 "sync"
 "sync/atomic"
 "time"

 "github.com/AlexxIT/go2rtc/internal/api"
 "github.com/AlexxIT/go2rtc/internal/streams"
 mp4pkg "github.com/AlexxIT/go2rtc/pkg/mp4"
)

const (
 luxeMaxRecords = 3
 luxeMaxDuration = 1800
 luxeTaskTTL = 5 * time.Minute
)
var luxeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,80}$`)
var luxeFilenamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,180}\.mp4$`)

type luxeRecordTask struct {
 ID string `json:"id"`
 Src string `json:"src"`
 Status string `json:"status"`
 StartedAt time.Time `json:"started_at"`
 EndedAt *time.Time `json:"ended_at,omitempty"`
 Bytes int64 `json:"bytes_sent"`
 Error string `json:"error,omitempty"`
 token string
 stopCh chan struct{}
 stopOnce sync.Once
}
type luxeTaskPublic struct {
 ID string `json:"id"`
 Src string `json:"src"`
 Status string `json:"status"`
 StartedAt time.Time `json:"started_at"`
 EndedAt *time.Time `json:"ended_at,omitempty"`
 Bytes int64 `json:"bytes_sent"`
 Error string `json:"error,omitempty"`
}
var luxeRecordManager = struct {
 sync.Mutex
 tasks map[string]*luxeRecordTask
}{tasks: make(map[string]*luxeRecordTask)}

func luxeRegister(t *luxeRecordTask) error {
 luxeRecordManager.Lock()
 defer luxeRecordManager.Unlock()
 now := time.Now()
 active := 0
 for id, old := range luxeRecordManager.tasks {
  if old.EndedAt != nil && now.Sub(*old.EndedAt) > luxeTaskTTL {
   delete(luxeRecordManager.tasks, id)
   continue
  }
  if old.EndedAt == nil {
   active++
   if old.Src == t.Src { return errors.New("camera is already recording") }
  }
 }
 if _, exists := luxeRecordManager.tasks[t.ID]; exists { return errors.New("recording id already used") }
 if active >= luxeMaxRecords { return errors.New("concurrent recording limit reached") }
 luxeRecordManager.tasks[t.ID] = t
 return nil
}
func luxeFinish(t *luxeRecordTask, status, message string) {
 luxeRecordManager.Lock()
 defer luxeRecordManager.Unlock()
 if t.EndedAt != nil { return }
 now := time.Now()
 t.EndedAt = &now
 t.Status = status
 t.Error = message
}
func luxeSetStatus(t *luxeRecordTask, status string) {
 luxeRecordManager.Lock()
 defer luxeRecordManager.Unlock()
 if t.EndedAt == nil { t.Status = status }
}
func luxeSnapshot(t *luxeRecordTask) luxeTaskPublic {
 luxeRecordManager.Lock()
 defer luxeRecordManager.Unlock()
 return luxeTaskPublic{
  ID:t.ID, Src:t.Src, Status:t.Status, StartedAt:t.StartedAt, EndedAt:t.EndedAt,
  Bytes:atomic.LoadInt64(&t.Bytes), Error:t.Error,
 }
}
func luxeAuthenticate(w http.ResponseWriter, r *http.Request) *luxeRecordTask {
 id, token := r.URL.Query().Get("id"), r.URL.Query().Get("token")
 luxeRecordManager.Lock()
 t := luxeRecordManager.tasks[id]
 if t == nil || token == "" || t.token != token {
  luxeRecordManager.Unlock()
  http.Error(w, "recording not found", http.StatusNotFound)
  return nil
 }
 luxeRecordManager.Unlock()
 return t
}
func luxeRecordStatus(w http.ResponseWriter, r *http.Request) {
 if r.Method != http.MethodGet { http.Error(w,"method not allowed",405); return }
 t:=luxeAuthenticate(w,r); if t==nil{return}
 w.Header().Set("Cache-Control","no-store")
 api.ResponseJSON(w,luxeSnapshot(t))
}
func luxeRecordStop(w http.ResponseWriter, r *http.Request) {
 if r.Method != http.MethodPost { http.Error(w,"method not allowed",405); return }
 t:=luxeAuthenticate(w,r); if t==nil{return}
 t.stopOnce.Do(func(){close(t.stopCh)})
 luxeSetStatus(t,"stopping")
 w.Header().Set("Cache-Control","no-store")
 api.ResponseJSON(w,luxeSnapshot(t))
}

// luxeRecordingWriter keeps the regular net/http.Flusher behavior available to
// core.WriteBuffer and marks recording after the first media fragment is emitted.
type luxeRecordingWriter struct {
 http.ResponseWriter
 task *luxeRecordTask
 writes int
}
func (w *luxeRecordingWriter) Write(p []byte)(int,error){
 n,err:=w.ResponseWriter.Write(p)
 if n>0 {
  atomic.AddInt64(&w.task.Bytes,int64(n))
  w.writes++
  if w.writes>=2 { luxeSetStatus(w.task,"recording") }
 }
 return n,err
}
func (w *luxeRecordingWriter) Flush(){
 if f,ok:=w.ResponseWriter.(http.Flusher);ok{f.Flush()}
}
func luxeRecordStream(w http.ResponseWriter,r *http.Request){
 if r.Method!=http.MethodGet {http.Error(w,"method not allowed",405);return}
 q:=r.URL.Query()
 id, token,src:=q.Get("id"),q.Get("token"),q.Get("src")
 if !luxeIDPattern.MatchString(id)||!luxeIDPattern.MatchString(token)||src=="" {
  http.Error(w,"invalid recording id, token or src",http.StatusBadRequest);return
 }
 stream:=streams.Get(src)
 if stream==nil {http.Error(w,"stream not found",http.StatusNotFound);return}
 duration:=luxeMaxDuration
 if raw:=q.Get("duration");raw!=""{
  parsed,err:=strconv.Atoi(raw)
  if err!=nil||parsed<1||parsed>luxeMaxDuration {
   http.Error(w,"duration must be between 1 and 1800 seconds",400);return
  }
  duration=parsed
 }
 filename:=q.Get("filename")
 if filename=="" {filename="luxe-"+id+".mp4"}
 if !luxeFilenamePattern.MatchString(filename){
  http.Error(w,"invalid filename (ASCII letters, digits, _.- only)",400);return
 }
 t:=&luxeRecordTask{ID:id,token:token,Src:src,Status:"starting",StartedAt:time.Now(),stopCh:make(chan struct{})}
 if err:=luxeRegister(t);err!=nil{http.Error(w,err.Error(),http.StatusConflict);return}

 // The recording-only codec selection remains independent of live-preview mute.
 if q.Get("mp4")=="" {q.Set("mp4","all")}
 cons:=mp4pkg.NewConsumer(mp4pkg.ParseQuery(q))
 cons.FormatName="mp4"
 cons.Protocol="http"
 cons.WithRequest(r)
 if err:=stream.AddConsumer(cons);err!=nil{
  luxeFinish(t,"failed",err.Error())
  http.Error(w,err.Error(),http.StatusInternalServerError)
  return
 }
 // The stop API must end the consumer rather than cancel the download request.
 stopped:=make(chan string,1)
 writerDone:=make(chan struct{})
 go func(){
  timer:=time.NewTimer(time.Duration(duration)*time.Second)
  defer timer.Stop()
  reason:="stopped"
  select {
  case <-t.stopCh:
  case <-timer.C: reason="time_limit"
  case <-r.Context().Done(): reason="disconnected"
  case <-writerDone: reason="writer_finished"
  }
  luxeSetStatus(t,"stopping")
  stream.RemoveConsumer(cons)
  stopped<-reason
 }()
 w.Header().Set("Content-Type","video/mp4")
 w.Header().Set("Content-Disposition",mime.FormatMediaType("attachment",map[string]string{"filename":filename}))
 w.Header().Set("Cache-Control","no-store, no-transform")
 w.Header().Set("X-Content-Type-Options","nosniff")
 w.Header().Set("X-Accel-Buffering","no")
 writer:=&luxeRecordingWriter{ResponseWriter:w,task:t}
 _,writeErr:=cons.WriteTo(writer)
 close(writerDone)
 // A normal stream closure should make the existing WriteBuffer.WriteTo return;
 // only request-context cancellation is an interrupted client download.
 reason:=<-stopped
 switch {
 case r.Context().Err()!=nil || reason=="disconnected":
  luxeFinish(t,"interrupted","client download disconnected")
 case writeErr!=nil:
  luxeFinish(t,"failed",writeErr.Error())
 default:
  luxeFinish(t,"completed","")
 }
}

