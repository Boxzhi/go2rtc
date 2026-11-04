// Package luxerecord implements short-retention independent MP4 recording for
// Luxe Xiaomi Camera. The module is OPT-IN and disabled on stock go2rtc.
// It shares the tested go2rtc MP4 recording consumer without touching HLC7.
package luxerecord

import (
	"crypto/rand"
		"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
		"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
)

const (
	routePrefix = "/api/luxe/recordings/"
	retention = 15 * time.Minute
	maxDuration = 30 * time.Minute
	maxActive = 3
	// Conservative default upper limits: 3 recordings at 1 GiB each.
	// Available space must additionally exceed a 1 GiB reserve.
	perFileLimit int64 = 1 << 30
	totalLimit int64 = 3 << 30
	minFree int64 = 1 << 30
	gracefulDownloadLimit = 5 * time.Minute
	// Browser sends a heartbeat every 3 seconds. If the iOS WebView is
	// suspended before its background-abort request completes, the server
	// drops unfinished MP4s within this finite fallback window.
	heartbeatTimeout = 12 * time.Second
)

var safeName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

type session struct {
	ID string `json:"id"`
	Token string `json:"token"`
	Source string `json:"source"`
	Group string `json:"group,omitempty"`
	Filename string `json:"filename"`
	State string `json:"state"`
	Message string `json:"message,omitempty"`
	Started time.Time `json:"started"`
	ReadyAt time.Time `json:"ready_at,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Duration time.Duration `json:"duration"`
	Bytes int64 `json:"bytes"`
	VideoCodec string `json:"video_codec,omitempty"`
	AudioCodec string `json:"audio_codec,omitempty"`

	lastHeartbeat time.Time `json:"-"`
	stop chan struct{} `json:"-"`
	stopOnce sync.Once
	stream *streams.Stream `json:"-"`
	consumer *mp4.Consumer `json:"-"`
}

type view struct {
	StartedAt time.Time `json:"started_at"`
	ID string `json:"id"`
	Source string `json:"source"`
	Group string `json:"group,omitempty"`
	Filename string `json:"filename"`
	State string `json:"state"`
	Message string `json:"message,omitempty"`
	Bytes int64 `json:"bytes"`
	DurationSeconds int `json:"duration_seconds"`
	VideoCodec string `json:"video_codec,omitempty"`
	AudioCodec string `json:"audio_codec,omitempty"`
	ReadyAt *time.Time `json:"ready_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

func (s *session) view() view {
	v := view{
		StartedAt:s.Started, ID:s.ID, Source:s.Source, Group:s.Group, Filename:s.Filename,
		State:s.State, Message:s.Message, Bytes:s.Bytes,
		DurationSeconds:int(s.Duration/time.Second),
		VideoCodec:s.VideoCodec, AudioCodec:s.AudioCodec,
	}
	if s.State == "ready" {
		a,b := s.ReadyAt,s.ExpiresAt
		v.ReadyAt=&a
		v.ExpiresAt=&b
		v.DownloadURL="api/luxe/recordings/file/"+url.PathEscape(s.ID)+"?token="+url.QueryEscape(s.Token)
	}
	return v
}

type manager struct {
	mu sync.Mutex
	dir string
	sessions map[string]*session
	active map[string]string // source -> reservation ID, including network setup
}

func newManager(directory string) (*manager, error) {
	if err:=os.MkdirAll(directory,0700);err!=nil { return nil,err }
	if err:=os.Chmod(directory,0700);err!=nil {return nil,err}
	m:=&manager{
		dir:directory,
		sessions:make(map[string]*session),
		active:make(map[string]string),
	}
	if err:=m.restore();err!=nil {return nil,err}
	return m,nil
}

func Init() {
	var cfg struct {
		Recording struct {
			Enabled bool `yaml:"enabled"`
			Directory string `yaml:"directory"`
		} `yaml:"luxe_recording"`
	}
	app.LoadConfig(&cfg)
	if !cfg.Recording.Enabled {return}
	log:=app.GetLogger("luxerecord")
	dir:=cfg.Recording.Directory
	if dir=="" {dir="/data/luxe-recordings"}
	m,err:=newManager(dir)
	if err!=nil {
		log.Error().Err(err).Msg("Luxe recorder disabled: invalid cache configuration")
		return
	}
	api.HandleFunc("api/luxe/recordings/",m.serve)
	go m.janitor()
	log.Info().Str("dir",dir).Msg("Luxe recorder enabled: 3 streams; 30 min recording; 15 min per-file TTL")
}

func randID() (string,error) {
	buf:=make([]byte,18)
	if _,err:=rand.Read(buf);err!=nil{return "",err}
	return base64.RawURLEncoding.EncodeToString(buf),nil
}

// Supervisor verifies the user's authenticated Ingress session before
// proxying traffic to this app. The documented proxy gateway is 172.30.32.2.
// Only the TCP peer address is trusted: request headers such as
// X-Remote-User-Id and X-Forwarded-For can be forged on a direct 1984 socket.
// No additional user-supplied secret is required.
func supervisorIngressRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil { return false }
	ip := net.ParseIP(host)
	return ip != nil && ip.Equal(net.ParseIP("172.30.32.2"))
}

func jsonResponse(w http.ResponseWriter,code int,data any) {
	w.Header().Set("Content-Type","application/json; charset=utf-8")
	w.Header().Set("Cache-Control","no-store")
	w.Header().Set("X-Content-Type-Options","nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}
func errorResponse(w http.ResponseWriter,code int,msg string) {
	jsonResponse(w,code,map[string]string{"error":msg})
}

func (m *manager) filePath(id string) string {
	return filepath.Join(m.dir,id+".mp4")
}
func (m *manager) metaPath(id string) string {
	return filepath.Join(m.dir,id+".json")
}
