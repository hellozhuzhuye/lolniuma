package hh_lol_prophet

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	"github.com/avast/retry-go"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"github.com/real-web-world/hh-lol-prophet/global"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/real-web-world/hh-lol-prophet/services/lcu"
	"github.com/real-web-world/hh-lol-prophet/services/lcu/models"
	"github.com/real-web-world/hh-lol-prophet/services/logger"
)

type (
	lcuWsEvt  string
	GameState string
	Prophet   struct {
		ctx          context.Context
		opts         *options
		httpSrv      *http.Server
		lcuPort      int
		lcuToken     string
		lcuActive    bool
		currSummoner *lcu.CurrSummoner
		cancel       func()
		api          *Api
		mu           *sync.Mutex
		GameState    GameState
	}
	wsMsg struct {
		Data      interface{} `json:"data"`
		EventType string      `json:"event_type"`
		Uri       string      `json:"uri"`
	}
	options struct {
		debug       bool
		enablePprof bool
		httpAddr    string
	}
)

const (
	onJsonApiEventPrefixLen = len(`[8,"OnJsonApiEvent",`)
	gameFlowChangedEvt      = "/lol-gameflow/v1/gameflow-phase"
)

func NewProphet() *Prophet {
	ctx, cancel := context.WithCancel(context.Background())
	return &Prophet{
		ctx:    ctx,
		cancel: cancel,
		mu:     &sync.Mutex{},
	}
}
func (p Prophet) Run() error {
	go p.MonitorStart()
	go p.captureStartMessage()
	log.Printf("%s已启动 v%s -- %s  有问题联系作者微信xjx8260", global.AppName, APPVersion, global.WebsiteTitle)
	return p.notifyQuit()
}
func (p *Prophet) captureStartMessage() {
	for i := 0; i < 5; i++ {
		if global.GetUserInfo().IP != "" {
			break
		}
		time.Sleep(time.Second * 2)
	}
	// sentry.CaptureMessage(global.AppName + "已启动")
}
func (p Prophet) isLcuActive() bool {
	return p.lcuActive
}
func (p Prophet) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	// stop all task
	return nil
}
func (p Prophet) MonitorStart() {
	for {
		if !p.isLcuActive() {
			port, token, err := lcu.GetLolClientApiInfo()
			if err != nil {
				if !errors.Is(lcu.ErrLolProcessNotFound, err) {
					logger.Error("获取lcu info 失败", zap.Error(err))
				}
				continue
			}
			p.initLcuClient(port, token)
			err = p.initGameFlowMonitor(port, token)
			if err != nil {
				logger.Error("游戏流程监视器 err:", err)
			}
			p.lcuActive = false
		}
		time.Sleep(time.Second)
	}

}

