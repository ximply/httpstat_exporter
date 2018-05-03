package main

import (
	"flag"
	"net"
	"os"
	"net/http"
	"strings"
	"regexp"
	"github.com/ximply/ping_exporter/cache"
	"fmt"
	"github.com/robfig/cron"
	"io"
	"strconv"
	"sync"
	"github.com/ximply/httpstat_exporter/httpstat"
	hs "github.com/tcnksm/go-httpstat"
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

func doWork() {
	if doing {
		return
	}
	doing = true

	var wg sync.WaitGroup
	for _, target := range destList {
		wg.Add(1)
		go httpstat.Worker(target, &wg, *curl)
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
	for _, k := range destList {
		b, r := cache.GetInstance().Value(k)
		if b && r != nil {
			ret += fmt.Sprintf("%s{uri=\"%s\",time_line=\"dns_lookup\"} %g\n",
				namespace, k, float64(r.(hs.Result).DNSLookup))
			ret += fmt.Sprintf("%s{uri=\"%s\",time_line=\"tcp_conn\"} %g\n",
				namespace, k, float64(r.(hs.Result).TCPConnection))
			ret += fmt.Sprintf("%s{uri=\"%s\",time_line=\"tls_handshake\"} %g\n",
				namespace, k, float64(r.(hs.Result).TLSHandshake))
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
	l := strings.Split(*dest, ",")
	for _, i := range l {
		_, _, _, ok := isURI(i)
		if ok {
			destList = append(destList, i)
		}
	}

	if len(destList) == 0 {
		panic("no one to ping")
	}

	doing = false
	doWork()
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