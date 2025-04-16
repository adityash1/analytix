package main

import (
	"context"
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

func corsMiddleware(next http.Handler) http.Handler {
	allowedOrigins := map[string]bool{
		"http://localhost:5173": true,
		"http://127.0.0.1:8081": true,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-KEY")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	flag.StringVar(&forceIP, "ip", "", "force IP for request, useful in local")
	flag.Parse()

	// Use TextHandler for development (more readable), JSONHandler for production
	// logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	tracker.LoadConfig()

	if err := events.Open(); err != nil {
		logger.Error("Failed to connect to ClickHouse", slog.Any("error", err))
		os.Exit(1)
	} else if err := events.EnsureTable(); err != nil {
		logger.Error("Failed to ensure ClickHouse table exists", slog.Any("error", err))
		os.Exit(1)
	}

	// Start the event processing loop
	eventsCtx, eventsCancel := context.WithCancel(context.Background())
	go events.Run(eventsCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/track", track)
	mux.HandleFunc("/stats", stats)

	corsHandler := corsMiddleware(mux)

	server := &http.Server{
		Addr:    ":9876",
		Handler: corsHandler,
	}

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

	<-stopChan

	logger.Info("Shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", slog.Any("error", err))
	}

	logger.Info("Stopping event processor...")
	eventsCancel() // Signal Run() to stop accepting new events via context cancellation

	events.WaitFlush()
	logger.Info("Event processor stopped.")

	logger.Info("Shutdown complete.")
}

func track(w http.ResponseWriter, r *http.Request) {
	requestLogger := logger.With(slog.String("path", r.URL.Path), slog.String("method", r.Method))

	var trk tracker.Tracking
	var err error

	if err := json.NewDecoder(r.Body).Decode(&trk); err != nil {
		requestLogger.Error("Failed to decode tracking data from request body", slog.Any("error", err))
		// Try to determine if the error is client-side (bad JSON) or server-side
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError
		if errors.As(err, &syntaxError) || errors.As(err, &unmarshalTypeError) {
			http.Error(w, "Bad Request: Invalid JSON format", http.StatusBadRequest)
		} else {
			http.Error(w, "Internal Server Error: Could not read request body", http.StatusInternalServerError)
		}
		return
	}

	ua := useragent.Parse(trk.Action.UserAgent)

	headers := []string{"X-Forward-For", "X-Real-IP"}
	ip, ipErr := tracker.IPFromRequest(headers, r, forceIP)
	if ipErr != nil {
		requestLogger.Error("Failed to get IP from request", slog.Any("error", ipErr))
		// Continue processing even if IP fails
	}

	var geoInfo *tracker.GeoInfo
	if ip != nil {
		geoInfo, err = tracker.GetGeoInfo(ip.String())
		if err != nil {
			requestLogger.Warn("Failed to get geo info", slog.Any("error", err), slog.String("ip", ip.String()))
			// Continue processing even if GeoIP fails
		}
	} else {
		requestLogger.Debug("Skipping geo lookup due to missing IP")
	}

	if len(trk.Action.Referrer) > 0 {
		u, parseErr := url.Parse(trk.Action.Referrer)
		if parseErr == nil {
			trk.Action.ReferrerHost = u.Host
		} else {
			requestLogger.Warn("Failed to parse referrer URL", slog.String("referrer", trk.Action.Referrer), slog.Any("error", parseErr))
		}
	}

	if len(trk.Action.Identity) == 0 {
		if ip != nil {
			trk.Action.Identity = fmt.Sprintf("%s-%s", ip.String(), trk.Action.UserAgent)
			requestLogger.Debug("Generated identity from IP and UserAgent", slog.String("identity", trk.Action.Identity))
		} else {
			trk.Action.Identity = fmt.Sprintf("unknown-%s", trk.Action.UserAgent)
			requestLogger.Debug("Generated identity from 'unknown' and UserAgent", slog.String("identity", trk.Action.Identity))
		}
	}

	// Send event for processing
	if err := events.Add(r.Context(), trk, ua, geoInfo); err != nil {
		requestLogger.Error("Failed to add event to queue", slog.Any("error", err))
		http.Error(w, "Internal Server Error: Could not process event", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	requestLogger.Debug("Event tracked successfully")
}

func stats(w http.ResponseWriter, r *http.Request) {
	requestLogger := logger.With(slog.String("path", r.URL.Path))

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

	metrics, err := events.GetStats(r.Context(), data)
	if err != nil {
		requestLogger.Error("Failed to get stats from database", slog.Any("error", err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(metrics); err != nil {
		requestLogger.Error("Failed to encode stats response", slog.Any("error", err))
		return
	}
}
