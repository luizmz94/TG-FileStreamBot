package config

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const (
	// Non-secret defaults (hardcoded in code as requested)
	defaultAPIID                     int32  = 0
	defaultLogChannelID              int64  = 0
	defaultMediaChannelID            int64  = 0
	defaultDev                       bool   = false
	defaultLogLevel                  string = "info"
	defaultPort                      int    = 8080
	defaultStatusPort                int    = 9090
	defaultHost                      string = ""
	defaultHashLength                int    = 6
	defaultUseSessionFile            bool   = true
	defaultUsePublicIP               bool   = false
	defaultFirebaseProjectID         string = "mediatg-16cbb"
	defaultFirebaseCertsURL          string = "https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"
	defaultStreamSessionTTLSeconds   int    = 28800
	defaultStreamSessionCleanupSecs  int    = 60
	defaultStreamSessionCookieName   string = "fsb_stream_session"
	defaultStreamSessionCookieSec    bool   = true
	defaultStreamSessionCookieDomain string = ""
	defaultStreamAllowLegacyHMAC     bool   = true
)

var ValueOf = &config{
	ApiID:                       defaultAPIID,
	LogChannelID:                defaultLogChannelID,
	MediaChannelID:              defaultMediaChannelID,
	Dev:                         defaultDev,
	LogLevel:                    defaultLogLevel,
	Port:                        defaultPort,
	StatusPort:                  defaultStatusPort,
	Host:                        defaultHost,
	HashLength:                  defaultHashLength,
	UseSessionFile:              defaultUseSessionFile,
	UsePublicIP:                 defaultUsePublicIP,
	FirebaseProjectID:           defaultFirebaseProjectID,
	FirebaseCertsURL:            defaultFirebaseCertsURL,
	StreamSessionTTLSeconds:     defaultStreamSessionTTLSeconds,
	StreamSessionCleanupSeconds: defaultStreamSessionCleanupSecs,
	StreamSessionCookieName:     defaultStreamSessionCookieName,
	StreamSessionCookieSecure:   defaultStreamSessionCookieSec,
	StreamSessionCookieDomain:   defaultStreamSessionCookieDomain,
	StreamAllowLegacyHMAC:       defaultStreamAllowLegacyHMAC,
}

type allowedUsers []int64

func (au *allowedUsers) Decode(value string) error {
	if value == "" {
		return nil
	}
	ids := strings.Split(string(value), ",")
	for _, id := range ids {
		idInt, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return err
		}
		*au = append(*au, idInt)
	}
	return nil
}

type config struct {
	ApiID                     int32        `envconfig:"API_ID" required:"true"`
	ApiHash                   string       `envconfig:"API_HASH" required:"true"`
	BotToken                  string       `envconfig:"BOT_TOKEN" required:"true"`
	LogChannelID              int64        `envconfig:"LOG_CHANNEL" required:"true"`
	MediaChannelID            int64        `envconfig:"MEDIA_CHANNEL_ID"`
	Dev                       bool         `envconfig:"DEV" default:"false"`
	LogLevel                  string       `envconfig:"LOG_LEVEL" default:"info"`
	Port                      int          `envconfig:"PORT" default:"8080"`
	StatusPort                int          `envconfig:"STATUS_PORT" default:"9090"`
	Host                      string       `envconfig:"HOST" default:""`
	HashLength                int          `envconfig:"HASH_LENGTH" default:"6"`
	UseSessionFile            bool         `envconfig:"USE_SESSION_FILE" default:"true"`
	UserSession               string       `envconfig:"USER_SESSION"`
	UsePublicIP               bool         `envconfig:"USE_PUBLIC_IP" default:"false"`
	AllowedUsers              allowedUsers `envconfig:"ALLOWED_USERS"`
	StreamSecret              string       `envconfig:"STREAM_SECRET"` // HMAC secret for /direct route authentication
	WorkerStartTimeoutSeconds int          `envconfig:"WORKER_START_TIMEOUT_SECONDS" default:"120"`
	// Firebase one-time auth configuration (exchange Firebase ID token to short-lived stream session token)
	FirebaseProjectID           string
	FirebaseCertsURL            string
	StreamSessionTTLSeconds     int // 8h
	StreamSessionCleanupSeconds int
	StreamSessionCookieName     string
	StreamSessionCookieSecure   bool
	StreamSessionCookieDomain   string
	StreamAllowLegacyHMAC       bool
	MultiTokens                 []string
}

