package erwscascade

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/util"
)

/**
	subordinate (下级平台 订阅自己的 flv 流) ws 推送 到上级平台
	streamPath:   original
**/

type WscPusher struct {
	Subscriber
	Pusher
	mutex  sync.Mutex
	Conn   *net.Conn
	Status int

	Cc    *ErWsCascadeConfig
	absTS uint32 //绝对时间戳
	buf   util.Buffer
	pool  util.BytesPool

	connectCount int // 统计链接次数
}

func NewWscPusher(cc *ErWsCascadeConfig) *WscPusher {

	pusher := new(WscPusher)
	pusher.Conn = nil
	pusher.Cc = cc
	pusher.Status = 0
	pusher.connectCount = 0
	pusher.buf = util.Buffer(make([]byte, len(codec.FLVHeader)))
	pusher.pool = make(util.BytesPool, 17)

	return pusher
	// return new(WscPusher{
	// 	Conn:         nil,
	// 	Cc:           cc,
	// 	Status:       0,
	// 	connectCount: 0,
	// 	buf:          util.Buffer(make([]byte, len(codec.FLVHeader))),
	// 	pool:         make(util.BytesPool, 17),
	// 	// buf:  util.Buffer(make([]byte, len(codec.FLVHeader))),
	// 	// pool: make(util.BytesPool, 17),
	// })
}
func (pusher *WscPusher) Disconnect() {
	// if pusher.Closer != nil {
	// 	pusher.Closer.Close()
	// }
	pusher.mutex.Lock()
	defer pusher.mutex.Unlock()

	if pusher.Status == 0 {
		return
	}
	pusher.Status = 0
	//客户端主动断开webscocket 链接
	if pusher.Conn != nil {
		pusher.Info("WscPusher Disconnect to close ws connect")
		(*pusher.Conn).Close()
		pusher.Conn = nil
	}

	//移除订阅者
	stream := pusher.GetSubscriber().Stream
	stream.Receive(Unsubscribe(pusher)) // 通知stream移除订阅者

	pusher.Subscriber.Stop()

}

//自动重连问题需要，需要修改  engin pusher.go badPusher 判断返回问题

func (pusher *WscPusher) Connect() (err error) {

	pusher.connectCount++
	// 创建自定义的 TLS 配置
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	// 创建 Dialer
	dialer := ws.Dialer{
		TLSConfig: tlsConfig,
	}

	url := ""
	if !strings.Contains(pusher.RemoteURL, "?cid=") {
		url = pusher.RemoteURL + "?cid=" + pusher.Cc.CInfo.Cid
	} else {
		url = pusher.RemoteURL
	}

	//url := pusher.RemoteURL + "?cid=" + pusher.Cc.CInfo.Cid
	//url += "&streamPath=" + pusher.StreamPath

	pusher.Info("WscPusher try connect times:"+strconv.Itoa(pusher.connectCount), zap.String("remoteURL", url))

	conn, _, _, err := dialer.Dial(context.Background(), url)
	if err != nil {
		pusher.Error("WscPusher connect faild", zap.Error(err))
		return err
	}
	//
	pusher.SetParentCtx(context.Background()) //注入context
	pusher.RemoteAddr = url
	//conn.SetWriteDeadline(time.Now().Add(pusher.WriteTimeout))
	pusher.SetIO(conn)

	pusher.Conn = &conn
	pusher.Status = 1

	//发送FlvHeader
	pusher.WriteFlvHeader()

	return nil
}

