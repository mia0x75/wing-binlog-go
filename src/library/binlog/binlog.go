package binlog

import (
	"github.com/siddontang/go-mysql/canal"
	"github.com/siddontang/go-mysql/mysql"
	"github.com/siddontang/go-mysql/replication"
	"sync/atomic"
	"fmt"
	"time"
	"os"
	"strings"
	log "github.com/sirupsen/logrus"
	"strconv"
	"library/services"
	"library/file"
	wstring "library/string"
	"reflect"
)

func NewBinlog() *Binlog {
	config, _ := GetMysqlConfig()
	log.Debug("binlog配置：", config)
	return &Binlog{Config:config}
}

func (h *binlogHandler) notify(msg []byte) {
	log.Println("binlog发送广播：", string(msg))
	h.tcp_service.SendAll(msg)
	h.websocket_service.SendAll(msg)
	h.http_service.SendAll(msg)
}

func (h *binlogHandler) OnRow(e *canal.RowsEvent) error {
	// 发生变化的数据表e.Table，如xsl.x_reports
	// 发生的操作类型e.Action，如update、insert、delete
	// 如update的数据，update的数据以双数出现前面为更新前的数据，后面的为更新后的数据
	// 0，2，4偶数的为更新前的数据，奇数的为更新后的数据
	// [[1 1 3074961 [115 102 103 98 114]   1 1485739538 1485739538]
	// [1 1 3074961 [115 102 103 98 114] 1 1 1485739538 1485739538]]
	// delete一次返回一条数据
	// delete的数据delete [[3 1 3074961 [97 115 100 99 97 100 115] 1,2,2 1 1485768268 1485768268]]
	// 一次插入多条的时候，同时返回
	// insert的数据insert xsl.x_reports [[6 0 0 [] 0 1 0 0]]
	columns_len := len(e.Table.Columns)
	log.Debugf("binlog缓冲区详细信息: %d %d", len(h.buf), cap(h.buf))
	db    := []byte(e.Table.Schema)
	point := []byte(".")
	table := []byte(e.Table.Name)
	dblen := len(db) + len(table) + len(point)
	if e.Action == "update" {
		for i := 0; i < len(e.Rows); i += 2 {
			atomic.AddInt64(&h.Event_index, int64(1))
			buf := h.buf[:0]
			buf = append(buf, byte(dblen))
			buf = append(buf, byte(dblen >> 8))
			buf = append(buf, db...)
			buf = append(buf, point...)
			buf = append(buf, table...)
			buf = append(buf, "{\"database\":\""...)
			buf = append(buf, e.Table.Schema...)
			buf = append(buf, "\",\"event\":{\"data\":{\"old_data\":{"...)
			rows_len := len(e.Rows[i])
			for k, col := range e.Table.Columns {
				buf = append(buf, "\""...)
				buf = append(buf, col.Name...)
				buf = append(buf, "\":"...)
				var edata interface{}
				if k < rows_len {
					edata = e.Rows[i][k]
				} else {
					log.Warn("binlog未知的行", col.Name)
					edata = nil
				}
				switch edata.(type) {
				case string:
					buf = append(buf, "\""...)
					for _, v := range []byte(edata.(string)) {
						if v == 34 {
							buf = append(buf, "\\"...)
						}
						buf = append(buf, v)
					}
					buf = append(buf, "\""...)
				case []uint8:
					buf = append(buf, "\""...)
					buf = append(buf, edata.([]byte)...)
					buf = append(buf, "\""...)
				case int:
					buf = strconv.AppendInt(buf, int64(edata.(int)), 10)
				case int8:
					buf = strconv.AppendInt(buf, int64(edata.(int8)), 10)
				case int64:
					buf = strconv.AppendInt(buf, int64(edata.(int64)), 10)
				case int32:
					buf = strconv.AppendInt(buf, int64(edata.(int32)), 10)
				case uint:
					buf = strconv.AppendUint(buf, uint64(edata.(uint)), 10)
				case float64:
					buf = strconv.AppendFloat(buf, edata.(float64), 'f', DEFAULT_FLOAT_PREC, 32)
				case float32:
					buf = strconv.AppendFloat(buf, float64(edata.(float32)), 'f', DEFAULT_FLOAT_PREC, 32)
				default:
					log.Warnf("binlog不支持的类型：%s %s", col.Name, reflect.TypeOf(edata))
					if edata != nil {
						buf = append(buf, "\"--unkonw type--\""...)
					} else {
						buf = append(buf, "NULL"...)
					}
				}
				if k < columns_len - 1 {
					buf = append(buf, ","...)
				}
			}
			buf = append(buf, "},\"new_data\":{"...)
			rows_len = len(e.Rows[i+1])
			for k, col := range e.Table.Columns {
				buf = append(buf, "\""...)
				buf = append(buf, col.Name...)
				buf = append(buf, "\":"...)
				var edata interface{}
				if k < rows_len {
					edata = e.Rows[i+1][k]
				} else {
					log.Warn("binlog未知的行", col.Name)
					edata = nil
				}
				switch edata.(type) {
				case string:
					buf = append(buf, "\""...)
					for _, v := range []byte(edata.(string)) {
						if v == 34 {
							buf = append(buf, "\\"...)
						}
						buf = append(buf, v)
					}
					buf = append(buf, "\""...)
				case []uint8:
					buf = append(buf, "\""...)
					buf = append(buf, edata.([]byte)...)
					buf = append(buf, "\""...)
				case int:
					buf = strconv.AppendInt(buf, int64(edata.(int)), 10)
				case int8:
					buf = strconv.AppendInt(buf, int64(edata.(int8)), 10)
				case int64:
					buf = strconv.AppendInt(buf, int64(edata.(int64)), 10)
				case int32:
					buf = strconv.AppendInt(buf, int64(edata.(int32)), 10)
				case uint:
					buf = strconv.AppendUint(buf, uint64(edata.(uint)), 10)
				case float64:
					buf = strconv.AppendFloat(buf, edata.(float64), 'f', DEFAULT_FLOAT_PREC, 32)
				case float32:
					buf = strconv.AppendFloat(buf, float64(edata.(float32)), 'f', DEFAULT_FLOAT_PREC, 32)
				default:
						log.Warnf("binlog不支持的类型：%s %s", col.Name, reflect.TypeOf(edata))
					if edata != nil {
						buf = append(buf, "\"--unkonw type--\""...)
					} else {
						buf = append(buf, "NULL"...)
					}
				}
				if k < columns_len - 1 {
					buf = append(buf, ","...)
				}
			}
			buf = append(buf, "}},\"event_type\":\""...)
			buf = append(buf, e.Action...)
			buf = append(buf, "\",\"time\":"...)
			buf = strconv.AppendInt(buf, time.Now().Unix(), 10)
			buf = append(buf, "},\"event_index\":"...)
			buf = strconv.AppendInt(buf, h.Event_index, 10)
			buf = append(buf, ",\"table\":\""...)
			buf = append(buf, e.Table.Name...)
			buf = append(buf, "\"}"...)
			h.notify(buf)
		}
	} else {
		for i := 0; i < len(e.Rows); i += 1 {
			atomic.AddInt64(&h.Event_index, int64(1))
			buf := h.buf[:0]
			buf = append(buf, byte(dblen))
			buf = append(buf, byte(dblen >> 8))
			buf = append(buf, db...)
			buf = append(buf, point...)
			buf = append(buf, table...)
			buf = append(buf, "{\"database\":\""...)
			buf = append(buf, e.Table.Schema...)
			rows_len := len(e.Rows[i])
			for k, col := range e.Table.Columns {
				buf = append(buf, "\""...)
				buf = append(buf, col.Name...)
				buf = append(buf, "\":"...)
				var edata interface{}
				if k < rows_len {
					edata = e.Rows[i][k]
				} else {
					log.Warn("binlog未知的行", col.Name)
					edata = nil
				}
				switch edata.(type) {
				case string:
					buf = append(buf, "\""...)
					for _, v := range []byte(edata.(string)){
						if v == 34 {
							buf = append(buf, "\\"...)
						}
						buf = append(buf, v)
					}
					buf = append(buf, "\""...)
				case []uint8:
					buf = append(buf, "\""...)
					buf = append(buf, string(edata.([]byte))...)
					buf = append(buf, "\""...)
				case int:
					buf = strconv.AppendInt(buf, int64(edata.(int)), 10)
				case int8:
					buf = strconv.AppendInt(buf, int64(edata.(int8)), 10)
				case int64:
					buf = strconv.AppendInt(buf, int64(edata.(int64)), 10)
				case int32:
					buf = strconv.AppendInt(buf, int64(edata.(int32)), 10)
				case uint:
					buf = strconv.AppendUint(buf, uint64(edata.(uint)), 10)
				case float64:
					buf = strconv.AppendFloat(buf, edata.(float64), 'f', DEFAULT_FLOAT_PREC, 64)
				case float32:
					buf = strconv.AppendFloat(buf, float64(edata.(float32)), 'f', DEFAULT_FLOAT_PREC, 64)
				default:
						log.Warnf("binlog不支持的类型：%s %s", col.Name, reflect.TypeOf(edata))
					if edata != nil {
						buf = append(buf, "\"--unkonw type--\""...)
					} else {
						buf = append(buf, "NULL"...)
					}
				}
				if k < columns_len - 1 {
					buf = append(buf, ","...)
				}
			}
			buf = append(buf, "},\"event_type\":\""...)
			buf = append(buf, e.Action...)
			buf = append(buf, "\",\"time\":"...)
			buf = strconv.AppendInt(buf, time.Now().Unix(), 10)
			buf = append(buf, "},\"event_index\":"...)
			buf = strconv.AppendInt(buf, h.Event_index, 10)
			buf = append(buf, ",\"table\":\""...)
			buf = append(buf, e.Table.Name...)
			buf = append(buf, "\"}"...)
			h.notify(buf)
		}
	}
	return nil
}

