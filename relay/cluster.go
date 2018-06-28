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

	"github.com/sumaig/toolkits/consistent"
)

var (
	ErrQueryForbidden = errors.New("query forbidden")
	ForbidCmd         = "(?i:select\\s+\\*|^\\s*delete|^\\s*drop|^\\s*grant|^\\s*revoke|\\(\\)\\$)"
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
	stats          *Statistics
	ticker         *time.Ticker
	defaultTags    map[string]string
	ring           *consistent.Map
	formerRing     *consistent.Map
	nodes          map[string][]*HttpBackend
	formerNodes    map[string][]*HttpBackend
}

type Statistics struct {
	sync.Mutex
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

	ic.stats = &Statistics{}
	ic.nodes = make(map[string][]*HttpBackend)
	ic.ring = consistent.New(cfg.Replicas, nil)
	ic.ticker = time.NewTicker(time.Duration(5) * time.Second)

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
		ic.formerNodes = make(map[string][]*HttpBackend)
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

	ic.Flush()

	return ic
}

func (ic *InfluxCluster) Flush() {
	ic.stats.QueryRequests = 0
	ic.stats.QueryRequestsFail = 0
	ic.stats.WriteRequests = 0
	ic.stats.WriteRequestsFail = 0
	ic.stats.PingRequests = 0
	ic.stats.PingRequestsFail = 0
	ic.stats.PointsWritten = 0
	ic.stats.PointsWrittenFail = 0
	ic.stats.WriteRequestDuration = 0
	ic.stats.QueryRequestDuration = 0
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

	return
}

func (ic *InfluxCluster) Query(w http.ResponseWriter, req *http.Request) {
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

	if err := ic.CheckQuery(q); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
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
			// log.Printf("query from [new] %s result: %s\n", n.name, pn.String())
			break
		}
	}

	// 扩容后需要同时从查询之前节点
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
				// log.Printf("query from [former] %s result: %s\n", n.name, po.String())
				break
			}
		}
	}

	// 合并查询结果
	pp, err := merge(pn.Bytes(), po.Bytes())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintln("merge query failed: ", err)))
		return
	}

	// log.Println("query result: ", string(pp))

	putBuf(pn)
	putBuf(po)

	if err == nil {
		w.Write(pp)
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
	var wg sync.WaitGroup
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
		wg.Add(1)
		go func(b *HttpBackend) {
			defer wg.Done()
			if b.bufferOn {
				_, err := b.rb.Write(line, query, auth)
				if err != nil {
					log.Printf("cluster write fail: %s\n", key)
					atomic.AddInt64(&ic.stats.PointsWrittenFail, 1)
					return
				}
			} else {
				_, err := b.Write(line, query, auth)
				if err != nil {
					log.Printf("cluster write fail: %s\n", key)
					atomic.AddInt64(&ic.stats.PointsWrittenFail, 1)
					return
				}
			}

			// log.Printf("%s write to %s done", string(line), b.name)
		}(b)
	}
	wg.Wait()
	atomic.AddInt64(&ic.stats.PointsWritten, 1)
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