func (sub *WscPusher) WriteFlvHeader() {
	at, vt := sub.Audio, sub.Video
	hasAudio, hasVideo := at != nil, vt != nil
	var amf util.AMF
	amf.Marshal("onMetaData")
	metaData := util.EcmaArray{
		"MetaDataCreator": "m7s" + Engine.Version,
		"hasVideo":        hasVideo,
		"hasAudio":        hasAudio,
		"hasMatadata":     true,
		"canSeekToEnd":    false,
		"duration":        0,
		"hasKeyFrames":    0,
		"framerate":       0,
		"videodatarate":   0,
		"filesize":        0,
	}
	var flags byte
	if hasAudio {
		flags |= (1 << 2)
		metaData["audiocodecid"] = int(at.CodecID)
		metaData["audiosamplerate"] = at.SampleRate
		metaData["audiosamplesize"] = at.SampleSize
		metaData["stereo"] = at.Channels == 2
	}
	if hasVideo {
		flags |= 1
		metaData["videocodecid"] = int(vt.CodecID)
		metaData["width"] = vt.SPSInfo.Width
		metaData["height"] = vt.SPSInfo.Height
	}
	amf.Marshal(metaData)
	// 写入FLV头
	if err := wsutil.WriteClientBinary(sub, []byte{'F', 'L', 'V', 0x01, flags, 0, 0, 0, 9, 0, 0, 0, 0}); err != nil {
		sub.OnConnErr(zap.Error(err))
	}
	//codec.WriteFLVTag(sub, codec.FLV_TAG_TYPE_SCRIPT, 0, amf.Buffer)

	//发送自定义FLV_TAG_TYPE_SCRIPT 脚本信息
	buffers := codec.AVCC2FLV(codec.FLV_TAG_TYPE_SCRIPT, 0, amf.Buffer)
	var data []byte
	for _, buf := range buffers {
		data = append(data, buf...)
	}
	if err := wsutil.WriteClientBinary(sub, data); err != nil {
		sub.OnConnErr(zap.Error(err))
	}

}

func (pusher *WscPusher) PlayFlv() {
	go pusher.Subscriber.PlayFLV()
}

// 预留双向通信
func (pusher *WscPusher) ReadFLVTag() {
	var startTs uint32
	offsetTs := pusher.absTS
	for {
		//读取掩码头部信息
		_, err := ws.ReadHeader(*pusher.Conn)
		if err != nil {
			// 处理读取头部信息错误
			pusher.OnConnErr(zap.Error(err))
			break
		}

		//log.Println(header)
		//{"error": "frames from client to server must be masked"}
		data, err := wsutil.ReadServerBinary(*pusher.Conn)
		if err != nil {
			//延迟10秒
			pusher.OnConnErr(zap.Error(err))
			break
		}

		// 解码数据
		// mask := header.Mask
		// for i, b := range data {
		// 	data[i] = b ^ mask[i%4]
		// }
		t, timestamp, payload, err := pusher.ParseFLVTag(data)
		if err != nil {
			pusher.OnConnErr(zap.Error(err))
		}
		if startTs == 0 {
			startTs = timestamp
		}
		pusher.absTS = offsetTs + (timestamp - startTs)

		var frame util.BLL

		mem := pusher.pool.Get(int(len(payload)))
		frame.Push(mem)
		//mem.Value = payload
		copy(mem.Value, payload)
		//log.Printf("type:%v, absTS:%v timestamp:%v\n", t, recever.absTS, timestamp)
		switch t {
		case codec.FLV_TAG_TYPE_AUDIO:
			//recever.WriteAVCCAudio(recever.absTS, &frame, recever.pool)
		case codec.FLV_TAG_TYPE_VIDEO:
			//recever.WriteAVCCVideo(recever.absTS, &frame, recever.pool)
		case codec.FLV_TAG_TYPE_SCRIPT:
			//recever.Info("script", zap.ByteString("data", payload))
			//frame.Recycle()
		}
	}

	pusher.Info("WscPusher ReadFLVTag end...")
}

func (pusher *WscPusher) Push() (err error) {
	pusher.Info("WscPusher try PlayFlv push...")
	pusher.PlayFlv()

	//这个循环对应Push 接口很重要否则会进入不断重连
	//阻塞等待接收服务回传的flv tag 应用与后期开发平台级联对讲功能
	go pusher.ReadFLVTag()
	//pusher.Info("WscPusher PlayFlv end...")

	for pusher.Status != 0 {
		//判断
		time.Sleep(15 * time.Second)
		stream := pusher.GetSubscriber().Stream
		//判断订阅的流是否关闭，如果关闭则，断开当前链接重新链接
		//流被关闭，指的是流的发布者
		if stream.IsClosed() {
			pusher.OnConnErr(zap.Error(errors.New("stream close")))
		}
		//订阅者是否正常播放
		if !pusher.IsPlaying() {
			pusher.OnConnErr(zap.Error(errors.New("stream sub not palying")))
		}

		//pusher.Info(fmt.Sprintf("WscPusher push  stream IsClosed:%v", stream.IsClosed()))
	}

	pusher.Info("WscPusher Push end...")
	return nil
}

