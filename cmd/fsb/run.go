package main

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/routes"
	"EverythingSuckz/fsb/internal/types"
	"EverythingSuckz/fsb/internal/utils"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var runCmd = &cobra.Command{
	Use:                "run",
	Short:              "Run the bot with the given configuration.",
	DisableSuggestions: false,
	Run:                runApp,
}

var startTime time.Time = time.Now()

func runApp(cmd *cobra.Command, args []string) {
	// Initialize logger with default settings first
	utils.InitLogger(false, "info")
	log := utils.Logger
	mainLogger := log.Named("Main")
	mainLogger.Info("Starting server")
	config.Load(log, cmd)

	// Re-initialize logger with actual config values
	utils.InitLogger(config.ValueOf.Dev, config.ValueOf.LogLevel)
	log = utils.Logger
	mainLogger = log.Named("Main")

	// Create main router for file streaming
	router := getRouter(log)

	// Create separate router for status monitoring
	statusRouter := getStatusRouter(log)

	mainBot, err := bot.StartClient(log)
	if err != nil {
		log.Panic("Failed to start main bot", zap.Error(err))
	}
	cache.InitCache(log)
	workers, err := bot.StartWorkers(log)
	if err != nil {
		log.Panic("Failed to start workers", zap.Error(err))
		return
	}
	workers.AddDefaultClient(mainBot, mainBot.Self)
	bot.StartUserBot(log)

	mainLogger.Info("Server started", zap.Int("mainPort", config.ValueOf.Port), zap.Int("statusPort", config.ValueOf.StatusPort))
	mainLogger.Info("File Stream Bot", zap.String("version", versionString))
	mainLogger.Sugar().Infof("Main server is running at %s", config.ValueOf.Host)
	mainLogger.Sugar().Infof("Status server is running at http://0.0.0.0:%d/status", config.ValueOf.StatusPort)

	// Start status server in a goroutine
	go func() {
		statusLogger := log.Named("StatusServer")
		statusLogger.Info("Starting status server", zap.Int("port", config.ValueOf.StatusPort))
		err := statusRouter.Run(fmt.Sprintf(":%d", config.ValueOf.StatusPort))
		if err != nil {
			statusLogger.Sugar().Fatalln("Failed to start status server:", err)
		}
	}()

	// Start main server (blocking)
	err = router.Run(fmt.Sprintf(":%d", config.ValueOf.Port))
	if err != nil {
		mainLogger.Sugar().Fatalln(err)
	}
}

func getRouter(log *zap.Logger) *gin.Engine {
	if config.ValueOf.Dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Disable GIN default logger if log level is error or warn
	var router *gin.Engine
	if config.ValueOf.LogLevel == "error" || config.ValueOf.LogLevel == "warn" {
		router = gin.New()
		router.Use(gin.Recovery())
		router.Use(gin.ErrorLogger())
	} else {
		router = gin.Default()
		router.Use(gin.ErrorLogger())
	}

	router.GET("/", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, types.RootResponse{
			Message: "Server is running.",
			Ok:      true,
			Uptime:  utils.TimeFormat(uint64(time.Since(startTime).Seconds())),
			Version: versionString,
		})
	})
	routes.Load(log, router)
	return router
}

func getStatusRouter(log *zap.Logger) *gin.Engine {
	if config.ValueOf.Dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create a minimal router for status only
	var router *gin.Engine
	if config.ValueOf.LogLevel == "error" || config.ValueOf.LogLevel == "warn" {
		router = gin.New()
		router.Use(gin.Recovery())
	} else {
		router = gin.Default()
	}

	// Only load the status route
	routes.LoadStatusOnly(log, router)

	return router
}
