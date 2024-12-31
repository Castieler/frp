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

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/fatedier/golib/crypto"
	"github.com/samber/lo"

	"github.com/fatedier/frp/client/proxy"
	"github.com/fatedier/frp/pkg/auth"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
	httppkg "github.com/fatedier/frp/pkg/util/http"
	"github.com/fatedier/frp/pkg/util/log"
	netpkg "github.com/fatedier/frp/pkg/util/net"
	"github.com/fatedier/frp/pkg/util/version"
	"github.com/fatedier/frp/pkg/util/wait"
	"github.com/fatedier/frp/pkg/util/xlog"
)

// init 函数在包初始化时执行，用于设置默认值和环境变量。
func init() {
	// 设置默认的加密盐值，用于加密相关操作。
	crypto.DefaultSalt = "frp"
	// 禁用 quic-go 的接收缓冲区警告，避免日志中出现不必要的警告信息。
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	// 默认禁用 quic-go 的 ECN（Explicit Congestion Notification）支持，因为某些操作系统上可能会导致问题。
	if os.Getenv("QUIC_GO_DISABLE_ECN") == "" {
		os.Setenv("QUIC_GO_DISABLE_ECN", "true")
	}
}

// cancelErr 是一个自定义错误类型，用于包装取消操作时的错误。
type cancelErr struct {
	Err error
}

// Error 方法实现了 error 接口，返回错误信息。
func (e cancelErr) Error() string {
	return e.Err.Error()
}

// ServiceOptions 包含创建客户端服务时的配置选项。
type ServiceOptions struct {
	Common      *v1.ClientCommonConfig // 客户端通用配置
	ProxyCfgs   []v1.ProxyConfigurer   // 代理配置列表
	VisitorCfgs []v1.VisitorConfigurer // 访问者配置列表

	// ConfigFilePath 是用于初始化的配置文件路径。
	// 如果为空，则表示未使用配置文件进行初始化，可能是通过命令行参数或直接调用初始化的。
	ConfigFilePath string

	// ClientSpec 是控制客户端行为的客户端规范。
	ClientSpec *msg.ClientSpec

	// ConnectorCreator 是一个函数，用于创建新的连接器以连接到服务器。
	// 连接器屏蔽了底层连接的细节，无论是通过 TCP 还是 QUIC 连接，也不管是否使用了多路复用。
	//
	// 如果未设置，则使用默认的 frpc 连接器。
	// 通过使用自定义连接器，可以实现 VirtualClient，通过管道而不是真实的物理连接连接到 frps。
	ConnectorCreator func(context.Context, *v1.ClientCommonConfig) Connector

	// HandleWorkConnCb 是一个回调函数，当新的工作连接创建时调用。
	//
	// 如果未设置，则使用默认的 frpc 实现。
	HandleWorkConnCb func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) bool
}

// setServiceOptionsDefault 为 ServiceOptions 设置默认值。
func setServiceOptionsDefault(options *ServiceOptions) {
	// 如果 Common 配置不为空，则调用其 Complete 方法完成配置。
	if options.Common != nil {
		options.Common.Complete()
	}
	// 如果未设置 ConnectorCreator，则使用默认的 NewConnector 函数。
	if options.ConnectorCreator == nil {
		options.ConnectorCreator = NewConnector
	}
}

// Service 是客户端服务，负责连接到 frps 并提供代理服务。
type Service struct {
	ctlMu sync.RWMutex // 控制连接读写锁
	// manager control connection with server
	ctl *Control // 控制连接管理器
	// Uniq id got from frps, it will be attached to loginMsg.
	runID string // 从 frps 获取的唯一 ID，用于登录消息

	// Sets authentication based on selected method
	authSetter auth.Setter // 认证设置器

	// web server for admin UI and apis
	webServer *httppkg.Server // 用于管理界面和 API 的 Web 服务器

	cfgMu       sync.RWMutex           // 配置读写锁
	common      *v1.ClientCommonConfig // 客户端通用配置
	proxyCfgs   []v1.ProxyConfigurer   // 代理配置列表
	visitorCfgs []v1.VisitorConfigurer // 访问者配置列表
	clientSpec  *msg.ClientSpec        // 客户端规范

	// The configuration file used to initialize this client, or an empty
	// string if no configuration file was used.
	configFilePath string // 用于初始化客户端的配置文件路径

	// service context
	ctx context.Context // 服务上下文
	// call cancel to stop service
	cancel                   context.CancelCauseFunc // 取消函数，用于停止服务
	gracefulShutdownDuration time.Duration           // 优雅关闭的持续时间

	connectorCreator func(context.Context, *v1.ClientCommonConfig) Connector      // 连接器创建函数
	handleWorkConnCb func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) bool // 工作连接回调函数
}

