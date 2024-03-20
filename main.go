package erwscascade

import (
	"embed"
	"io/ioutil"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/config"
	"m7s.live/engine/v4/util"
)

//go:embed ui
var f embed.FS

var defaultYaml DefaultYaml

type ServerConfig struct {
	Protocol   string `default:"https" desc:"上级平台端口" yaml:"protocol"`
	Host       string `default:"127.0.0.1" desc:"上级平台IP" yaml:"host"`
	Port       int    `default:"8440" desc:"上级平台端口" yaml:"port"`
	ConextPath string `default:"" desc:"上级平台根目录" yaml:"conextpath"`
}

type ClientInfo struct {
	Cid    string `default:"" desc:"上级平台端口" yaml:"cid" json:"cid"`
	Name   string `default:"" desc:"上级平台端口" yaml:"name" json:"name"`
	Serial string `default:"" desc:"上级平台端口" yaml:"serial" json:"serial"`
}

type ErWsCascadeConfig struct {
	DefaultYaml
	CInfo ClientInfo `desc:"客户端信息"  yaml:"cinfo"`
	//erwscascade/wsocket/register
	ServerConfig []ServerConfig `yaml:"server"`
	config.Publish
	config.Subscribe
	config.Push
}

func (p *ErWsCascadeConfig) push(streamPath string, url string) {
	if err := ErWsCascadePlugin.Push(streamPath, url, NewWscPusher(p), false); err != nil {
		ErWsCascadePlugin.Error("push", zap.String("streamPath", streamPath), zap.String("url", url), zap.Error(err))
	}
}

func (p *ErWsCascadeConfig) OnEvent(event any) {
	switch event.(type) {
	case FirstConfig:
		p.onClientSetup()
		for streamPath, url := range p.PushList {
			p.push(streamPath, url)
		}
		break
	case config.Config:
		break
	case SEclose:
		break
	}
}

var ErWsCascadePlugin = InstallPlugin(&ErWsCascadeConfig{})

// http 接口向上级平台推流
func (p *ErWsCascadeConfig) API_Push(rw http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	err := ErWsCascadePlugin.Push(query.Get("streamPath"), query.Get("target"), NewWscPusher(p), query.Has("save"))
	if err != nil {
		util.ReturnError(util.APIErrorQueryParse, err.Error(), rw, r)
	} else {
		util.ReturnOK(rw, r)
	}
}

/*
http ws 代理转发接口,实现 m7s 上级代理转发下级api

示例1：请求下级平台test-c001,通过erwscascade ws 推流接口推流到上级   推送本地的流njtv/glgc 到上级平台 ws://127.0.0.1:8450/erwscascade/wspush/on 这个地址

http://127.0.0.1:8450/erwscascade/httpproxy/?cid=test-c001&httpPath=/erwscascade/api/push?streamPath=njtv/glgc&target=ws://127.0.0.1:8450/erwscascade/wspush/on
示例2:  请求下级平台 test-c001,  通过rtmp 推流接口推送流到上级  推送本地的流njtv/glgc 到上级平台 rtmp://127.0.0.1:1945/njtv/glgc-rtmp-push 这个地址

http://127.0.0.1:8450/erwscascade/httpproxy/?cid=test-c001&httpPath=/rtmp/api/push?streamPath=njtv/glgc&target=rtmp://127.0.0.1:1945/njtv/glgc-rtmp-push
*/
func (p *ErWsCascadeConfig) HttpProxy_(w http.ResponseWriter, r *http.Request) {
	///erwscascade/httpProxy/webrtc/play/live/webrtc
	// 修改请求 URL
	//fmt.Printf("config: %v\n", config.Gloub)
	// 去除原始请求中的 /proxy 部分
	//targetPath := strings.TrimPrefix(r.URL.Path, "/httpproxy")

	// if r.URL.RawQuery != "" {
	// 	targetURL += "?" + r.URL.RawQuery
	// }

	// 构造新的查询参数
	queryParams := r.URL.Query()
	cid := queryParams.Get("cid")
	if cid != "" {
		queryParams.Del("cid")
	} else {
		util.ReturnError(util.APIErrorQueryParse, "invalid cid", w, r)
		return
	}
	httpPath := queryParams.Get("httpPath")
	if httpPath != "" {
		queryParams.Del("httpPath")
	} else {
		util.ReturnError(util.APIErrorQueryParse, "invalid httpPath", w, r)
		return
	}

	// 重新构造查询参数
	newQuery := queryParams.Encode() //特殊字符被编码了
	//代理到下级平台
	if newQuery != "" {
		if strings.Contains(httpPath, "?") {
			httpPath += "&"
		} else {
			httpPath += "?"
		}
		httpPath += newQuery
	}

	decodedStr, err := url.QueryUnescape(httpPath) //url 解码
	if err != nil {
		util.ReturnError(util.APIErrorQueryParse, "QueryUnescape faild", w, r)
		return
	}
	req := ProxyMessage{
		Url:    decodedStr, //url 解码
		Method: r.Method,
	}
	// 创建一个新的 http.Header 对象
	newHeader := make(http.Header)
	// 获取 Content-Type
	contentType := r.Header.Get("Content-Type")
	if contentType != "" {
		newHeader.Set("Content-Type", contentType)
	}
	// 获取 Set-Cookie
	setCookie := r.Header.Get("Set-Cookie")
	if setCookie != "" {
		newHeader.Set("Set-Cookie", setCookie)
	}
	req.Header = newHeader

	if r.ContentLength > 0 {
		// 读取 Body 的内容
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			ErWsCascadePlugin.Error("Error reading request body", zap.Error(err))
			return
		}
		req.Body = body
	}
	//ws send msg
	rsp, err := p.transWsProxyMessage(cid, req)
	if err != nil {
		ErWsCascadePlugin.Error("WsProxy faild", zap.Error(err))
		return
	}
	// 将读取的 Body 内容作为响应返回给客户端
	w.Write(rsp)

	return

	// //本机代理
	// targetURL := "http://127.0.0.1:8440" + targetPath
	// if newQuery != "" {
	// 	targetURL += "?" + newQuery
	// }
	// // 创建新的请求
	// req, err := http.NewRequest(r.Method, targetURL, r.Body)
	// if err != nil {
	// 	http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	return
	// }

	// // 复制请求头
	// req.Header = make(http.Header)
	// for key, values := range r.Header {
	// 	for _, value := range values {
	// 		req.Header.Add(key, value)
	// 	}
	// }

	// // 发送请求
	// resp, err := http.DefaultClient.Do(req)
	// if err != nil {
	// 	http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	return
	// }
	// defer resp.Body.Close()

	// // 将响应写回给客户端
	// for key, values := range resp.Header {
	// 	for _, value := range values {
	// 		w.Header().Add(key, value)
	// 	}
	// 	w.WriteHeader(resp.StatusCode)
	// 	io.Copy(w, resp.Body)
	// }
}

