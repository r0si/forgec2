package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

type EventType string

const (
	EventImplantCheckin    EventType = "implant.checkin"
	EventImplantDisconnect EventType = "implant.disconnect"
	EventTaskComplete      EventType = "task.complete"
	EventTaskFail          EventType = "task.fail"
	EventCredentialFound   EventType = "credential.found"
	EventAlertTriggered    EventType = "alert.triggered"
	// Build outcomes are events because a failed build is a thing an operator wants told about, not a thing they discover by
	// refreshing a page. The payload carries the hint code, so a rule can route on the CAUSE rather than on the message text.
	EventBuildFailed    EventType = "build.failed"
	EventBuildSucceeded EventType = "build.succeeded"
)

type Event struct {
	Type      EventType
	AgentID   string
	AgentHost string
	AgentIP   string
	AgentOS   string
	// TenantID attributes the event to a tenant for automation containment
	// (rule firing + action targeting + script reach). 0 is legacy global.
	TenantID  uint
	TaskID    uint
	User      string
	Timestamp time.Time
	Data      map[string]interface{}
}

type EventHandler func(Event)

const maxConcurrentEventHandlers = 32
const eventQueueSize = 256

type EventManager struct {
	mu         sync.RWMutex
	handlers   map[EventType][]EventHandler
	db         *gorm.DB
	workerSem  chan struct{}
	eventQueue chan Event
	ctx        context.Context
	cancel     context.CancelFunc

	// dropCounter, when set, counts silently-dropped internal events by
	// reason (queue_full, handler_saturated). Wired by the server after
	// metrics init; nil in unit tests.
	dropCounter *prometheus.CounterVec
}

func NewEventManager(database *gorm.DB) *EventManager {
	ctx, cancel := context.WithCancel(context.Background())
	em := &EventManager{
		handlers:   make(map[EventType][]EventHandler),
		db:         database,
		workerSem:  make(chan struct{}, maxConcurrentEventHandlers),
		eventQueue: make(chan Event, eventQueueSize),
		ctx:        ctx,
		cancel:     cancel,
	}
	go em.queueReader()
	return em
}

func (em *EventManager) Shutdown() {
	em.cancel()
}

// SetDropCounter wires the drop metric. Safe to call once at startup.
func (em *EventManager) SetDropCounter(c *prometheus.CounterVec) {
	em.dropCounter = c
}

func (em *EventManager) noteDrop(reason string) {
	if em.dropCounter != nil {
		em.dropCounter.WithLabelValues(reason).Inc()
	}
}

func (em *EventManager) On(et EventType, handler EventHandler) {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.handlers[et] = append(em.handlers[et], handler)
}

func (em *EventManager) Emit(evt Event) {
	select {
	case <-em.ctx.Done():
		return
	case em.eventQueue <- evt:
	default:
		em.noteDrop("queue_full")
		slog.Warn("Event queue full, dropping event", "type", evt.Type)
	}
}

func (em *EventManager) queueReader() {
	for {
		select {
		case <-em.ctx.Done():
			return
		case evt := <-em.eventQueue:
			em.mu.RLock()
			handlers := make([]EventHandler, len(em.handlers[evt.Type]))
			copy(handlers, em.handlers[evt.Type])
			em.mu.RUnlock()

			for _, h := range handlers {
				select {
				case <-em.ctx.Done():
					return
				case em.workerSem <- struct{}{}:
					go func(handler EventHandler) {
						defer func() {
							<-em.workerSem
							if r := recover(); r != nil {
								slog.Error("Event handler panic", "panic", r)
							}
						}()
						handler(evt)
					}(h)
				default:
					em.noteDrop("handler_saturated")
					slog.Warn("Event handler worker saturated, dropping event", "type", evt.Type)
				}
			}
		}
	}
}