// NewService 创建一个新的客户端服务实例。
func NewService(options ServiceOptions) (*Service, error) {
	// 设置 ServiceOptions 的默认值。
	setServiceOptionsDefault(&options)

	var webServer *httppkg.Server
	// 如果配置中启用了 Web 服务器，则创建 Web 服务器实例。
	if options.Common.WebServer.Port > 0 {
		ws, err := httppkg.NewServer(options.Common.WebServer)
		if err != nil {
			return nil, err
		}
		webServer = ws
	}
	// 创建并初始化 Service 实例。
	s := &Service{
		ctx:              context.Background(),
		authSetter:       auth.NewAuthSetter(options.Common.Auth),
		webServer:        webServer,
		common:           options.Common,
		configFilePath:   options.ConfigFilePath,
		proxyCfgs:        options.ProxyCfgs,
		visitorCfgs:      options.VisitorCfgs,
		clientSpec:       options.ClientSpec,
		connectorCreator: options.ConnectorCreator,
		handleWorkConnCb: options.HandleWorkConnCb,
	}
	// 如果 Web 服务器存在，则注册路由处理函数。
	if webServer != nil {
		webServer.RouteRegister(s.registerRouteHandlers)
	}
	return s, nil
}

// Run 启动客户端服务。
func (svr *Service) Run(ctx context.Context) error {
	// 创建新的上下文和取消函数，用于控制服务的生命周期。
	ctx, cancel := context.WithCancelCause(ctx)
	svr.ctx = xlog.NewContext(ctx, xlog.FromContextSafe(ctx))
	svr.cancel = cancel

	// 如果配置中指定了 DNS 服务器，则设置默认的 DNS 服务器地址。
	if svr.common.DNSServer != "" {
		netpkg.SetDefaultDNSAddress(svr.common.DNSServer)
	}

	// 如果 Web 服务器存在，则启动 Web 服务器。
	if svr.webServer != nil {
		go func() {
			log.Infof("admin server listen on %s", svr.webServer.Address())
			if err := svr.webServer.Run(); err != nil {
				log.Warnf("admin server exit with error: %v", err)
			}
		}()
	}

	// 尝试登录到 frps，直到成功或达到最大重试次数。
	svr.loopLoginUntilSuccess(10*time.Second, lo.FromPtr(svr.common.LoginFailExit))
	if svr.ctl == nil {
		cancelCause := cancelErr{}
		_ = errors.As(context.Cause(svr.ctx), &cancelCause)
		return fmt.Errorf("login to the server failed: %v. With loginFailExit enabled, no additional retries will be attempted", cancelCause.Err)
	}

	// 保持控制连接工作。
	go svr.keepControllerWorking()

	// 等待服务上下文结束，然后停止服务。
	<-svr.ctx.Done()
	svr.stop()
	return nil
}

// keepControllerWorking 保持控制连接工作，如果控制连接断开则尝试重新连接。
func (svr *Service) keepControllerWorking() {
	// 等待控制连接结束。
	<-svr.ctl.Done()

	// 使用指数退避策略尝试重新连接。
	wait.BackoffUntil(func() (bool, error) {
		// 尝试登录到服务器，直到成功。
		svr.loopLoginUntilSuccess(20*time.Second, false)
		if svr.ctl != nil {
			// 如果控制连接存在，则等待其结束。
			<-svr.ctl.Done()
			return false, errors.New("control is closed and try another loop")
		}
		// 如果控制连接不存在，则表示登录失败，服务已关闭。
		return false, nil
	}, wait.NewFastBackoffManager(
		wait.FastBackoffOptions{
			Duration:        time.Second,
			Factor:          2,
			Jitter:          0.1,
			MaxDuration:     20 * time.Second,
			FastRetryCount:  3,
			FastRetryDelay:  200 * time.Millisecond,
			FastRetryWindow: time.Minute,
			FastRetryJitter: 0.5,
		},
	), true, svr.ctx.Done())
}

// login 创建一个连接到 frps 并注册自己为客户端。
func (svr *Service) login() (conn net.Conn, connector Connector, err error) {
	xl := xlog.FromContextSafe(svr.ctx)
	// 创建连接器并打开连接。
	connector = svr.connectorCreator(svr.ctx, svr.common)
	if err = connector.Open(); err != nil {
		return nil, nil, err
	}

	// 如果发生错误，则关闭连接器。
	defer func() {
		if err != nil {
			connector.Close()
		}
	}()

	// 连接到服务器。
	conn, err = connector.Connect()
	if err != nil {
		return
	}

	// 创建登录消息并发送到服务器。
	loginMsg := &msg.Login{
		Arch:      runtime.GOARCH,
		Os:        runtime.GOOS,
		PoolCount: svr.common.Transport.PoolCount,
		User:      svr.common.User,
		Version:   version.Full(),
		Timestamp: time.Now().Unix(),
		RunID:     svr.runID,
		Metas:     svr.common.Metadatas,
	}
	if svr.clientSpec != nil {
		loginMsg.ClientSpec = *svr.clientSpec
	}

	// 设置认证信息。
	if err = svr.authSetter.SetLogin(loginMsg); err != nil {
		return
	}

	// 发送登录消息。
	if err = msg.WriteMsg(conn, loginMsg); err != nil {
		return
	}

	// 读取登录响应消息。
	var loginRespMsg msg.LoginResp
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err = msg.ReadMsgInto(conn, &loginRespMsg); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	// 如果登录响应中包含错误信息，则返回错误。
	if loginRespMsg.Error != "" {
		err = fmt.Errorf("%s", loginRespMsg.Error)
		xl.Errorf("%s", loginRespMsg.Error)
		return
	}

	// 设置 runID 并记录日志。
	svr.runID = loginRespMsg.RunID
	xl.AddPrefix(xlog.LogPrefix{Name: "runID", Value: svr.runID})

	xl.Infof("login to server success, get run id [%s]", loginRespMsg.RunID)
	return
}

