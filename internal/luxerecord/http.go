package luxerecord

import (
	"crypto/subtle"
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

func (m *manager) serve(w http.ResponseWriter,r *http.Request) {
	w.Header().Set("Cache-Control","no-store")
	// Do not trust a client-supplied header or browser cookie on public 1984.
	// HA Supervisor has already checked the user's Ingress session before
	// forwarding the request, including native MP4 Range downloads.
	if !supervisorIngressRequest(r) {
		errorResponse(w,http.StatusForbidden,"recording API requires authenticated Home Assistant Ingress")
		return
	}
	at:=strings.LastIndex(r.URL.Path,routePrefix)
	if at<0 {errorResponse(w,404,"unknown recording endpoint");return}
	route:=strings.Trim(strings.TrimPrefix(r.URL.Path[at:],routePrefix),"/")
	parts:=strings.Split(route,"/")
	if len(parts)==2 && parts[0]=="file" &&
		(r.Method==http.MethodGet||r.Method==http.MethodHead) {
		m.download(w,r,parts[1]);return
	}
	switch {
	case route=="" && r.Method==http.MethodGet:
		m.list(w,r)
	case route=="start" && r.Method==http.MethodPost:
		m.start(w,r)
	case len(parts)==2 && parts[0]=="status" && r.Method==http.MethodGet:
		m.status(w,parts[1])
	case len(parts)==2 && parts[0]=="stop" && r.Method==http.MethodPost:
		m.stop(w,parts[1])
	case len(parts)==2 && parts[0]=="heartbeat" && r.Method==http.MethodPost:
		m.heartbeat(w,parts[1])
	case len(parts)==2 && parts[0]=="abort" && r.Method==http.MethodPost:
		m.abort(w,parts[1])
	case len(parts)==2 && parts[0]=="ack" && r.Method==http.MethodPost:
		m.ack(w,r,parts[1])
	default:
		errorResponse(w,404,"unknown recording endpoint")
	}
}

func (m *manager) list(w http.ResponseWriter,r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
	var entries []view
	group:=r.URL.Query().Get("group")
	for _,s:=range m.sessions {
		if group=="" || group==s.Group {
			entries=append(entries,s.view())
		}
	}
	sort.Slice(entries,func(i,j int)bool{return entries[i].ID<entries[j].ID})
	if entries==nil {entries=[]view{}}
	jsonResponse(w,200,map[string]any{
		"sessions":entries,
		"retention_seconds":int(retention/time.Second),
		"max_duration_seconds":int(maxDuration/time.Second),
	})
}

func (m *manager) status(w http.ResponseWriter,id string) {
	if !safeName.MatchString(id) {errorResponse(w,404,"recording unavailable");return}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
	s:=m.sessions[id]
	if s==nil {errorResponse(w,404,"recording missing or expired");return}
	jsonResponse(w,200,s.view())
}

func (m *manager) stop(w http.ResponseWriter,id string) {
	m.mu.Lock()
	s:=m.sessions[id]
	if s==nil || (s.State!="recording" && s.State!="stopping") {
		m.mu.Unlock();errorResponse(w,409,"recording not active");return
	}
	if s.State=="recording" {
		s.State="stopping"
		s.stopOnce.Do(func(){close(s.stop)})
	}
	result:=s.view()
	m.mu.Unlock()
	jsonResponse(w,200,result)
}

// heartbeat is strictly a foreground lease renewal and never changes an
// MP4 cache expiry. A nonexistent or already-aborted recording cannot revive.
func (m *manager) heartbeat(w http.ResponseWriter,id string) {
	if !safeName.MatchString(id) {errorResponse(w,404,"recording unavailable");return}
	m.mu.Lock()
	defer m.mu.Unlock()
	s:=m.sessions[id]
	if s==nil || (s.State!="recording" && s.State!="stopping" && s.State!="finalizing") {
		errorResponse(w,409,"recording is not active");return
	}
	s.lastHeartbeat=time.Now()
	w.WriteHeader(http.StatusNoContent)
}

// abort is an immediate cancel-and-discard operation, never the normal Stop
// path. It may be requested when the app transitions to the background.
// The recording goroutine owns closing and deleting the partial MP4.
func (m *manager) abort(w http.ResponseWriter,id string) {
	if !safeName.MatchString(id) {errorResponse(w,404,"recording unavailable");return}
	m.mu.Lock()
	if s:=m.sessions[id];s!=nil {
		// If finalization won the race with the background event, the
		// explicit abort still must discard this newly completed MP4.
		// Only the caller's actively recording session sends abort.
		if s.State=="ready" {
			delete(m.sessions,id)
			m.deleteFiles(id)
			if m.active[s.Source]==id {delete(m.active,s.Source)}
		} else {
			m.abortLocked(s)
		}
	}
	m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// "presented" acknowledges only the invocation of a native download/share
// sheet, not actual saving. It hides the Save UI while preserving the MP4 for
// the remainder of its original 15-minute TTL. "saved" and "discard" are
// optional explicit cleanup actions and delete the file immediately.
func (m *manager) ack(w http.ResponseWriter,r *http.Request,id string) {
	if !safeName.MatchString(id) {errorResponse(w,404,"recording unavailable");return}
	r.Body=http.MaxBytesReader(w,r.Body,512)
	defer r.Body.Close()
	var body struct{
		Action string `json:"action"`
	}
	if err:=json.NewDecoder(r.Body).Decode(&body);err!=nil ||
		(body.Action!="saved" && body.Action!="discard" && body.Action!="presented") {
		errorResponse(w,400,"action must be presented, saved or discard");return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
	s:=m.sessions[id]
	if s==nil {
		w.Header().Set("Cache-Control","no-store")
		w.WriteHeader(http.StatusNoContent) // idempotent after prior ack or expiry
		return
	}
	if body.Action=="presented" {
		// A download/share sheet was invoked, NOT proof that iOS saved it.
		// Remove Save UI by moving to a recoverable hidden state while
		// retaining bytes and the original expiration timestamp.
		if s.State=="presented" {w.WriteHeader(http.StatusNoContent);return}
		if s.State!="ready" {errorResponse(w,409,"recording not ready");return}
		s.State="presented"
		if err:=m.saveManifestLocked(s);err!=nil {
			s.State="ready"
			errorResponse(w,500,"cannot persist presented state");return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.State!="ready" && s.State!="presented" {errorResponse(w,409,"recording not finalized");return}
	delete(m.sessions,id)
	m.deleteFiles(id)
	w.WriteHeader(http.StatusNoContent)
}

func (m *manager) download(w http.ResponseWriter,r *http.Request,id string) {
	if !safeName.MatchString(id){errorResponse(w,404,"file unavailable");return}
	token:=r.URL.Query().Get("token")
	m.mu.Lock()
	m.pruneLocked(time.Now())
	s:=m.sessions[id]
	if s==nil||(s.State!="ready" && s.State!="presented")||token==""||
		len(token)!=len(s.Token)||
		subtle.ConstantTimeCompare([]byte(token),[]byte(s.Token))!=1 {
		m.mu.Unlock()
		errorResponse(w,404,"file unavailable or expired")
		return
	}
	// Open under lock so prune/ack cannot unlink the file before the download
	// has acquired its descriptor. On Linux, unlink at expiration revokes new
	// requests while existing readers may finish via their open fd.
	file,err:=os.Open(m.filePath(id))
	filename:=s.Filename
	m.mu.Unlock()
	if err!=nil {errorResponse(w,404,"temporary MP4 missing");return}
	defer file.Close()
	info,err:=file.Stat()
	if err!=nil {errorResponse(w,500,"cannot stat temporary MP4");return}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(gracefulDownloadLimit))
	w.Header().Set("Content-Type","video/mp4")
	w.Header().Set("Content-Disposition",mime.FormatMediaType("attachment",map[string]string{"filename":filename}))
	w.Header().Set("X-Content-Type-Options","nosniff")
	w.Header().Set("Cache-Control","no-store, no-transform")
	w.Header().Set("Referrer-Policy","no-referrer")
	w.Header().Set("Accept-Ranges","bytes")
	http.ServeContent(w,r,filename,info.ModTime(),file)
}
