package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
)

func main() {
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
	fmt.Println("site id", trk)
}

func decodeData(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