func (h *binlogHandler) String() string {
	return "binlogHandler"
}

func (h *binlogHandler) OnRotate(e *replication.RotateEvent) error {
	log.Debugf("binlog事件：OnRotate")
	return nil
}

func (h *binlogHandler) OnDDL(p mysql.Position, e *replication.QueryEvent) error {
	log.Debugf("binlog事件：OnDDL")
	return nil
}

func (h *binlogHandler) OnXID(p mysql.Position) error {
	log.Debugf("binlog事件：OnXID")
	return nil
}

func (h *binlogHandler) OnGTID(g mysql.GTIDSet) error {
	log.Debugf("binlog事件：OnGTID", g)
	return nil
}

func (h *binlogHandler) OnPosSynced(p mysql.Position, b bool) error {
	log.Debugf("binlog事件：OnPosSynced %d %d", p, b)
	h.SaveBinlogPostionCache(p)
	return nil
}

func (h *Binlog) Close() {
	if !h.is_connected  {
		return
	}
	h.handler.Close()
	h.is_connected = false
	close(h.binlog_handler.chan_save_position)
}

func (h *binlogHandler) SaveBinlogPostionCache(p mysql.Position) {
	if len(h.chan_save_position) >= MAX_CHAN_FOR_SAVE_POSITION - 10 {
		for k := 0; k <= MAX_CHAN_FOR_SAVE_POSITION - 10; k++ {
			<-h.chan_save_position //丢弃掉未写入的部分数据，优化性能，这里丢弃的pos并不影响最终的结果
		}
	}
	h.chan_save_position <- positionCache{p, atomic.LoadInt64(&h.Event_index)}
}

