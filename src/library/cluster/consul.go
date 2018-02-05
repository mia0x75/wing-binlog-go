package cluster

import (
	log "github.com/sirupsen/logrus"
	"time"
	"sync"
	"os"
	"github.com/hashicorp/consul/api"
	"fmt"
	"strconv"
)

func NewConsul(onLeaderCallback func(), onPosChange func([]byte)) *Consul {
	config, err := getConfig()
	if err != nil {
		log.Panicf("new consul client with error: %+v", err)
	}
	log.Debugf("cluster start with config: %+v", *config.Consul)
	con := &Consul {
		serviceIp        : config.Consul.ServiceIp,
		isLock           : 0,
		lock             : new(sync.Mutex),
		sessionId        : GetSession(),
		onLeaderCallback : onLeaderCallback,
		onPosChange      : onPosChange,
		enable           : config.Enable,
		TcpServiceIp     : "",
		TcpServicePort   : 0,
	}
	if !con.enable {
		return con
	}

	ConsulConfig := api.DefaultConfig()
	ConsulConfig.Address = config.Consul.ServiceIp
	con.Client, err = api.NewClient(ConsulConfig)
	if err != nil {
		log.Panicf("create consul session with error: %+v", err)
	}
	con.Session = &Session {
		Address : config.Consul.ServiceIp,
		ID      : "",
		s       : con.Client.Session(),
	}
	con.Session.create()
	con.Kv = con.Client.KV()
	con.agent = con.Client.Agent()
	// check self is locked in start
	// if is locked, try unlock
	m := con.getService()
	if m != nil {
		if m.IsLeader && m.Status == STATUS_OFFLINE {
			log.Warnf("current node is lock in start, try to unlock")
			con.Unlock()
			con.Delete(LOCK)
		}
	}
	//超时检测，即检测leader是否挂了，如果挂了，要重新选一个leader
	//如果当前不是leader，重新选leader。leader不需要check
	//如果被选为leader，则还需要执行一个onLeader回调
	go con.checkAlive()
	//////还需要一个keepalive
	go con.keepalive()
	////还需要一个检测pos变化回调，即如果不是leader，要及时更新来自leader的pos变化
	go con.watch()
	return con
}

func (con *Consul) SetService(ip string, port int) {
	con.TcpServiceIp = ip
	con.TcpServicePort = port
}

func (con *Consul) getService() *ClusterMember{
	if !con.enable {
		return nil
	}
	members := con.GetMembers()
	if members == nil {
		return nil
	}
	for _, v := range members {
		if v != nil && v.Session == con.sessionId {
			return v
		}
	}
	return nil
}

// register service
func (con *Consul) registerService() {
	if !con.enable {
		return
	}
	con.lock.Lock()
	defer con.lock.Unlock()
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	//name := hostname + con.sessionId
	t := time.Now().Unix()
	service := &api.AgentServiceRegistration{
		ID:                con.sessionId,
		Name:              con.sessionId,
		Tags:              []string{fmt.Sprintf("%d", con.isLock), con.sessionId, fmt.Sprintf("%d", t), hostname},
		Port:              con.TcpServicePort,
		Address:           con.TcpServiceIp,
		EnableTagOverride: false,
		Check:             nil,
		Checks:            nil,
	}
	err = con.agent.ServiceRegister(service)
	if err != nil {
		log.Errorf("register service with error: %+v", err)
	}
}

// 服务发现，获取服务列表
func (con *Consul) GetServices() map[string]*api.AgentService {
	if !con.enable {
		return nil
	}
	//1516574111-0hWR-E6IN-lrsO: {
	// ID:1516574111-0hWR-E6IN-lrsO
	// Service:yuyideMacBook-Pro.local1516574111-0hWR-E6IN-lrsO
	// Tags:[ 1516574111-0hWR-E6IN-lrsO /7tZ yuyideMacBook-Pro.local]
	// Port:9998 Address:127.0.0.1
	// EnableTagOverride:false
	// CreateIndex:0
	// ModifyIndex:0
	// }
	ser, err := con.agent.Services()
	if err != nil {
		log.Errorf("get service list error: %+v", err)
		return nil
	}
	return ser
}

// keepalive
func (con *Consul) keepalive() {
	if !con.enable {
		return
	}
	for {
		con.Session.renew()
		con.registerService()
		time.Sleep(time.Second * KEEPALIVE_INTERVAL)
	}
}

