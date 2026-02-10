package streamauth

import (
	"context"
	"time"

	"go.uber.org/zap"
)

type ServiceOptions struct {
	FirebaseProjectID string
	FirebaseCertsURL  string
	SessionTTL        time.Duration
	CleanupInterval   time.Duration
	CookieName        string
	CookieSecure      bool
	CookieDomain      string
}

type Service struct {
	enabled bool
	log     *zap.Logger

	verifier *firebaseVerifier
	sessions *sessionStore

	cookieName   string
	cookieSecure bool
	cookieDomain string
}

func NewService(log *zap.Logger, opts ServiceOptions) (*Service, error) {
	svc := &Service{
		log:          log.Named("StreamAuth"),
		enabled:      opts.FirebaseProjectID != "",
		cookieName:   opts.CookieName,
		cookieSecure: opts.CookieSecure,
		cookieDomain: opts.CookieDomain,
	}

	if svc.cookieName == "" {
		svc.cookieName = "fsb_stream_session"
	}

	if !svc.enabled {
		svc.log.Info("Firebase stream auth disabled (FIREBASE_PROJECT_ID not set)")
		return svc, nil
	}

	verifier, err := newFirebaseVerifier(log, opts.FirebaseProjectID, opts.FirebaseCertsURL)
	if err != nil {
		return nil, err
	}

	svc.verifier = verifier
	svc.sessions = newSessionStore(log, opts.SessionTTL, opts.CleanupInterval)
	svc.log.Info("Firebase stream auth enabled",
		zap.String("projectID", opts.FirebaseProjectID),
		zap.Duration("sessionTTL", opts.SessionTTL))

	return svc, nil
}

func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

func (s *Service) CookieName() string {
	return s.cookieName
}

func (s *Service) CookieSecure() bool {
	return s.cookieSecure
}

func (s *Service) CookieDomain() string {
	return s.cookieDomain
}

func (s *Service) VerifyFirebaseToken(ctx context.Context, token string) (*FirebaseClaims, error) {
	return s.verifier.VerifyToken(ctx, token)
}

func (s *Service) CreateSession(userID string, email string) (string, time.Time, error) {
	return s.sessions.Create(userID, email)
}

func (s *Service) ValidateSession(token string) (Session, bool) {
	return s.sessions.Validate(token)
}
