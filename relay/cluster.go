package relay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/sumaig/toolkits/consistent"
)

var (
	ErrQueryForbidden  = errors.New("query forbidden")
	ErrNotClusterQuery = errors.New("not a cluster query")
	ForbidCmd          = "(?i:select\\s+\\*|^\\s*delete|^\\s*drop|^\\s*grant|^\\s*revoke|\\(\\)\\$)"
	SupportCmd         = "(?i:where.*time|show.*from)"
	ExecutorCmd        = "(?i:show.*measurements)"
)

func ScanKey(point []byte) (key string, err error) {
	var keyBuf [100]byte
	keySlice := keyBuf[0:0]
	bufLen := len(point)
	for i := 0; i < bufLen; i++ {
		c := point[i]
		switch c {
		case '\\':
			i++
			keySlice = append(keySlice, point[i])
		case ' ', ',':
			key = string(keySlice)
			return
		default:
			keySlice = append(keySlice, c)
		}
	}
	return "", io.EOF
}

type InfluxCluster struct {
	lock           sync.RWMutex
	ForbiddenQuery []*regexp.Regexp
	ObligatedQuery []*regexp.Regexp
	stats          *Statistics
	counter        *Statistics
	ticker         *time.Ticker
	defaultTags    map[string]string
	ring           *consistent.Map
	formerRing     *consistent.Map
	nodes          map[string][]*HttpBackend
	formerNodes    map[string][]*HttpBackend
}

type Statistics struct {
	QueryRequests        int64
	QueryRequestsFail    int64
	WriteRequests        int64
	WriteRequestsFail    int64
	PingRequests         int64
	PingRequestsFail     int64
	PointsWritten        int64
	PointsWrittenFail    int64
	WriteRequestDuration int64
	QueryRequestDuration int64
}

func NewInfluxCluster(cfg HTTPConfig) *InfluxCluster {
	ic := new(InfluxCluster)

	ic.ring = consistent.New(cfg.Replicas, nil)

	for k, v := range cfg.Outputs {
		ic.ring.Add(k)
		for _, b := range v {
			backend, err := NewHttpBackend(&b)
			if err != nil {
				continue
			}
			if _, ok := ic.nodes[k]; !ok {
				ic.nodes[k] = []*HttpBackend{backend}
			} else {
				ic.nodes[k] = append(ic.nodes[k], backend)
			}
		}
	}

	// 加载扩容前的节点
	if cfg.Former != nil {
		ic.formerRing = consistent.New(cfg.Replicas, nil)
		for k, v := range cfg.Former {
			ic.formerRing.Add(k)
			for _, b := range v {
				backend, err := NewHttpBackend(&b)
				if err != nil {
					continue
				}
				if _, ok := ic.formerNodes[k]; !ok {
					ic.formerNodes[k] = []*HttpBackend{backend}
				} else {
					ic.formerNodes[k] = append(ic.formerNodes[k], backend)
				}
			}
		}
	}

	err := ic.ForbidQuery(ForbidCmd)
	if err != nil {
		panic(err)
	}

	err = ic.EnsureQuery(SupportCmd)
	if err != nil {
		panic(err)
	}
	return ic
}

func (ic *InfluxCluster) statistics() {
	// how to quit
	for {
		<-ic.ticker.C
		ic.Flush()
		ic.counter = (*Statistics)(atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(&ic.stats)),
			unsafe.Pointer(ic.counter)))
	}
}

func (ic *InfluxCluster) Flush() {
	ic.counter.QueryRequests = 0
	ic.counter.QueryRequestsFail = 0
	ic.counter.WriteRequests = 0
	ic.counter.WriteRequestsFail = 0
	ic.counter.PingRequests = 0
	ic.counter.PingRequestsFail = 0
	ic.counter.PointsWritten = 0
	ic.counter.PointsWrittenFail = 0
	ic.counter.WriteRequestDuration = 0
	ic.counter.QueryRequestDuration = 0
}

func (ic *InfluxCluster) ForbidQuery(s string) (err error) {
	r, err := regexp.Compile(s)
	if err != nil {
		return
	}

	ic.lock.Lock()
	defer ic.lock.Unlock()
	ic.ForbiddenQuery = append(ic.ForbiddenQuery, r)
	return
}

func (ic *InfluxCluster) EnsureQuery(s string) (err error) {
	r, err := regexp.Compile(s)
	if err != nil {
		return
	}

	ic.lock.Lock()
	defer ic.lock.Unlock()
	ic.ObligatedQuery = append(ic.ObligatedQuery, r)
	return
}

func (ic *InfluxCluster) CheckQuery(q string) (err error) {
	ic.lock.RLock()
	defer ic.lock.RUnlock()

	if len(ic.ForbiddenQuery) != 0 {
		for _, fq := range ic.ForbiddenQuery {
			if fq.MatchString(q) {
				return ErrQueryForbidden
			}
		}
	}

	if len(ic.ObligatedQuery) != 0 {
		for _, pq := range ic.ObligatedQuery {
			if pq.MatchString(q) {
				return
			}
		}
		return ErrQueryForbidden
	}

	return
}

