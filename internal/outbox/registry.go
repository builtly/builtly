package outbox

import (
	"context"
	"sync"
)

// Handler processes one claimed outbox row. payload is decrypted JSON.
type Handler func(ctx context.Context, row Row, payload []byte) error

// Registry maps event types to subscriber names and handlers by subscriber.
type Registry struct {
	mu          sync.RWMutex
	subscribers map[string][]string // event type → subscriber names
	handlers    map[string]Handler  // subscriber → handler
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		subscribers: map[string][]string{},
		handlers:    map[string]Handler{},
	}
}

// DefaultSubscriptions registers the v1 event → subscriber fan-out table.
func (r *Registry) DefaultSubscriptions() {
	r.Subscribe(EventUserRegistered, SubAnalytics, SubEmail)
	r.Subscribe(EventEmailRequested, SubEmail)
	r.Subscribe(EventPurchaseCompleted, SubAnalytics, SubAffiliate, SubReferral)
	r.Subscribe(EventPurchaseRefunded, SubAnalytics, SubAffiliate, SubReferral)
	r.Subscribe(EventReferralRewarded, SubAnalytics)
	r.Subscribe(EventCreditsConsumed, SubAnalytics)
	r.Subscribe(EventUserDeleted, SubAppleRevoke, SubStoragePurge, SubAnalytics)
}

// Subscribe adds subscriber names for an event type (publish-time fan-out).
func (r *Registry) Subscribe(eventType string, subscribers ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers[eventType] = append(append([]string{}, r.subscribers[eventType]...), subscribers...)
}

// SetHandler registers the delivery handler for a subscriber name.
func (r *Registry) SetHandler(subscriber string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[subscriber] = h
}

// SubscribersFor returns subscriber names for an event type.
func (r *Registry) SubscribersFor(eventType string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string{}, r.subscribers[eventType]...)
}

// HandlerFor returns the handler for a subscriber, if any.
func (r *Registry) HandlerFor(subscriber string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[subscriber]
	return h, ok
}
