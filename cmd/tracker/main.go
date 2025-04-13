package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tracker"

	"github.com/mileusna/useragent"
)

var (
	forceIP                 = ""
	events  *tracker.Events = &tracker.Events{}
	logger  *slog.Logger
)

func main() {
	flag.StringVar(&forceIP, "ip", "", "force IP for request, useful in local")
	flag.Parse()

	// --- Setup Logger ---
	// Use TextHandler for development (more readable), JSONHandler for production
	// logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)
	// --------------------

	// loadSites()
	tracker.LoadConfig()

	if err := events.Open(); err != nil {
		logger.Error("Failed to connect to ClickHouse", slog.Any("error", err))
		os.Exit(1)
	} else if err := events.EnsureTable(); err != nil {
		logger.Error("Failed to ensure ClickHouse table exists", slog.Any("error", err))
		os.Exit(1)
	}

	// Start the event processing loop
	eventsCtx, eventsCancel := context.WithCancel(context.Background()) // Context for event processor
	go events.Run(eventsCtx)

	// --- Setup HTTP Server ---
	mux := http.NewServeMux()
	mux.HandleFunc("/track", track)
	mux.HandleFunc("/stats", stats)

	server := &http.Server{
		Addr:    ":9876",
		Handler: mux,
	}
	// -------------------------

	// --- Graceful Shutdown Logic ---
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Info("Tracker server starting", slog.String("address", server.Addr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Server failed to start", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-stopChan

	logger.Info("Shutting down server...")

	// Create a deadline context for shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second) // 30-second timeout
	defer shutdownCancel()

	// Shutdown the HTTP server gracefully
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", slog.Any("error", err))
	}

	// Signal the event processor to stop and flush
	logger.Info("Stopping event processor...")
	eventsCancel() // Signal Run() to stop accepting new events via context cancellation

	// Wait for the event processor to flush remaining events (Requires modification in db.go)
	events.WaitFlush() // We'll need to add this WaitFlush method to tracker.Events
	logger.Info("Event processor stopped.")

	logger.Info("Shutdown complete.")
	// ---------------------------
}

func track(w http.ResponseWriter, r *http.Request) {
	requestLogger := logger.With(slog.String("path", r.URL.Path)) // Add context to logs

	data := r.URL.Query().Get("data")
	trk, err := decodeData(data)
	if err != nil {
		requestLogger.Error("Failed to decode tracking data", slog.Any("error", err), slog.String("rawData", data))
		http.Error(w, "Bad Request: Invalid data format", http.StatusBadRequest)
		return
	}

	ua := useragent.Parse(trk.Action.UserAgent)

	headers := []string{"X-Forward-For", "X-Real-IP"}
	ip, err := tracker.IPFromRequest(headers, r, forceIP)
	if err != nil {
		// Log internal error, but don't expose details to client unless necessary
		requestLogger.Error("Failed to get IP from request", slog.Any("error", err))
		// Depending on requirements, you might still proceed or return an error
		// For now, we'll proceed but log it. If GeoIP is critical, return 500 here.
	}

	var geoInfo *tracker.GeoInfo
	if ip == nil { // Only get GeoInfo if IP was found
		geoInfo, err = tracker.GetGeoInfo(ip.String())
		if err != nil {
			// Log the error, but often tracking can proceed without geo-info
			requestLogger.Warn("Failed to get geo info", slog.Any("error", err), slog.String("ip", ip.String()))
			// Don't return an error to the client for non-critical geo failures
		}
	} else {
		requestLogger.Warn("Skipping geo lookup due to missing IP")
	}

	if len(trk.Action.Referrer) > 0 {
		u, err := url.Parse(trk.Action.Referrer)
		if err == nil {
			trk.Action.ReferrerHost = u.Host
		} else {
			requestLogger.Warn("Failed to parse referrer URL", slog.String("referrer", trk.Action.Referrer), slog.Any("error", err))
		}
	}

	if len(trk.Action.Identity) == 0 && ip != nil { // Check ip is not nil before using
		trk.Action.Identity = fmt.Sprintf("%s-%s", ip.String(), trk.Action.UserAgent)
	} else if len(trk.Action.Identity) == 0 {
		// Fallback if IP is also missing
		trk.Action.Identity = fmt.Sprintf("unknown-%s", trk.Action.UserAgent)
	}

	// Send event for processing
	if err := events.Add(r.Context(), trk, ua, geoInfo); err != nil { // Pass request context, handle error from Add
		requestLogger.Error("Failed to add event to queue", slog.Any("error", err))
		// Decide if this is a 500 (server issue) or 503 (service unavailable)
		http.Error(w, "Internal Server Error: Could not process event", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK) // Send OK status *after* successfully adding
}

func decodeData(s string) (data tracker.Tracking, err error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return data, fmt.Errorf("base64 decode error: %w", err) // Wrap error
	}

	err = json.Unmarshal(b, &data)
	if err != nil {
		return data, fmt.Errorf("json unmarshal error: %w", err) // Wrap error
	}
	return data, nil
}

func stats(w http.ResponseWriter, r *http.Request) {
	requestLogger := logger.With(slog.String("path", r.URL.Path)) // Add context to logs

	key := r.Header.Get("X-API-KEY")
	if key != tracker.GetConfig().APIKey {
		requestLogger.Warn("Unauthorized stats access attempt")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var data tracker.MetricData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		requestLogger.Error("Failed to decode stats request body", slog.Any("error", err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Pass request context to GetStats
	metrics, err := events.GetStats(r.Context(), data)
	if err != nil {
		requestLogger.Error("Failed to get stats from database", slog.Any("error", err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError) // Avoid leaking DB errors
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(metrics); err != nil { // Use encoder for efficiency
		requestLogger.Error("Failed to encode stats response", slog.Any("error", err))
		// Don't write further errors if headers are already sent
		return
	}
}
