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
	ctx context.Context, // 上下文，用于控制生命周期
	rc *controller.ResourceController, // 资源控制器
	pxyManager *proxy.Manager, // 代理管理器
	pluginManager *plugin.Manager, // 插件管理器
	authVerifier auth.Verifier, // 验证器，用于身份验证
	ctlConn net.Conn, // 控制连接
	ctlConnEncrypted bool, // 控制连接是否加密
	loginMsg *msg.Login, // 登录消息
	serverCfg *v1.ServerConfig, // 服务器配置
) (*Control, error) {
	poolCount := loginMsg.PoolCount                        // 从登录消息中获取连接池数量
	if poolCount > int(serverCfg.Transport.MaxPoolCount) { // 如果超过最大连接池数量
		poolCount = int(serverCfg.Transport.MaxPoolCount) // 限制连接池数量
	}
	ctl := &Control{
		rc:            rc,                                // 初始化资源控制器
		pxyManager:    pxyManager,                        // 初始化代理管理器
		pluginManager: pluginManager,                     // 初始化插件管理器
		authVerifier:  authVerifier,                      // 初始化验证器
		conn:          ctlConn,                           // 初始化控制连接
		loginMsg:      loginMsg,                          // 初始化登录消息
		workConnCh:    make(chan net.Conn, poolCount+10), // 创建工作连接通道，容量为连接池数量加10
		proxies:       make(map[string]proxy.Proxy),      // 初始化代理映射
		poolCount:     poolCount,                         // 设置连接池数量
		portsUsedNum:  0,                                 // 初始化已使用端口数量
		runID:         loginMsg.RunID,                    // 设置运行ID
		serverCfg:     serverCfg,                         // 设置服务器配置
		xl:            xlog.FromContextSafe(ctx),         // 从上下文中获取日志记录器
		ctx:           ctx,                               // 设置上下文
		doneCh:        make(chan struct{}),               // 创建关闭通道
	}
	ctl.lastPing.Store(time.Now()) // 存储当前时间为上次Ping时间

	if ctlConnEncrypted { // 如果连接是加密的
		cryptoRW, err := netpkg.NewCryptoReadWriter(ctl.conn, []byte(ctl.serverCfg.Auth.Token)) // 创建加密读写器
		if err != nil {
			return nil, err // 如果加密失败，返回错误
		}
		ctl.msgDispatcher = msg.NewDispatcher(cryptoRW) // 使用加密连接创建消息分发器
	} else {
		ctl.msgDispatcher = msg.NewDispatcher(ctl.conn) // 使用普通连接创建消息分发器
	}
	ctl.registerMsgHandlers()                                                             // 注册消息处理器
	ctl.msgTransporter = transport.NewMessageTransporter(ctl.msgDispatcher.SendChannel()) // 创建消息传输器
	return ctl, nil                                                                       // 返回Control实例
}

// 向客户端发送登录成功消息并开始工作
func (ctl *Control) Start() {
	loginRespMsg := &msg.LoginResp{
		Version: version.Full(), // 设置版本信息
		RunID:   ctl.runID,      // 设置运行ID
		Error:   "",             // 没有错误信息
	}
	_ = msg.WriteMsg(ctl.conn, loginRespMsg) // 发送登录响应消息

	go func() {
		for i := 0; i < ctl.poolCount; i++ { // 根据连接池数量请求工作连接
			_ = ctl.msgDispatcher.Send(&msg.ReqWorkConn{}) // 请求工作连接
		}
	}()
	go ctl.worker() // 启动工作协程
}

// 关闭控制连接
func (ctl *Control) Close() error {
	ctl.conn.Close() // 关闭连接
	return nil
}

// 替换旧的Control对象
func (ctl *Control) Replaced(newCtl *Control) {
	xl := ctl.xl
	xl.Infof("Replaced by client [%s]", newCtl.runID) // 记录替换日志
	ctl.runID = ""                                    // 清空运行ID
	ctl.conn.Close()                                  // 关闭连接
}

