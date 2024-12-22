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
	"fmt"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samber/lo"

	"github.com/fatedier/frp/pkg/auth"
	"github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	pkgerr "github.com/fatedier/frp/pkg/errors"
	"github.com/fatedier/frp/pkg/msg"
	plugin "github.com/fatedier/frp/pkg/plugin/server"
	"github.com/fatedier/frp/pkg/transport"
	netpkg "github.com/fatedier/frp/pkg/util/net"
	"github.com/fatedier/frp/pkg/util/util"
	"github.com/fatedier/frp/pkg/util/version"
	"github.com/fatedier/frp/pkg/util/wait"
	"github.com/fatedier/frp/pkg/util/xlog"
	"github.com/fatedier/frp/server/controller"
	"github.com/fatedier/frp/server/metrics"
	"github.com/fatedier/frp/server/proxy"
)

type ControlManager struct {
	// 通过运行ID索引的控制对象
	ctlsByRunID map[string]*Control

	// 读写锁，用于保护ctlsByRunID的并发访问
	mu sync.RWMutex
}

// 创建一个新的ControlManager实例
func NewControlManager() *ControlManager {
	return &ControlManager{
		ctlsByRunID: make(map[string]*Control),
	}
}

// 添加一个Control对象到管理器中
func (cm *ControlManager) Add(runID string, ctl *Control) (old *Control) {
	cm.mu.Lock() // 加锁以保护共享资源
	defer cm.mu.Unlock()

	var ok bool
	old, ok = cm.ctlsByRunID[runID] // 检查是否已有相同runID的Control
	if ok {
		old.Replaced(ctl) // 如果存在，替换旧的Control
	}
	cm.ctlsByRunID[runID] = ctl // 添加新的Control
	return
}

// 删除一个Control对象，确保是同一个Control才删除
func (cm *ControlManager) Del(runID string, ctl *Control) {
	cm.mu.Lock() // 加锁以保护共享资源
	defer cm.mu.Unlock()
	if c, ok := cm.ctlsByRunID[runID]; ok && c == ctl {
		delete(cm.ctlsByRunID, runID) // 删除Control
	}
}

// 根据运行ID获取Control对象
func (cm *ControlManager) GetByID(runID string) (ctl *Control, ok bool) {
	cm.mu.RLock() // 读锁以保护共享资源
	defer cm.mu.RUnlock()
	ctl, ok = cm.ctlsByRunID[runID]
	return
}

// 关闭所有Control对象
func (cm *ControlManager) Close() error {
	cm.mu.Lock() // 加锁以保护共享资源
	defer cm.mu.Unlock()
	for _, ctl := range cm.ctlsByRunID {
		ctl.Close() // 关闭每个Control
	}
	cm.ctlsByRunID = make(map[string]*Control) // 清空map
	return nil
}

type Control struct {
	// 资源控制器
	rc *controller.ResourceController

	// 代理管理器
	pxyManager *proxy.Manager

	// 插件管理器
	pluginManager *plugin.Manager

	// 验证器，用于验证身份认证
	authVerifier auth.Verifier

	// 用于与客户端通信的消息传输器
	msgTransporter transport.MessageTransporter

	// 消息分发器，用于处理不同类型的消息
	msgDispatcher *msg.Dispatcher

	// 登录消息
	loginMsg *msg.Login

	// 控制连接
	conn net.Conn

	// 工作连接通道
	workConnCh chan net.Conn

	// 客户端中的代理
	proxies map[string]proxy.Proxy

	// 连接池数量
	poolCount int

	// 使用的端口数量
	portsUsedNum int

	// 上次接收到Ping消息的时间
	lastPing atomic.Value

	// 运行ID，用于标识客户端
	runID string

	// 读写锁，用于保护共享资源
	mu sync.RWMutex

	// 服务器配置
	serverCfg *v1.ServerConfig

	// 日志记录器
	xl *xlog.Logger

	// 上下文
	ctx context.Context

	// 关闭通道
	doneCh chan struct{}
}

