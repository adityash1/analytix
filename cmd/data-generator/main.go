package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"time"
)

type TrackingData struct {
	Type          string `json:"type"`
	Identity      string `json:"identity"` // Will be auto-generated if empty
	UserAgent     string `json:"ua"`
	Event         string `json:"event"`
	Category      string `json:"category"`
	Referrer      string `json:"referrer"`
	ReferrerHost  string `json:"-"` // Ignored in JSON, filled by server
	IsTouchDevice bool   `json:"isTouchDevice"`
	OccuredAt     uint32 `json:"-"` // Ignored in JSON, set by server
}

type Tracking struct {
	SiteID string       `json:"site_id"`
	Action TrackingData `json:"tracking"`
}

var (
	logger *slog.Logger
)

// --- Data Generation Helpers ---

var sites = []string{"siteA", "siteB", "cool-blog", "news-corp"}
var eventTypes = []string{"pageview", "click", "form_submit", "video_play"}
var categories = map[string][]string{
	"pageview":    {"/", "/about", "/contact", "/products/1", "/products/2", "/blog/post-1"},
	"click":       {"cta_button", "nav_link", "footer_link", "product_image"},
	"form_submit": {"contact_form", "newsletter_signup", "login_form"},
	"video_play":  {"intro_video", "product_demo"},
}
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4.1 Safari/605.1.15",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 13; SM-G991U) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0",
}
var referrers = []string{
	"https://www.google.com/",
	"https://www.bing.com/",
	"https://duckduckgo.com/",
	"https://t.co/", // Twitter
	"https://www.facebook.com/",
	"", // Direct visit
	"",
	"https://news.ycombinator.com/",
}

func randomElement(slice []string) string {
	if len(slice) == 0 {
		return ""
	}
	return slice[rand.Intn(len(slice))]
}

func generateEvent() Tracking {
	site := randomElement(sites)
	eventType := randomElement(eventTypes)
	category := ""
	possibleCategories := categories[eventType]
	if len(possibleCategories) > 0 {
		category = randomElement(possibleCategories)
	}

	// For pageviews, event name is usually the path
	eventName := category
	if eventType != "pageview" {
		eventName = eventType + "_" + category // Simple event name
	}

	// Simulate ~10% touch devices
	isTouch := rand.Intn(10) == 0

	// Generate a somewhat unique identity stub (server completes it with IP)
	identityStub := fmt.Sprintf("user-%d", rand.Intn(1000)) // Simulate ~1000 unique users

	return Tracking{
		SiteID: site,
		Action: TrackingData{
			Type:          eventType,
			Identity:      identityStub, // Server uses this + IP if provided
			UserAgent:     randomElement(userAgents),
			Event:         eventName,
			Category:      category,
			Referrer:      randomElement(referrers),
			IsTouchDevice: isTouch,
		},
	}
}

func main() {
	// --- Logger Setup ---
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)
	// --- ---

	// --- Flags ---
	trackerURL := flag.String("url", "http://localhost:9876/track", "Tracker /track endpoint URL")
	numEvents := flag.Int("n", 100, "Number of events to send")
	delayMs := flag.Int("delay", 100, "Delay between requests in milliseconds")
	flag.Parse()
	// --- ---

	rand.New(rand.NewSource(time.Now().UnixNano())) // Seed random number generator

	logger.Info("Starting data generator",
		slog.String("targetUrl", *trackerURL),
		slog.Int("count", *numEvents),
		slog.Int("delayMs", *delayMs))

	client := &http.Client{
		Timeout: 5 * time.Second, // Add a timeout to requests
	}
	delay := time.Duration(*delayMs) * time.Millisecond
	successCount := 0
	errorCount := 0

	for i := 0; i < *numEvents; i++ {
		event := generateEvent()

		jsonData, err := json.Marshal(event)
		if err != nil {
			logger.Error("Failed to marshal event to JSON", slog.Any("error", err), slog.Int("eventIndex", i))
			errorCount++
			continue // Skip this event
		}

		encodedData := base64.StdEncoding.EncodeToString(jsonData)

		// Construct URL with query parameter
		target, err := url.Parse(*trackerURL)
		if err != nil {
			logger.Error("Invalid tracker URL provided", slog.String("url", *trackerURL), slog.Any("error", err))
			return // Fatal error if URL is bad
		}
		query := target.Query()
		query.Set("data", encodedData)
		target.RawQuery = query.Encode()

		req, err := http.NewRequest("GET", target.String(), nil)
		if err != nil {
			logger.Error("Failed to create HTTP request", slog.Any("error", err), slog.Int("eventIndex", i))
			errorCount++
			continue
		}
		// Optional: Add headers if needed, e.g., req.Header.Add("User-Agent", "DataGenerator/1.0")

		resp, err := client.Do(req)
		if err != nil {
			logger.Error("Failed to send request to tracker", slog.Any("error", err), slog.Int("eventIndex", i))
			errorCount++
		} else {
			if resp.StatusCode == http.StatusOK {
				logger.Debug("Event sent successfully", slog.Int("eventIndex", i), slog.String("siteId", event.SiteID), slog.String("type", event.Action.Type))
				successCount++
			} else {
				logger.Warn("Tracker responded with non-OK status", slog.Int("statusCode", resp.StatusCode), slog.Int("eventIndex", i))
				// You might want to read resp.Body here for more details in case of errors
				errorCount++
			}
			resp.Body.Close() // Important to close the body
		}

		if i < *numEvents-1 {
			time.Sleep(delay)
		}
	}

	logger.Info("Data generation complete",
		slog.Int("successCount", successCount),
		slog.Int("errorCount", errorCount))
}
