package erwscascade

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

var responseChan = make(chan CascadingWsMessage)

type WsClientConn struct {
	Conn    *net.Conn
	RspChan chan CascadingWsMessage
	CInfo   ClientInfo
}

var clientConnections = make(map[string]*WsClientConn)
var connectionsThreads = make(map[string]chan struct{})
var connectionsLock sync.RWMutex

var gSn int = 0

func (p *ErWsCascadeConfig) transWsProxyMessage(cid string, req ProxyMessage) ([]byte, error) {
	connectionsLock.Lock()
	client, ok := clientConnections[cid]
	connectionsLock.Unlock()

	if !ok {
		return nil, errors.New("no find client")
	}

	reqPad, _ := json.Marshal(req)

	gSn++
	reqMsg := CascadingWsMessage{
		Sn:   gSn,
		Type: HTTPProxyReq,
		Pad:  reqPad,
	}
	reqBytes, _ := json.Marshal(reqMsg)

	log.Printf("server proxy msg sn:%v, type:%v\n", reqMsg.Sn, reqMsg.Type)

	err := wsutil.WriteServerMessage(*client.Conn, ws.OpText, reqBytes)
	if err != nil {
		log.Println("WriteServerMessage err:", err)
		return nil, err
	}

	// 设置超时时间为 10 秒
	timeout := time.After(10 * time.Second)

	for {
		select {
		case rspMsg := <-client.RspChan:

			log.Printf("client rsp msg sn:%v, type:%v\n", rspMsg.Sn, rspMsg.Type)

			// 收到响应消息
			if rspMsg.Sn == reqMsg.Sn {
				return rspMsg.Pad, nil
			} else {
				//继续等超时
				continue
			}
		case <-timeout:
			log.Println("Timeout: No response received within 10 seconds")
			return nil, errors.New("time out")
		}
	}

	return nil, nil

}

func (p *ErWsCascadeConfig) sendWsMessageToClient(cid string, message string) {
	connectionsLock.Lock()
	client, ok := clientConnections[cid]
	connectionsLock.Unlock()

	if ok {
		err := wsutil.WriteServerMessage(*client.Conn, ws.OpText, []byte(message))
		if err != nil {
			log.Println("Error sending message to client:", err)
		}
	}
}

func (p *ErWsCascadeConfig) receiveWsMessages(client *WsClientConn, cid string) {
	for {
		msg, _, err := wsutil.ReadClientData(*client.Conn)
		if err != nil {
			log.Println("Error reading message:", err)
			break
		}
		log.Printf("Received message from client: %s\n", cid)
		// Handle client messages here
		//p.sendWsMessageToClient(cid, "Server RSP")

		// 解析收到的消息为 CascadingWsProxyMessage
		var wsMessage CascadingWsMessage
		err = json.Unmarshal(msg, &wsMessage)
		if err != nil {
			log.Printf("Error decoding message: %v", err)
			continue
		}

		if wsMessage.Type == HTTPProxyRsp {
			//通知阻塞函数
			client.RspChan <- wsMessage

		} else if wsMessage.Type == CInfo {
			var clientInfo ClientInfo
			err := json.Unmarshal(wsMessage.Pad, &clientInfo)
			if err != nil {
				log.Println("Error parsing clientInfo:", err)
				continue
			}
			log.Println("Parsed ClientInfo:", clientInfo)
			client.CInfo = clientInfo
		} else {
			log.Println("MessageType is:", wsMessage.Type.String())
		}

	}

	log.Printf("ws client offline cid: %s\n", cid)

	// 断开连接时清理操作
	connectionsLock.Lock()
	delete(clientConnections, cid)
	close(connectionsThreads[cid])
	connectionsLock.Unlock()
}

func (p *ErWsCascadeConfig) Wsocket_(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") == "websocket" {
		// 判断 URL 中是否包含 "register" 关键字

		//customHeader := r.Header.Get("X-Custom-Header")
		// 获取完整的 URL
		fullURL := r.URL.String()

		//判断URL编码特殊字符%2F,%2B,%3F,%25，包含这些则自动转码?= 等
		log.Printf("Full URL: %s\n", fullURL)

		if matched, _ := regexp.MatchString("/register", r.URL.Path); matched {
			// 获取 cid 参数
			//cid := r.URL.Query().Get("cid")

			// 解码 URL
			decodedURL, err := url.QueryUnescape(fullURL)
			if err != nil {
				log.Printf("Error decoding URL: %v\n", err)
				return
			}

			// 手动解析解码后的 URL 中的参数
			var queryString string
			if strings.Contains(decodedURL, "?") {
				queryString = strings.Split(decodedURL, "?")[1]
			}
			queryParams, err := url.ParseQuery(queryString)
			if err != nil {
				log.Printf("Error parsing query string: %v\n", err)
				return
			}
			cid := queryParams.Get("cid")
			log.Printf("CID value: %s\n", cid)

			conn, _, _, err := ws.UpgradeHTTP(r, w)
			if err != nil {
				log.Printf("UpgradeHTTP error:%v", err)
				return
			}

			if cid == "" {
				log.Printf("invalid cid refuse connect\n")
				conn.Close()
				return
			}

			client := WsClientConn{
				Conn:    &conn,
				RspChan: make(chan CascadingWsMessage),
			}
			connectionsLock.Lock()
			clientConnections[cid] = &client
			connectionsThreads[cid] = make(chan struct{})
			connectionsLock.Unlock()

			// 启动线程接收客户端消息
			go p.receiveWsMessages(&client, cid)
		}
		return
	} else {
		return
	}
}
