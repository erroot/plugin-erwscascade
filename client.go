package erwscascade

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"
)

var wsclients = make(map[string]*CascadingWsClient)

var cSn int = 0

type CascadingWsClient struct {
	CInfo     ClientInfo
	URL       string
	Conn      net.Conn
	IsClosed  bool
	IsRecving bool
}

type ProxyMessage struct {
	Url    string      `json:"url"`
	Header http.Header `json:"header"`
	Method string      `json:"method"`
	Body   []byte      `json:"body"`
}

// MessageType 定义枚举类型 MessageType
type MessageType int

const (
	CInfo MessageType = iota
	HTTPProxyReq
	HTTPProxyRsp
	// 在此添加更多的枚举成员
)

func (m MessageType) String() string {
	types := [...]string{"CInfo", "HTTPProxyReq", "HTTPProxyRsp"}
	if m < CInfo || m > HTTPProxyRsp {
		return "Unknown"
	}
	return types[m]
}

type CascadingWsMessage struct {
	Sn   int         `json:"sn"`
	Type MessageType `json:"type"`
	Pad  []byte      `json:"pad"`
}

func NewCascadingWsClient(cinfo ClientInfo, url string) *CascadingWsClient {
	return &CascadingWsClient{
		CInfo:     cinfo,
		URL:       url,
		IsClosed:  true,
		IsRecving: false,
	}
}

func (c *CascadingWsClient) Connect() error {
	log.Printf("try 2 ws Connect: %v", c.URL)

	// 创建自定义的 TLS 配置
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	// 创建 Dialer
	dialer := ws.Dialer{
		TLSConfig: tlsConfig,
	}
	//conn, _, _, err := ws.Dial(context.Background(), c.URL)
	conn, _, _, err := dialer.Dial(context.Background(), c.URL)
	if err != nil {
		log.Printf("ws Connect faild: %v", err)
		return err
	}

	c.Conn = conn
	c.IsClosed = false

	return nil
}

func (c *CascadingWsClient) Close() {
	c.Conn.Close()
	c.IsClosed = true
}
func (c *CascadingWsClient) SendClientInfo() error {
	infoBytes, _ := json.Marshal(c.CInfo)
	cSn++
	msg := CascadingWsMessage{
		Sn:   cSn,
		Type: CInfo,
		Pad:  infoBytes,
	}

	msgBytes, _ := json.Marshal(msg)

	err := wsutil.WriteClientMessage(c.Conn, ws.OpText, msgBytes)
	if err != nil {
		return err
	}

	return nil
}

func (c *CascadingWsClient) Reconnect() error {
	if !c.IsClosed {
		//c.Close()
		//链接成功
		return nil
	}

	err := c.Connect()
	if err != nil {
		//5 秒后重新链接
		time.Sleep(time.Duration(5) * time.Second)
		log.Printf("Connect: %v", err)
		return err
	}

	log.Printf("connect 2 ws server sucess....")

	err = c.SendClientInfo()
	if err != nil {
		log.Printf("SendClientInfo: %v", err)
		return err
	}

	if !c.IsRecving {
		go c.receiveWsMessages()
	}

	return nil
}

func (c *CascadingWsClient) onWsProxyMessages(wsMessage CascadingWsMessage, proxyMessage ProxyMessage) error {

	targetURL := proxyMessage.Url

	log.Printf("Parsed ProxyMessage:%v,%v,%v\n",
		proxyMessage.Url,
		proxyMessage.Header,
		proxyMessage.Method)

	if !strings.HasPrefix(proxyMessage.Url, "http:") {
		//log.Println("字符串以'http:'打头")
		targetURL = "http://127.0.0.1:8440" + proxyMessage.Url
	}

	// 发起代理请求
	req, err := http.NewRequest(proxyMessage.Method, targetURL, bytes.NewBuffer(proxyMessage.Body))
	if err != nil {
		log.Printf("Error creating HTTP request: %v", err)
		return err
	}
	if proxyMessage.Header != nil {
		//req.Header = proxyMessage.Header
		for key, values := range proxyMessage.Header {
			// 添加自定义的 Header
			req.Header.Set(key, values[0])
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending HTTP request: %v", err)
		return err
	}
	defer resp.Body.Close()

	// 读取响应内容
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body: %v", err)
		return err
	}
	var rspProxyMessage CascadingWsMessage
	// 将响应内容填充到 CascadingWsProxyMessage 中并返回给服务器端
	rspProxyMessage.Pad = respBody
	rspProxyMessage.Sn = wsMessage.Sn
	rspProxyMessage.Type = HTTPProxyRsp

	responseJSON, _ := json.Marshal(rspProxyMessage)
	err = wsutil.WriteClientMessage(c.Conn, ws.OpText, responseJSON)
	if err != nil {
		log.Printf("Error sending response: %v", err)
		//延迟10秒
		return err
	}

	return nil
}

