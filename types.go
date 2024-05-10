package main

type TrackingData struct {
	Type          string `json:"type"`
	Identity      string `json:"identity"`
	UserAgent     string `json:"ua"`
	Event         string `json:"event"`
	Category      string `json:"category"`
	Referrer      string `json:"referrer"`
	IsTouchDevice bool   `json:"isTouchDevice"`
	OccuredAt     uint32
}

type Tracking struct {
	SiteID string       `json:"site_id"`
	Action TrackingData `json:"tracking"`
}
