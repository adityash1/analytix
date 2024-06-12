package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"

	"tracker"

	"github.com/mileusna/useragent"
)

var (
	sites   map[string]string
	forceIP                 = ""
	events  *tracker.Events = &tracker.Events{}
)

func loadSites() error {
	b, err := os.ReadFile("sites.json")
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &sites)
}

func main() {
	flag.StringVar(&forceIP, "ip", "", "force IP for request, useful in local")
	flag.Parse()

	tracker.LoadConfig()

	if err := events.Open(); err != nil {
		log.Fatal(err)
	} else if err := events.EnsureTable(); err != nil {
		log.Fatal(err)
	}

	go events.Run()

	http.HandleFunc("/track", track)
	http.HandleFunc("/stats", stats)
	http.ListenAndServe(":9876", nil)
}

func track(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)

	data := r.URL.Query().Get("data")
	trk, err := decodeData(data)
	if err != nil {
		fmt.Print(err)
	}

	site, ok := sites[trk.SiteID]
	if !ok {
		return
	}

	trk.SiteID = site

	ua := useragent.Parse(trk.Action.UserAgent)

	headers := []string{"X-Forward-For", "X-Real-IP"}
	ip, err := tracker.IPFromRequest(headers, r, forceIP)
	if err != nil {
		fmt.Println("error getting IP: ", err)
		return
	}

	geoInfo, err := tracker.GetGeoInfo(ip.String())
	if err != nil {
		fmt.Println("error getting geo info: ", err)
		return
	}

	if len(trk.Action.Referrer) > 0 {
		u, err := url.Parse(trk.Action.Referrer)
		if err == nil {
			trk.Action.ReferrerHost = u.Host
		}
	}

	if len(trk.Action.Identity) == 0 {
		trk.Action.Identity = fmt.Sprintf("%s-%s", geoInfo.IP, trk.Action.UserAgent)
	}

	go events.Add(trk, ua, geoInfo)
}

func decodeData(s string) (data tracker.Tracking, err error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return
	}

	err = json.Unmarshal(b, &data)
	return
}

func stats(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-API-KEY")
	if key != tracker.GetConfig().APIKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var data tracker.MetricData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	metrics, err := events.GetStats(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	b, err := json.Marshal(metrics)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(b)
}
