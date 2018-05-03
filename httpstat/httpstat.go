package httpstat

import (
	"sync"
	hs "github.com/tcnksm/go-httpstat"
	"time"
	"io"
	"io/ioutil"
	"net/http"
	"github.com/ximply/httpstat_exporter/cache"
	"os/exec"
	"os"
	"strings"
	"strconv"
	"fmt"
)

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

func Worker(uri string, wg *sync.WaitGroup, useCurl bool) {
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		wg.Done()
		return
	}

	var result hs.Result


	if useCurl {
        // curl -o /dev/null -s -w %{time_namelookup}:%{time_connect}:%{time_appconnect}
        // /usr/bin/curl
        path := "/usr/bin/curl"
		exist, _ := fileExists(path)
		if !exist {
			path, err = exec.LookPath("curl")
			if err != nil {
				wg.Done()
				return
			}
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
			time_appconnect := time_appconnect_tl * 1000000000 - time_connect - time_namelookup

			result.DNSLookup = time.Duration(time_namelookup) / time.Nanosecond
			result.TCPConnection = time.Duration(time_connect) / time.Nanosecond
			result.TLSHandshake = time.Duration(time_appconnect) / time.Nanosecond
		}
	} else {
		ctx := hs.WithHTTPStat(req.Context(), &result)
		req = req.WithContext(ctx)

		client := http.DefaultClient
		client.Timeout = 5 * time.Second
		res, err := client.Do(req)
		if err != nil {
			wg.Done()
			return
		}

		if _, err := io.Copy(ioutil.Discard, res.Body); err != nil {
			wg.Done()
			return
		}
		res.Body.Close()
		result.End(time.Now())
	}

	cache.GetInstance().Add(uri, 3 * time.Minute, result)
	wg.Done()
}