func (ic *InfluxCluster) Query(w http.ResponseWriter, req *http.Request) {
	atomic.AddInt64(&ic.stats.QueryRequests, 1)
	defer func(start time.Time) {
		atomic.AddInt64(&ic.stats.QueryRequestDuration, time.Since(start).Nanoseconds())
	}(time.Now())

	q := strings.TrimSpace(req.FormValue("q"))
	if q == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty query"))
		atomic.AddInt64(&ic.stats.QueryRequestsFail, 1)
		return
	}

	matched, err := regexp.MatchString(ExecutorCmd, q)
	if err != nil || !matched {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprint(ErrNotClusterQuery)))
		return
	}

	err = ic.CheckQuery(q)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("query forbidden"))
		atomic.AddInt64(&ic.stats.QueryRequestsFail, 1)
		return
	}

	key, err := GetMeasurementFromInfluxQL(q)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("can't get measurement"))
		atomic.AddInt64(&ic.stats.QueryRequestsFail, 1)
		log.Printf("can't get measurement: %s\n", q)
		return
	}

	node := ic.ring.Get(key)

	pn := getBuf()
	po := getBuf()

	for _, n := range ic.nodes[node] {
		if !n.IsActive() {
			continue
		}

		resp, err := n.Query(req)
		if err == nil {
			copyHeader(w.Header(), resp.Header)
			p, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				log.Printf("read body error: %s,the query is %s\n", err, q)
				return
			}
			w.WriteHeader(resp.StatusCode)
			pn.Write(p)
			resp.Body.Close()
			break
		}
	}

	// 扩容后需要同时从之前的节点查询
	if ic.formerRing != nil {
		node := ic.formerRing.Get(key)
		for _, n := range ic.formerNodes[node] {
			if !n.IsActive() {
				continue
			}

			resp, err := n.Query(req)
			if err == nil {
				copyHeader(w.Header(), resp.Header)
				p, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					log.Printf("read body error: %s,the query is %s\n", err, q)
					return
				}
				w.WriteHeader(resp.StatusCode)
				po.Write(p)
				resp.Body.Close()
				break
			}
		}
	}

	// 合并查询结果
	pp, err := merge(pn.Bytes(), po.Bytes())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("query merge failed"))
		return
	}

	putBuf(pn)
	putBuf(po)

	if err == nil {
		w.Write(pp)
		w.WriteHeader(http.StatusOK)
		atomic.AddInt64(&ic.stats.QueryRequests, 1)
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte("invalid query"))
	log.Print("invalid query")
	atomic.AddInt64(&ic.stats.QueryRequestsFail, 1)
	return
}

func (ic *InfluxCluster) Write(p []byte, query, auth string) {
	atomic.AddInt64(&ic.stats.WriteRequests, 1)
	defer func(start time.Time) {
		atomic.AddInt64(&ic.stats.WriteRequestDuration, time.Since(start).Nanoseconds())
	}(time.Now())

	buf := bytes.NewBuffer(p)

	for {
		line, err := buf.ReadBytes('\n')
		switch err {
		default:
			log.Printf("error: %s\n", err)
			atomic.AddInt64(&ic.stats.WriteRequestsFail, 1)
			return
		case io.EOF, nil:
			err = nil
		}

		if len(line) == 0 {
			break
		}

		ic.WriteRow(line, query, auth)
	}
}

// Wrong in one row will not stop others.
// So don't try to return error, just print it.
func (ic *InfluxCluster) WriteRow(line []byte, query, auth string) {
	atomic.AddInt64(&ic.stats.PointsWritten, 1)
	// maybe trim?
	line = bytes.TrimRight(line, " \t\r\n")

	// empty line, ignore it.
	if len(line) == 0 {
		return
	}

	key, err := ScanKey(line)
	if err != nil {
		log.Printf("scan key error: %s\n", err)
		atomic.AddInt64(&ic.stats.PointsWrittenFail, 1)
		return
	}

	c := ic.ring.Get(key)

	for _, b := range ic.nodes[c] {
		if !b.Active || b == nil {
			continue
		}
		go func(*HttpBackend) {
			_, err := b.rb.Write(line, query, auth)
			if err != nil {
				log.Printf("cluster write fail: %s\n", key)
				atomic.AddInt64(&ic.stats.PointsWrittenFail, 1)
				return
			}
			atomic.AddInt64(&ic.stats.PointsWritten, 1)
		}(b)
	}
	return
}

func (ic *InfluxCluster) Close() {
	ic.lock.Lock()
	defer ic.lock.Unlock()

	for _, c := range ic.nodes {
		for _, b := range c {
			b.Close()
		}
	}
}