func (pusher *WscPusher) IsClosed() bool {
	return pusher.Status != 1
}

// 统一处理读写错误
func (pusher *WscPusher) OnConnErr(reason ...zapcore.Field) {
	pusher.Error("WscPusher OnConnErr", reason[0])

	//停止订阅
	//pusher.Stop(reason[0])

	pusher.Disconnect()
}

func (pusher *WscPusher) ParseFLVTag(b []byte) (t byte, timestamp uint32, payload []byte, err error) {
	if len(b) < 11 {
		return 0, 0, nil, errors.New("FLV Tag data is too short")
	}

	t = b[0]                                                      // Tag类型
	dataSize := uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) // 数据部分大小
	timestamp = uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6]) // 时间戳
	timestamp |= uint32(b[7]) << 24                               // 扩展时间戳
	//streamID := uint32(b[8])<<16 | uint32(b[9])<<8 | uint32(b[10]) // 流ID  默认为0

	payload = b[11 : 11+dataSize] // 数据payload

	//log.Printf("dataSize:%v ,buf len:%v", dataSize+1, len(b))

	return t, timestamp, payload, nil
}

func (pusher *WscPusher) WriteFLVTag(tag FLVFrame) {
	//pusher.Info("try WriteFLVTag...")
	if pusher.Status == 0 {
		//pusher.Info("WriteFLVTag status not ready...")
		return
	}

	// 掩码密钥
	mask := [4]byte{0x12, 0x34, 0x56, 0x78}

	// 将FLVFrame转换为net.Buffers类型
	buffers := net.Buffers(tag)
	// 逐个取出字节缓冲区中的内容，并写入单独的字节切片
	var data []byte
	for _, buf := range buffers {
		data = append(data, buf...)
	}

	//for debug
	// t, timestamp, _, err := pusher.ParseFLVTag(data)
	// if err != nil {
	// 	pusher.OnConnErr(zap.Error(err))
	// } else {
	// 	//log.Println("type:", t)
	// 	//log.Println("timestamp:", timestamp)
	// 	//log.Println("payload:", payload)
	// }

	// 对FLV数据集进行掩码处理
	// maskedFLVData := make([]byte, len(data))
	// for i, b := range data {
	// 	maskedFLVData[i] = b ^ mask[i%4]
	// }

	//{"error": "frames from client to server must be masked"}
	//需要注意，websocket 客户端主动向服务器发送二进制消息需要添加掩码，否则服务器接收数据会提示错误：frames from client to server must be masked
	//发送ws.Header 掩码信息后，我局域网测试可以骗过服务器，实际上没有对flv 进行掩码，如果测试不行则打开前面注释，对数据进行掩码处理
	//
	if err := ws.WriteHeader(pusher, ws.Header{
		Fin:    true,
		OpCode: ws.OpBinary,
		Masked: true,
		Mask:   mask, //[4]byte{byte(rand.Intn(256)), byte(rand.Intn(256)), byte(rand.Intn(256)), byte(rand.Intn(256))},
		Length: int64(len(data)),
	}); err != nil {
		pusher.OnConnErr(zap.Error(err))
		return
	}

	if err := wsutil.WriteClientBinary(pusher, data); err != nil {
		pusher.OnConnErr(zap.Error(err))
	}
}

func (pusher *WscPusher) OnEvent(event any) {
	switch v := event.(type) {
	case ISubscriber:

	case FLVFrame:
		pusher.WriteFLVTag(v)
	default:
		pusher.Subscriber.OnEvent(event)
	}
}
