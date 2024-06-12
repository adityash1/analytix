package tracker

import "os"

var config Config

func LoadConfig() {
	config = Config{
		APIKey:             os.Getenv("API_KEY"),
		EchoIPHost:         os.Getenv("ECHOIP_HOST"),
		ClickHouseHost:     os.Getenv("CLICKHOUSE_HOST"),
		ClickHouseDB:       os.Getenv("CLICKHOUSE_DB"),
		ClickHouseUser:     os.Getenv("CLICKHOUSE_USER"),
		ClickHousePassword: os.Getenv("CLICKHOUSE_PASSWORD"),
		GoTrackerHost:      os.Getenv("GOTRACKER_HOST"),
	}
}

func GetConfig() Config {
	return config
}
