package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

type logRecord struct {
	Level      int            `json:"level"`
	Time       time.Time      `json:"time"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes"`
}

type Handler struct {
	level     slog.Level
	queue     []slog.Record
	mutex     sync.Mutex
	batchSize int
	timeout   time.Duration
	subject   string
	natsConn  *nats.Conn
	stop      chan struct{}
	waitGroup sync.WaitGroup
}

type Options struct {
	Level     slog.Level
	BatchSize int
	Timeout   time.Duration
	NATSURL   string
	Subject   string
}

func New(opts Options) (*Handler, error) {
	natsConn, err := nats.Connect(opts.NATSURL)
	if err != nil {
		return nil, err
	}
	h := &Handler{
		level:     opts.Level,
		queue:     make([]slog.Record, 0, opts.BatchSize),
		batchSize: opts.BatchSize,
		timeout:   opts.Timeout,
		subject:   opts.Subject,
		natsConn:  natsConn,
		stop:      make(chan struct{}),
	}
	h.waitGroup.Add(1)
	go h.worker()
	return h, nil
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	h.mutex.Lock()
	h.queue = append(h.queue, r)
	shouldFlush := len(h.queue) >= h.batchSize
	h.mutex.Unlock()

	if shouldFlush {
		h.flush()
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return h
}

func (h *Handler) flush() {
	h.mutex.Lock()
	if len(h.queue) == 0 {
		h.mutex.Unlock()
		return
	}
	records := h.queue
	h.queue = make([]slog.Record, 0, h.batchSize)
	h.mutex.Unlock()

	entries := make([]logRecord, len(records))
	for i, r := range records {
		attrs := make(map[string]any)
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		entries[i] = logRecord{
			Level:      int(r.Level),
			Time:       r.Time,
			Message:    r.Message,
			Attributes: attrs,
		}
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return
	}

	go func() {
		h.natsConn.Publish(h.subject, data)
	}()
}

func (h *Handler) worker() {
	defer h.waitGroup.Done()
	ticker := time.NewTicker(h.timeout)
	defer ticker.Stop()

	for {
		select {
		case <-h.stop:
			h.flush()
			return
		case <-ticker.C:
			h.flush()
		}
	}
}

func (h *Handler) Close() error {
	close(h.stop)
	h.waitGroup.Wait()
	h.natsConn.Flush()
	h.natsConn.Close()
	return nil
}