// 创建一个新的Control实例
func NewControl(
	ctx context.Context,
	rc *controller.ResourceController,
	pxyManager *proxy.Manager,
	pluginManager *plugin.Manager,
	authVerifier auth.Verifier,
	ctlConn net.Conn,
	ctlConnEncrypted bool,
	loginMsg *msg.Login,
	serverCfg *v1.ServerConfig,
) (*Control, error) {
	poolCount := loginMsg.PoolCount
	if poolCount > int(serverCfg.Transport.MaxPoolCount) {
		poolCount = int(serverCfg.Transport.MaxPoolCount) // 限制连接池数量
	}
	ctl := &Control{
		rc:            rc,
		pxyManager:    pxyManager,
		pluginManager: pluginManager,
		authVerifier:  authVerifier,
		conn:          ctlConn,
		loginMsg:      loginMsg,
		workConnCh:    make(chan net.Conn, poolCount+10), // 创建工作连接通道
		proxies:       make(map[string]proxy.Proxy),
		poolCount:     poolCount,
		portsUsedNum:  0,
		runID:         loginMsg.RunID,
		serverCfg:     serverCfg,
		xl:            xlog.FromContextSafe(ctx),
		ctx:           ctx,
		doneCh:        make(chan struct{}),
	}
	ctl.lastPing.Store(time.Now()) // 存储当前时间为上次Ping时间

	if ctlConnEncrypted {
		cryptoRW, err := netpkg.NewCryptoReadWriter(ctl.conn, []byte(ctl.serverCfg.Auth.Token))
		if err != nil {
			return nil, err // 如果加密失败，返回错误
		}
		ctl.msgDispatcher = msg.NewDispatcher(cryptoRW) // 使用加密连接创建消息分发器
	} else {
		ctl.msgDispatcher = msg.NewDispatcher(ctl.conn) // 使用普通连接创建消息分发器
	}
	ctl.registerMsgHandlers()                                                             // 注册消息处理器
	ctl.msgTransporter = transport.NewMessageTransporter(ctl.msgDispatcher.SendChannel()) // 创建消息传输器
	return ctl, nil
}

// 向客户端发送登录成功消息并开始工作
func (ctl *Control) Start() {
	loginRespMsg := &msg.LoginResp{
		Version: version.Full(),
		RunID:   ctl.runID,
		Error:   "",
	}
	_ = msg.WriteMsg(ctl.conn, loginRespMsg) // 发送登录响应消息

	go func() {
		for i := 0; i < ctl.poolCount; i++ {
			_ = ctl.msgDispatcher.Send(&msg.ReqWorkConn{}) // 请求工作连接
		}
	}()
	go ctl.worker() // 启动工作协程
}

// 关闭控制连接
func (ctl *Control) Close() error {
	ctl.conn.Close()
	return nil
}

// 替换旧的Control对象
func (ctl *Control) Replaced(newCtl *Control) {
	xl := ctl.xl
	xl.Infof("Replaced by client [%s]", newCtl.runID) // 记录替换日志
	ctl.runID = ""
	ctl.conn.Close() // 关闭连接
}

// 注册工作连接
func (ctl *Control) RegisterWorkConn(conn net.Conn) error {
	xl := ctl.xl
	defer func() {
		if err := recover(); err != nil {
			xl.Errorf("panic error: %v", err) // 捕获并记录panic错误
			xl.Errorf(string(debug.Stack()))
		}
	}()

	select {
	case ctl.workConnCh <- conn: // 将连接放入工作连接通道
		xl.Debugf("new work connection registered")
		return nil
	default:
		xl.Debugf("work connection pool is full, discarding") // 如果通道已满，丢弃连接
		return fmt.Errorf("work connection pool is full, discarding")
	}
}

// 获取工作连接
func (ctl *Control) GetWorkConn() (workConn net.Conn, err error) {
	xl := ctl.xl
	defer func() {
		if err := recover(); err != nil {
			xl.Errorf("panic error: %v", err) // 捕获并记录panic错误
			xl.Errorf(string(debug.Stack()))
		}
	}()

	var ok bool
	select {
	case workConn, ok = <-ctl.workConnCh: // 从工作连接通道获取连接
		if !ok {
			err = pkgerr.ErrCtlClosed
			return
		}
		xl.Debugf("get work connection from pool")
	default:
		if err := ctl.msgDispatcher.Send(&msg.ReqWorkConn{}); err != nil { // 请求更多工作连接
			return nil, fmt.Errorf("control is already closed")
		}

		select {
		case workConn, ok = <-ctl.workConnCh: // 再次尝试获取连接
			if !ok {
				err = pkgerr.ErrCtlClosed
				xl.Warnf("no work connections available, %v", err)
				return
			}

		case <-time.After(time.Duration(ctl.serverCfg.UserConnTimeout) * time.Second): // 超时处理
			err = fmt.Errorf("timeout trying to get work connection")
			xl.Warnf("%v", err)
			return
		}
	}

	_ = ctl.msgDispatcher.Send(&msg.ReqWorkConn{}) // 获取到连接后请求新的连接
	return
}

