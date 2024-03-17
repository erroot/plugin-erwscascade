package erwscascade

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/util"
)

/**

	Superior(上级平台) ws 接收推送 flv  视频流，发布为自己的流
	streamPath:   original-cid   //原始流path 拼接cid

**/

type WssRecever struct {
	Publisher
	Cid    string // 与配置文件中cid 不同
	Conn   *net.Conn
	Status int
	absTS  uint32 //绝对时间戳
	buf    util.Buffer
	pool   util.BytesPool

	Cc *ErWsCascadeConfig
}

func NewWssRecever(cc *ErWsCascadeConfig, cid string, conn *net.Conn) *WssRecever {
	return &WssRecever{
		Cc:   cc,
		Cid:  cid,
		Conn: conn,
		buf:  util.Buffer(make([]byte, len(codec.FLVHeader))),
		pool: make(util.BytesPool, 17),
	}
}

// 统一处理读写错误
func (recever *WssRecever) OnConnErr(reason ...zapcore.Field) {
	ErWsCascadePlugin.Error("WssRecever OnConnErr", reason[0])

	recever.Status = 0
	//停止发布
	recever.Stop(reason[0])
}

func (recever *WssRecever) ParseFLVTag(b []byte) (t byte, timestamp uint32, payload []byte, err error) {
	if len(b) < 11 {
		return 0, 0, nil, errors.New("FLV Tag data is too short")
	}

	t = b[0]                                                      // Tag类型
	dataSize := uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) // 数据部分大小
	timestamp = uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6]) // 时间戳
	timestamp |= uint32(b[7]) << 24                               // 扩展时间戳
	//streamID := uint32(b[8])<<16 | uint32(b[9])<<8 | uint32(b[10]) // 流ID

	payload = b[11 : 11+dataSize] // 数据payload

	//log.Printf("dataSize:%v ,buf len:%v", dataSize+1, len(b))

	return t, timestamp, payload, nil
}

func (recever *WssRecever) ReadFLVTag() {
	var startTs uint32
	offsetTs := recever.absTS
	for {
		//读取掩码头部信息
		_, err := ws.ReadHeader(*recever.Conn)
		if err != nil {
			// 处理读取头部信息错误
			recever.OnConnErr(zap.Error(err))
			break
		}

		//log.Println(header)
		//{"error": "frames from client to server must be masked"}
		data, err := wsutil.ReadClientBinary(*recever.Conn)
		if err != nil {
			//延迟10秒
			recever.OnConnErr(zap.Error(err))
			break
		}

		// 解码数据
		// mask := header.Mask
		// for i, b := range data {
		// 	data[i] = b ^ mask[i%4]
		// }
		t, timestamp, payload, err := recever.ParseFLVTag(data)
		if err != nil {
			recever.OnConnErr(zap.Error(err))
		}
		if startTs == 0 {
			startTs = timestamp
		}
		recever.absTS = offsetTs + (timestamp - startTs)

		var frame util.BLL

		mem := recever.pool.Get(int(len(payload)))
		frame.Push(mem)
		//mem.Value = payload
		copy(mem.Value, payload)
		//log.Printf("type:%v, absTS:%v timestamp:%v\n", t, recever.absTS, timestamp)
		switch t {
		case codec.FLV_TAG_TYPE_AUDIO:
			recever.WriteAVCCAudio(recever.absTS, &frame, recever.pool)
		case codec.FLV_TAG_TYPE_VIDEO:
			recever.WriteAVCCVideo(recever.absTS, &frame, recever.pool)
		case codec.FLV_TAG_TYPE_SCRIPT:
			recever.Info("script", zap.ByteString("data", payload))
			//frame.Recycle()
		}
	}
}

/*
推送flv 数据
上级平台推送接口
ws://127.0.0.1:8450/erwscascade/wspush/on
*/
func (p *ErWsCascadeConfig) Wspush_(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" {
		return
	}
	//customHeader := r.Header.Get("X-Custom-Header")
	// 获取完整的 URL
	fullURL := r.URL.String()
	//判断URL编码特殊字符%2F,%2B,%3F,%25，包含这些则自动转码?= 等
	ErWsCascadePlugin.Info("Full URL:" + fullURL)

	// 获取 cid 参数
	//cid := r.URL.Query().Get("cid")
	// 解码 URL
	decodedURL, err := url.QueryUnescape(fullURL)
	if err != nil {
		ErWsCascadePlugin.Error("Error decodedURL", zap.Error(err))
		return
	}

	// 手动解析解码后的 URL 中的参数
	var queryString string
	if strings.Contains(decodedURL, "?") {
		queryString = strings.Split(decodedURL, "?")[1]
	}
	queryParams, err := url.ParseQuery(queryString)
	if err != nil {
		ErWsCascadePlugin.Error("parse query string", zap.Error(err))
		return
	}
	cid := queryParams.Get("cid")
	ErWsCascadePlugin.Info("wspush CID:" + cid)

	// 配置WebSocket服务器选项
	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		ErWsCascadePlugin.Error("UpgradeHTTP error", zap.Error(err))
		return
	}

	if cid == "" {
		ErWsCascadePlugin.Error("wspush", zap.Error(errors.New("invalid cid refuse connect")))
		conn.Close()
		return
	}

	streamPath := queryParams.Get("streamPath")
	if streamPath == "" {
		ErWsCascadePlugin.Error("wspush", zap.Error(errors.New("invalid streamPath refuse connect")))
		conn.Close()
		return
	}

	//生成客户推送的streamPath  修改添加 cid
	newStreamPath := streamPath + "-" + cid

	//read flv head
	head, err := wsutil.ReadClientBinary(conn)
	if err != nil {
		ErWsCascadePlugin.Error("wspush read flv head faild", zap.Error(err))
		conn.Close()
		return
	}

	if head[0] != 'F' || head[1] != 'L' || head[2] != 'V' {
		ErWsCascadePlugin.Error("wspush", zap.Error(errors.New("invalid flv head faild")))
		conn.Close()
		return
	}
	configCopy := p.GetPublishConfig()
	configCopy.PubAudio = head[4]&0x04 != 0
	configCopy.PubVideo = head[4]&0x01 != 0

	//读取校验自定义脚本
	sc, err := wsutil.ReadClientBinary(conn)
	if err != nil {
		ErWsCascadePlugin.Error("wspush", zap.Error(errors.New("read FLV_TAG_TYPE_SCRIPT faild")))
		conn.Close()
		return
	}

	if sc[0] != codec.FLV_TAG_TYPE_SCRIPT {
		ErWsCascadePlugin.Error("wspush", zap.Error(errors.New("parse FLV_TAG_TYPE_SCRIPT faild")))
		conn.Close()
		return
	}

	wssRecever := NewWssRecever(p, cid, &conn)

	wssRecever.Config = &configCopy

	s := Streams.Get(newStreamPath)
	if s == nil || s.Publisher == nil {
		if err := ErWsCascadePlugin.Publish(newStreamPath, wssRecever); err != nil {
			//p.Stream.Tracks =
			ErWsCascadePlugin.Error("wspush", zap.Error(err))
			conn.Close()
			return
		}
		puber := wssRecever.GetPublisher()
		// 老流中的音视频轨道不可再使用
		puber.AudioTrack = nil
		puber.VideoTrack = nil
		//log.Println("Wspush publish tm:", wssRecever.Publisher.Stream.PublishTimeout)
	}

	//阻塞读取数据
	wssRecever.ReadFLVTag()
}
