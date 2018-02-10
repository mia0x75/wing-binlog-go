package binlog

import (
	"sync"
	"sync/atomic"
	"library/services"
	"github.com/siddontang/go-mysql/mysql"
	log "github.com/sirupsen/logrus"
	"library/app"
	"time"
)

func NewBinlog(ctx *app.Context) *Binlog {
	config, _ := GetMysqlConfig()
	tcpConfig, _:= services.GetTcpConfig()
	binlog := &Binlog{
		Config   : config,
		wg       : new(sync.WaitGroup),
		lock     : new(sync.Mutex),
		ctx      : ctx,
		services : make(map[string]services.Service),
		//tcp service ip and port
		ServiceIp   : tcpConfig.ServiceIp,
		ServicePort : tcpConfig.Port,
		//isClosed    : true,
		//isRunning   : 0,
		startServiceChan:make(chan struct{}, 100),
		stopServiceChan:make(chan bool, 100),
	}
	atomic.StoreInt32(&binlog.isRunning, 0)
	//init consul
	binlog.consulInit()
	binlog.handlerInit()
	binlog.lookService()
	return binlog
}

func (h *Binlog) Close() {
	isRunning := atomic.LoadInt32(&h.isRunning)
	if isRunning == 0 {
		log.Debugf("binlog service is not running")
		return
	}
	log.Warn("binlog service exit")
	h.StopService(true)
	close(h.startServiceChan)
	close(h.stopServiceChan)
	for name, service := range h.services {
		log.Debugf("%s service exit", name)
		service.Close()
	}
	h.closeConsul()
}

func (h *Binlog) lookService() {
	h.wg.Add(2)
	go func() {
		for {
			select {
			case _, ok := <- h.startServiceChan:
				if !ok {
					h.wg.Done()
					return
				}
				for {
					isRunning := atomic.LoadInt32(&h.isRunning)
					if isRunning > 0 {
						log.Debug("binlog service is still running")
						break
					}
					log.Debug("binlog service start")
					atomic.StoreInt32(&h.isRunning, 1)
					go func() {
						for {
							if h.lastBinFile == "" {
								log.Warn("binlog lastBinFile is empty, wait for init")
								time.Sleep(time.Second)
								continue
							}
							break
						}
						startPos := mysql.Position{
							Name: h.lastBinFile,
							Pos:  h.lastPos,
						}
						for {
							if h.handler == nil {
								log.Warn("binlog handler is nil, wait for init")
								time.Sleep(time.Second)
								continue
							}
							break
						}
						err := h.handler.RunFrom(startPos)
						if err != nil {
							log.Warnf("binlog service exit with error: %+v", err)
							return
						}
					}()
					break
				}
			}
		}
	}()
	go func(){
		for {
			select {
			case exit, ok:= <- h.stopServiceChan:
				if !ok {
					h.wg.Done()
					return
				}
				log.Debug("binlog service stop")
				isRunning := atomic.LoadInt32(&h.isRunning)
				if isRunning > 0 && !exit {
					h.handler.Close()
					//reset handler
					h.setHandler()
				}
				if exit {
					h.SaveBinlogPostionCache(h.lastBinFile,
						int64(h.lastPos),
						atomic.LoadInt64(&h.EventIndex))
					h.cacheHandler.Close()
				}
				atomic.StoreInt32(&h.isRunning, 0)
			}
		}
	}()
}

func (h *Binlog) StopService(exit bool) {
	h.stopServiceChan <- exit
}

func (h *Binlog) StartService() {
	h.startServiceChan <- struct{}{}
}

func (h *Binlog) Start() {
	log.Debugf("===========binlog service start===========")
	for _, service := range h.services {
		service.Start()
	}
	if h.Lock() {
		log.Debugf("current node will run as leader")
		h.StartService()
	} else {
		go func() {
			var serviceIp = ""
			var port= 0
			for {
				serviceIp, port = h.GetLeader()
				if serviceIp == "" || port == 0 {
					log.Warnf("leader ip and port is empty, wait for init")
					time.Sleep(time.Second)
					continue
				}
				log.Debugf("leader ip and port: %s:%d", serviceIp, port)
				break
			}
			for _, s := range h.services {
				s.AgentStart(serviceIp, port)
			}
		}()
	}
}

func (h *Binlog) Reload(service string) {
	if service == "all" {
		for _, s := range h.services {
			s.Reload()
		}
	} else {
		h.services[service].Reload()
	}
}

func (h *Binlog) onNewLeader() {
	log.Debugf("current run as leader, start running")
	h.StartService()
	for _, s := range h.services {
		s.AgentStop()
	}
}


