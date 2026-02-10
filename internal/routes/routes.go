package routes

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/streamauth"
	"reflect"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type Route struct {
	Name   string
	Engine *gin.Engine
}

func (r *Route) Init(engine *gin.Engine) {
	r.Engine = engine
}

type allRoutes struct {
	log        *zap.Logger
	streamAuth *streamauth.Service
}

func Load(log *zap.Logger, r *gin.Engine) {
	log = log.Named("routes")
	defer log.Sugar().Info("Loaded all API Routes")

	streamAuthService, err := streamauth.NewService(log, streamauth.ServiceOptions{
		FirebaseProjectID: config.ValueOf.FirebaseProjectID,
		FirebaseCertsURL:  config.ValueOf.FirebaseCertsURL,
		SessionTTL:        time.Duration(config.ValueOf.StreamSessionTTLSeconds) * time.Second,
		CleanupInterval:   time.Duration(config.ValueOf.StreamSessionCleanupSeconds) * time.Second,
		CookieName:        config.ValueOf.StreamSessionCookieName,
		CookieSecure:      config.ValueOf.StreamSessionCookieSecure,
		CookieDomain:      config.ValueOf.StreamSessionCookieDomain,
	})
	if err != nil {
		log.Fatal("Failed to initialize stream authentication", zap.Error(err))
	}

	route := &Route{Name: "/", Engine: r}
	route.Init(r)
	all := &allRoutes{
		log:        log,
		streamAuth: streamAuthService,
	}
	Type := reflect.TypeOf(all)
	Value := reflect.ValueOf(all)
	for i := 0; i < Type.NumMethod(); i++ {
		Type.Method(i).Func.Call([]reflect.Value{Value, reflect.ValueOf(route)})
	}
}

// LoadStatusOnly loads only the status route on a separate router
// This is used for the dedicated status server on a different port
func LoadStatusOnly(log *zap.Logger, r *gin.Engine) {
	log = log.Named("routes")
	defer log.Sugar().Info("Loaded status route")
	route := &Route{Name: "/", Engine: r}
	route.Init(r)
	allRoutes := &allRoutes{log: log}
	allRoutes.LoadStatus(route)
}
