package luxerecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
)

type startRequest struct {
	Source string `json:"source"`
	Group string `json:"group,omitempty"`
	DurationSeconds int `json:"duration_seconds"`
}

func (m *manager) start(w http.ResponseWriter,r *http.Request) {
	r.Body=http.MaxBytesReader(w,r.Body,4096)
	defer r.Body.Close()
	var req startRequest
	dec:=json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err:=dec.Decode(&req);err!=nil {
		errorResponse(w,400,"invalid JSON start request");return
	}
	if req.DurationSeconds==0 {req.DurationSeconds=60}
	if req.DurationSeconds<3 || req.DurationSeconds>1800 {
		errorResponse(w,400,"duration_seconds must be 3..1800");return
	}
	if !safeName.MatchString(req.Source) ||
		(req.Group!="" && !safeName.MatchString(req.Group)) {
		errorResponse(w,400,"source/group must be 1..100 letters, digits, hyphens or underscores");return
	}
	stream:=streams.Get(req.Source)
	if stream==nil {errorResponse(w,404,"source not found in go2rtc");return}
	id,err:=randID()
	if err!=nil {errorResponse(w,500,"session ID unavailable");return}
	token,err:=randID()
	if err!=nil {errorResponse(w,500,"download capability unavailable");return}

	m.mu.Lock()
	m.pruneLocked(time.Now())
	if len(m.active)>=maxActive {
		m.mu.Unlock();errorResponse(w,409,"up to three simultaneous recordings");return
	}
	if _,busy:=m.active[req.Source];busy {
		m.mu.Unlock();errorResponse(w,409,"camera is recording");return
	}
	for _,s:=range m.sessions {
		if s.Source==req.Source && (s.State=="ready"||s.State=="finalizing") {
			m.mu.Unlock();errorResponse(w,409,"camera has a pending video to save or discard");return
		}
	}
	if err:=m.spaceForStartLocked();err!=nil {
		m.mu.Unlock();errorResponse(w,507,err.Error());return
	}
	m.active[req.Source]=id
	m.mu.Unlock()

	reserved:=true
	defer func(){
		if !reserved{return}
		m.mu.Lock()
		if m.active[req.Source]==id{delete(m.active,req.Source)}
		m.mu.Unlock()
	}()

	tmpPath:=m.filePath(id)+".part"
	file,err:=os.OpenFile(tmpPath,os.O_WRONLY|os.O_EXCL|os.O_CREATE,0600)
	if err!=nil {errorResponse(w,507,"cannot create temporary MP4");return}

	media:=mp4.ParseQuery(url.Values{"mp4":{"all"}})
	consumer:=mp4.NewConsumer(media)
	consumer.FormatName="mp4"
	consumer.Protocol="file"
	if err=stream.AddConsumer(consumer);err!=nil {
		_ = file.Close();_ = os.Remove(tmpPath)
		errorResponse(w,502,"recording source negotiation failed: "+err.Error());return
	}
	var videoCodec,audioCodec string
	for _,codec:=range consumer.Codecs(){
		switch codec.Name {
		case core.CodecH264,core.CodecH265:videoCodec=codec.Name
		case core.CodecAAC,core.CodecOpus,core.CodecMP3,core.CodecFLAC:audioCodec=codec.Name
		}
	}
	if videoCodec=="" {
		stream.RemoveConsumer(consumer)
		_ = file.Close();_ = os.Remove(tmpPath)
		errorResponse(w,415,"no supported video codec");return
	}
	now:=time.Now()
	s:=&session{
		ID:id,Token:token,Source:req.Source,Group:req.Group,
		Filename:fmt.Sprintf("luxe_%s_%s_%s.mp4",req.Source,now.Format("20060102_150405"),id[:8]),
		State:"recording", Started:now,
		Duration:time.Duration(req.DurationSeconds)*time.Second,
		VideoCodec:videoCodec,AudioCodec:audioCodec,
		stream:stream,consumer:consumer,stop:make(chan struct{}),
	}
	m.mu.Lock()
	// A previously presented recording is hidden from the Save UI but remains
	// temporarily cached. Starting a new successful recording for this same
	// camera invalidates that older cache immediately, independently of TTL.
	// Do it only after AddConsumer succeeded; failed starts never discard it.
	m.purgePresentedForSourceLocked(req.Source)
	m.sessions[id]=s
	m.mu.Unlock()
	go m.record(s,file)
	reserved=false
	jsonResponse(w,http.StatusAccepted,s.view())
}

var errFileLimit=errors.New("temporary MP4 exceeds 1 GiB per-file limit")
type limitedWriter struct {
	out io.Writer
	n,max int64
}
func (v *limitedWriter) Write(p []byte)(int,error){
	if int64(len(p))>v.max-v.n{return 0,errFileLimit}
	n,err:=v.out.Write(p)
	v.n+=int64(n)
	return n,err
}

func (m *manager) record(s *session,file *os.File) {
	writer:=&limitedWriter{out:file,max:perFileLimit}
	writerDone:=make(chan struct{})
	consumerStopped:=make(chan struct{})
	go func(){
		timer:=time.NewTimer(s.Duration)
		defer timer.Stop()
		select{
		case <-timer.C:
		case <-s.stop:
		case <-writerDone:
		}
		s.stream.RemoveConsumer(s.consumer)
		close(consumerStopped)
	}()
	_,writeErr:=s.consumer.WriteTo(writer)
	close(writerDone)
	<-consumerStopped

	m.mu.Lock()
	manuallyStopped:=s.State=="stopping"
	if !manuallyStopped {s.State="finalizing"}
	m.mu.Unlock()

	if writeErr==nil && !manuallyStopped && time.Since(s.Started)+3*time.Second<s.Duration {
		writeErr=errors.New("upstream ended before requested recording duration")
	}
	if writeErr==nil && writer.n<1024 {writeErr=errors.New("MP4 contains insufficient media")}
	if err:=file.Sync();writeErr==nil {writeErr=err}
	if err:=file.Close();writeErr==nil {writeErr=err}
	if writeErr==nil {
		check,err:=os.Open(m.filePath(s.ID)+".part")
		if err!=nil {writeErr=err} else {
			var prefix [4]byte
			_,err=io.ReadFull(io.NewSectionReader(check,4,4),prefix[:])
			_ = check.Close()
			if err!=nil {writeErr=err
			}else if string(prefix[:])!="ftyp" {writeErr=errors.New("invalid MP4 header")}
		}
	}
	if writeErr==nil {writeErr=os.Rename(m.filePath(s.ID)+".part",m.filePath(s.ID))}

	m.mu.Lock()
	defer m.mu.Unlock()
	finished:=time.Now()
	s.Bytes=writer.n
	if writeErr!=nil {
		s.State="failed"
		s.Message=writeErr.Error()
		s.ExpiresAt=finished.Add(3*time.Minute)
		m.deleteFiles(s.ID)
	}else{
		s.State="ready"
		s.ReadyAt=finished
		s.ExpiresAt=finished.Add(retention)
		if err:=m.saveManifestLocked(s);err!=nil{
			s.State="failed"
			s.Message="cannot persist recording metadata: "+err.Error()
			s.ExpiresAt=finished.Add(3*time.Minute)
			m.deleteFiles(s.ID)
		}
	}
	if m.active[s.Source]==s.ID {delete(m.active,s.Source)}
}

