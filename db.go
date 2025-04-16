package tracker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/mileusna/useragent"
)

type QueryType int

const (
	QueryPageViews QueryType = iota
	QueryPageViewList
	QueryUniqueVisitors
	QueryReferrerHost
	QueryReferrer
	QueryBrowsers
	QueryOSes
	QueryCountry
)

type qdata struct {
	trk Tracking
	ua  useragent.UserAgent
	geo *GeoInfo
}

type Events struct {
	DB   driver.Conn
	ch   chan qdata
	lock sync.RWMutex
	q    []qdata
	wg   sync.WaitGroup
	log  *slog.Logger
}

func (e *Events) Open() error {
	// Use default logger set in main
	e.log = slog.Default().With(slog.String("component", "Events"))

	ctx := context.Background()
	options := &clickhouse.Options{
		Addr: []string{config.ClickHouseHost},
		Auth: clickhouse.Auth{
			Database: config.ClickHouseDB,
			Username: config.ClickHouseUser,
			Password: config.ClickHousePassword,
		},
		Debug: true,
		Debugf: func(format string, v ...any) {
			e.log.Debug(fmt.Sprintf(format, v...))
		},
		Settings: clickhouse.Settings{
			"max_execution_time": 60,
		},
		Compression: &clickhouse.Compression{
			Method: clickhouse.CompressionLZ4,
		},
		DialTimeout:          time.Second * 30,
		MaxOpenConns:         5,
		MaxIdleConns:         5,
		ConnMaxLifetime:      time.Duration(10) * time.Minute,
		ConnOpenStrategy:     clickhouse.ConnOpenInOrder,
		BlockBufferSize:      10,
		MaxCompressionBuffer: 10240,
		ClientInfo: clickhouse.ClientInfo{
			Products: []struct {
				Name    string
				Version string
			}{
				{Name: "analytics-go", Version: "0.2"},
			},
		},
	}

	conn, err := clickhouse.Open(options)
	if err != nil {
		return fmt.Errorf("failed to open clickhouse connection: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		if exception, ok := err.(*clickhouse.Exception); ok {
			e.log.Error("ClickHouse connection ping failed (exception)",
				slog.Int("code", int(exception.Code)),
				slog.String("message", exception.Message),
				slog.String("stacktrace", exception.StackTrace))
		} else {
			e.log.Error("ClickHouse connection ping failed", slog.Any("error", err))
		}
		return fmt.Errorf("clickhouse ping failed: %w", err)
	}
	e.DB = conn
	e.log.Info("Successfully connected to ClickHouse")
	return nil
}

func (e *Events) EnsureTable() error {
	qry := `
		CREATE TABLE IF NOT EXISTS events (
			site_id String NOT NULL,
			occured_at UInt32 NOT NULL,
			type String NOT NULL,
			user_id String NOT NULL,
			event String NOT NULL,
			category String NOT NULL,
			referrer String NOT NULL,
			referrer_domain String NOT NULL,
			is_touch BOOLEAN NOT NULL,
			browser_name String NOT NULL,
			os_name String NOT NULL,
			device_type String NOT NULL,
			country String NOT NULL,
			region String NOT NULL,
			timestamp DateTime DEFAULT now()
		)
		ENGINE MergeTree
		ORDER BY (site_id, occured_at);
	`

	ctx := context.Background()
	err := e.DB.Exec(ctx, qry)
	if err != nil {
		e.log.Error("Failed to execute EnsureTable query", slog.Any("error", err))
		return fmt.Errorf("failed ensuring table: %w", err)
	}
	e.log.Debug("Events table ensured")
	return nil
}

func (e *Events) Add(ctx context.Context, trk Tracking, ua useragent.UserAgent, geo *GeoInfo) error {
	if geo == nil {
		geo = &GeoInfo{} // Use an empty struct to avoid nil pointer dereferences later
	}
	data := qdata{trk, ua, geo}

	select {
	case e.ch <- data:
		return nil
	case <-ctx.Done():
		e.log.Warn("Failed to add event: context cancelled", slog.Any("error", ctx.Err()))
		return ctx.Err()
		// Optional: Add a default case with a short timeout if you want to handle buffer full scenario
		// default:
		//  e.log.Warn("Failed to add event: channel buffer might be full")
		//  return errors.New("event channel buffer full or closed")
	}
}

// Run now accepts a context for cancellation
func (e *Events) Run(ctx context.Context) {
	e.wg.Add(1)
	defer e.wg.Done()

	e.ch = make(chan qdata, 100)
	flushInterval := 10 * time.Second
	maxBatchSize := 50
	timer := time.NewTimer(flushInterval)

	e.log.Info("Event processor started", slog.Duration("flushInterval", flushInterval), slog.Int("maxBatchSize", maxBatchSize))

	for {
		select {
		case data, ok := <-e.ch:
			if !ok {
				// Channel closed, means we are shutting down and no more data will come
				e.log.Info("Event channel closed, processing remaining buffered events before exit.")
				e.flushQueue() // Final flush
				return
			}

			e.lock.Lock()
			e.q = append(e.q, data)
			currentSize := len(e.q)
			e.lock.Unlock()

			// Reset timer if we add an item, avoids unnecessary timed flush right after batch flush
			if !timer.Stop() {
				<-timer.C // Drain timer if Stop() returned false
			}
			timer.Reset(flushInterval)

			if currentSize >= maxBatchSize {
				e.log.Debug("Flushing due to batch size limit", slog.Int("size", currentSize))
				e.flushQueue()
			}

		case <-timer.C:
			e.log.Debug("Flushing due to timer")
			e.flushQueue()
			timer.Reset(flushInterval) // Reset timer after flush

		case <-ctx.Done():
			e.log.Info("Shutdown signal received, stopping event processor.", slog.Any("reason", ctx.Err()))
			close(e.ch) // Close channel to signal no more adds
			// Drain remaining items from channel if any were sent after context cancel but before close
			// This might not be strictly necessary if Add checks context, but safer.
			for data := range e.ch {
				e.lock.Lock()
				e.q = append(e.q, data)
				e.lock.Unlock()
			}
			e.log.Info("Flushing final batch before exit.")
			e.flushQueue() // Final flush after draining channel
			return
		}
	}
}

