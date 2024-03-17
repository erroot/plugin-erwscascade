## 预览插件
![image](https://github.com/erroot/plugin-erwscascade/blob/main/result.jpg)
## 主要功能
  基于websocket 实现多个m7s级联
  - 支持http请求代理（webscoket 代理），实现实时控制下级平台信令通道
  - 支持公网级联，下级平台在局域网（如4G 网络）
  - 支持一个端口级联（http/https端口）要求支持websocket
  - 支持音视频级联（下级平台推流到上级平台）
  - 支持推流协议： rtmp 推流，rtsp 推流， websocket flv 推流等（上级平台端口资源限制开放，仅需http/https端口）
  - rtmp 推流，rtsp 推流功能是m7s 开源源插件功能，需要使能相关插件

## 插件地址

https://github.com/erroot/plugin-erwscascade.git

## 插件引入

```go
import (
    _ "m7s.live/plugin/erwscascade/v4"
)
```

## 配置

# websocket级联配置
```markdown

```golang
erwscascade:
  cid: "test-c001"            #本机平台ID 不配置则随机uuid
  server:                     #级联上级平台配置，支持同时接入多个上级平台
    -
      protocol: "wss"         #支持的协议ws,wss
      host: "47.111.28.16"
      port: 8441
      conextpath: ""
  push:
    repush: -1
    pushlist:
      njtv/glgc: ws://127.0.0.1:8450/erwscascade/wspush/on #推送本地流到上级平台，新的streamPath 为 streamPath-cid
## API
## server API
- `/erwscascade/httpproxy?cid=test-c001&httpPath=[dympath]`  ，http协议透传接口
- xx_m7s_url_xx 含义是 m7s 普通url 链接
- cid: 客户端ID(必须)
- httpPath:  代理请求的目的地址(必须)

- 示例1：请求下级平台test-c001,通过erwscascade ws 推流接口推流到上级   推送本地的流njtv/glgc 到上级平台 ws://127.0.0.1:8450/erwscascade/wspush/on 这个地址

http://127.0.0.1:8450/erwscascade/httpproxy/?cid=test-c001&httpPath=/erwscascade/api/push?streamPath=njtv/glgc&target=ws://127.0.0.1:8450/erwscascade/wspush/on


- 示例2:  请求下级平台 test-c001,  通过rtmp 推流接口推送流到上级  推送本地的流njtv/glgc 到上级平台 rtmp://127.0.0.1:1945/njtv/glgc-rtmp-push 这个地址

http://127.0.0.1:8450/erwscascade/httpproxy/?cid=test-c001&httpPath=/rtmp/api/push?streamPath=njtv/glgc&target=rtmp://127.0.0.1:1945/njtv/glgc-rtmp-push

- 示例3:  webrtc  sdp 交互信息代理透传
  浏览器请求链接：https://www.server.com.cn:8441/erwscascade/httpproxy?httpPath=webrtc/play/njtv/glgc-ts?cid=test-c001
  server 收到请求后，通过ws链路 ，把http 请求转换封装为json对象,发送给client, client 解析转发给自己的webrtc 插件接口，把结果再发送给sever,server 再把结果响应给浏览器

<!--
                POST sdp                          ws sdp                         POST sdp
            |--------------------          --------------------          --------------------|
browser <-- |                   -- server --                    -- client --                   |<--client service--
            |<--------------------         --------------------          --------------------|
                RSP sdp                           ws sdp                         RSP sdp
-->


```markdown
# websocket 消息体
```golang
type ProxyMessage struct {
	Url    string      `json:"url"`
	Header http.Header `json:"header"`
	Method string `json:"method"`
	Body   []byte `json:"body"`
}

type CascadingWsMessage struct {
	Sn   int         `json:"sn"`
	Type MessageType `json:"type"`
	Pad  []byte      `json:"pad"`
}
const (
	CInfo MessageType = iota
	HTTPProxyReq
	HTTPProxyRsp
	// 在此添加更多的枚举成员
)
## 使用erwscascade注意事项

- 无