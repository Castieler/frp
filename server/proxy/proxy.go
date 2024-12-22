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

package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"sync"
	"time"

	libio "github.com/fatedier/golib/io"
	"golang.org/x/time/rate"

	"github.com/fatedier/frp/pkg/config/types"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
	plugin "github.com/fatedier/frp/pkg/plugin/server"
	"github.com/fatedier/frp/pkg/util/limit"
	netpkg "github.com/fatedier/frp/pkg/util/net"
	"github.com/fatedier/frp/pkg/util/xlog"
	"github.com/fatedier/frp/server/controller"
	"github.com/fatedier/frp/server/metrics"
)

// proxyFactoryRegistry 是一个映射，用于存储不同类型的代理工厂函数。
var proxyFactoryRegistry = map[reflect.Type]func(*BaseProxy) Proxy{}

// RegisterProxyFactory 注册一个代理工厂函数到 proxyFactoryRegistry 中。
func RegisterProxyFactory(proxyConfType reflect.Type, factory func(*BaseProxy) Proxy) {
	// 将工厂函数与代理配置类型关联并存储在注册表中。
	proxyFactoryRegistry[proxyConfType] = factory
}

// GetWorkConnFn 是一个函数类型，用于获取工作连接。
type GetWorkConnFn func() (net.Conn, error)

// Proxy 接口定义了代理需要实现的方法。
type Proxy interface {
	// Context 返回代理的上下文。
	Context() context.Context
	// Run 启动代理并返回远程地址和可能的错误。
	Run() (remoteAddr string, err error)
	// GetName 返回代理的名称。
	GetName() string
	// GetConfigurer 返回代理的配置器。
	GetConfigurer() v1.ProxyConfigurer
	// GetWorkConnFromPool 从连接池中获取工作连接。
	GetWorkConnFromPool(src, dst net.Addr) (workConn net.Conn, err error)
	// GetUsedPortsNum 返回使用的端口数量。
	GetUsedPortsNum() int
	// GetResourceController 返回资源控制器。
	GetResourceController() *controller.ResourceController
	// GetUserInfo 返回用户信息。
	GetUserInfo() plugin.UserInfo
	// GetLimiter 返回速率限制器。
	GetLimiter() *rate.Limiter
	// GetLoginMsg 返回登录消息。
	GetLoginMsg() *msg.Login
	// Close 关闭代理。
	Close()
}

// BaseProxy 是一个基础的代理结构体，包含了代理的基本信息和功能。
type BaseProxy struct {
	name          string                         // 代理名称
	rc            *controller.ResourceController // 资源控制器
	listeners     []net.Listener                 // 监听器列表
	usedPortsNum  int                            // 使用的端口数量
	poolCount     int                            // 连接池数量
	getWorkConnFn GetWorkConnFn                  // 获取工作连接的函数
	serverCfg     *v1.ServerConfig               // 服务器配置
	limiter       *rate.Limiter                  // 速率限制器
	userInfo      plugin.UserInfo                // 用户信息
	loginMsg      *msg.Login                     // 登录消息
	configurer    v1.ProxyConfigurer             // 配置器

	mu  sync.RWMutex    // 读写锁，用于保护共享资源
	xl  *xlog.Logger    // 日志记录器，用于记录日志信息
	ctx context.Context // 上下文，用于传递取消信号和其他请求范围的数据
}

// GetName 返回代理的名称。
func (pxy *BaseProxy) GetName() string {
	return pxy.name // 返回代理的名称字段
}

// Context 返回代理的上下文。
func (pxy *BaseProxy) Context() context.Context {
	return pxy.ctx // 返回代理的上下文字段
}

// GetUsedPortsNum 返回使用的端口数量。
func (pxy *BaseProxy) GetUsedPortsNum() int {
	return pxy.usedPortsNum // 返回代理使用的端口数量
}

// GetResourceController 返回资源控制器。
func (pxy *BaseProxy) GetResourceController() *controller.ResourceController {
	return pxy.rc // 返回代理的资源控制器
}

// GetUserInfo 返回用户信息。
func (pxy *BaseProxy) GetUserInfo() plugin.UserInfo {
	return pxy.userInfo // 返回代理的用户信息
}

// GetLoginMsg 返回登录消息。
func (pxy *BaseProxy) GetLoginMsg() *msg.Login {
	return pxy.loginMsg // 返回代理的登录消息
}

// GetLimiter 返回速率限制器。
func (pxy *BaseProxy) GetLimiter() *rate.Limiter {
	return pxy.limiter // 返回代理的速率限制器
}

