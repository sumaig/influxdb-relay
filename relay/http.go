package relay

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/influxdata/influxdb/models"
)

// HTTP is a relay for HTTP influxdb writes
type HTTP struct {
	addr   string
	name   string
	schema string

	cert string
	rp   string

	closing int64
	l       net.Listener
	ic      *InfluxCluster
	mux     *http.ServeMux
}

const (
	DefaultHTTPTimeout      = 10 * time.Second
	DefaultHTTPInterval     = 10 * time.Second
	DefaultMaxDelayInterval = 10 * time.Second
	DefaultBatchSizeKB      = 512

	KB = 1024
	MB = 1024 * KB
)

func NewHTTP(cfg HTTPConfig) (Relay, error) {
	h := new(HTTP)

	h.addr = cfg.Addr
	h.name = cfg.Name

	h.cert = cfg.SSLCombinedPem
	h.rp = cfg.DefaultRetentionPolicy
	h.ic = NewInfluxCluster(cfg)

	h.schema = "http"
	if h.cert != "" {
		h.schema = "https"
	}

	h.mux = http.NewServeMux()
	h.Register()

	return h, nil
}

func (h *HTTP) Register() {
	h.mux.HandleFunc("/ping", h.HandlerPing)
	h.mux.HandleFunc("/stats", h.HandlerCounter)
	h.mux.HandleFunc("/query", h.HandlerQuery)
	h.mux.HandleFunc("/write", h.HandlerWrite)
	h.mux.HandleFunc("/debug/pprof/", pprof.Index)
	h.mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
}

func (h *HTTP) Name() string {
	if h.name == "" {
		return fmt.Sprintf("%s://%s", h.schema, h.addr)
	}
	return h.name
}

func (h *HTTP) Run() error {
	l, err := net.Listen("tcp", h.addr)
	if err != nil {
		return err
	}

	// support HTTPS
	if h.cert != "" {
		cert, err := tls.LoadX509KeyPair(h.cert, h.cert)
		if err != nil {
			return err
		}

		l = tls.NewListener(l, &tls.Config{
			Certificates: []tls.Certificate{cert},
		})
	}

	h.l = l

	log.Printf("Starting %s relay %q on %v", strings.ToUpper(h.schema), h.Name(), h.addr)

	err = http.Serve(l, h.mux)
	if atomic.LoadInt64(&h.closing) != 0 {
		return nil
	}
	return err
}

func (h *HTTP) Stop() error {
	atomic.StoreInt64(&h.closing, 1)
	h.ic.Close()
	return h.l.Close()
}

func (h *HTTP) HandlerPing(w http.ResponseWriter, req *http.Request) {
	if req.Method == "GET" || req.Method == "HEAD" {
		w.Header().Add("X-InfluxDB-Version", "relay")
		w.WriteHeader(http.StatusNoContent)
		return
	}
}

func (h *HTTP) HandlerQuery(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET", "POST":
	default:
		jsonError(w, http.StatusMethodNotAllowed, "invalid method")
		atomic.AddInt64(&h.ic.stats.QueryRequestsFail, 1)
		return
	}

	params := req.URL.Query()

	if params.Get("db") == "" {
		atomic.AddInt64(&h.ic.stats.QueryRequestsFail, 1)
		jsonError(w, http.StatusBadRequest, "missing parameter: db")
		return
	}

	h.ic.Query(w, req)
}

