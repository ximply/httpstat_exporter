package main

import (
	"flag"
	"net"
	"os"
	"net/http"
	"strings"
	"regexp"
	"fmt"
	"github.com/robfig/cron"
	"io"
	"strconv"
	"sync"
	hs "github.com/tcnksm/go-httpstat"
	"runtime"
	"time"
	"io/ioutil"
	"os/exec"
)

var (
	Name           = "httpstat_exporter"
	listenAddress  = flag.String("unix-sock", "/dev/shm/httpstat_exporter.sock", "Address to listen on for unix sock access and telemetry.")
	metricsPath    = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	dest           = flag.String("dest", "", "Destination list to get, multi split with ,.")
	curl           = flag.Bool("curl", true, "Use curl cmd or not.")
)

var destList []string
var doing bool
var g_lock sync.RWMutex
var g_statMap map[string]hs.Result
var g_httpClientMap map[string]*http.Client

func isURI(uri string) (schema, host string, port int, matched bool) {
	const reExp = `^((?P<schema>((ht|f)tp(s?))|tcp)\://)?((([a-zA-Z0-9_\-]+\.)+[a-zA-Z]{2,})|((?:(?:25[0-5]|2[0-4]\d|[01]\d\d|\d?\d)((\.?\d)\.)){4})|(25[0-5]|2[0-4][0-9]|[0-1]{1}[0-9]{2}|[1-9]{1}[0-9]{1}|[1-9])\.(25[0-5]|2[0-4][0-9]|[0-1]{1}[0-9]{2}|[1-9]{1}[0-9]{1}|[1-9]|0)\.(25[0-5]|2[0-4][0-9]|[0-1]{1}[0-9]{2}|[1-9]{1}[0-9]{1}|[1-9]|0)\.(25[0-5]|2[0-4][0-9]|[0-1]{1}[0-9]{2}|[1-9]{1}[0-9]{1}|[0-9]))(:([0-9]+))?(/[a-zA-Z0-9\-\._\?\,\'/\\\+&amp;%\$#\=~]*)?$`
	pattern := regexp.MustCompile(reExp)
	res := pattern.FindStringSubmatch(uri)
	if len(res) == 0 {
		return
	}
	matched = true
	schema = res[2]
	if schema == "" {
		schema = "tcp"
	}
	host = res[6]
	if res[17] == "" {
		if schema == "https" {
			port = 443
		} else {
			port = 80
		}
	} else {
		port, _ = strconv.Atoi(res[17])
	}

	return
}

