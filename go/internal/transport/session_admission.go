package transport

import (
	"context"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Credentials and devices bind individual sessions. They share a capacity
// budget only when they authenticate as the same principal.
type sessionOwner struct {
	source, principal string
}

func ownerOfSession(id wire.Identity) sessionOwner {
	return sessionOwner{source: id.Source, principal: id.PrincipalID}
}

type initReservation struct {
	ctx   context.Context
	actor wire.Identity
}

type admissionState int

const (
	admissionOK admissionState = iota
	admissionCapacity
	admissionDraining
)

// reserveInitialize makes room before the SDK allocates its session. The old
// session and its slot change hands under the same lock as active-request pins.
func (s *Server) reserveInitialize(ctx context.Context, actor wire.Identity) (*initReservation, admissionState) {
	limit := s.Config.Int("maxHttpSessions", 64)
	perOwner := limit
	if s.Config.String("principalsFile", "") != "" {
		perOwner = max(1, limit/4)
	}
	owner := ownerOfSession(actor)

	s.mu.Lock()
	if s.draining || s.ctx.Err() != nil || ctx.Err() != nil {
		s.mu.Unlock()
		return nil, admissionDraining
	}
	owned := 0
	var oldestID string
	var oldest *sessionRecord
	for id, record := range s.sessions {
		if ownerOfSession(record.identity) != owner {
			continue
		}
		owned++
		if record.active == 0 && (oldest == nil || record.lastSeen.Before(oldest.lastSeen) ||
			(record.lastSeen.Equal(oldest.lastSeen) && id < oldestID)) {
			oldestID, oldest = id, record
		}
	}
	for pending := range s.pending {
		if ownerOfSession(pending.actor) == owner {
			owned++
		}
	}
	full := owned >= perOwner || len(s.sessions)+len(s.pending) >= limit
	if full {
		if oldest == nil {
			s.mu.Unlock()
			return nil, admissionCapacity
		}
		delete(s.sessions, oldestID)
	}
	r := &initReservation{ctx: ctx, actor: actor}
	if s.pending == nil {
		s.pending = make(map[*initReservation]struct{})
	}
	s.pending[r] = struct{}{}
	s.mu.Unlock()

	if full {
		s.closeDetachedSession(oldestID, oldest)
	}
	return r, admissionOK
}

func (s *Server) releaseInitialize(r *initReservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, r)
}

// Publish before the SDK sends success, so the next bound request can use
// the ID immediately. HTTP completion releases the initial active pin.
func (s *Server) publishInitialize(r *initReservation, id string, actor wire.Identity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[r]; !ok || s.draining || s.ctx.Err() != nil || r.ctx.Err() != nil ||
		id == "" || s.sessions[id] != nil || !sameBinding(r.actor, actor) {
		return false
	}
	delete(s.pending, r)
	lifetime, cancel := context.WithCancel(s.ctx)
	s.sessions[id] = &sessionRecord{identity: actor, lastSeen: time.Now(), active: 1, ctx: lifetime, cancel: cancel}
	return true
}

func (s *Server) closeSDKSession(id string) {
	for session := range s.SDK.Sessions() {
		if session.ID() == id {
			_ = session.Close()
			return
		}
	}
}

// Only the goroutine that detached the record calls the engine's close hook.
func (s *Server) closeDetachedSession(id string, record *sessionRecord) {
	record.cancel()
	s.closeSDKSession(id)
	if s.closeSession != nil {
		s.closeSession(wire.Scope{Identity: record.identity, PrincipalID: s.resourceOwnerKey(record.identity), SessionID: id, TransportID: id})
	}
}