// 注册工作连接
func (ctl *Control) RegisterWorkConn(conn net.Conn) error {
	xl := ctl.xl
	defer func() {
		if err := recover(); err != nil { // 捕获并记录panic错误
			xl.Errorf("panic error: %v", err)
			xl.Errorf(string(debug.Stack()))
		}
	}()

	select {
	case ctl.workConnCh <- conn: // 将连接放入工作连接通道
		xl.Debugf("new work connection registered") // 记录新连接注册
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
		if err := recover(); err != nil { // 捕获并记录panic错误
			xl.Errorf("panic error: %v", err)
			xl.Errorf(string(debug.Stack()))
		}
	}()

	var ok bool
	select {
	case workConn, ok = <-ctl.workConnCh: // 从工作连接通道获取连接
		if !ok {
			err = pkgerr.ErrCtlClosed // 如果通道关闭，返回错误
			return
		}
		xl.Debugf("get work connection from pool") // 记录获取连接
	default:
		if err := ctl.msgDispatcher.Send(&msg.ReqWorkConn{}); err != nil { // 请求更多工作连接
			return nil, fmt.Errorf("control is already closed")
		}

		select {
		case workConn, ok = <-ctl.workConnCh: // 再次尝试获取连接
			if !ok {
				err = pkgerr.ErrCtlClosed
				xl.Warnf("no work connections available, %v", err) // 记录无可用连接
				return
			}

		case <-time.After(time.Duration(ctl.serverCfg.UserConnTimeout) * time.Second): // 超时处理
			err = fmt.Errorf("timeout trying to get work connection")
			xl.Warnf("%v", err) // 记录超时
			return
		}
	}

	_ = ctl.msgDispatcher.Send(&msg.ReqWorkConn{}) // 获取到连接后请求新的连接
	return
}

// 心跳检测工作协程
func (ctl *Control) heartbeatWorker() {
	if ctl.serverCfg.Transport.HeartbeatTimeout <= 0 { // 如果心跳超时未设置
		return
	}

	xl := ctl.xl
	go wait.Until(func() {
		if time.Since(ctl.lastPing.Load().(time.Time)) > time.Duration(ctl.serverCfg.Transport.HeartbeatTimeout)*time.Second { // 检查心跳超时
			xl.Warnf("heartbeat timeout") // 记录心跳超时
			ctl.conn.Close()              // 关闭连接
			return
		}
	}, time.Second, ctl.doneCh) // 每秒检查一次
}

// 阻塞直到Control关闭
func (ctl *Control) WaitClosed() {
	<-ctl.doneCh // 等待关闭通道
}

