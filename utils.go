package tracker

import (
	"log"
	"strconv"
	"time"
)

func TimeToInt(d time.Time) uint32 {
	now := d.Format("20240102")
	i, err := strconv.ParseInt(now, 10, 32)
	if err != nil {
		log.Fatal(err)
	}
	return uint32(i)
}