// 心跳检测工作协程
func (ctl *Control) heartbeatWorker() {
	if ctl.serverCfg.Transport.HeartbeatTimeout <= 0 {
		return
	}

	xl := ctl.xl
	go wait.Until(func() {
		if time.Since(ctl.lastPing.Load().(time.Time)) > time.Duration(ctl.serverCfg.Transport.HeartbeatTimeout)*time.Second {
			xl.Warnf("heartbeat timeout") // 心跳超时处理
			ctl.conn.Close()
			return
		}
	}, time.Second, ctl.doneCh)
}

// 阻塞直到Control关闭
func (ctl *Control) WaitClosed() {
	<-ctl.doneCh
}

// 工作协程
func (ctl *Control) worker() {
	xl := ctl.xl

	go ctl.heartbeatWorker()   // 启动心跳检测
	go ctl.msgDispatcher.Run() // 启动消息分发器

	<-ctl.msgDispatcher.Done() // 等待消息分发器完成
	ctl.conn.Close()

	ctl.mu.Lock()
	defer ctl.mu.Unlock()

	close(ctl.workConnCh) // 关闭工作连接通道
	for workConn := range ctl.workConnCh {
		workConn.Close()
	}

	for _, pxy := range ctl.proxies {
		pxy.Close() // 关闭代理
		ctl.pxyManager.Del(pxy.GetName())
		metrics.Server.CloseProxy(pxy.GetName(), pxy.GetConfigurer().GetBaseConfig().Type)

		notifyContent := &plugin.CloseProxyContent{
			User: plugin.UserInfo{
				User:  ctl.loginMsg.User,
				Metas: ctl.loginMsg.Metas,
				RunID: ctl.loginMsg.RunID,
			},
			CloseProxy: msg.CloseProxy{
				ProxyName: pxy.GetName(),
			},
		}
		go func() {
			_ = ctl.pluginManager.CloseProxy(notifyContent) // 通知插件关闭代理
		}()
	}

	metrics.Server.CloseClient() // 关闭客户端
	xl.Infof("client exit success")
	close(ctl.doneCh) // 关闭完成通道
}

// 注册消息处理器
func (ctl *Control) registerMsgHandlers() {
	ctl.msgDispatcher.RegisterHandler(&msg.NewProxy{}, ctl.handleNewProxy)     // 注册新代理处理器
	ctl.msgDispatcher.RegisterHandler(&msg.Ping{}, ctl.handlePing)             // 注册Ping处理器
	ctl.msgDispatcher.RegisterHandler(&msg.CloseProxy{}, ctl.handleCloseProxy) // 注册关闭代理处理器
}

// 处理新代理消息
func (ctl *Control) handleNewProxy(m msg.Message) {
	xl := ctl.xl
	inMsg := m.(*msg.NewProxy)

	content := &plugin.NewProxyContent{
		User: plugin.UserInfo{
			User:  ctl.loginMsg.User,
			Metas: ctl.loginMsg.Metas,
			RunID: ctl.loginMsg.RunID,
		},
		NewProxy: *inMsg,
	}
	var remoteAddr string
	retContent, err := ctl.pluginManager.NewProxy(content)
	if err == nil {
		inMsg = &retContent.NewProxy
		remoteAddr, err = ctl.RegisterProxy(inMsg) // 注册代理
	}

	resp := &msg.NewProxyResp{
		ProxyName: inMsg.ProxyName,
	}
	if err != nil {
		xl.Warnf("new proxy [%s] type [%s] error: %v", inMsg.ProxyName, inMsg.ProxyType, err)
		resp.Error = util.GenerateResponseErrorString(fmt.Sprintf("new proxy [%s] error", inMsg.ProxyName),
			err, lo.FromPtr(ctl.serverCfg.DetailedErrorsToClient))
	} else {
		resp.RemoteAddr = remoteAddr
		xl.Infof("new proxy [%s] type [%s] success", inMsg.ProxyName, inMsg.ProxyType)
		metrics.Server.NewProxy(inMsg.ProxyName, inMsg.ProxyType)
	}
	_ = ctl.msgDispatcher.Send(resp) // 发送响应
}

