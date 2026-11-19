// Package usage persists the gateway's per-call usage ledger and answers summaries over it.
package usage

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

// Ledger writes usage records asynchronously in batches. Record never blocks a request: when the
// queue is full the record is dropped and counted.
type Ledger struct {
	db        *gorm.DB
	ch        chan models.UsageRecord
	retention time.Duration
	observers []func(models.UsageRecord)
	dropped   atomic.Int64
	wg        sync.WaitGroup
	closeOnce sync.Once
	done      chan struct{}
	flushReq  chan chan struct{}
}

// NewLedger starts a ledger over db keeping records for retention (0 = forever).
func NewLedger(db *gorm.DB, retention time.Duration) *Ledger {
	l := &Ledger{db: db, ch: make(chan models.UsageRecord, 4096), retention: retention,
		done: make(chan struct{}), flushReq: make(chan chan struct{})}
	l.wg.Add(1)
	go l.run()
	return l
}

// Observe registers fn to see every record synchronously as it is recorded (spend tracking).
// Call before the ledger is used concurrently.
func (l *Ledger) Observe(fn func(models.UsageRecord)) {
	if l != nil && fn != nil {
		l.observers = append(l.observers, fn)
	}
}

// Record enqueues rec (TS defaults to now).
func (l *Ledger) Record(rec models.UsageRecord) {
	if l == nil {
		return
	}
	if rec.TS == 0 {
		rec.TS = time.Now().UnixMilli()
	}
	for _, fn := range l.observers {
		fn(rec)
	}
	select {
	case l.ch <- rec:
	default:
		if n := l.dropped.Add(1); n == 1 || n%1000 == 0 {
			log.Printf("⚠️ usage ledger queue full: %d records dropped", n)
		}
	}
}

// Dropped returns how many records were dropped because the queue was full.
func (l *Ledger) Dropped() int64 { return l.dropped.Load() }

func (l *Ledger) run() {
	defer l.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	prune := time.NewTicker(6 * time.Hour)
	defer prune.Stop()
	l.prune()
	batch := make([]models.UsageRecord, 0, 256)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := l.db.CreateInBatches(batch, 256).Error; err != nil {
			log.Printf("⚠️ usage ledger write failed (%d records): %v", len(batch), err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case rec := <-l.ch:
			batch = append(batch, rec)
			if len(batch) >= 256 {
				flush()
			}
		case <-tick.C:
			flush()
		case <-prune.C:
			l.prune()
		case ack := <-l.flushReq:
			for drained := false; !drained; {
				select {
				case rec := <-l.ch:
					batch = append(batch, rec)
				default:
					drained = true
				}
			}
			flush()
			close(ack)
		case <-l.done:
			for {
				select {
				case rec := <-l.ch:
					batch = append(batch, rec)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (l *Ledger) prune() {
	if l.retention <= 0 {
		return
	}
	cutoff := time.Now().Add(-l.retention).UnixMilli()
	if res := l.db.Where("ts < ?", cutoff).Delete(&models.UsageRecord{}); res.Error != nil {
		log.Printf("⚠️ usage ledger prune failed: %v", res.Error)
	}
}

// Flush writes everything queued so far before returning.
func (l *Ledger) Flush() {
	if l == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case l.flushReq <- ack:
		<-ack
	case <-l.done:
	}
}

// Close drains the queue and stops the writer.
func (l *Ledger) Close(ctx context.Context) {
	if l == nil {
		return
	}
	l.closeOnce.Do(func() { close(l.done) })
	waited := make(chan struct{})
	go func() { l.wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-ctx.Done():
	}
}
