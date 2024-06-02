package tracker

type TrackingData struct {
	Type          string `json:"type"`
	Identity      string `json:"identity"`
	UserAgent     string `json:"ua"`
	Event         string `json:"event"`
	Category      string `json:"category"`
	Referrer      string `json:"referrer"`
	ReferrerHost  string
	IsTouchDevice bool `json:"isTouchDevice"`
	OccuredAt     uint32
}

type Tracking struct {
	SiteID string       `json:"site_id"`
	Action TrackingData `json:"tracking"`
}

type GeoInfo struct {
	IP         string  `json:"ip"`
	Country    string  `json:"country"`
	CountryISO string  `json:"country_iso"`
	RegionName string  `json:"region_name"`
	RegionCode string  `json:"region_code"`
	City       string  `json:"city"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
}

type Metric struct {
	OccuredAt uint32 `json:"occuredAt"`
	Value     string `json:"value"`
	Count     uint64 `json:"count"`
}

type MetricData struct {
	What   QueryType `json:"what"`
	SiteID string    `json:"siteId"`
	Start  uint32    `json:"start"`
	End    uint32    `json:"end"`
	Extra  string    `json:"extra"`
}