// get all members nodes
func (con *Consul) GetMembers() []*ClusterMember {
	if !con.enable {
		return nil
	}
	members := con.GetServices()
	if members == nil {
		return nil
	}
	m := make([]*ClusterMember, len(members))
	var i = 0
	for _, v := range members {
		m[i] = &ClusterMember{}
		t, _:= strconv.ParseInt(v.Tags[2], 10, 64)
		m[i].Status = STATUS_ONLINE
		if time.Now().Unix() - t > TIMEOUT {
			m[i].Status = STATUS_OFFLINE
		}
		m[i].IsLeader  = v.Tags[0] == "1"
		m[i].Hostname  = v.Tags[3]
		m[i].Session   = v.Tags[1]
		m[i].ServiceIp = v.Address
		m[i].Port      = v.Port
	}
	return m
}

// check service is alive
// if leader is not alive, try to select a new one
func (con *Consul) checkAlive() {
	if !con.enable {
		return
	}
	for {
		//获取所有的服务
		//判断服务的心跳时间是否超时
		//如果超时，更新状态为
		services := con.GetServices()
		if services == nil {
			time.Sleep(time.Second * CHECK_ALIVE_INTERVAL)
			continue
		}
		for _, v := range services {
			isLock := v.Tags[0] == "1"
			t, _ := strconv.ParseInt(v.Tags[2], 10, 64)
			if time.Now().Unix()-t > TIMEOUT {
				log.Warnf("%s is timeout, will be deregister", v.ID)
				con.agent.ServiceDeregister(v.ID)
				// if is leader, try delete lock and reselect a new leader
				if isLock {
					con.Delete(LOCK)
					if con.Lock() {
						log.Debugf("current is the new leader")
						if con.onLeaderCallback != nil {
							con.onLeaderCallback()
						}
					}
				}
			}
		}
		time.Sleep(time.Second * CHECK_ALIVE_INTERVAL)
	}
}

// watch pos change
// if pos write by other node
// all nodes will get change
func (con *Consul) watch() {
	if !con.enable {
		return
	}
	for {
		con.lock.Lock()
		if con.isLock == 1 {
			con.lock.Unlock()
			// leader does not need watch
			time.Sleep(time.Second * 3)
			continue
		}
		con.lock.Unlock()
		_, meta, err := con.Kv.Get(POS_KEY, nil)
		if err != nil {
			log.Errorf("watch pos change with error：%#v", err)
			time.Sleep(time.Second)
			continue
		}
		if meta == nil {
			time.Sleep(time.Second)
			continue
		}
		v, _, err := con.Kv.Get(POS_KEY, &api.QueryOptions{
			WaitIndex : meta.LastIndex,
			WaitTime : time.Second * 86400,
		})
		if err != nil {
			log.Errorf("watch chang with error：%#v, %+v", err, v)
			time.Sleep(time.Second)
			continue
		}
		if v == nil {
			time.Sleep(time.Second)
			continue
		}
		con.onPosChange(v.Value)
		time.Sleep(time.Millisecond * 10)
	}
}

// get leader service ip and port
// if not found or some error happened
// return empty string and 0
func (con *Consul) GetLeader() (string, int) {
	if !con.enable {
		return "", 0
	}
	members := con.GetMembers()
	if members == nil {
		return "", 0
	}
	for _, v := range members {
		if v != nil && v.IsLeader {
			return v.ServiceIp, v.Port
		}
	}
	return "", 0
}

// if app is close, it will be call for clear some source
func (con *Consul) Close() {
	if !con.enable {
		return
	}
	con.Delete(PREFIX_KEEPALIVE + con.sessionId)
	log.Debugf("current is leader %d", con.isLock)
	con.lock.Lock()
	l := con.isLock
	con.lock.Unlock()
	if l == 1 {
		log.Debugf("delete lock %s", LOCK)
		con.Unlock()
		con.Delete(LOCK)
	}
	con.Session.delete()
}

// write pos kv to consul
// use by src/library/binlog/handler.go SaveBinlogPostionCache
func (con *Consul) Write(data []byte) bool {
	if !con.enable {
		return true
	}
	log.Debugf("write consul pos kv: %s, %v", POS_KEY, data)
	_, err := con.Kv.Put(&api.KVPair{Key: POS_KEY, Value: data}, nil)
	if err != nil {
		log.Errorf("write consul pos kv with error: %+v", err)
	}
	return nil == err
}
