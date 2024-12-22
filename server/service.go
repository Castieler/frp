// Copyright 2017 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/fatedier/golib/crypto"
	"github.com/fatedier/golib/net/mux"
	fmux "github.com/hashicorp/yamux"
	quic "github.com/quic-go/quic-go"
	"github.com/samber/lo"

	"github.com/fatedier/frp/pkg/auth"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	modelmetrics "github.com/fatedier/frp/pkg/metrics"
	"github.com/fatedier/frp/pkg/msg"
	plugin "github.com/fatedier/frp/pkg/plugin/server"
	"github.com/fatedier/frp/pkg/ssh"
	"github.com/fatedier/frp/pkg/transport"
	httppkg "github.com/fatedier/frp/pkg/util/http"
	"github.com/fatedier/frp/pkg/util/log"
	netpkg "github.com/fatedier/frp/pkg/util/net"
	"github.com/fatedier/frp/pkg/util/tcpmux"
	"github.com/fatedier/frp/pkg/util/util"
	"github.com/fatedier/frp/pkg/util/version"
	"github.com/fatedier/frp/pkg/util/vhost"
	"github.com/fatedier/frp/pkg/util/xlog"
	"github.com/fatedier/frp/server/controller"
	"github.com/fatedier/frp/server/group"
	"github.com/fatedier/frp/server/metrics"
	"github.com/fatedier/frp/server/ports"
	"github.com/fatedier/frp/server/proxy"
	"github.com/fatedier/frp/server/visitor"
)

const (
	connReadTimeout       time.Duration = 10 * time.Second // 连接读取超时时间
	vhostReadWriteTimeout time.Duration = 30 * time.Second // 虚拟主机读写超时时间
)

func init() {
	crypto.DefaultSalt = "frp" // 设置默认的加密盐值
	// 禁用 quic-go 的接收缓冲区警告
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	// 默认禁用 quic-go 的 ECN 支持，可能在某些操作系统上引发问题
	if os.Getenv("QUIC_GO_DISABLE_ECN") == "" {
		os.Setenv("QUIC_GO_DISABLE_ECN", "true")
	}
}

// Server 服务
type Service struct {
	muxer *mux.Mux // 多路复用器，用于将连接分发到不同的处理程序

	listener net.Listener // 接收客户端连接的监听器

	ctlManager *ControlManager // 管理所有控制器

	pxyManager *proxy.Manager // 管理所有代理

	httpVhostRouter *vhost.Routers // HTTP 虚拟主机路由器

	rc *controller.ResourceController // 所有资源管理器和控制器

	webServer *httppkg.Server // 用于仪表板 UI 和 API 的 Web 服务器

	sshTunnelGateway *ssh.Gateway // SSH 隧道网关

	authVerifier auth.Verifier // 基于选定方法验证身份验证

	tlsConfig *tls.Config // TLS 配置

	cfg *v1.ServerConfig // 服务器配置

	ctx    context.Context    // 服务上下文
	cancel context.CancelFunc // 调用以停止服务
}

