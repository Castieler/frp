// Copyright 2019 fatedier, fatedier@gmail.com
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
	"fmt"
	"net"
	"reflect"
	"strconv"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

// 初始化函数，用于注册TCP代理工厂
func init() {
	// 注册一个新的代理工厂，使用TCPProxyConfig类型和NewTCPProxy构造函数
	RegisterProxyFactory(reflect.TypeOf(&v1.TCPProxyConfig{}), NewTCPProxy)
}

// TCPProxy结构体，继承自BaseProxy
type TCPProxy struct {
	*BaseProxy                    // 嵌入BaseProxy结构体
	cfg        *v1.TCPProxyConfig // TCP代理的配置

	realBindPort int // 实际绑定的端口
}

// NewTCPProxy函数，创建一个新的TCPProxy实例
func NewTCPProxy(baseProxy *BaseProxy) Proxy {
	// 从BaseProxy中获取配置器，并断言为TCPProxyConfig类型
	unwrapped, ok := baseProxy.GetConfigurer().(*v1.TCPProxyConfig)
	if !ok {
		// 如果断言失败，返回nil
		return nil
	}
	// 设置使用的端口数量为1
	baseProxy.usedPortsNum = 1
	// 返回一个新的TCPProxy实例
	return &TCPProxy{
		BaseProxy: baseProxy,
		cfg:       unwrapped,
	}
}

// Run方法，启动TCP代理
func (pxy *TCPProxy) Run() (remoteAddr string, err error) {
	xl := pxy.xl                          // 获取日志记录器
	if pxy.cfg.LoadBalancer.Group != "" { // 如果配置了负载均衡组
		// 监听负载均衡组的端口
		l, realBindPort, errRet := pxy.rc.TCPGroupCtl.Listen(pxy.name, pxy.cfg.LoadBalancer.Group, pxy.cfg.LoadBalancer.GroupKey,
			pxy.serverCfg.ProxyBindAddr, pxy.cfg.RemotePort)
		if errRet != nil {
			// 如果监听失败，返回错误
			err = errRet
			return
		}
		defer func() {
			// 如果发生错误，关闭监听器
			if err != nil {
				l.Close()
			}
		}()
		// 设置实际绑定的端口
		pxy.realBindPort = realBindPort
		// 将监听器添加到监听器列表中
		pxy.listeners = append(pxy.listeners, l)
		// 记录日志信息
		xl.Infof("tcp proxy listen port [%d] in group [%s]", pxy.cfg.RemotePort, pxy.cfg.LoadBalancer.Group)
	} else { // 如果没有配置负载均衡组
		// 从TCP端口管理器中获取一个端口
		pxy.realBindPort, err = pxy.rc.TCPPortManager.Acquire(pxy.name, pxy.cfg.RemotePort)
		if err != nil {
			// 如果获取失败，返回错误
			return
		}
		defer func() {
			// 如果发生错误，释放端口
			if err != nil {
				pxy.rc.TCPPortManager.Release(pxy.realBindPort)
			}
		}()
		// 监听指定的地址和端口
		listener, errRet := net.Listen("tcp", net.JoinHostPort(pxy.serverCfg.ProxyBindAddr, strconv.Itoa(pxy.realBindPort)))
		if errRet != nil {
			// 如果监听失败，返回错误
			err = errRet
			return
		}
		// 将监听器添加到监听器列表中
		pxy.listeners = append(pxy.listeners, listener)
		// 记录日志信息
		xl.Infof("tcp proxy listen port [%d]", pxy.cfg.RemotePort)
	}

	// 更新配置中的远程端口为实际绑定的端口
	pxy.cfg.RemotePort = pxy.realBindPort
	// 设置远程地址
	remoteAddr = fmt.Sprintf(":%d", pxy.realBindPort)
	// 启动通用的TCP监听器处理程序
	pxy.startCommonTCPListenersHandler()
	return
}

// Close方法，关闭TCP代理
func (pxy *TCPProxy) Close() {
	// 调用BaseProxy的Close方法
	pxy.BaseProxy.Close()
	// 如果没有配置负载均衡组，释放端口
	if pxy.cfg.LoadBalancer.Group == "" {
		pxy.rc.TCPPortManager.Release(pxy.realBindPort)
	}
}