var botTokenRegex = regexp.MustCompile(`MULTI\_TOKEN\d+=(.*)`)

func (c *config) loadFromEnvFile(log *zap.Logger) {
	envPath := filepath.Clean("fsb.env")
	log.Sugar().Infof("Trying to load ENV vars from %s", envPath)
	err := godotenv.Load(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Sugar().Errorf("ENV file not found: %s", envPath)
			log.Sugar().Info("Please create fsb.env file")
			log.Sugar().Info("For more info, refer: https://github.com/EverythingSuckz/TG-FileStreamBot/tree/golang#setting-up-things")
			log.Sugar().Info("Please ignore this message if you are hosting it in a service like Heroku or other alternatives.")
		} else {
			log.Fatal("Unknown error while parsing env file.", zap.Error(err))
		}
	}
}

func SetFlagsFromConfig(cmd *cobra.Command) {
	cmd.Flags().Int32("api-id", ValueOf.ApiID, "Telegram API ID")
	cmd.Flags().String("api-hash", ValueOf.ApiHash, "Telegram API Hash")
	cmd.Flags().String("bot-token", ValueOf.BotToken, "Telegram Bot Token")
	cmd.Flags().Int64("log-channel", ValueOf.LogChannelID, "Telegram Log Channel ID")
	cmd.Flags().Bool("dev", ValueOf.Dev, "Enable development mode")
	cmd.Flags().IntP("port", "p", ValueOf.Port, "Server port")
	cmd.Flags().String("host", ValueOf.Host, "Server host that will be included in links")
	cmd.Flags().Int("hash-length", ValueOf.HashLength, "Hash length in links")
	cmd.Flags().Bool("use-session-file", ValueOf.UseSessionFile, "Use session files")
	cmd.Flags().String("user-session", ValueOf.UserSession, "Pyrogram user session")
	cmd.Flags().Bool("use-public-ip", ValueOf.UsePublicIP, "Use public IP instead of local IP")
	cmd.Flags().String("multi-token-txt-file", "", "Multi token txt file (Not implemented)")
}

func (c *config) loadConfigFromArgs(log *zap.Logger, cmd *cobra.Command) {
	if cmd.Flags().Changed("api-id") {
		apiID, _ := cmd.Flags().GetInt32("api-id")
		os.Setenv("API_ID", strconv.Itoa(int(apiID)))
	}
	if cmd.Flags().Changed("api-hash") {
		apiHash, _ := cmd.Flags().GetString("api-hash")
		os.Setenv("API_HASH", apiHash)
	}
	if cmd.Flags().Changed("bot-token") {
		botToken, _ := cmd.Flags().GetString("bot-token")
		os.Setenv("BOT_TOKEN", botToken)
	}
	if cmd.Flags().Changed("log-channel") {
		logChannelID, _ := cmd.Flags().GetString("log-channel")
		os.Setenv("LOG_CHANNEL", logChannelID)
	}
	if cmd.Flags().Changed("dev") {
		dev, _ := cmd.Flags().GetBool("dev")
		os.Setenv("DEV", strconv.FormatBool(dev))
	}
	if cmd.Flags().Changed("port") {
		port, _ := cmd.Flags().GetInt("port")
		os.Setenv("PORT", strconv.Itoa(port))
	}
	if cmd.Flags().Changed("host") {
		host, _ := cmd.Flags().GetString("host")
		os.Setenv("HOST", host)
	}
	if cmd.Flags().Changed("hash-length") {
		hashLength, _ := cmd.Flags().GetInt("hash-length")
		os.Setenv("HASH_LENGTH", strconv.Itoa(hashLength))
	}
	if cmd.Flags().Changed("use-session-file") {
		useSessionFile, _ := cmd.Flags().GetBool("use-session-file")
		os.Setenv("USE_SESSION_FILE", strconv.FormatBool(useSessionFile))
	}
	if cmd.Flags().Changed("user-session") {
		userSession, _ := cmd.Flags().GetString("user-session")
		os.Setenv("USER_SESSION", userSession)
	}
	if cmd.Flags().Changed("use-public-ip") {
		usePublicIP, _ := cmd.Flags().GetBool("use-public-ip")
		os.Setenv("USE_PUBLIC_IP", strconv.FormatBool(usePublicIP))
	}

	multiTokens, _ := cmd.Flags().GetString("multi-token-txt-file")
	if multiTokens != "" {
		log.Sugar().Warn("multi-token-txt-file is not implemented yet")
	}
}