// GetConfigurer 返回配置器。
func (pxy *BaseProxy) GetConfigurer() v1.ProxyConfigurer {
	return pxy.configurer // 返回代理的配置器
}

// Close 关闭代理，释放所有资源。
func (pxy *BaseProxy) Close() {
	xl := xlog.FromContextSafe(pxy.ctx) // 从上下文中安全地获取日志记录器
	xl.Infof("proxy closing")           // 记录代理关闭的信息
	for _, l := range pxy.listeners {   // 遍历所有监听器
		l.Close() // 关闭每个监听器
	}
}

// GetWorkConnFromPool 从连接池中获取一个新的工作连接。
func (pxy *BaseProxy) GetWorkConnFromPool(src, dst net.Addr) (workConn net.Conn, err error) {
	xl := xlog.FromContextSafe(pxy.ctx) // 从上下文中安全地获取日志记录器
	// 尝试从连接池中获取连接
	for i := 0; i < pxy.poolCount+1; i++ { // 循环尝试获取连接，次数为连接池数量加一
		if workConn, err = pxy.getWorkConnFn(); err != nil { // 调用获取工作连接的函数
			xl.Warnf("failed to get work connection: %v", err) // 如果失败，记录警告日志
			return                                             // 返回错误
		}
		xl.Debugf("get a new work connection: [%s]", workConn.RemoteAddr().String()) // 记录获取到的新连接
		xl.Spawn().AppendPrefix(pxy.GetName())                                       // 为日志记录器添加前缀
		workConn = netpkg.NewContextConn(pxy.ctx, workConn)                          // 将连接与上下文关联

		var (
			srcAddr    string // 源地址
			dstAddr    string // 目标地址
			srcPortStr string // 源端口字符串
			dstPortStr string // 目标端口字符串
			srcPort    uint64 // 源端口
			dstPort    uint64 // 目标端口
		)

		if src != nil { // 如果源地址不为空
			srcAddr, srcPortStr, _ = net.SplitHostPort(src.String()) // 分割源地址和端口
			srcPort, _ = strconv.ParseUint(srcPortStr, 10, 16)       // 将源端口字符串转换为整数
		}
		if dst != nil { // 如果目标地址不为空
			dstAddr, dstPortStr, _ = net.SplitHostPort(dst.String()) // 分割目标地址和端口
			dstPort, _ = strconv.ParseUint(dstPortStr, 10, 16)       // 将目标端口字符串转换为整数
		}
		err := msg.WriteMsg(workConn, &msg.StartWorkConn{ // 发送 StartWorkConn 消息
			ProxyName: pxy.GetName(),   // 代理名称
			SrcAddr:   srcAddr,         // 源地址
			SrcPort:   uint16(srcPort), // 源端口
			DstAddr:   dstAddr,         // 目标地址
			DstPort:   uint16(dstPort), // 目标端口
			Error:     "",              // 错误信息
		})
		if err != nil { // 如果发送消息失败
			xl.Warnf("failed to send message to work connection from pool: %v, times: %d", err, i) // 记录警告日志
			workConn.Close()                                                                       // 关闭连接
		} else {
			break // 成功发送消息，跳出循环
		}
	}

	if err != nil { // 如果获取连接失败
		xl.Errorf("try to get work connection failed in the end") // 记录错误日志
		return                                                    // 返回错误
	}
	return // 返回成功获取的连接
}

// startCommonTCPListenersHandler 为每个监听器启动一个 goroutine 处理程序。
func (pxy *BaseProxy) startCommonTCPListenersHandler() {
	xl := xlog.FromContextSafe(pxy.ctx)      // 从上下文中安全地获取日志记录器
	for _, listener := range pxy.listeners { // 遍历所有监听器
		go func(l net.Listener) { // 为每个监听器启动一个 goroutine
			var tempDelay time.Duration // 接受失败时的睡眠时间

			for { // 无限循环
				// 阻塞
				// 如果监听器关闭，返回错误
				c, err := l.Accept() // 接受新的连接
				if err != nil {      // 如果接受失败
					if err, ok := err.(interface{ Temporary() bool }); ok && err.Temporary() { // 如果是临时错误
						if tempDelay == 0 { // 如果没有延迟
							tempDelay = 5 * time.Millisecond // 设置初始延迟
						} else {
							tempDelay *= 2 // 延迟加倍
						}
						if maxTime := 1 * time.Second; tempDelay > maxTime { // 如果延迟超过最大时间
							tempDelay = maxTime // 设置为最大时间
						}
						xl.Infof("met temporary error: %s, sleep for %s ...", err, tempDelay) // 记录信息日志
						time.Sleep(tempDelay)                                                 // 睡眠一段时间
						continue                                                              // 继续循环
					}

					xl.Warnf("listener is closed: %s", err) // 记录警告日志
					return                                  // 退出 goroutine
				}
				xl.Infof("get a user connection [%s]", c.RemoteAddr().String()) // 记录获取到的用户连接
				go pxy.handleUserTCPConnection(c)                               // 启动 goroutine 处理用户连接
			}
		}(listener) // 传入监听器
	}
}