func (h *Binlog) GetBinlogPositionCache() (string, int64, int64) {
	wfile := file.WFile{file.GetCurrentPath() +"/cache/mysql_binlog_position.pos"}
	str := wfile.ReadAll()
	if str == "" {
		return "", int64(0), int64(0)
	}
	res := strings.Split(str, ":")
	if len(res) < 3 {
		return "", int64(0), int64(0)
	}
	wstr  := wstring.WString{res[1]}
	pos   := wstr.ToInt64()
	wstr2 := wstring.WString{res[2]}
	index := wstr2.ToInt64()
	return res[0], pos, index
}

func (h *Binlog) writeCache() {
	wfile := file.WFile{file.GetCurrentPath() +"/cache/mysql_binlog_position.pos"}
	for {
		select {
		case pos := <-h.binlog_handler.chan_save_position:
			if pos.pos.Name != "" && pos.pos.Pos > 0 {
				wfile.Write(fmt.Sprintf("%s:%d:%d", pos.pos.Name, pos.pos.Pos, pos.index), false)
			}
		}
	}
}
func (h *Binlog) Start(
	tcp_service *services.TcpService,
	websocket_service *services.WebSocketService,
	http_service *services.HttpService) {

	// 服务支持
	tcp_service.Start()
	websocket_service.Start()
	http_service.Start()

	config_file := file.GetCurrentPath() + "/config/canal.toml"
	cfg, err := canal.NewConfigWithFile(config_file)
	if err != nil {
		log.Panic("binlog错误：", err)
		os.Exit(1)
	}
	log.Debug("binlog配置：", cfg)
	/*
	cfg         := canal.NewDefaultConfig()
	cfg.Addr     = fmt.Sprintf("%s:%d", h.DB_Config.Mysql.Host, h.DB_Config.Mysql.Port)
	cfg.User     = h.DB_Config.Mysql.User
	cfg.Password = h.DB_Config.Mysql.Password
	cfg.Flavor   = "mysql"

	cfg.ReadTimeout        = 90*time.Second//*readTimeout
	cfg.HeartbeatPeriod    = 10*time.Second//*heartbeatPeriod
	cfg.ServerID           = uint32(h.DB_Config.Client.Slave_id)
	cfg.Dump.ExecutionPath = ""//mysqldump" 不支持mysqldump写为空
	cfg.Dump.DiscardErr    = false

	var err error*/
	h.handler, err = canal.NewCanal(cfg)
	if err != nil {
		log.Panic("binlog创建canal错误：", err)
		os.Exit(1)
	}
	//log.Println("binlog忽略的表", h.DB_Config.Client.Ignore_tables)
	//for _, v := range h.DB_Config.Client.Ignore_tables {
	//	db_table := strings.Split(v, ".")
	//	h.handler.AddDumpIgnoreTables(db_table[0], db_table[1])
	//}
	f, p, index := h.GetBinlogPositionCache()
	h.binlog_handler = binlogHandler{Event_index: index}
	var b [defaultBufSize]byte
	h.binlog_handler.buf = b[:0]
	// 3种服务
	h.binlog_handler.tcp_service        = tcp_service
	h.binlog_handler.websocket_service  = websocket_service
	h.binlog_handler.http_service       = http_service
	h.binlog_handler.chan_save_position = make(chan positionCache, MAX_CHAN_FOR_SAVE_POSITION)
	h.handler.SetEventHandler(&h.binlog_handler)
	h.is_connected = true
	bin_file := h.Config.BinFile
	bin_pos  := h.Config.BinPos
	if f != "" {
		bin_file = f
	}
	if p > 0 {
		bin_pos = p
	}
	go h.writeCache()
	go func() {
		startPos := mysql.Position{
			Name: bin_file,
			Pos:  uint32(bin_pos),
		}
		err = h.handler.RunFrom(startPos)
		if err != nil {
			log.Fatalf("start canal err %v", err)
		}
	}()
}