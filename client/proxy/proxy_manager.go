// Copyright 2023 The frp Authors
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
	"net"
	"reflect"
	"sync"

	"github.com/samber/lo"

	"github.com/fatedier/frp/client/event"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
	"github.com/fatedier/frp/pkg/transport"
	"github.com/fatedier/frp/pkg/util/xlog"
)

// Manager 结构体用于管理代理（proxies）的生命周期和状态。
type Manager struct {
	proxies            map[string]*Wrapper                                          // 代理的映射表，键为代理名称，值为代理的封装对象
	msgTransporter     transport.MessageTransporter                                 // 消息传输器，用于发送和接收消息
	inWorkConnCallback func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) bool // 工作连接回调函数，用于处理工作连接

	closed bool         // 标记 Manager 是否已关闭
	mu     sync.RWMutex // 读写锁，用于保护并发访问

	clientCfg *v1.ClientCommonConfig // 客户端配置

	ctx context.Context // 上下文，用于传递取消信号和超时
}

// NewManager 创建一个新的 Manager 实例。
func NewManager(
	ctx context.Context,
	clientCfg *v1.ClientCommonConfig,
	msgTransporter transport.MessageTransporter,
) *Manager {
	return &Manager{
		proxies:        make(map[string]*Wrapper), // 初始化代理映射表
		msgTransporter: msgTransporter,            // 设置消息传输器
		closed:         false,                     // 初始化关闭状态为 false
		clientCfg:      clientCfg,                 // 设置客户端配置
		ctx:            ctx,                       // 设置上下文
	}
}

// StartProxy 启动指定名称的代理。
func (pm *Manager) StartProxy(name string, remoteAddr string, serverRespErr string) error {
	pm.mu.RLock()               // 加读锁
	pxy, ok := pm.proxies[name] // 从代理映射表中获取代理
	pm.mu.RUnlock()             // 解读锁
	if !ok {
		return fmt.Errorf("proxy [%s] not found", name) // 如果代理不存在，返回错误
	}

	err := pxy.SetRunningStatus(remoteAddr, serverRespErr) // 设置代理的运行状态
	if err != nil {
		return err
	}
	return nil
}

// SetInWorkConnCallback 设置工作连接的回调函数。
func (pm *Manager) SetInWorkConnCallback(cb func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) bool) {
	pm.inWorkConnCallback = cb // 设置回调函数
}

// Close 关闭 Manager，停止所有代理并清空代理映射表。
func (pm *Manager) Close() {
	pm.mu.Lock()         // 加写锁
	defer pm.mu.Unlock() // 解写锁
	for _, pxy := range pm.proxies {
		pxy.Stop() // 停止每个代理
	}
	pm.proxies = make(map[string]*Wrapper) // 清空代理映射表
}

// HandleWorkConn 处理工作连接。
func (pm *Manager) HandleWorkConn(name string, workConn net.Conn, m *msg.StartWorkConn) {
	pm.mu.RLock()              // 加读锁
	pw, ok := pm.proxies[name] // 从代理映射表中获取代理
	pm.mu.RUnlock()            // 解读锁
	if ok {
		pw.InWorkConn(workConn, m) // 如果代理存在，处理工作连接
	} else {
		workConn.Close() // 如果代理不存在，关闭工作连接
	}
}

// HandleEvent 处理事件，根据事件类型发送相应的消息。
func (pm *Manager) HandleEvent(payload interface{}) error {
	var m msg.Message
	switch e := payload.(type) {
	case *event.StartProxyPayload: // 如果是启动代理事件
		m = e.NewProxyMsg // 获取启动代理的消息
	case *event.CloseProxyPayload: // 如果是关闭代理事件
		m = e.CloseProxyMsg // 获取关闭代理的消息
	default:
		return event.ErrPayloadType // 如果事件类型不支持，返回错误
	}

	return pm.msgTransporter.Send(m) // 发送消息
}

// GetAllProxyStatus 获取所有代理的状态。
func (pm *Manager) GetAllProxyStatus() []*WorkingStatus {
	ps := make([]*WorkingStatus, 0) // 初始化状态列表
	pm.mu.RLock()                   // 加读锁
	defer pm.mu.RUnlock()           // 解读锁
	for _, pxy := range pm.proxies {
		ps = append(ps, pxy.GetStatus()) // 获取每个代理的状态并添加到列表中
	}
	return ps
}

// GetProxyStatus 获取指定名称的代理的状态。
func (pm *Manager) GetProxyStatus(name string) (*WorkingStatus, bool) {
	pm.mu.RLock()                        // 加读锁
	defer pm.mu.RUnlock()                // 解读锁
	if pxy, ok := pm.proxies[name]; ok { // 从代理映射表中获取代理
		return pxy.GetStatus(), true // 如果代理存在，返回其状态
	}
	return nil, false // 如果代理不存在，返回 nil 和 false
}

// UpdateAll 更新所有代理的配置。
func (pm *Manager) UpdateAll(proxyCfgs []v1.ProxyConfigurer) {
	xl := xlog.FromContextSafe(pm.ctx) // 从上下文中获取日志记录器
	proxyCfgsMap := lo.KeyBy(proxyCfgs, func(c v1.ProxyConfigurer) string {
		return c.GetBaseConfig().Name // 将代理配置列表转换为映射表，键为代理名称
	})
	pm.mu.Lock()         // 加写锁
	defer pm.mu.Unlock() // 解写锁

	delPxyNames := make([]string, 0) // 初始化待删除的代理名称列表
	for name, pxy := range pm.proxies {
		del := false
		cfg, ok := proxyCfgsMap[name]                // 从新的配置映射表中获取代理配置
		if !ok || !reflect.DeepEqual(pxy.Cfg, cfg) { // 如果代理配置不存在或与当前配置不同
			del = true // 标记为待删除
		}

		if del {
			delPxyNames = append(delPxyNames, name) // 将代理名称添加到待删除列表
			delete(pm.proxies, name)                // 从代理映射表中删除代理
			pxy.Stop()                              // 停止代理
		}
	}
	if len(delPxyNames) > 0 {
		xl.Infof("proxy removed: %s", delPxyNames) // 记录删除的代理
	}

	addPxyNames := make([]string, 0) // 初始化待添加的代理名称列表
	for _, cfg := range proxyCfgs {
		name := cfg.GetBaseConfig().Name    // 获取代理名称
		if _, ok := pm.proxies[name]; !ok { // 如果代理不存在于当前映射表中
			pxy := NewWrapper(pm.ctx, cfg, pm.clientCfg, pm.HandleEvent, pm.msgTransporter) // 创建新的代理封装对象
			if pm.inWorkConnCallback != nil {
				pxy.SetInWorkConnCallback(pm.inWorkConnCallback) // 设置工作连接回调函数
			}
			pm.proxies[name] = pxy                  // 将代理添加到映射表中
			addPxyNames = append(addPxyNames, name) // 将代理名称添加到待添加列表

			pxy.Start() // 启动代理
		}
	}
	if len(addPxyNames) > 0 {
		xl.Infof("proxy added: %s", addPxyNames) // 记录添加的代理
	}
}