/*
	func (p *ErWsCascadeConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/erwscascade/" {
			var s string
			Streams.Range(func(streamPath string, _ *Stream) {
				s += fmt.Sprintf("<a href='%s'>%s</a><br>", streamPath, streamPath)
			})
			if s != "" {
				s = "<b>Live Streams</b><br>" + s
			}
			for name, p := range Plugins {
				if pullcfg, ok := p.Config.(config.PullConfig); ok {
					if pullonsub := pullcfg.GetPullConfig().PullOnSub; pullonsub != nil {
						s += fmt.Sprintf("<b>%s pull stream on subscribe</b><br>", name)
						for streamPath, url := range pullonsub {
							s += fmt.Sprintf("<a href='%s'>%s</a> <-- %s<br>", streamPath, streamPath, url)
						}
					}
				}
			}
			w.Write([]byte(s))
			return
		}
		ss := strings.Split(r.URL.Path, "/")
		if b, err := f.ReadFile("ui/" + ss[len(ss)-1]); err == nil {
			w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(ss[len(ss)-1])))
			w.Write(b)
		} else {
			//w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			//w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
			b, err = f.ReadFile("ui/index.html")
			w.Write(b)
		}
	}
*/

func (p *ErWsCascadeConfig) Ui_(w http.ResponseWriter, r *http.Request) {
	ss := strings.Split(r.URL.Path, "/")
	if b, err := f.ReadFile("ui/" + ss[len(ss)-1]); err == nil {
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(ss[len(ss)-1])))
		w.Write(b)
	} else {
		//w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		//w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		b, err = f.ReadFile("ui/index.html")
		w.Write(b)
	}
}

type CascadingStream struct {
	Source     string
	StreamPath string
}

func filterStreams() (ss []*CascadingStream) {

	//优先获取配置文件中视频流
	for name, p := range Plugins {
		if pullcfg, ok := p.Config.(config.PullConfig); ok {

			if pullonstart := pullcfg.GetPullConfig().PullOnStart; pullonstart != nil {
				//s += fmt.Sprintf("<b>%s pull stream on subscribe</b><br>", name)
				//for streamPath, url := range pullonsub {
				var sourcename string = name
				sourcename += "Pull"

				for streamPath := range pullonstart {
					ss = append(ss, &CascadingStream{sourcename, streamPath})
					//s += fmt.Sprintf("<a href='%s'>%s</a> <-- %s<br>", streamPath, streamPath, url)
				}
			}

			if pullonsub := pullcfg.GetPullConfig().PullOnSub; pullonsub != nil {
				//s += fmt.Sprintf("<b>%s pull stream on subscribe</b><br>", name)
				//for streamPath, url := range pullonsub {
				var sourcename string = name

				sourcename += "Pull"

				for streamPath := range pullonsub {
					ss = append(ss, &CascadingStream{sourcename, streamPath})
					//s += fmt.Sprintf("<a href='%s'>%s</a> <-- %s<br>", streamPath, streamPath, url)
				}
			}
		}
	}

	//过滤出动态添加的视频流
	//Streams.RLock()
	//defer Streams.RUnlock()

	Streams.Range(func(streamPath string, s *Stream) {
		//s += fmt.Sprintf("<a href='%s'>%s</a><br>", streamPath, streamPath)
		var isrepeat bool = false
		for _, s := range ss {
			if streamPath == s.StreamPath {
				isrepeat = true
			}
		}
		if !isrepeat {
			ss = append(ss, &CascadingStream{"api", streamPath})
		}
	})

	return
}

// 客户端列表
func (*ErWsCascadeConfig) API_clientlist(w http.ResponseWriter, r *http.Request) {
	// 输出map对象内容
	list := make([]ClientInfo, 0)

	for _, value := range clientConnections {
		//fmt.Println("Key:", key, "Value:", value)
		list = append(list, value.CInfo)
	}
	util.ReturnValue(list, w, r)
}

func (p *ErWsCascadeConfig) API_streamlist(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	cid := query.Get("cid")

	// 本级流资源列表
	if cid == "" {
		util.ReturnFetchValue(filterStreams, w, r)
		return
	}

	// 下级平台流列表
	req := ProxyMessage{
		Url:    "/erwscascade/api/streamlist", //url 解码
		Method: "GET",
	}

	//ws send msg
	rsp, err := p.transWsProxyMessage(cid, req)
	if err != nil {
		ErWsCascadePlugin.Error("WsProxy faild", zap.Error(err))
		return
	}
	// 将读取的 Body 内容作为响应返回给客户端
	w.Write(rsp)

	return

}
