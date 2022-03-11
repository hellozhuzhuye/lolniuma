package global

import (
	"log"
	"sync"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/real-web-world/hh-lol-prophet/conf"
)

type (
	AppInfo struct {
		Version   string
		Commit    string
		BuildUser string
		BuildTime string
	}
	UserInfo struct {
		IP string `json:"ip"`
	}
)


// DB
var (
	SqliteDB *gorm.DB
)

const (
	LogWriterCleanupKey   = "logWriter"
	sentryDsn             = "https://1c762696e30c4febbb6f8cbcf8835603@o1144230.ingest.sentry.io/6207862"
	buffApiUrl            = "https://lol.buffge.com"
	defaultLogPath        = "./logs/hh-lol-prophet.log"
	WebsiteTitle          = "lol.smartsoftware.top"
	AdaptChatWebsiteTitle = "lol.smartsoftware.top"
	AppName               = "lol牛马分析"
)

var (
	confMu      = sync.Mutex{}
	userInfo    = UserInfo{}
	scoreConfMu = sync.Mutex{}
	Conf        = &conf.AppConf{}
	ClientConf   = new(conf.Client)
	Logger      *zap.SugaredLogger
	Cleanups    = make(map[string]func() error)
	AppBuildInfo = AppInfo{}
)

func SetClientConf(cfg conf.UpdateClientConfReq) *conf.Client {
	confMu.Lock()
	defer confMu.Unlock()
	if cfg.AutoAcceptGame != nil {
		ClientConf.AutoAcceptGame = *cfg.AutoAcceptGame
	}
	if cfg.AutoPickChampID != nil {
		ClientConf.AutoPickChampID = *cfg.AutoPickChampID
	}
	if cfg.AutoBanChampID != nil {
		ClientConf.AutoBanChampID = *cfg.AutoBanChampID
	}
	if cfg.AutoSendTeamHorse != nil {
		ClientConf.AutoSendTeamHorse = *cfg.AutoSendTeamHorse
	}
	if cfg.ShouldSendSelfHorse != nil {
		ClientConf.ShouldSendSelfHorse = *cfg.ShouldSendSelfHorse
	}
	if cfg.HorseNameConf != nil {
		ClientConf.HorseNameConf = *cfg.HorseNameConf
	}
	if cfg.ChooseSendHorseMsg != nil {
		ClientConf.ChooseSendHorseMsg = *cfg.ChooseSendHorseMsg
	}
	if cfg.ChooseChampSendMsgDelaySec != nil {
		ClientConf.ChooseChampSendMsgDelaySec = *cfg.ChooseChampSendMsgDelaySec
	}
	if cfg.ShouldInGameSaveMsgToClipBoard != nil {
		ClientConf.ShouldInGameSaveMsgToClipBoard = *cfg.ShouldInGameSaveMsgToClipBoard
	}
	if cfg.ShouldAutoOpenBrowser != nil {
		ClientConf.ShouldAutoOpenBrowser = cfg.ShouldAutoOpenBrowser
	}
	return ClientConf
}
func GetClientConf() conf.Client {
	confMu.Lock()
	defer confMu.Unlock()
	data := *ClientConf
	return data
}

func SetUserInfo(info UserInfo) {
	userInfo = info
}
func GetUserInfo() UserInfo {
	return userInfo
}
func Cleanup() {
	for name, cleanup := range Cleanups {
		if err := cleanup(); err != nil {
			log.Printf("%s cleanup err:%v\n", name, err)
		}
	}
	if fn, ok := Cleanups[LogWriterCleanupKey]; ok {
		_ = fn()
	}
}
func IsDevMode() bool {
	return GetEnv() == conf.ModeDebug
}
func GetEnv() conf.Mode {
	return Conf.Mode
}
func GetScoreConf() conf.CalcScoreConf {
	scoreConfMu.Lock()
	defer scoreConfMu.Unlock()
	return Conf.CalcScore
}
func SetScoreConf(scoreConf conf.CalcScoreConf) {
	scoreConfMu.Lock()
	Conf.CalcScore = scoreConf
	scoreConfMu.Unlock()
	return
}
