package httpstat

import (
	"sync"
	hs "github.com/tcnksm/go-httpstat"
	"time"
	"io"
	"io/ioutil"
	"net/http"
	"github.com/ximply/httpstat_exporter/cache"
)

func Worker(uri string, wg *sync.WaitGroup) {
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		wg.Done()
		return
	}

	var result hs.Result
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

	cache.GetInstance().Add(uri, 3 * time.Minute, result)
	wg.Done()
}