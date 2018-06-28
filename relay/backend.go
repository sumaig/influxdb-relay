package relay

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type HttpBackend struct {
	name      string
	client    *http.Client
	transport http.Transport
	Location  string
	Active    bool
	bufferOn  bool
	Ticker    *time.Ticker
	rb        *retryBuffer
}

func NewHttpBackend(cfg *HTTPOutputConfig) (*HttpBackend, error) {
	timeout := DefaultHTTPTimeout
	if cfg.Timeout != "" {
		t, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("error parsing HTTP timeout '%v'", err)
		}
		timeout = t
	}

	interval := DefaultHTTPInterval
	if cfg.Interval != "" {
		i, err := time.ParseDuration(cfg.Interval)
		if err != nil {
			return nil, fmt.Errorf("error parsing HTTP interval '%v'", err)
		}
		interval = i
	}

	hb := &HttpBackend{
		client: &http.Client{
			Timeout: timeout,
		},

		// TODO: query timeout? use req.Cancel
		// client_query: &http.Client{
		// 	Timeout: time.Millisecond * time.Duration(cfg.TimeoutQuery),
		// },
		name:     cfg.Name,
		Location: cfg.Location,
		Active:   true,
		bufferOn: false,
		Ticker:   time.NewTicker(interval),
	}

	// If configured, create a retryBuffer per backend.
	// This way we serialize retries against each backend.
	if cfg.BufferSizeMB > 0 {
		max := DefaultMaxDelayInterval
		if cfg.MaxDelayInterval != "" {
			m, err := time.ParseDuration(cfg.MaxDelayInterval)
			if err != nil {
				return nil, err
			}
			max = m
		}

		batch := DefaultBatchSizeKB * KB
		if cfg.MaxBatchKB > 0 {
			batch = cfg.MaxBatchKB * KB
		}

		hb.bufferOn = true
		hb.rb = newRetryBuffer(cfg.BufferSizeMB*MB, batch, max, hb)
	}
	go hb.CheckActive()
	return hb, nil
}

func (hb *HttpBackend) CheckActive() {
	for range hb.Ticker.C {
		_, err := hb.Ping()
		if err != nil {
			hb.Active = false
			log.Printf("%s inactive.", hb.name)
		} else {
			hb.Active = true
		}
	}
}

func (hb *HttpBackend) IsActive() bool {
	return hb.Active
}

func (hb *HttpBackend) Ping() (version string, err error) {
	resp, err := hb.client.Get(hb.Location + "/ping")
	if err != nil {
		log.Println("http ping error: ", err)
		return
	}
	defer resp.Body.Close()

	version = resp.Header.Get("X-Influxdb-Version")

	if resp.StatusCode == http.StatusNoContent {
		return
	}

	log.Printf("write status code: %d, the backend is %s\n", resp.StatusCode, hb.Location)

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("readall body error: ", err)
		return
	}
	log.Println("ping result: ", respBody)
	return
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// Don't setup Accept-Encoding: gzip. Let real client do so.
// If real client don't support gzip and we setted, it will be a mistake.
func (hb *HttpBackend) Query(req *http.Request) (resp *http.Response, err error) {
	if len(req.Form) == 0 {
		req.Form = url.Values{}
	}

	req.ContentLength = 0

	req.URL, err = url.Parse(hb.Location + "/query?" + req.Form.Encode())
	if err != nil {
		log.Print("internal url parse error: ", err)
		return
	}

	return hb.transport.RoundTrip(req)
}

func (hb *HttpBackend) Write(buf []byte, query, auth string) (*responseData, error) {
	location := hb.Location + "/write"
	req, err := http.NewRequest("POST", location, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}

	req.URL.RawQuery = query
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Content-Length", strconv.Itoa(len(buf)))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := hb.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return &responseData{
		ContentType:     resp.Header.Get("Content-Type"),
		ContentEncoding: resp.Header.Get("Content-Encoding"),
		StatusCode:      resp.StatusCode,
		Body:            data,
	}, nil
}

func (hb *HttpBackend) Close() (err error) {
	hb.transport.CloseIdleConnections()
	hb.Ticker.Stop()
	hb.Active = false
	return
}