func (c *CascadingWsClient) receiveWsMessages() {

	c.IsRecving = true
	for {
		if c.IsClosed {
			//延迟1秒
			time.Sleep(time.Duration(1) * time.Second)
			continue
		}

		msg, _, err := wsutil.ReadServerData(c.Conn)
		if err != nil {
			// 判断错误类型
			// if opErr, ok := err.(*net.OpError); ok {
			// 	// 检测错误消息中是否包含特定子字符串
			// 	if strings.Contains(opErr.Err.Error(), "An existing connection was forcibly closed by the remote host") {
			// 		log.Println("Connection forcibly closed by remote host")
			// 		// 在这里可以添加处理逻辑，比如自动重连等
			// 	} else {
			// 		log.Println("Other net.OpError occurred:", opErr)
			// 	}
			// } else if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
			// 	log.Println("Temporary error occurred, retrying...")
			// 	// 在这里可以添加自动重连的逻辑
			// 	c.Close()
			// 	continue
			// } else if err == net.ErrClosed {
			// 	// 在这里可以添加自动重连的逻辑
			// 	log.Println("Connection closed by server,try reconnect...")
			// 	c.Close()
			// 	continue
			// }
			log.Printf("Error reading message: %v", err)
			//延迟10秒
			//continue
			//断开重连
			c.Close()
			break
		}

		// 解析收到的消息为 CascadingWsProxyMessage
		var wsMessage CascadingWsMessage
		err = json.Unmarshal(msg, &wsMessage)
		if err != nil {
			log.Printf("Error decoding message: %v", err)
			continue
		}
		// 根据 MessageType 的值解析 Pad 字段为 ProxyMessage
		var proxyMessage ProxyMessage
		if wsMessage.Type == HTTPProxyReq {
			err := json.Unmarshal(wsMessage.Pad, &proxyMessage)
			if err != nil {
				log.Println("Error parsing ProxyMessage:", err)
				continue
			}

			c.onWsProxyMessages(wsMessage, proxyMessage)
		} else {
			log.Println("MessageType is:", wsMessage.Type.String())
		}
	}
	c.IsRecving = false
}

func (p *ErWsCascadeConfig) onClientSetup() {
	cid := p.CInfo.Cid
	if cid == "" {
		// 创建一个新的UUID
		newUUID, err := uuid.NewRandom()
		if err != nil {
			log.Println("生成UUID错误:", err)
			return
		}
		// 将UUID转换为字符串形式
		cid = newUUID.String()
	}

	go func() {
		for idx, s := range p.ServerConfig {
			//client.Reconnect()
			protocol := "ws"
			if s.Protocol == "https" {
				protocol = "wss"
			} else if s.Protocol == "wss" {
				protocol = "wss"
			}
			host := s.Host
			if s.Port != 80 && s.Port != 443 {
				host += fmt.Sprintf(":%d", s.Port)
			}
			u := url.URL{
				Scheme: protocol,
				Host:   host,
				Path:   s.ConextPath + "/erwscascade/wsocket/register?cid=" + cid,
			}
			client := NewCascadingWsClient(p.CInfo, u.String())
			wsclients[fmt.Sprintf("%d", idx)] = client
		}
		// 保持连接
		for {
			for _, client := range wsclients {
				client.Reconnect()
			}
			time.Sleep(time.Duration(5) * time.Second)
		}

	}()
}