func NewService(cfg *v1.ServerConfig) (*Service, error) {
	tlsConfig, err := transport.NewServerTLSConfig(
		cfg.Transport.TLS.CertFile,
		cfg.Transport.TLS.KeyFile,
		cfg.Transport.TLS.TrustedCaFile)
	if err != nil {
		return nil, err // 如果创建 TLS 配置失败，返回错误
	}

	var webServer *httppkg.Server
	if cfg.WebServer.Port > 0 {
		ws, err := httppkg.NewServer(cfg.WebServer)
		if err != nil {
			return nil, err // 如果创建 Web 服务器失败，返回错误
		}
		webServer = ws

		modelmetrics.EnableMem() // 启用内存指标
		if cfg.EnablePrometheus {
			modelmetrics.EnablePrometheus() // 启用 Prometheus 指标
		}
	}

	svr := &Service{
		ctlManager: NewControlManager(), // 创建新的控制管理器
		pxyManager: proxy.NewManager(),  // 创建新的代理管理器
		rc: &controller.ResourceController{
			VisitorManager: visitor.NewManager(),                                       // 创建新的访客管理器
			TCPPortManager: ports.NewManager("tcp", cfg.ProxyBindAddr, cfg.AllowPorts), // 创建新的 TCP 端口管理器
			UDPPortManager: ports.NewManager("udp", cfg.ProxyBindAddr, cfg.AllowPorts), // 创建新的 UDP 端口管理器
		},
		httpVhostRouter: vhost.NewRouters(),             // 创建新的虚拟主机路由器
		authVerifier:    auth.NewAuthVerifier(cfg.Auth), // 创建新的身份验证器
		webServer:       webServer,                      // 设置 Web 服务器
		tlsConfig:       tlsConfig,                      // 设置 TLS 配置
		cfg:             cfg,                            // 设置服务器配置
		ctx:             context.Background(),           // 创建新的上下文
	}
	if webServer != nil {
		webServer.RouteRegister(svr.registerRouteHandlers) // 注册路由处理程序
	}

	// 创建 tcpmux httpconnect 多路复用器
	if cfg.TCPMuxHTTPConnectPort > 0 {
		var l net.Listener
		address := net.JoinHostPort(cfg.ProxyBindAddr, strconv.Itoa(cfg.TCPMuxHTTPConnectPort))
		l, err = net.Listen("tcp", address)
		if err != nil {
			return nil, fmt.Errorf("create server listener error, %v", err) // 如果创建监听器失败，返回错误
		}

		svr.rc.TCPMuxHTTPConnectMuxer, err = tcpmux.NewHTTPConnectTCPMuxer(l, cfg.TCPMuxPassthrough, vhostReadWriteTimeout)
		if err != nil {
			return nil, fmt.Errorf("create vhost tcpMuxer error, %v", err) // 如果创建虚拟主机 tcpMuxer 失败，返回错误
		}
		log.Infof("tcpmux httpconnect multiplexer listen on %s, passthough: %v", address, cfg.TCPMuxPassthrough)
	}

	// 初始化组控制器
	svr.rc.TCPGroupCtl = group.NewTCPGroupCtl(svr.rc.TCPPortManager)

	// 初始化 HTTP 组控制器
	svr.rc.HTTPGroupCtl = group.NewHTTPGroupController(svr.httpVhostRouter)

	// 初始化 TCP 多路复用组控制器
	svr.rc.TCPMuxGroupCtl = group.NewTCPMuxGroupCtl(svr.rc.TCPMuxHTTPConnectMuxer)

	// 初始化 404 未找到页面
	vhost.NotFoundPagePath = cfg.Custom404Page

	var (
		httpMuxOn  bool
		httpsMuxOn bool
	)
	if cfg.BindAddr == cfg.ProxyBindAddr {
		if cfg.BindPort == cfg.VhostHTTPPort {
			httpMuxOn = true
		}
		if cfg.BindPort == cfg.VhostHTTPSPort {
			httpsMuxOn = true
		}
	}

	// 监听客户端连接
	address := net.JoinHostPort(cfg.BindAddr, strconv.Itoa(cfg.BindPort))
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("create server listener error, %v", err) // 如果创建监听器失败，返回错误
	}

	svr.muxer = mux.NewMux(ln)                                                      // 创建新的多路复用器
	svr.muxer.SetKeepAlive(time.Duration(cfg.Transport.TCPKeepAlive) * time.Second) // 设置保持连接时间
	go func() {
		_ = svr.muxer.Serve() // 启动多路复用器服务
	}()
	ln = svr.muxer.DefaultListener()

	svr.listener = ln
	log.Infof("frps tcp listen on %s", address)

	// 创建 http 虚拟主机多路复用器
	if cfg.VhostHTTPPort > 0 {
		rp := vhost.NewHTTPReverseProxy(vhost.HTTPReverseProxyOptions{
			ResponseHeaderTimeoutS: cfg.VhostHTTPTimeout,
		}, svr.httpVhostRouter)
		svr.rc.HTTPReverseProxy = rp

		address := net.JoinHostPort(cfg.ProxyBindAddr, strconv.Itoa(cfg.VhostHTTPPort))
		server := &http.Server{
			Addr:              address,
			Handler:           rp,
			ReadHeaderTimeout: 60 * time.Second,
		}
		var l net.Listener
		if httpMuxOn {
			l = svr.muxer.ListenHTTP(1)
		} else {
			l, err = net.Listen("tcp", address)
			if err != nil {
				return nil, fmt.Errorf("create vhost http listener error, %v", err) // 如果创建虚拟主机 http 监听器失败，返回错误
			}
		}
		go func() {
			_ = server.Serve(l) // 启动 HTTP 服务
		}()
		log.Infof("http service listen on %s", address)
	}

	// 创建 https 虚拟主机多路复用器
	if cfg.VhostHTTPSPort > 0 {
		var l net.Listener
		if httpsMuxOn {
			l = svr.muxer.ListenHTTPS(1)
		} else {
			address := net.JoinHostPort(cfg.ProxyBindAddr, strconv.Itoa(cfg.VhostHTTPSPort))
			l, err = net.Listen("tcp", address)
			if err != nil {
				return nil, fmt.Errorf("create server listener error, %v", err) // 如果创建服务器监听器失败，返回错误
			}
			log.Infof("https service listen on %s", address)
		}

		svr.rc.VhostHTTPSMuxer, err = vhost.NewHTTPSMuxer(l, vhostReadWriteTimeout)
		if err != nil {
			return nil, fmt.Errorf("create vhost httpsMuxer error, %v", err) // 如果创建虚拟主机 httpsMuxer 失败，返回错误
		}
	}

	return svr, nil
}

