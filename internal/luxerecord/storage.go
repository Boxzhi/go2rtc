package luxerecord

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func (m *manager) restore() error {
	now:=time.Now()
	entries,err:=os.ReadDir(m.dir)
	if err!=nil{return err}
	valid:=make(map[string]bool)
	for _,entry:=range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(),".json") {continue}
		id:=strings.TrimSuffix(entry.Name(),".json")
		if !safeName.MatchString(id) {
			_ = os.Remove(filepath.Join(m.dir,entry.Name()))
			continue
		}
		data,readErr:=os.ReadFile(m.metaPath(id))
		var s session
		if readErr!=nil || json.Unmarshal(data,&s)!=nil ||
			s.ID!=id || s.State!="ready" ||
			!safeName.MatchString(s.Source) ||
			s.Token=="" || len(s.Token)<20 ||
			s.ExpiresAt.IsZero() || !now.Before(s.ExpiresAt) ||
			s.ReadyAt.IsZero() || s.Bytes<1024 || s.Bytes>perFileLimit {
			m.deleteFiles(id)
			continue
		}
		fi,statErr:=os.Lstat(m.filePath(id))
		if statErr!=nil || !fi.Mode().IsRegular() || fi.Size()!=s.Bytes {
			m.deleteFiles(id)
			continue
		}
		file,openErr:=os.Open(m.filePath(id))
		if openErr!=nil {m.deleteFiles(id);continue}
		var magic [4]byte
		_,readErr=io.ReadFull(io.NewSectionReader(file,4,4),magic[:])
		_ = file.Close()
		if readErr!=nil || string(magic[:])!="ftyp" {
			m.deleteFiles(id)
			continue
		}
		// The only sessions recoverable after a crash are fully finalized files.
		// Sessions that were still recording leave a .part that is removed below.
		s.stop=nil;s.stream=nil;s.consumer=nil
		m.sessions[id]=&s
		valid[id]=true
	}
	for _,entry:=range entries {
		if entry.IsDir(){continue}
		name:=entry.Name()
		switch {
		case strings.HasSuffix(name,".mp4.part"):
			_ = os.Remove(filepath.Join(m.dir,name))
		case strings.HasSuffix(name,".json.tmp"):
			_ = os.Remove(filepath.Join(m.dir,name))
		case strings.HasSuffix(name,".mp4"):
			id:=strings.TrimSuffix(name,".mp4")
			if !valid[id] {_ = os.Remove(filepath.Join(m.dir,name))}
		}
	}
	return nil
}

func (m *manager) saveManifestLocked(s *session) error {
	if s.State!="ready" {return fmt.Errorf("only finalized MP4 may be persisted")}
	data,err:=json.Marshal(s)
	if err!=nil{return err}
	path:=m.metaPath(s.ID)
	tmp:=path+".tmp"
	f,err:=os.OpenFile(tmp,os.O_WRONLY|os.O_CREATE|os.O_TRUNC,0600)
	if err!=nil{return err}
	if _,err=f.Write(data);err!=nil {_=f.Close();_ = os.Remove(tmp);return err}
	if err=f.Sync();err!=nil {_=f.Close();_ = os.Remove(tmp);return err}
	if err=f.Close();err!=nil {_=os.Remove(tmp);return err}
	if err=os.Rename(tmp,path);err!=nil {_=os.Remove(tmp);return err}
	return nil
}

func (m *manager) deleteFiles(id string) {
	_ = os.Remove(m.metaPath(id))
	_ = os.Remove(m.metaPath(id)+".tmp")
	_ = os.Remove(m.filePath(id))
	_ = os.Remove(m.filePath(id)+".part")
}

func (m *manager) pruneLocked(now time.Time) {
	for id,s:=range m.sessions {
		if s.State=="recording" || s.State=="stopping" || s.State=="finalizing" {continue}
		if !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt) {
			delete(m.sessions,id)
			m.deleteFiles(id)
		}
	}
}

func (m *manager) janitor() {
	t:=time.NewTicker(10*time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		m.pruneLocked(time.Now())
		m.mu.Unlock()
	}
}

func (m *manager) spaceForStartLocked() error {
	var fs syscall.Statfs_t
	if err:=syscall.Statfs(m.dir,&fs);err!=nil {return err}
	var completed int64
	for _,s:=range m.sessions {
		if s.State=="ready" {completed+=s.Bytes}
	}
	count:=len(m.active)+1
	reserve:=int64(count)*perFileLimit
	if reserve>totalLimit || completed>totalLimit-reserve {
		return fmt.Errorf("recordings exceed the 3 GiB total temporary capacity budget")
	}
	// Statfs already includes committed and written bytes; reserve entire
	// theoretical active workload before admitting one more recording.
	free:=uint64(fs.Bavail)*uint64(fs.Bsize)
	if free<uint64(reserve+minFree) {
		return fmt.Errorf("insufficient disk free space for 3-stream temporary recording budget")
	}
	return nil
}
