package services

import (
	"net"
	"fmt"
	log "github.com/sirupsen/logrus"
	"time"
	"encoding/json"
)

func (tcp *TcpService) agentKeepalive() {
	data := pack(CMD_TICK, []byte("agent keep alive"))
	for {
		select {
			case <-tcp.ctx.Ctx.Done():
				return
			default:
		}
		if tcp.node == nil || tcp.node.conn == nil ||
			tcp.status & agentStatusDisconnect > 0 ||
			tcp.status & agentStatusOffline > 0 {
			time.Sleep(3 * time.Second)
			continue
		}
		n, err := tcp.node.conn.Write(data)
		if n <= 0 || err != nil {
			log.Errorf("agent keepalive error: %d, %v", n, err)
			tcp.disconnect()
		}
		time.Sleep(3 * time.Second)
	}
}

func (tcp *TcpService) nodeInit(ip string, port int) {
	if tcp.node != nil && tcp.node.conn != nil {
		tcp.disconnect()
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		log.Panicf("start agent with error: %+v", err)
	}
	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	tcp.node = &agentNode{
		conn:conn,
	}
	if err != nil {
		log.Errorf("start agent with error: %+v", err)
		tcp.node.conn = nil
	}
}

func (tcp *TcpService) AgentStart(serviceIp string, port int) {
	go func() {
		if serviceIp == "" || port == 0 {
			log.Warnf("ip or port empty %s:%d", serviceIp, port)
			return
		}
		if tcp.status&agentStatusConnect > 0 {
			return
		}
		tcp.lock.Lock()
		if tcp.status&agentStatusOffline > 0 {
			tcp.status ^= agentStatusOffline
			tcp.status |= agentStatusOnline
		}
		tcp.lock.Unlock()
		agentH := pack(CMD_AGENT, []byte(""))
		var readBuffer [tcpDefaultReadBufferSize]byte
		for {
			select {
			case <-tcp.ctx.Ctx.Done():
				return
			default:
			}
			if tcp.status&agentStatusOffline > 0 {
				log.Warnf("agentStatusOffline return")
				return
			}
			tcp.nodeInit(serviceIp, port)
			if tcp.node == nil || tcp.node.conn == nil {
				log.Warnf("node | conn is nil")
				time.Sleep(time.Second * 3)
				continue
			}
			tcp.lock.Lock()
			if tcp.status&agentStatusDisconnect > 0 {
				tcp.status ^= agentStatusDisconnect
				tcp.status |= agentStatusConnect
			}
			tcp.lock.Unlock()
			log.Debugf("====================agent start %s:%d====================", serviceIp, port)
			// 简单的握手
			n, err := tcp.node.conn.Write(agentH)
			if n <= 0 || err != nil {
				log.Warnf("write agent header data with error: %d, err", n, err)
				tcp.disconnect()
				continue
			}
			for {
				//log.Debugf("====agent is running====")
				if tcp.status&agentStatusOffline > 0 {
					log.Warnf("agentStatusOffline return - 2===%d:%d", tcp.status, tcp.status&agentStatusOffline)
					return
				}
				size, err := tcp.node.conn.Read(readBuffer[0:])
				//log.Debugf("read buffer len: %d, cap:%d", len(readBuffer), cap(readBuffer))
				if err != nil || size <= 0 {
					log.Warnf("agent read with error: %+v", err)
					tcp.disconnect()
					break
				}
				//log.Debugf("agent receive %d bytes: %+v, %s", size, readBuffer[:size], string(readBuffer[:size]))
				tcp.onAgentMessage(readBuffer[:size])
				select {
				case <-tcp.ctx.Ctx.Done():
					return
				default:
				}
			}
		}
	}()
}

func (tcp *TcpService) onAgentMessage(msg []byte) {
	tcp.buffer = append(tcp.buffer, msg...)
	for {
		bufferLen := len(tcp.buffer)
		if bufferLen < 6 {
			return
		}
		//4字节长度，包含2自己的cmd
		contentLen := int(tcp.buffer[0]) | int(tcp.buffer[1]) << 8 | int(tcp.buffer[2]) << 16 | int(tcp.buffer[3]) << 24
		//2字节 command
		cmd := int(tcp.buffer[4]) | int(tcp.buffer[5]) << 8
		//log.Debugf("bufferLen=%d, buffercap:%d, contentLen=%d, cmd=%d", bufferLen, cap(tcp.buffer), contentLen, cmd)
		//log.Debugf("%v, %v", tcp.buffer, string(tcp.buffer))

		if !hasCmd(cmd) {
			log.Errorf("cmd %d dos not exists: %v", cmd, tcp.buffer)
			tcp.buffer = make([]byte, 0)
			return
		}
		//数据未接收完整，等待下一次处理
		if bufferLen < 4 + contentLen {
			//log.Error("content len error")
			return
		}
		//log.Debugf("%v", tcp.buffer)
		dataB := tcp.buffer[6:4 + contentLen]
		//log.Debugf("clen=%d, cmd=%d, (%d)%+v", contentLen, cmd, len(dataB), dataB)
		switch cmd {
		case CMD_EVENT:
			var data map[string] interface{}
			err := json.Unmarshal(dataB, &data)
			if err == nil {
				log.Debugf("agent receive event: %+v", data)
				tcp.SendAll(data["table"].(string), dataB)
			} else {
				log.Errorf("json Unmarshal error: %+v, %s, %+v", dataB, string(dataB), err)
			}
		case CMD_TICK:
			//log.Debugf("keepalive: %s", string(dataB))
		case CMD_POS:
			log.Debugf("receive pos: %v", dataB)
			for {
				if len(tcp.ctx.PosChan) < cap(tcp.ctx.PosChan) {
					break
				}
				log.Warnf("cache full, try wait")
			}
			tcp.ctx.PosChan <- string(dataB)
		default:
			tcp.sendRaw(pack(cmd, msg))
		}
		//remove(&tcp.buffer, contentLen + 4)
		//log.Debugf("%d, contentLen + 4=%d", len(tcp.buffer), contentLen + 4)
		//log.Debugf("%v", tcp.buffer)
		//if len(tcp.buffer) >= contentLen + 4 {
		if len(tcp.buffer) <= 0 {
			log.Errorf("tcp.buffer is empty")
			return
		}
		tcp.buffer = append(tcp.buffer[:0], tcp.buffer[contentLen+4:]...)
		//log.Debugf("=================>bufferLen=%d, buffercap:%d, contentLen=%d, cmd=%d", bufferLen, cap(tcp.buffer), contentLen, cmd)
	}
}

func (tcp *TcpService) disconnect() {
	if tcp.node == nil || tcp.status & agentStatusDisconnect > 0 {
		log.Debugf("agent is in disconnect status")
		return
	}
	log.Warnf("====================agent disconnect====================")
	tcp.node.conn.Close()

	tcp.lock.Lock()
	if tcp.status & agentStatusConnect > 0 {
		tcp.status ^= agentStatusConnect
		tcp.status |= agentStatusDisconnect
	}
	tcp.lock.Unlock()
}

func (tcp *TcpService) AgentStop() {
	if tcp.status & agentStatusOffline > 0 {
		//log.Debugf("agent close was called, but not running")
		return
	}
	log.Warnf("====================agent close====================")
	tcp.disconnect()

	tcp.lock.Lock()
	if tcp.status & agentStatusOnline > 0 {
		tcp.status ^= agentStatusOnline
		tcp.status |= agentStatusOffline
	}
	tcp.lock.Unlock()
}