func fileExists(file string) (bool, error) {
	_, err := os.Stat(file)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func worker(uri string, wg *sync.WaitGroup, useCurl bool) {
	var result hs.Result

	if useCurl {
		// curl -o /dev/null -s -w %{time_namelookup}:%{time_connect}:%{time_appconnect}
		// /usr/bin/curl
		path := "/usr/bin/curl"
		exist, _ := fileExists(path)
		if !exist {
			path2, err := exec.LookPath("curl")
			if err != nil {
				wg.Done()
				return
			}
			path = path2
		}

		cmdStr := fmt.Sprintf("%s -o /dev/null -s -w %%{time_namelookup}:%%{time_connect}:%%{time_appconnect} %s",
			path, uri)
		cmd := exec.Command("/bin/sh", "-c", cmdStr)
		out, err := cmd.Output()
		cmd.Wait()
		if err != nil {
			wg.Done()
			return
		}
		s := strings.TrimRight(string(out), "\n")
		l := strings.Split(s, ":")
		if len(l) == 3 {
			time_namelookup_tl, _ := strconv.ParseFloat(l[0], 64)
			time_connect_tl, _ := strconv.ParseFloat(l[1], 64)
			time_appconnect_tl, _ := strconv.ParseFloat(l[2], 64)

			time_namelookup := time_namelookup_tl * 1000000000
			time_connect := time_connect_tl * 1000000000 - time_namelookup
			time_appconnect := time_appconnect_tl * 1000000000 - time_connect_tl * 1000000000

			result.DNSLookup = time.Duration(time_namelookup) / time.Nanosecond
			result.TCPConnection = time.Duration(time_connect) / time.Nanosecond
			result.TLSHandshake = time.Duration(time_appconnect) / time.Nanosecond
		}
	} else {
		req, err := http.NewRequest("GET", uri, nil)
		if err != nil {
			wg.Done()
			return
		}
		ctx := hs.WithHTTPStat(req.Context(), &result)
		req = req.WithContext(ctx)
		req.Close = true

		res, err := g_httpClientMap[uri].Do(req)
		if res != nil {
			defer res.Body.Close()
		}
		if err != nil {
			result.End(time.Now())
			wg.Done()
			return
		}
		defer res.Body.Close()

		if _, err := ioutil.ReadAll(res.Body); err != nil {
			result.End(time.Now())
			wg.Done()
			return
		}
		result.End(time.Now())
	}

	g_lock.Lock()
	g_statMap[uri] = result
	g_lock.Unlock()

	wg.Done()
}

func doWork() {
	if doing {
		return
	}
	doing = true

	var wg sync.WaitGroup
	for _, target := range destList {
		wg.Add(1)
		go worker(target, &wg, *curl)
	}
	wg.Wait()

	doing = false
}

func metrics(w http.ResponseWriter, r *http.Request) {
	//DNSLookup        time.Duration
	//TCPConnection    time.Duration
	//TLSHandshake     time.Duration

	ret := ""
	namespace := "httpstat"

	g_lock.RLock()
	m := g_statMap
	g_lock.RUnlock()

	for _, k := range destList {
		if v, ok := m[k]; ok {
			ret += fmt.Sprintf("%s{uri=\"%s\",time_line=\"dns_lookup\"} %g\n",
				namespace, k, float64(v.DNSLookup))
			ret += fmt.Sprintf("%s{uri=\"%s\",time_line=\"tcp_conn\"} %g\n",
				namespace, k, float64(v.TCPConnection))
			if strings.HasPrefix(k, "https") {
				ret += fmt.Sprintf("%s{uri=\"%s\",time_line=\"tls_handshake\"} %g\n",
					namespace, k, float64(v.TLSHandshake))
			} else {
				ret += fmt.Sprintf("%s{uri=\"%s\",time_line=\"tls_handshake\"} %g\n",
					namespace, k, float64(0))
			}
		}
	}

	io.WriteString(w, ret)
}

func main() {
	flag.Parse()

	addr := "/dev/shm/httpstat_exporter.sock"
	if listenAddress != nil {
		addr = *listenAddress
	}

	if dest == nil || len(*dest) == 0 {
		panic("error dest")
	}
	g_httpClientMap = make(map[string]*http.Client)
	l := strings.Split(*dest, ",")
	for _, i := range l {
		_, _, _, ok := isURI(i)
		if ok {
			destList = append(destList, i)
			g_httpClientMap[i] = &http.Client{
				Transport: &http.Transport{
					DisableKeepAlives: true,
				},
				Timeout: 5 * time.Second,
			}
		}
	}

	if len(destList) == 0 {
		panic("no one to ping")
	}

	runtime.GOMAXPROCS(runtime.NumCPU())

	doing = false
	g_statMap = make(map[string]hs.Result)

	//doWork()
	c := cron.New()
	c.AddFunc("0 */1 * * * ?", doWork)
	c.Start()

	mux := http.NewServeMux()
	mux.HandleFunc(*metricsPath, metrics)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Httpstat Exporter</title></head>
             <body>
             <h1>Httpstat Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})
	server := http.Server{
		Handler: mux, // http.DefaultServeMux,
	}
	os.Remove(addr)

	listener, err := net.Listen("unix", addr)
	if err != nil {
		panic(err)
	}
	server.Serve(listener)
}