// handleUserTCPConnection 处理传入的用户 TCP 连接。
func (pxy *BaseProxy) handleUserTCPConnection(userConn net.Conn) {
	xl := xlog.FromContextSafe(pxy.Context()) // 从上下文中安全地获取日志记录器
	defer userConn.Close()                    // 确保在函数结束时关闭用户连接

	serverCfg := pxy.serverCfg            // 获取服务器配置
	cfg := pxy.configurer.GetBaseConfig() // 获取基础配置
	// 服务器插件钩子
	rc := pxy.GetResourceController()      // 获取资源控制器
	content := &plugin.NewUserConnContent{ // 创建新的用户连接内容
		User:       pxy.GetUserInfo(),              // 用户信息
		ProxyName:  pxy.GetName(),                  // 代理名称
		ProxyType:  cfg.Type,                       // 代理类型
		RemoteAddr: userConn.RemoteAddr().String(), // 用户远程地址
	}
	_, err := rc.PluginManager.NewUserConn(content) // 调用插件管理器的新用户连接方法
	if err != nil {                                 // 如果连接被拒绝
		xl.Warnf("the user conn [%s] was rejected, err:%v", content.RemoteAddr, err) // 记录警告日志
		return                                                                       // 返回
	}

	// 尝试从连接池中获取连接
	workConn, err := pxy.GetWorkConnFromPool(userConn.RemoteAddr(), userConn.LocalAddr()) // 获取工作连接
	if err != nil {                                                                       // 如果获取失败
		return // 返回
	}
	defer workConn.Close() // 确保在函数结束时关闭工作连接

	var local io.ReadWriteCloser = workConn // 将工作连接赋值给本地变量
	xl.Tracef("handler user tcp connection, use_encryption: %t, use_compression: %t",
		cfg.Transport.UseEncryption, cfg.Transport.UseCompression) // 记录加密和压缩信息
	if cfg.Transport.UseEncryption { // 如果使用加密
		local, err = libio.WithEncryption(local, []byte(serverCfg.Auth.Token)) // 创建加密流
		if err != nil {                                                        // 如果创建失败
			xl.Errorf("create encryption stream error: %v", err) // 记录错误日志
			return                                               // 返回
		}
	}
	if cfg.Transport.UseCompression { // 如果使用压缩
		var recycleFn func()                                    // 定义回收函数
		local, recycleFn = libio.WithCompressionFromPool(local) // 创建压缩流
		defer recycleFn()                                       // 确保在函数结束时调用回收函数
	}

	if pxy.GetLimiter() != nil { // 如果有速率限制器
		local = libio.WrapReadWriteCloser(limit.NewReader(local, pxy.GetLimiter()), limit.NewWriter(local, pxy.GetLimiter()), func() error {
			return local.Close() // 包装读写关闭器
		})
	}

	xl.Debugf("join connections, workConn(l[%s] r[%s]) userConn(l[%s] r[%s])", workConn.LocalAddr().String(),
		workConn.RemoteAddr().String(), userConn.LocalAddr().String(), userConn.RemoteAddr().String()) // 记录连接信息

	name := pxy.GetName()                                   // 获取代理名称
	proxyType := cfg.Type                                   // 获取代理类型
	metrics.Server.OpenConnection(name, proxyType)          // 打开连接计数
	inCount, outCount, _ := libio.Join(local, userConn)     // 连接本地和用户连接
	metrics.Server.CloseConnection(name, proxyType)         // 关闭连接计数
	metrics.Server.AddTrafficIn(name, proxyType, inCount)   // 增加流入流量
	metrics.Server.AddTrafficOut(name, proxyType, outCount) // 增加流出流量
	xl.Debugf("join connections closed")                    // 记录连接关闭信息
}

