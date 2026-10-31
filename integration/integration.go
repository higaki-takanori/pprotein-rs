package integration

import (
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"

	"github.com/felixge/fgprof"
	"github.com/goccy/go-json"
	"github.com/gorilla/mux"
	"github.com/kaz/pprotein/internal/git"
	"github.com/kaz/pprotein/internal/tail"
)

var (
	httplogPath       = getEnvOrDefault("PPROTEIN_HTTPLOG", "/var/log/nginx/access.log")
	slowlogPath       = getEnvOrDefault("PPROTEIN_SLOWLOG", findPostgresLog())
	gitRepositoryPath = getEnvOrDefault("PPROTEIN_GIT_REPOSITORY", ".")
)

func NewDebugHandler() http.Handler {
	r := mux.NewRouter()
	RegisterDebugHandlers(r)
	return r
}

func RegisterDebugHandlers(r *mux.Router) {
	r.Use(gitRepositoryMiddleware)

	r.Handle("/debug/log/httplog", tail.NewTailHandler(httplogPath))
	r.Handle("/debug/log/slowlog", tail.NewTailHandler(slowlogPath))

	r.Handle("/debug/fgprof", fgprof.Handler())

	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)
	r.HandleFunc("/debug/pprof/{h:.*}", pprof.Index)
}

func gitRepositoryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		defer next.ServeHTTP(rw, r)

		headInfo, err := git.GetInfo(gitRepositoryPath)
		if err != nil {
			log.Printf("failed to get git info: %v", err)
			return
		}

		data, err := json.Marshal(headInfo)
		if err != nil {
			log.Printf("failed to marshal git info: %v", err)
			return
		}

		rw.Header().Set("X-Git-Repository", string(data))
	})
}

func getEnvOrDefault(key string, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

// findPostgresLog は /var/log/postgresql/ にある PostgreSQL のログ
// (postgresql-<版>-main.log など) を探す。複数あれば最後に更新されたものを使う。
func findPostgresLog() string {
	const fallback = "/var/log/postgresql/postgresql-16-main.log"

	paths, _ := filepath.Glob("/var/log/postgresql/postgresql*.log")
	best := ""
	var bestTime int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if t := info.ModTime().UnixNano(); best == "" || t > bestTime {
			best, bestTime = p, t
		}
	}
	if best == "" {
		return fallback
	}
	return best
}