// 处理Ping消息
func (ctl *Control) handlePing(m msg.Message) {
	xl := ctl.xl
	inMsg := m.(*msg.Ping)

	content := &plugin.PingContent{
		User: plugin.UserInfo{
			User:  ctl.loginMsg.User,
			Metas: ctl.loginMsg.Metas,
			RunID: ctl.loginMsg.RunID,
		},
		Ping: *inMsg,
	}
	retContent, err := ctl.pluginManager.Ping(content)
	if err == nil {
		inMsg = &retContent.Ping
		err = ctl.authVerifier.VerifyPing(inMsg) // 验证Ping消息
	}
	if err != nil {
		xl.Warnf("received invalid ping: %v", err)
		_ = ctl.msgDispatcher.Send(&msg.Pong{
			Error: util.GenerateResponseErrorString("invalid ping", err, lo.FromPtr(ctl.serverCfg.DetailedErrorsToClient)),
		})
		return
	}
	ctl.lastPing.Store(time.Now()) // 更新上次Ping时间
	xl.Debugf("receive heartbeat")
	_ = ctl.msgDispatcher.Send(&msg.Pong{}) // 发送Pong响应
}

// 处理关闭代理消息
func (ctl *Control) handleCloseProxy(m msg.Message) {
	xl := ctl.xl
	inMsg := m.(*msg.CloseProxy)
	_ = ctl.CloseProxy(inMsg) // 关闭代理
	xl.Infof("close proxy [%s] success", inMsg.ProxyName)
}

// 注册代理
func (ctl *Control) RegisterProxy(pxyMsg *msg.NewProxy) (remoteAddr string, err error) {
	var pxyConf v1.ProxyConfigurer
	pxyConf, err = config.NewProxyConfigurerFromMsg(pxyMsg, ctl.serverCfg) // 从消息中加载配置
	if err != nil {
		return
	}

	userInfo := plugin.UserInfo{
		User:  ctl.loginMsg.User,
		Metas: ctl.loginMsg.Metas,
		RunID: ctl.runID,
	}

	pxy, err := proxy.NewProxy(ctl.ctx, &proxy.Options{
		UserInfo:           userInfo,
		LoginMsg:           ctl.loginMsg,
		PoolCount:          ctl.poolCount,
		ResourceController: ctl.rc,
		GetWorkConnFn:      ctl.GetWorkConn,
		Configurer:         pxyConf,
		ServerCfg:          ctl.serverCfg,
	})
	if err != nil {
		return remoteAddr, err
	}

	if ctl.serverCfg.MaxPortsPerClient > 0 {
		ctl.mu.Lock()
		if ctl.portsUsedNum+pxy.GetUsedPortsNum() > int(ctl.serverCfg.MaxPortsPerClient) {
			ctl.mu.Unlock()
			err = fmt.Errorf("exceed the max_ports_per_client")
			return
		}
		ctl.portsUsedNum += pxy.GetUsedPortsNum()
		ctl.mu.Unlock()

		defer func() {
			if err != nil {
				ctl.mu.Lock()
				ctl.portsUsedNum -= pxy.GetUsedPortsNum()
				ctl.mu.Unlock()
			}
		}()
	}

	if ctl.pxyManager.Exist(pxyMsg.ProxyName) {
		err = fmt.Errorf("proxy [%s] already exists", pxyMsg.ProxyName)
		return
	}

	remoteAddr, err = pxy.Run() // 运行代理
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			pxy.Close()
		}
	}()

	err = ctl.pxyManager.Add(pxyMsg.ProxyName, pxy) // 添加代理到管理器
	if err != nil {
		return
	}

	ctl.mu.Lock()
	ctl.proxies[pxy.GetName()] = pxy // 将代理添加到Control
	ctl.mu.Unlock()
	return
}

// 关闭代理
func (ctl *Control) CloseProxy(closeMsg *msg.CloseProxy) (err error) {
	ctl.mu.Lock()
	pxy, ok := ctl.proxies[closeMsg.ProxyName]
	if !ok {
		ctl.mu.Unlock()
		return
	}

	if ctl.serverCfg.MaxPortsPerClient > 0 {
		ctl.portsUsedNum -= pxy.GetUsedPortsNum()
	}
	pxy.Close() // 关闭代理
	ctl.pxyManager.Del(pxy.GetName())
	delete(ctl.proxies, closeMsg.ProxyName)
	ctl.mu.Unlock()

	metrics.Server.CloseProxy(pxy.GetName(), pxy.GetConfigurer().GetBaseConfig().Type)

	notifyContent := &plugin.CloseProxyContent{
		User: plugin.UserInfo{
			User:  ctl.loginMsg.User,
			Metas: ctl.loginMsg.Metas,
			RunID: ctl.loginMsg.RunID,
		},
		CloseProxy: msg.CloseProxy{
			ProxyName: pxy.GetName(),
		},
	}
	go func() {
		_ = ctl.pluginManager.CloseProxy(notifyContent) // 通知插件关闭代理
	}()
	return
}