// Options 是用于创建新代理的选项。
type Options struct {
	UserInfo           plugin.UserInfo                // 用户信息
	LoginMsg           *msg.Login                     // 登录消息
	PoolCount          int                            // 连接池数量
	ResourceController *controller.ResourceController // 资源控制器
	GetWorkConnFn      GetWorkConnFn                  // 获取工作连接的函数
	Configurer         v1.ProxyConfigurer             // 配置器
	ServerCfg          *v1.ServerConfig               // 服务器配置
}

// NewProxy 创建一个新的代理实例。
func NewProxy(ctx context.Context, options *Options) (pxy Proxy, err error) {
	configurer := options.Configurer                                                      // 获取配置器
	xl := xlog.FromContextSafe(ctx).Spawn().AppendPrefix(configurer.GetBaseConfig().Name) // 创建日志记录器

	var limiter *rate.Limiter                                                                                        // 定义速率限制器
	limitBytes := configurer.GetBaseConfig().Transport.BandwidthLimit.Bytes()                                        // 获取带宽限制字节数
	if limitBytes > 0 && configurer.GetBaseConfig().Transport.BandwidthLimitMode == types.BandwidthLimitModeServer { // 如果有带宽限制
		limiter = rate.NewLimiter(rate.Limit(float64(limitBytes)), int(limitBytes)) // 创建速率限制器
	}

	basePxy := BaseProxy{ // 创建基础代理
		name:          configurer.GetBaseConfig().Name, // 代理名称
		rc:            options.ResourceController,      // 资源控制器
		listeners:     make([]net.Listener, 0),         // 初始化监听器列表
		poolCount:     options.PoolCount,               // 连接池数量
		getWorkConnFn: options.GetWorkConnFn,           // 获取工作连接的函数
		serverCfg:     options.ServerCfg,               // 服务器配置
		limiter:       limiter,                         // 速率限制器
		xl:            xl,                              // 日志记录器
		ctx:           xlog.NewContext(ctx, xl),        // 上下文
		userInfo:      options.UserInfo,                // 用户信息
		loginMsg:      options.LoginMsg,                // 登录消息
		configurer:    configurer,                      // 配置器
	}

	factory := proxyFactoryRegistry[reflect.TypeOf(configurer)] // 从注册表中获取工厂函数
	if factory == nil {                                         // 如果工厂函数不存在
		return pxy, fmt.Errorf("proxy type not support") // 返回错误
	}
	pxy = factory(&basePxy) // 使用工厂函数创建代理
	if pxy == nil {         // 如果代理创建失败
		return nil, fmt.Errorf("proxy not created") // 返回错误
	}
	return pxy, nil // 返回创建的代理
}

// Manager 管理多个代理实例。
type Manager struct {
	// 通过代理名称索引的代理
	pxys map[string]Proxy // 代理映射

	mu sync.RWMutex // 读写锁，用于保护代理映射
}

// NewManager 创建一个新的代理管理器。
func NewManager() *Manager {
	return &Manager{
		pxys: make(map[string]Proxy), // 初始化代理映射
	}
}

// Add 添加一个新的代理到管理器中。
func (pm *Manager) Add(name string, pxy Proxy) error {
	pm.mu.Lock()                    // 加锁
	defer pm.mu.Unlock()            // 确保在函数结束时解锁
	if _, ok := pm.pxys[name]; ok { // 如果代理名称已存在
		return fmt.Errorf("proxy name [%s] is already in use", name) // 返回错误
	}

	pm.pxys[name] = pxy // 将代理添加到映射中
	return nil          // 返回成功
}

// Exist 检查代理是否存在。
func (pm *Manager) Exist(name string) bool {
	pm.mu.RLock()          // 加读锁
	defer pm.mu.RUnlock()  // 确保在函数结束时解锁
	_, ok := pm.pxys[name] // 检查代理是否存在
	return ok              // 返回结果
}

// Del 删除一个代理。
func (pm *Manager) Del(name string) {
	pm.mu.Lock()          // 加锁
	defer pm.mu.Unlock()  // 确保在函数结束时解锁
	delete(pm.pxys, name) // 从映射中删除代理
}

// GetByName 根据名称获取代理。
func (pm *Manager) GetByName(name string) (pxy Proxy, ok bool) {
	pm.mu.RLock()           // 加读锁
	defer pm.mu.RUnlock()   // 确保在函数结束时解锁
	pxy, ok = pm.pxys[name] // 获取代理
	return                  // 返回代理和结果
}