// loopLoginUntilSuccess 尝试登录到服务器，直到成功或达到最大重试次数。
func (svr *Service) loopLoginUntilSuccess(maxInterval time.Duration, firstLoginExit bool) {
	xl := xlog.FromContextSafe(svr.ctx)

	loginFunc := func() (bool, error) {
		xl.Infof("try to connect to server...")
		conn, connector, err := svr.login()
		if err != nil {
			xl.Warnf("connect to server error: %v", err)
			if firstLoginExit {
				svr.cancel(cancelErr{Err: err})
			}
			return false, err
		}

		svr.cfgMu.RLock()
		proxyCfgs := svr.proxyCfgs
		visitorCfgs := svr.visitorCfgs
		svr.cfgMu.RUnlock()
		connEncrypted := true
		if svr.clientSpec != nil && svr.clientSpec.Type == "ssh-tunnel" {
			connEncrypted = false
		}
		sessionCtx := &SessionContext{
			Common:        svr.common,
			RunID:         svr.runID,
			Conn:          conn,
			ConnEncrypted: connEncrypted,
			AuthSetter:    svr.authSetter,
			Connector:     connector,
		}
		ctl, err := NewControl(svr.ctx, sessionCtx)
		if err != nil {
			conn.Close()
			xl.Errorf("NewControl error: %v", err)
			return false, err
		}
		ctl.SetInWorkConnCallback(svr.handleWorkConnCb)

		ctl.Run(proxyCfgs, visitorCfgs)
		// 关闭并替换之前的控制连接。
		svr.ctlMu.Lock()
		if svr.ctl != nil {
			svr.ctl.Close()
		}
		svr.ctl = ctl
		svr.ctlMu.Unlock()
		return true, nil
	}

	// 使用指数退避策略尝试重新连接。
	wait.BackoffUntil(loginFunc, wait.NewFastBackoffManager(
		wait.FastBackoffOptions{
			Duration:    time.Second,
			Factor:      2,
			Jitter:      0.1,
			MaxDuration: maxInterval,
		}), true, svr.ctx.Done())
}

// UpdateAllConfigurer 更新所有代理和访问者配置。
func (svr *Service) UpdateAllConfigurer(proxyCfgs []v1.ProxyConfigurer, visitorCfgs []v1.VisitorConfigurer) error {
	svr.cfgMu.Lock()
	svr.proxyCfgs = proxyCfgs
	svr.visitorCfgs = visitorCfgs
	svr.cfgMu.Unlock()

	svr.ctlMu.RLock()
	ctl := svr.ctl
	svr.ctlMu.RUnlock()

	if ctl != nil {
		return svr.ctl.UpdateAllConfigurer(proxyCfgs, visitorCfgs)
	}
	return nil
}

// Close 关闭客户端服务。
func (svr *Service) Close() {
	svr.GracefulClose(time.Duration(0))
}

// GracefulClose 优雅关闭客户端服务。
func (svr *Service) GracefulClose(d time.Duration) {
	svr.gracefulShutdownDuration = d
	svr.cancel(nil)
}

// stop 停止客户端服务。
func (svr *Service) stop() {
	svr.ctlMu.Lock()
	defer svr.ctlMu.Unlock()
	if svr.ctl != nil {
		svr.ctl.GracefulClose(svr.gracefulShutdownDuration)
		svr.ctl = nil
	}
}

// getProxyStatus 获取指定代理的状态。
func (svr *Service) getProxyStatus(name string) (*proxy.WorkingStatus, bool) {
	svr.ctlMu.RLock()
	ctl := svr.ctl
	svr.ctlMu.RUnlock()

	if ctl == nil {
		return nil, false
	}
	return ctl.pm.GetProxyStatus(name)
}

// StatusExporter 返回一个状态导出器，用于获取代理状态。
func (svr *Service) StatusExporter() StatusExporter {
	return &statusExporterImpl{
		getProxyStatusFunc: svr.getProxyStatus,
	}
}

// StatusExporter 是一个接口，用于导出代理状态。
type StatusExporter interface {
	GetProxyStatus(name string) (*proxy.WorkingStatus, bool)
}

// statusExporterImpl 是 StatusExporter 接口的实现。
type statusExporterImpl struct {
	getProxyStatusFunc func(name string) (*proxy.WorkingStatus, bool)
}

// GetProxyStatus 获取指定代理的状态。
func (s *statusExporterImpl) GetProxyStatus(name string) (*proxy.WorkingStatus, bool) {
	return s.getProxyStatusFunc(name)
}
