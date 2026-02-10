package routes

import (
	"EverythingSuckz/fsb/internal/streamauth"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// LoadFirebaseAuth registers the endpoint that exchanges Firebase ID tokens
// for short-lived stream session tokens.
func (e *allRoutes) LoadFirebaseAuth(r *Route) {
	authLog := e.log.Named("FirebaseAuth")
	if e.streamAuth == nil || !e.streamAuth.Enabled() {
		authLog.Info("Firebase auth route disabled")
		return
	}

	handler := getFirebaseExchangeRoute(authLog, e.streamAuth)
	r.Engine.POST("/auth/firebase/exchange", handler)
	r.Engine.GET("/auth/firebase/exchange", handler)
	authLog.Info("Loaded firebase auth exchange route")
}

func getFirebaseExchangeRoute(logger *zap.Logger, authService *streamauth.Service) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		bearerToken := extractBearerToken(ctx.GetHeader("Authorization"))
		if bearerToken == "" {
			ctx.JSON(http.StatusUnauthorized, gin.H{
				"error": "missing firebase bearer token",
			})
			return
		}

		claims, err := authService.VerifyFirebaseToken(ctx.Request.Context(), bearerToken)
		if err != nil {
			logger.Warn("Firebase token verification failed",
				zap.String("clientIP", ctx.ClientIP()),
				zap.Error(err))
			ctx.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid firebase token",
			})
			return
		}

		sessionToken, expiresAt, err := authService.CreateSession(claims.Subject, claims.Email)
		if err != nil {
			logger.Error("Failed to create stream session", zap.Error(err))
			ctx.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to create stream session",
			})
			return
		}

		maxAge := int(time.Until(expiresAt).Seconds())
		if maxAge < 0 {
			maxAge = 0
		}

		http.SetCookie(ctx.Writer, &http.Cookie{
			Name:     authService.CookieName(),
			Value:    sessionToken,
			Path:     "/",
			Domain:   authService.CookieDomain(),
			MaxAge:   maxAge,
			Expires:  expiresAt,
			HttpOnly: true,
			Secure:   authService.CookieSecure(),
			SameSite: http.SameSiteLaxMode,
		})

		ctx.Header("Cache-Control", "no-store")
		ctx.JSON(http.StatusOK, gin.H{
			"stream_token": sessionToken,
			"token_type":   "Bearer",
			"expires_at":   expiresAt.Unix(),
			"user_id":      claims.Subject,
			"email":        claims.Email,
		})
	}
}

func extractBearerToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 8 {
		return ""
	}
	if !strings.EqualFold(value[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}