// flushQueue extracts the current queue and calls Insert
// should only be called from Run() or internally where lock is managed
func (e *Events) flushQueue() {
	e.lock.Lock()
	if len(e.q) == 0 {
		e.lock.Unlock()
		return // Nothing to flush
	}
	// Copy buffer to temporary slice to minimize lock time
	tmp := make([]qdata, len(e.q))
	copy(tmp, e.q)
	e.q = e.q[:0] // Clear original slice while keeping capacity
	e.lock.Unlock()

	e.log.Debug("Attempting to insert batch", slog.Int("count", len(tmp)))
	if err := e.Insert(tmp); err != nil {
		e.log.Error("Error inserting event batch", slog.Any("error", err), slog.Int("failed_count", len(tmp)))
		// Consider adding retry logic or dead-letter queue here for production
	} else {
		e.log.Debug("Successfully inserted batch", slog.Int("count", len(tmp)))
	}
}

func (e *Events) Insert(batchData []qdata) error {
	if len(batchData) == 0 {
		return nil
	}

	// Use a background context for the insert itself, or potentially derive from a shutdown context if available
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	qry := `
		INSERT INTO events
		(
			site_id, occured_at, type, user_id, event, category,
			referrer, referrer_domain, is_touch, browser_name, os_name,
			device_type, country, region
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)
	`

	batch, err := e.DB.PrepareBatch(ctx, qry)
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, qd := range batchData {
		err := batch.Append(
			qd.trk.SiteID,
			TimeToInt(time.Now()), // Consider using occured_at from client if available/trustworthy
			qd.trk.Action.Type,
			qd.trk.Action.Identity,
			qd.trk.Action.Event,
			qd.trk.Action.Category,
			qd.trk.Action.Referrer,
			qd.trk.Action.ReferrerHost,
			qd.trk.Action.IsTouchDevice,
			qd.ua.Name,
			qd.ua.OS,
			qd.ua.Device,
			qd.geo.Country,
			qd.geo.RegionName,
		)
		if err != nil {
			// Abort maybe? Or just log and continue? For now, return error.
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	err = batch.Send()
	if err != nil {
		return fmt.Errorf("failed to send batch: %w", err)
	}
	return nil
}

// WaitFlush waits for the Run goroutine to finish processing.
func (e *Events) WaitFlush() {
	e.log.Debug("Waiting for event processor to flush and stop...")
	e.wg.Wait() // Wait for Run() goroutine to complete
	e.log.Debug("Event processor finished.")
}

func (e *Events) GetStats(ctx context.Context, data MetricData) ([]Metric, error) {
	qry := e.GenQuery(data)

	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	rows, err := e.DB.Query(
		queryCtx,
		qry,
		data.SiteID,
		data.Start,
		data.End,
		data.Extra, // Ensure GenQuery handles this parameter safely
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			e.log.Error("Stats query timed out", slog.Any("error", err))
			return nil, fmt.Errorf("stats query timed out: %w", err)
		}
		e.log.Error("Error executing stats query", slog.Any("error", err))
		return nil, fmt.Errorf("stats query failed: %w", err)
	}
	defer rows.Close()

	var metrics []Metric
	for rows.Next() {
		var m Metric
		// Assuming Metric struct fields match the query output order
		if err := rows.Scan(&m.OccuredAt, &m.Value, &m.Count); err != nil {
			e.log.Error("Error scanning stats row", slog.Any("error", err))
			return nil, fmt.Errorf("failed scanning stats row: %w", err) // Return partial results? For now, fail.
		}
		metrics = append(metrics, m)
	}

	if err := rows.Err(); err != nil {
		e.log.Error("Error after iterating stats rows", slog.Any("error", err))
		return metrics, fmt.Errorf("error iterating stats rows: %w", err) // Return processed metrics + error
	}

	e.log.Debug("Successfully retrieved stats", slog.Int("count", len(metrics)))
	return metrics, nil
}

func (e *Events) GenQuery(data MetricData) string {
	field := ""
	daily := true
	where := "AND $4 = $4"
	switch data.What {
	case QueryPageViews:
		field = "event"
	case QueryPageViewList:
		field = "event"
		daily = false
	case QueryUniqueVisitors:
		field = "user_id"
	case QueryReferrer:
		field = "referrer"
		where = "AND referrer_domain = $3 "
		daily = false
	case QueryReferrerHost:
		field = "referrer_domain"
		daily = false
	case QueryBrowsers:
		field = "browser_name"
		daily = false
	case QueryOSes:
		field = "os_name"
		daily = false
	case QueryCountry:
		field = "country"
		daily = false
	}

	if daily {
		return fmt.Sprintf(`
		SELECT occured_at, %s, COUNT(*)
		FROM events
		WHERE site_id = $1
		AND category = 'Page views'
		GROUP BY occured_at, %s
		HAVING occured_at BETWEEN $2 AND $3
		ORDER BY 3 DESC;
	`, field, field)
	}

	return fmt.Sprintf(`
		SELECT toUInt32(0), %s, COUNT(*)
		FROM events
		WHERE site_id = $1
		AND occured_at BETWEEN $2 AND $3
		AND category = 'Page views'
		%s 
		GROUP BY %s
		ORDER BY 3 DESC;
	`, field, where, field)
}
