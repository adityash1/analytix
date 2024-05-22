package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/mileusna/useragent"
)

var (
	events *Events = NewEvents()
)

func main() {
	if err := events.Open(); err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/track", track)
	http.ListenAndServe(":9876", nil)
}

func track(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)

	data := r.URL.Query().Get("data")
	trk, err := decodeData(data)
	if err != nil {
		fmt.Print(err)
	}

	ua := useragent.Parse(trk.Action.UserAgent)

	headers := []string{"X-Forwarded-For", "X-Real-IP"}
	ip, err := ipFromRequest(headers, r)
	if err != nil {
		fmt.Println("error getting IP: ", err)
		return
	}

	geoInfo, err := getGeoInfo(ip.String())
	if err != nil {
		fmt.Println("error getting geo info: ", err)
		return
	}

	if err := events.Add(trk, ua, geoInfo); err != nil {
		fmt.Println(err)
	}
}

func decodeData(s string) (data Tracking, err error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return
	}

	err = json.Unmarshal(b, &data)
	return
}