func (h *HTTP) HandlerWrite(w http.ResponseWriter, req *http.Request) {
	start := time.Now()

	if req.Method != "POST" {
		w.Header().Set("Allow", "POST")
		if req.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
		} else {
			jsonError(w, http.StatusMethodNotAllowed, "invalid write method")
		}
		atomic.AddInt64(&h.ic.stats.WriteRequestsFail, 1)
		return
	}

	params := req.URL.Query()

	// fail early if we're missing the database
	if params.Get("db") == "" {
		jsonError(w, http.StatusBadRequest, "missing parameter: db")
		atomic.AddInt64(&h.ic.stats.WriteRequestsFail, 1)
		return
	}

	if params.Get("rp") == "" && h.rp != "" {
		params.Set("rp", h.rp)
	}

	var body = req.Body

	if req.Header.Get("Content-Encoding") == "gzip" {
		b, err := gzip.NewReader(req.Body)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "unable to decode gzip body")
			atomic.AddInt64(&h.ic.stats.WriteRequestsFail, 1)
			return
		}
		defer b.Close()
		body = b
	}

	bodyBuf := getBuf()
	_, err := bodyBuf.ReadFrom(body)
	if err != nil {
		putBuf(bodyBuf)
		jsonError(w, http.StatusInternalServerError, "problem reading request body")
		atomic.AddInt64(&h.ic.stats.WriteRequestsFail, 1)
		return
	}

	precision := params.Get("precision")
	points, err := models.ParsePointsWithPrecision(bodyBuf.Bytes(), start, precision)
	if err != nil {
		putBuf(bodyBuf)
		jsonError(w, http.StatusBadRequest, "unable to parse points")
		atomic.AddInt64(&h.ic.stats.WriteRequestsFail, 1)
		return
	}

	outBuf := getBuf()
	for _, p := range points {
		if _, err = outBuf.WriteString(p.PrecisionString(precision)); err != nil {
			break
		}
		if err = outBuf.WriteByte('\n'); err != nil {
			break
		}
	}

	// done with the input points
	putBuf(bodyBuf)

	if err != nil {
		putBuf(outBuf)
		jsonError(w, http.StatusInternalServerError, "problem writing points")
		atomic.AddInt64(&h.ic.stats.WriteRequestsFail, 1)
		return
	}

	// normalize query string
	query := params.Encode()

	outBytes := outBuf.Bytes()

	// check for authorization performed via the header
	authHeader := req.Header.Get("Authorization")
	h.ic.Write(outBytes, query, authHeader)
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTP) HandlerCounter(w http.ResponseWriter, req *http.Request) {
	metric := &Metric{
		Name: "influx.relay",
		Tags: h.ic.defaultTags,
		Fields: map[string]interface{}{
			"statQueryRequest":         h.ic.counter.QueryRequests,
			"statQueryRequestFail":     h.ic.counter.QueryRequestsFail,
			"statWriteRequest":         h.ic.counter.WriteRequests,
			"statWriteRequestFail":     h.ic.counter.WriteRequestsFail,
			"statPingRequest":          h.ic.counter.PingRequests,
			"statPingRequestFail":      h.ic.counter.PingRequestsFail,
			"statPointsWritten":        h.ic.counter.PointsWritten,
			"statPointsWrittenFail":    h.ic.counter.PointsWrittenFail,
			"statQueryRequestDuration": h.ic.counter.QueryRequestDuration,
			"statWriteRequestDuration": h.ic.counter.WriteRequestDuration,
		},
		Time: time.Now(),
	}

	stat, err := json.Marshal(metric)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "json marshal failed")
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(stat)
	return
}

type responseData struct {
	ContentType     string
	ContentEncoding string
	StatusCode      int
	Body            []byte
}

func (rd *responseData) Write(w http.ResponseWriter) {
	if rd.ContentType != "" {
		w.Header().Set("Content-Type", rd.ContentType)
	}

	if rd.ContentEncoding != "" {
		w.Header().Set("Content-Encoding", rd.ContentEncoding)
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(rd.Body)))
	w.WriteHeader(rd.StatusCode)
	w.Write(rd.Body)
}

func jsonError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	data := fmt.Sprintf("{\"error\":%q}\n", message)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(code)
	w.Write([]byte(data))
}

var ErrBufferFull = errors.New("retry buffer full")

var bufPool = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

func getBuf() *bytes.Buffer {
	if bb, ok := bufPool.Get().(*bytes.Buffer); ok {
		return bb
	}
	return new(bytes.Buffer)
}

func putBuf(b *bytes.Buffer) {
	b.Reset()
	bufPool.Put(b)
}