func (c *config) loadMultiTokensFromEnv() {
	c.MultiTokens = c.MultiTokens[:0]
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "MULTI_TOKEN") {
			continue
		}
		match := botTokenRegex.FindStringSubmatch(env)
		if len(match) != 2 {
			continue
		}
		token := strings.TrimSpace(match[1])
		if token == "" {
			continue
		}
		c.MultiTokens = append(c.MultiTokens, token)
	}
}

func (c *config) setupEnvVars(log *zap.Logger, cmd *cobra.Command) {
	c.loadFromEnvFile(log)
	c.loadConfigFromArgs(log, cmd)
	err := envconfig.Process("", c)
	if err != nil {
		log.Fatal("Error while parsing env variables", zap.Error(err))
	}
	c.loadMultiTokensFromEnv()

	var ipBlocked bool
	ip, err := getIP(c.UsePublicIP)
	if err != nil {
		log.Error("Error while getting IP", zap.Error(err))
		ipBlocked = true
	}
	if c.Host == "" {
		c.Host = "http://" + ip + ":" + strconv.Itoa(c.Port)
		if c.UsePublicIP {
			if ipBlocked {
				log.Sugar().Warn("Can't get public IP, using local IP")
			} else {
				log.Sugar().Warn("You are using a public IP, please be aware of the security risks while exposing your IP to the internet.")
				log.Sugar().Warn("Use 'HOST' variable to set a domain name")
			}
		}
		log.Sugar().Info("HOST not set, automatically set to " + c.Host)
	}
}

func Load(log *zap.Logger, cmd *cobra.Command) {
	log = log.Named("Config")
	defer log.Info("Loaded config")
	ValueOf.setupEnvVars(log, cmd)
	ValueOf.LogChannelID = int64(stripInt(log, int(ValueOf.LogChannelID)))
	// Process MEDIA_CHANNEL_ID: convert positive channel ID to the format Telegram expects
	if ValueOf.MediaChannelID != 0 {
		ValueOf.MediaChannelID = int64(stripInt(log, int(ValueOf.MediaChannelID)))
		log.Sugar().Infof("MEDIA_CHANNEL_ID configured: %d", ValueOf.MediaChannelID)
	} else {
		log.Sugar().Warn("MEDIA_CHANNEL_ID not set. The /direct/:message_id route will not work.")
	}
	if ValueOf.HashLength == 0 {
		log.Sugar().Info("HASH_LENGTH can't be 0, defaulting to 6")
		ValueOf.HashLength = 6
	}
	if ValueOf.HashLength > 32 {
		log.Sugar().Info("HASH_LENGTH can't be more than 32, changing to 32")
		ValueOf.HashLength = 32
	}
	if ValueOf.HashLength < 5 {
		log.Sugar().Info("HASH_LENGTH can't be less than 5, defaulting to 6")
		ValueOf.HashLength = 6
	}
	if ValueOf.FirebaseProjectID != "" {
		log.Sugar().Infof("Firebase stream auth enabled for project: %s", ValueOf.FirebaseProjectID)
	}
	if ValueOf.FirebaseProjectID == "" && ValueOf.StreamSecret == "" {
		log.Sugar().Warn("No stream auth configured. /direct route is publicly accessible.")
	}
}

func getIP(public bool) (string, error) {
	var ip string
	var err error
	if public {
		ip, err = GetPublicIP()
	} else {
		ip, err = getInternalIP()
	}
	if ip == "" {
		ip = "localhost"
	}
	if err != nil {
		return "localhost", err
	}
	return ip, nil
}

// https://stackoverflow.com/a/23558495/15807350
func getInternalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", errors.New("no internet connection")
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}

func GetPublicIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org?format=text")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if !checkIfIpAccessible(string(ip)) {
		return string(ip), errors.New("PORT is blocked by firewall")
	}
	return string(ip), nil
}

func checkIfIpAccessible(ip string) bool {
	conn, err := net.Dial("tcp", ip+":80")
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

func stripInt(log *zap.Logger, a int) int {
	strA := strconv.Itoa(abs(a))
	lastDigits := strings.Replace(strA, "100", "", 1)
	result, err := strconv.Atoi(lastDigits)
	if err != nil {
		log.Sugar().Fatalln(err)
		return 0
	}
	return result
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