// Run 方法启动服务并处理所有监听器
func (svr *Service) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx) // 创建一个可取消的上下文
	svr.ctx = ctx
	svr.cancel = cancel

	// 如果 Web 服务器不为空，则启动仪表板 Web 服务器
	if svr.webServer != nil {
		go func() {
			log.Infof("dashboard listen on %s", svr.webServer.Address()) // 打印仪表板监听地址
			if err := svr.webServer.Run(); err != nil {
				log.Warnf("dashboard server exit with error: %v", err) // 如果仪表板服务器退出时发生错误，记录警告
			}
		}()
	}

	// 处理主监听器
	svr.HandleListener(svr.listener, false)

	<-svr.ctx.Done() // 阻塞直到上下文被取消
	// 服务上下文可能不会被 svr.Close() 取消，我们应该在此处调用以释放资源
	if svr.listener != nil {
		svr.Close() // 关闭服务
	}
}

// Close 方法关闭所有监听器和管理器
func (svr *Service) Close() error {
	if svr.listener != nil {
		svr.listener.Close() // 关闭主监听器
		svr.listener = nil
	}
	svr.ctlManager.Close() // 关闭控制管理器
	if svr.cancel != nil {
		svr.cancel() // 取消上下文
	}
	return nil
}

// handleConnection 方法处理传入的连接
func (svr *Service) handleConnection(ctx context.Context, conn net.Conn, internal bool) {
	xl := xlog.FromContextSafe(ctx) // 从上下文中获取日志对象

	var (
		rawMsg msg.Message
		err    error
	)

	_ = conn.SetReadDeadline(time.Now().Add(connReadTimeout)) // 设置连接读取超时时间
	if rawMsg, err = msg.ReadMsg(conn); err != nil {
		log.Tracef("Failed to read message: %v", err) // 如果读取消息失败，记录跟踪日志
		conn.Close()                                  // 关闭连接
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // 清除读取超时时间

	switch m := rawMsg.(type) {
	case *msg.Login:
		// 服务器插件钩子
		content := &plugin.LoginContent{
			Login:         *m,
			ClientAddress: conn.RemoteAddr().String(),
		}
		m = &content.Login
		err = svr.RegisterControl(conn, m, internal) // 注册控制连接

		// 如果登录失败，发送错误消息
		// 否则在控制的工作协程中发送成功消息
		if err != nil {
			xl.Warnf("register control error: %v", err) // 记录警告日志
			_ = msg.WriteMsg(conn, &msg.LoginResp{
				Version: version.Full(),
				Error:   util.GenerateResponseErrorString("register control error", err, lo.FromPtr(svr.cfg.DetailedErrorsToClient)),
			})
			conn.Close() // 关闭连接
		}
	case *msg.NewWorkConn:
		if err := svr.RegisterWorkConn(conn, m); err != nil {
			conn.Close() // 如果注册工作连接失败，关闭连接
		}
	case *msg.NewVisitorConn:
		if err = svr.RegisterVisitorConn(conn, m); err != nil {
			xl.Warnf("register visitor conn error: %v", err) // 记录警告日志
			_ = msg.WriteMsg(conn, &msg.NewVisitorConnResp{
				ProxyName: m.ProxyName,
				Error:     util.GenerateResponseErrorString("register visitor conn error", err, lo.FromPtr(svr.cfg.DetailedErrorsToClient)),
			})
			conn.Close() // 关闭连接
		} else {
			_ = msg.WriteMsg(conn, &msg.NewVisitorConnResp{
				ProxyName: m.ProxyName,
				Error:     "",
			})
		}
	default:
		log.Warnf("Error message type for the new connection [%s]", conn.RemoteAddr().String()) // 记录警告日志
		conn.Close()                                                                            // 关闭连接
	}
}

// HandleListener 接收来自客户端的连接并调用 handleConnection 处理它们
// 如果 internal 为 true，则表示此监听器用于内部通信，如 ssh 隧道网关
// TODO: 通过上下文传递一些监听器/连接的参数以避免传递过多的参数
func (svr *Service) HandleListener(l net.Listener, internal bool) {
	// 监听来自客户端的传入连接
	for {
		c, err := l.Accept()
		if err != nil {
			log.Warnf("Listener for incoming connections from client closed") // 记录警告日志
			return
		}
		// 将 xlog 对象注入 net.Conn 上下文
		xl := xlog.New()
		ctx := context.Background()

		c = netpkg.NewContextConn(xlog.NewContext(ctx, xl), c)

		if !internal {
			log.Tracef("start check TLS connection...") // 开始检查 TLS 连接
			originConn := c
			forceTLS := svr.cfg.Transport.TLS.Force
			var isTLS, custom bool
			c, isTLS, custom, err = netpkg.CheckAndEnableTLSServerConnWithTimeout(c, svr.tlsConfig, forceTLS, connReadTimeout)
			if err != nil {
				log.Warnf("CheckAndEnableTLSServerConnWithTimeout error: %v", err) // 记录警告日志
				originConn.Close()
				continue
			}
			log.Tracef("check TLS connection success, isTLS: %v custom: %v internal: %v", isTLS, custom, internal) // 记录检查结果
		}

		// 启动一个新的 goroutine 处理连接
		go func(ctx context.Context, frpConn net.Conn) {
			// 检查是否启用了 TCP 多路复用，并且当前连接不是内部连接
			if lo.FromPtr(svr.cfg.Transport.TCPMux) && !internal {
				// 创建默认的 fmux 配置
				fmuxCfg := fmux.DefaultConfig()
				// 设置保持连接的时间间隔
				fmuxCfg.KeepAliveInterval = time.Duration(svr.cfg.Transport.TCPMuxKeepaliveInterval) * time.Second
				// 禁用 fmux 的日志输出
				fmuxCfg.LogOutput = io.Discard
				// 设置最大流窗口大小
				fmuxCfg.MaxStreamWindowSize = 6 * 1024 * 1024
				// 创建一个新的 fmux 服务器会话
				session, err := fmux.Server(frpConn, fmuxCfg)
				if err != nil {
					// 如果创建多路复用连接失败，记录警告日志并关闭连接
					log.Warnf("Failed to create mux connection: %v", err)
					frpConn.Close()
					return
				}

				// 不断接受新的多路复用流
				for {
					stream, err := session.AcceptStream()
					if err != nil {
						// 如果接受新的多路复用流失败，记录调试日志并关闭会话
						log.Debugf("Accept new mux stream error: %v", err)
						session.Close()
						return
					}
					// 启动一个新的 goroutine 处理多路复用流
					go svr.handleConnection(ctx, stream, internal)
				}
			} else {
				// 如果没有启用 TCP 多路复用或是内部连接，直接处理连接
				svr.handleConnection(ctx, frpConn, internal)
			}
		}(ctx, c)
	}
}

// HandleQUICListener 处理 QUIC 监听器的连接
func (svr *Service) HandleQUICListener(l *quic.Listener) {
	// 监听来自客户端的传入连接
	for {
		c, err := l.Accept(context.Background())
		if err != nil {
			log.Warnf("QUICListener for incoming connections from client closed") // 记录警告日志
			return
		}
		// 启动一个新的 goroutine 处理连接
		go func(ctx context.Context, frpConn quic.Connection) {
			for {
				stream, err := frpConn.AcceptStream(context.Background())
				if err != nil {
					log.Debugf("Accept new quic mux stream error: %v", err) // 记录调试日志
					_ = frpConn.CloseWithError(0, "")
					return
				}
				go svr.handleConnection(ctx, netpkg.QuicStreamToNetConn(stream, frpConn), false) // 处理 QUIC 流
			}
		}(context.Background(), c)
	}
}

// RegisterControl 注册一个新的控制连接
func (svr *Service) RegisterControl(ctlConn net.Conn, loginMsg *msg.Login, internal bool) error {
	// 如果客户端的 RunID 为空，则是新客户端，我们只需创建一个新的控制器
	// 否则，我们检查是否有一个控制器具有相同的运行 ID。如果是这样，我们释放以前的控制器并启动新的
	var err error
	if loginMsg.RunID == "" {
		loginMsg.RunID, err = util.RandID()
		if err != nil {
			return err
		}
	}

	ctx := netpkg.NewContextFromConn(ctlConn)
	xl := xlog.FromContextSafe(ctx)
	xl.AppendPrefix(loginMsg.RunID)
	ctx = xlog.NewContext(ctx, xl)
	xl.Infof("client login info: ip [%s] version [%s] hostname [%s] os [%s] arch [%s]",
		ctlConn.RemoteAddr().String(), loginMsg.Version, loginMsg.Hostname, loginMsg.Os, loginMsg.Arch) // 记录客户端登录信息

	// 检查身份验证
	authVerifier := svr.authVerifier
	if internal && loginMsg.ClientSpec.AlwaysAuthPass {
		authVerifier = auth.AlwaysPassVerifier
	}
	if err := authVerifier.VerifyLogin(loginMsg); err != nil {
		return err
	}

	// TODO: 使用 SessionContext
	ctl, err := NewControl(ctx, svr.rc, svr.pxyManager, authVerifier, ctlConn, !internal, loginMsg, svr.cfg)
	if err != nil {
		xl.Warnf("create new controller error: %v", err) // 记录警告日志
		// 不要向客户端返回详细错误
		return fmt.Errorf("unexpected error when creating new controller")
	}
	if oldCtl := svr.ctlManager.Add(loginMsg.RunID, ctl); oldCtl != nil {
		oldCtl.WaitClosed()
	}

	ctl.Start()

	// 用于统计
	metrics.Server.NewClient()

	go func() {
		// 阻塞直到控制关闭
		ctl.WaitClosed()
		svr.ctlManager.Del(loginMsg.RunID, ctl)
	}()
	return nil
}

// RegisterWorkConn 注册一个新的工作连接到需要它的控制和代理
func (svr *Service) RegisterWorkConn(workConn net.Conn, newMsg *msg.NewWorkConn) error {
	xl := netpkg.NewLogFromConn(workConn)
	ctl, exist := svr.ctlManager.GetByID(newMsg.RunID)
	if !exist {
		xl.Warnf("No client control found for run id [%s]", newMsg.RunID) // 记录警告日志
		return fmt.Errorf("no client control found for run id [%s]", newMsg.RunID)
	}
	// 服务器插件钩子
	content := &plugin.NewWorkConnContent{
		User: plugin.UserInfo{
			User:  ctl.loginMsg.User,
			Metas: ctl.loginMsg.Metas,
			RunID: ctl.loginMsg.RunID,
		},
		NewWorkConn: *newMsg,
	}

	newMsg = &content.NewWorkConn
	// 检查身份验证
	err := ctl.authVerifier.VerifyNewWorkConn(newMsg)
	if err != nil {
		xl.Warnf("invalid NewWorkConn with run id [%s]", newMsg.RunID) // 记录警告日志
		_ = msg.WriteMsg(workConn, &msg.StartWorkConn{
			Error: util.GenerateResponseErrorString("invalid NewWorkConn", err, lo.FromPtr(svr.cfg.DetailedErrorsToClient)),
		})
		return fmt.Errorf("invalid NewWorkConn with run id [%s]", newMsg.RunID)
	}
	return ctl.RegisterWorkConn(workConn)
}

// RegisterVisitorConn 注册一个新的访客连接
func (svr *Service) RegisterVisitorConn(visitorConn net.Conn, newMsg *msg.NewVisitorConn) error {
	visitorUser := ""
	// TODO: 兼容旧版本，可以没有 runID，用户为空。在以后的版本中，将强制要求包含 runID
	// 如果需要 runID，则不兼容 v0.50.0 之前的版本
	if newMsg.RunID != "" {
		ctl, exist := svr.ctlManager.GetByID(newMsg.RunID)
		if !exist {
			return fmt.Errorf("no client control found for run id [%s]", newMsg.RunID)
		}
		visitorUser = ctl.loginMsg.User
	}
	return svr.rc.VisitorManager.NewConn(newMsg.ProxyName, visitorConn, newMsg.Timestamp, newMsg.SignKey,
		newMsg.UseEncryption, newMsg.UseCompression, visitorUser)
}