func (p Prophet) notifyQuit() error {
	errC := make(chan error, 1)
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	go func() {
		for {
			select {
			case <-p.ctx.Done():
				errC <- p.ctx.Err()
				return
			case <-interrupt:
				_ = p.Stop()
			}
		}
	}()
	err := <-errC
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (p Prophet) initLcuClient(port int, token string) {
	lcu.InitCli(port, token)
}

func (p Prophet) initGameFlowMonitor(port int, authPwd string) error {
	dialer := websocket.DefaultDialer
	dialer.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}
	rawUrl := fmt.Sprintf("wss://127.0.0.1:%d/", port)
	header := http.Header{}
	authSecret := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("riot:%s", authPwd)))
	header.Set("Authorization", "Basic "+authSecret)
	u, _ := url.Parse(rawUrl)
	// logger.Debug(fmt.Sprintf("connect to lcu %s", u.String()))
	c, _, err := dialer.Dial(u.String(), header)
	if err != nil {
		logger.Error("连接到lcu ws 失败", zap.Error(err))
		return err
	}
	defer c.Close()
	err = retry.Do(func() error {
		currSummoner, err := lcu.GetCurrSummoner()
		if err == nil {
			p.currSummoner = currSummoner
		}
		return err
	}, retry.Attempts(5), retry.Delay(time.Second))
	if err != nil {
		return errors.New("获取当前召唤师信息失败:" + err.Error())
	}
	p.lcuActive = true

	_ = c.WriteMessage(websocket.TextMessage, []byte("[5, \"OnJsonApiEvent\"]"))
	for {
		msgType, message, err := c.ReadMessage()
		if err != nil {
			// log.Println("read:", err)
			logger.Error("lol事件监控读取消息失败", zap.Error(err))
			return err
		}
		msg := &wsMsg{}
		if msgType != websocket.TextMessage || len(message) < onJsonApiEventPrefixLen+1 {
			continue
		}
		_ = json.Unmarshal(message[onJsonApiEventPrefixLen:len(message)-1], msg)
		if msg.Uri == gameFlowChangedEvt {
			gameFlow, ok := msg.Data.(string)
			if !ok {
				continue
			}
			// logger.Debug("切换状态:" + gameFlow)
			if gameFlow == string(models.GameFlowChampionSelect) {
				log.Println("进入英雄选择阶段,正在计算我方分数")
				go p.ChampionSelectStart()
			}
			if gameFlow == string(models.GameFlowInProgress) {
				log.Println("进入游戏对局中,正在计算敌方分数")
				go p.CalcEnemyTeamScore()
			}

		}
		// log.Printf("recv: %s", message)
	}
}