// 工作协程
func (ctl *Control) worker() {
	xl := ctl.xl

	go ctl.heartbeatWorker()   // 启动心跳检测
	go ctl.msgDispatcher.Run() // 启动消息分发器

	<-ctl.msgDispatcher.Done() // 等待消息分发器完成
	ctl.conn.Close()           // 关闭连接

	ctl.mu.Lock() // 加锁以保护共享资源
	defer ctl.mu.Unlock()

	close(ctl.workConnCh)                  // 关闭工作连接通道
	for workConn := range ctl.workConnCh { // 关闭所有工作连接
		workConn.Close()
	}

	for _, pxy := range ctl.proxies { // 关闭所有代理
		pxy.Close()
		ctl.pxyManager.Del(pxy.GetName())                                                  // 从管理器中删除代理
		metrics.Server.CloseProxy(pxy.GetName(), pxy.GetConfigurer().GetBaseConfig().Type) // 更新指标

		notifyContent := &plugin.CloseProxyContent{ // 创建关闭代理通知内容
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

	metrics.Server.CloseClient()    // 关闭客户端指标
	xl.Infof("client exit success") // 记录客户端退出成功
	close(ctl.doneCh)               // 关闭完成通道
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
	inMsg := m.(*msg.NewProxy) // 类型断言为NewProxy消息

	content := &plugin.NewProxyContent{ // 创建新代理内容
		User: plugin.UserInfo{
			User:  ctl.loginMsg.User,
			Metas: ctl.loginMsg.Metas,
			RunID: ctl.loginMsg.RunID,
		},
		NewProxy: *inMsg,
	}
	var remoteAddr string
	retContent, err := ctl.pluginManager.NewProxy(content) // 调用插件管理器处理新代理
	if err == nil {
		inMsg = &retContent.NewProxy
		remoteAddr, err = ctl.RegisterProxy(inMsg) // 注册代理
	}

	resp := &msg.NewProxyResp{ // 创建新代理响应
		ProxyName: inMsg.ProxyName,
	}
	if err != nil {
		xl.Warnf("new proxy [%s] type [%s] error: %v", inMsg.ProxyName, inMsg.ProxyType, err) // 记录错误
		resp.Error = util.GenerateResponseErrorString(fmt.Sprintf("new proxy [%s] error", inMsg.ProxyName),
			err, lo.FromPtr(ctl.serverCfg.DetailedErrorsToClient)) // 生成错误响应
	} else {
		resp.RemoteAddr = remoteAddr                                                   // 设置远程地址
		xl.Infof("new proxy [%s] type [%s] success", inMsg.ProxyName, inMsg.ProxyType) // 记录成功
		metrics.Server.NewProxy(inMsg.ProxyName, inMsg.ProxyType)                      // 更新指标
	}
	_ = ctl.msgDispatcher.Send(resp) // 发送响应
}

// 处理Ping消息
func (ctl *Control) handlePing(m msg.Message) {
	xl := ctl.xl
	inMsg := m.(*msg.Ping) // 类型断言为Ping消息

	content := &plugin.PingContent{ // 创建Ping内容
		User: plugin.UserInfo{
			User:  ctl.loginMsg.User,
			Metas: ctl.loginMsg.Metas,
			RunID: ctl.loginMsg.RunID,
		},
		Ping: *inMsg,
	}
	retContent, err := ctl.pluginManager.Ping(content) // 调用插件管理器处理Ping
	if err == nil {
		inMsg = &retContent.Ping
		err = ctl.authVerifier.VerifyPing(inMsg) // 验证Ping消息
	}
	if err != nil {
		xl.Warnf("received invalid ping: %v", err) // 记录无效Ping
		_ = ctl.msgDispatcher.Send(&msg.Pong{
			Error: util.GenerateResponseErrorString("invalid ping", err, lo.FromPtr(ctl.serverCfg.DetailedErrorsToClient)), // 生成错误响应
		})
		return
	}
	ctl.lastPing.Store(time.Now())          // 更新上次Ping时间
	xl.Debugf("receive heartbeat")          // 记录心跳
	_ = ctl.msgDispatcher.Send(&msg.Pong{}) // 发送Pong响应
}

// 处理关闭代理消息
func (ctl *Control) handleCloseProxy(m msg.Message) {
	xl := ctl.xl
	inMsg := m.(*msg.CloseProxy)                          // 类型断言为CloseProxy消息
	_ = ctl.CloseProxy(inMsg)                             // 关闭代理
	xl.Infof("close proxy [%s] success", inMsg.ProxyName) // 记录成功
}

// 注册代理
func (ctl *Control) RegisterProxy(pxyMsg *msg.NewProxy) (remoteAddr string, err error) {
	var pxyConf v1.ProxyConfigurer
	pxyConf, err = config.NewProxyConfigurerFromMsg(pxyMsg, ctl.serverCfg) // 从消息中加载配置
	if err != nil {
		return
	}

	userInfo := plugin.UserInfo{ // 创建用户信息
		User:  ctl.loginMsg.User,
		Metas: ctl.loginMsg.Metas,
		RunID: ctl.runID,
	}

	pxy, err := proxy.NewProxy(ctl.ctx, &proxy.Options{ // 创建新代理
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

	if ctl.serverCfg.MaxPortsPerClient > 0 { // 如果设置了每个客户端的最大端口数
		ctl.mu.Lock()
		if ctl.portsUsedNum+pxy.GetUsedPortsNum() > int(ctl.serverCfg.MaxPortsPerClient) { // 检查是否超过最大端口数
			ctl.mu.Unlock()
			err = fmt.Errorf("exceed the max_ports_per_client") // 返回错误
			return
		}
		ctl.portsUsedNum += pxy.GetUsedPortsNum() // 更新已使用端口数
		ctl.mu.Unlock()

		defer func() {
			if err != nil {
				ctl.mu.Lock()
				ctl.portsUsedNum -= pxy.GetUsedPortsNum() // 如果出错，回滚已使用端口数
				ctl.mu.Unlock()
			}
		}()
	}

	if ctl.pxyManager.Exist(pxyMsg.ProxyName) { // 检查代理是否已存在
		err = fmt.Errorf("proxy [%s] already exists", pxyMsg.ProxyName) // 返回错误
		return
	}

	remoteAddr, err = pxy.Run() // 运行代理
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			pxy.Close() // 如果出错，关闭代理
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
	pxy, ok := ctl.proxies[closeMsg.ProxyName] // 获取代理
	if !ok {
		ctl.mu.Unlock()
		return // 如果代理不存在，返回
	}

	if ctl.serverCfg.MaxPortsPerClient > 0 { // 如果设置了每个客户端的最大端口数
		ctl.portsUsedNum -= pxy.GetUsedPortsNum() // 更新已使用端口数
	}
	pxy.Close()                             // 关闭代理
	ctl.pxyManager.Del(pxy.GetName())       // 从管理器中删除代理
	delete(ctl.proxies, closeMsg.ProxyName) // 从Control中删除代理
	ctl.mu.Unlock()

	metrics.Server.CloseProxy(pxy.GetName(), pxy.GetConfigurer().GetBaseConfig().Type) // 更新指标

	notifyContent := &plugin.CloseProxyContent{ // 创建关闭代理通知内容
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