func (p Prophet) ChampionSelectStart() {

	sendConversationMsgDelayCtx, cancel := context.WithTimeout(context.Background(),
		time.Second*time.Duration(3))
	defer cancel()
	var conversationID string
	var summonerIDList []int64
	for i := 0; i < 3; i++ {
		time.Sleep(time.Second)
		// 获取队伍所有用户信息
		conversationID, summonerIDList, _ = getTeamUsers()
		if len(summonerIDList) != 5 {
			continue
		}
	}

	// logger.Debug("队伍人员列表:", zap.Any("summonerIDList", summonerIDList))
	// 查询所有用户的信息并计算得分
	g := errgroup.Group{}
	summonerIDMapScore := map[int64]UserScore{}
	mu := sync.Mutex{}
	for _, summonerID := range summonerIDList {
		summonerID := summonerID
		g.Go(func() error {
			actScore, err := GetUserScore(summonerID)
			if err != nil {
				logger.Error("计算用户得分失败", zap.Error(err), zap.Int64("summonerID", summonerID))
				return nil
			}
			mu.Lock()
			summonerIDMapScore[summonerID] = *actScore
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	// 根据所有用户的分数判断小代上等马中等马下等马
	// for _, score := range summonerIDMapScore {
	// 	log.Printf("用户:%s,得分:%.2f\n", score.SummonerName, score.Score)
	// }
	scoreCfg := global.GetScoreConf()
	allMsg := ""
	// 发送到选人界面
	for _, scoreInfo := range summonerIDMapScore {
		var horse string
		for _, v := range scoreCfg.Horse {
			if scoreInfo.Score >= v.Score {
				horse = v.Name
				break
			}
		}
		currKDASb := strings.Builder{}
		for i := 0; i < 10 && i < len(scoreInfo.CurrKDA); i++ {
			currKDASb.WriteString(fmt.Sprintf("%d/%d/%d  ", scoreInfo.CurrKDA[i][0], scoreInfo.CurrKDA[i][1],
				scoreInfo.CurrKDA[i][2]))
		}
		currKDAMsg := currKDASb.String()
		if len(currKDAMsg) > 0 {
			currKDAMsg = currKDAMsg[:len(currKDAMsg)-1]
		}
		msg := fmt.Sprintf("\n我方%s(%d): %s %s 最近10场战绩: %s  by %s", horse, int(scoreInfo.Score), scoreInfo.SummonerName,
			scoreInfo.WinningPercentage, currKDAMsg, "lol.smartsoftware.top")
		allMsg += msg + "\n"
		<-sendConversationMsgDelayCtx.Done()
		_ = SendConversationMsg(msg, conversationID)
		time.Sleep(time.Millisecond * 1500)
	}
	log.Println(allMsg)
	log.Println("已将我方马匹信息复制到剪切板")
	_ = clipboard.WriteAll(allMsg)
}

func (p Prophet) CalcEnemyTeamScore() {
	// 获取当前游戏进程
	session, err := lcu.QueryGameFlowSession()
	if err != nil {
		log.Println(err)
		return
	}
	if session.Phase != models.GameFlowInProgress {
		log.Println("session.Phase" + session.Phase)
		return
	}
	if p.currSummoner == nil {
		log.Println("p.currSummoner == nil")
		return
	}
	selfID := p.currSummoner.SummonerId
	selfTeamUsers, enemyTeamUsers := getAllUsersFromSession(selfID, session)
	_ = selfTeamUsers
	summonerIDList := enemyTeamUsers

	// logger.Debug("敌方队伍人员列表:", zap.Any("summonerIDList", summonerIDList))
	if len(summonerIDList) == 0 {
		return
	}
	// 查询所有用户的信息并计算得分
	g := errgroup.Group{}
	summonerIDMapScore := map[int64]UserScore{}
	mu := sync.Mutex{}
	for _, summonerID := range summonerIDList {
		summonerID := summonerID
		g.Go(func() error {
			actScore, err := GetUserScore(summonerID)
			if err != nil {
				logger.Error("计算用户得分失败", zap.Error(err), zap.Int64("summonerID", summonerID))
				return nil
			}
			mu.Lock()
			summonerIDMapScore[summonerID] = *actScore
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	// 根据所有用户的分数判断小代上等马中等马下等马
	for _, score := range summonerIDMapScore {
		currKDASb := strings.Builder{}
		for i := 0; i < 10 && i < len(score.CurrKDA); i++ {
			currKDASb.WriteString(fmt.Sprintf("%d/%d/%d  ", score.CurrKDA[i][0], score.CurrKDA[i][1],
				score.CurrKDA[i][2]))
		}
		// currKDAMsg := currKDASb.String()
		// log.Printf("敌方用户:%s,得分:%.2f,kda:%s\n", score.SummonerName, score.Score, currKDAMsg)
	}
	scoreCfg := global.GetScoreConf()
	allMsg := ""
	// 发送到选人界面
	for _, scoreInfo := range summonerIDMapScore {
		time.Sleep(time.Second / 2)
		var horse string
		// horseIdx := 0
		for _, v := range scoreCfg.Horse {
			if scoreInfo.Score >= v.Score {
				horse = v.Name
				// horseIdx = i
				break
			}
		}
		currKDASb := strings.Builder{}
		for i := 0; i < 10 && i < len(scoreInfo.CurrKDA); i++ {
			currKDASb.WriteString(fmt.Sprintf("%d/%d/%d  ", scoreInfo.CurrKDA[i][0], scoreInfo.CurrKDA[i][1],
				scoreInfo.CurrKDA[i][2]))
		}
		currKDAMsg := currKDASb.String()
		if len(currKDAMsg) > 0 {
			currKDAMsg = currKDAMsg[:len(currKDAMsg)-1]
		}
		msg := fmt.Sprintf("\n我方%s(%d): %s %s 最近10场战绩: %s  by %s", horse, int(scoreInfo.Score), scoreInfo.SummonerName,
			scoreInfo.WinningPercentage, currKDAMsg, "lol.smartsoftware.top")
		allMsg += msg + "\n"
	}
	log.Println(allMsg)
	log.Println("已将敌方马匹信息复制到剪切板")
	_ = clipboard.WriteAll(allMsg)
}
