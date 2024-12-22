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

package v1

import (
	"os"

	"github.com/samber/lo"

	"github.com/fatedier/frp/pkg/util/util"
)

// ClientConfig 定义了客户端配置结构体
type ClientConfig struct {
	ClientCommonConfig // 嵌入了通用客户端配置

	Proxies  []TypedProxyConfig   `json:"proxies,omitempty"`  // 代理配置列表
	Visitors []TypedVisitorConfig `json:"visitors,omitempty"` // 访问者配置列表
}

// ClientCommonConfig 定义了通用客户端配置结构体
type ClientCommonConfig struct {
	APIMetadata // 嵌入了 API 元数据

	Auth AuthClientConfig `json:"auth,omitempty"` // 认证配置
	// User 用于指定代理名称的前缀，以便与其他客户端区分开来。
	// 如果此值不为空，代理名称将自动更改为 "{user}.{proxy_name}"。
	User string `json:"user,omitempty"`

	// ServerAddr 指定要连接的服务器地址。默认值为 "0.0.0.0"。
	ServerAddr string `json:"serverAddr,omitempty"`
	// ServerPort 指定要连接到服务器的端口。默认值为 7000。
	ServerPort int `json:"serverPort,omitempty"`
	// NatHoleSTUNServer 用于帮助穿透 NAT 的 STUN 服务器。
	NatHoleSTUNServer string `json:"natHoleStunServer,omitempty"`
	// DNSServer 指定 FRPC 使用的 DNS 服务器地址。如果此值为空，将使用默认 DNS。
	DNSServer string `json:"dnsServer,omitempty"`
	// LoginFailExit 控制客户端在登录失败后是否退出。如果为 false，客户端将重试直到登录成功。默认值为 true。
	LoginFailExit *bool `json:"loginFailExit,omitempty"`
	// Start 指定启用的代理名称集合。如果此集合为空，则启用所有提供的代理。默认值为空集合。
	Start []string `json:"start,omitempty"`

	Log       LogConfig             `json:"log,omitempty"`       // 日志配置
	WebServer WebServerConfig       `json:"webServer,omitempty"` // Web 服务器配置
	Transport ClientTransportConfig `json:"transport,omitempty"` // 传输配置

	// UDPPacketSize 指定 UDP 包的大小。默认值为 1500。
	UDPPacketSize int64 `json:"udpPacketSize,omitempty"`
	// Metadatas 客户端元数据信息
	Metadatas map[string]string `json:"metadatas,omitempty"`

	// IncludeConfigFiles 包含其他代理的配置文件。
	IncludeConfigFiles []string `json:"includes,omitempty"`
}

// Complete 方法用于填充 ClientCommonConfig 的默认值
func (c *ClientCommonConfig) Complete() {
	c.ServerAddr = util.EmptyOr(c.ServerAddr, "0.0.0.0")                              // 设置默认服务器地址
	c.ServerPort = util.EmptyOr(c.ServerPort, 7000)                                   // 设置默认服务器端口
	c.LoginFailExit = util.EmptyOr(c.LoginFailExit, lo.ToPtr(true))                   // 设置默认登录失败退出行为
	c.NatHoleSTUNServer = util.EmptyOr(c.NatHoleSTUNServer, "stun.easyvoip.com:3478") // 设置默认 STUN 服务器

	c.Auth.Complete()      // 完成认证配置
	c.Log.Complete()       // 完成日志配置
	c.Transport.Complete() // 完成传输配置
	c.WebServer.Complete() // 完成 Web 服务器配置

	c.UDPPacketSize = util.EmptyOr(c.UDPPacketSize, 1500) // 设置默认 UDP 包大小
}

// ClientTransportConfig 定义了客户端传输配置结构体
type ClientTransportConfig struct {
	// Protocol 指定与服务器交互时使用的协议。有效值为 "tcp", "kcp", "quic", "websocket" 和 "wss"。默认值为 "tcp"。
	Protocol string `json:"protocol,omitempty"`
	// DialServerTimeout 指定拨号到服务器的最大等待连接完成的时间。
	DialServerTimeout int64 `json:"dialServerTimeout,omitempty"`
	// DialServerKeepAlive 指定 frpc 和 frps 之间活动网络连接的保活探测间隔。
	// 如果为负数，则禁用保活探测。
	DialServerKeepAlive int64 `json:"dialServerKeepalive,omitempty"`
	// ConnectServerLocalIP 指定客户端连接到服务器时绑定的地址。
	// 注意：此值仅在 TCP/Websocket 协议中使用。在 KCP 协议中不支持。
	ConnectServerLocalIP string `json:"connectServerLocalIP,omitempty"`
	// ProxyURL 指定通过代理地址连接到服务器。如果此值为空，将直接连接到服务器。默认值从 "http_proxy" 环境变量读取。
	ProxyURL string `json:"proxyURL,omitempty"`
	// PoolCount 指定客户端将提前与服务器建立的连接数。
	PoolCount int `json:"poolCount,omitempty"`
	// TCPMux 切换 TCP 流多路复用。这允许客户端的多个请求共享单个 TCP 连接。
	// 如果此值为 true，服务器也必须启用 TCP 多路复用。默认值为 true。
	TCPMux *bool `json:"tcpMux,omitempty"`
	// TCPMuxKeepaliveInterval 指定 TCP 流多路复用器的保活间隔。
	// 如果 TCPMux 为 true，应用层的心跳是多余的，因为它可以仅依赖于 TCPMux 中的心跳。
	TCPMuxKeepaliveInterval int64 `json:"tcpMuxKeepaliveInterval,omitempty"`
	// QUIC 协议选项。
	QUIC QUICOptions `json:"quic,omitempty"`
	// HeartbeatInterval 指定心跳发送到服务器的间隔（以秒为单位）。不建议更改此值。默认值为 30。设置负值以禁用。
	HeartbeatInterval int64 `json:"heartbeatInterval,omitempty"`
	// HeartbeatTimeout 指定在连接终止之前允许的最大心跳响应延迟（以秒为单位）。不建议更改此值。默认值为 90。设置负值以禁用。
	HeartbeatTimeout int64 `json:"heartbeatTimeout,omitempty"`
	// TLS 指定与服务器连接的 TLS 设置。
	TLS TLSClientConfig `json:"tls,omitempty"`
}

// Complete 方法用于填充 ClientTransportConfig 的默认值
func (c *ClientTransportConfig) Complete() {
	c.Protocol = util.EmptyOr(c.Protocol, "tcp")                            // 设置默认协议
	c.DialServerTimeout = util.EmptyOr(c.DialServerTimeout, 10)             // 设置默认拨号超时时间
	c.DialServerKeepAlive = util.EmptyOr(c.DialServerKeepAlive, 7200)       // 设置默认保活间隔
	c.ProxyURL = util.EmptyOr(c.ProxyURL, os.Getenv("http_proxy"))          // 设置默认代理 URL
	c.PoolCount = util.EmptyOr(c.PoolCount, 1)                              // 设置默认连接池数量
	c.TCPMux = util.EmptyOr(c.TCPMux, lo.ToPtr(true))                       // 设置默认 TCP 多路复用
	c.TCPMuxKeepaliveInterval = util.EmptyOr(c.TCPMuxKeepaliveInterval, 30) // 设置默认 TCP 多路复用保活间隔
	if lo.FromPtr(c.TCPMux) {
		// 如果启用了 TCP 多路复用，应用层的心跳是多余的，因为可以依赖于 TCP 多路复用中的心跳。
		c.HeartbeatInterval = util.EmptyOr(c.HeartbeatInterval, -1)
		c.HeartbeatTimeout = util.EmptyOr(c.HeartbeatTimeout, -1)
	} else {
		c.HeartbeatInterval = util.EmptyOr(c.HeartbeatInterval, 30) // 设置默认心跳间隔
		c.HeartbeatTimeout = util.EmptyOr(c.HeartbeatTimeout, 90)   // 设置默认心跳超时
	}
	c.QUIC.Complete() // 完成 QUIC 配置
	c.TLS.Complete()  // 完成 TLS 配置
}

// TLSClientConfig 定义了 TLS 客户端配置结构体
type TLSClientConfig struct {
	// Enable 指定与服务器通信时是否应使用 TLS。
	// 如果 "tls.certFile" 和 "tls.keyFile" 有效，客户端将加载提供的 TLS 配置。
	// 自 v0.50.0 起，默认值已更改为 true，默认启用 TLS。
	Enable *bool `json:"enable,omitempty"`
	// DisableCustomTLSFirstByte 如果设置为 false，frpc 将在启用 TLS 时使用第一个自定义字节与 frps 建立连接。
	// 自 v0.50.0 起，默认值已更改为 true，默认禁用第一个自定义字节。
	DisableCustomTLSFirstByte *bool `json:"disableCustomTLSFirstByte,omitempty"`

	TLSConfig // 嵌入了 TLS 配置
}

// Complete 方法用于填充 TLSClientConfig 的默认值
func (c *TLSClientConfig) Complete() {
	c.Enable = util.EmptyOr(c.Enable, lo.ToPtr(true))                                       // 设置默认启用 TLS
	c.DisableCustomTLSFirstByte = util.EmptyOr(c.DisableCustomTLSFirstByte, lo.ToPtr(true)) // 设置默认禁用自定义 TLS 首字节
}

// AuthClientConfig 定义了认证客户端配置结构体
type AuthClientConfig struct {
	// Method 指定用于认证 frpc 与 frps 的认证方法。
	// 如果指定 "token" - 将在登录消息中读取令牌。
	// 如果指定 "oidc" - 将使用 OIDC 设置颁发 OIDC（开放 ID 连接）令牌。默认值为 "token"。
	Method AuthMethod `json:"method,omitempty"`
	// AdditionalScopes 指定是否在附加范围中包含认证信息。
	// 当前支持的范围有："HeartBeats", "NewWorkConns"。
	AdditionalScopes []AuthScope `json:"additionalScopes,omitempty"`
	// Token 指定用于创建要发送到服务器的密钥的授权令牌。
	// 服务器必须具有匹配的令牌才能成功授权。默认值为 ""。
	Token string               `json:"token,omitempty"`
	OIDC  AuthOIDCClientConfig `json:"oidc,omitempty"` // OIDC 客户端配置
}

// Complete 方法用于填充 AuthClientConfig 的默认值
func (c *AuthClientConfig) Complete() {
	c.Method = util.EmptyOr(c.Method, "token") // 设置默认认证方法
}

// AuthOIDCClientConfig 定义了 OIDC 客户端认证配置结构体
type AuthOIDCClientConfig struct {
	// ClientID 指定在 OIDC 认证中用于获取令牌的客户端 ID。
	ClientID string `json:"clientID,omitempty"`
	// ClientSecret 指定在 OIDC 认证中用于获取令牌的客户端密钥。
	ClientSecret string `json:"clientSecret,omitempty"`
	// Audience 指定 OIDC 认证中令牌的受众。
	Audience string `json:"audience,omitempty"`
	// Scope 指定 OIDC 认证中令牌的范围。
	Scope string `json:"scope,omitempty"`
	// TokenEndpointURL 指定实现 OIDC 令牌端点的 URL。
	// 它将用于获取 OIDC 令牌。
	TokenEndpointURL string `json:"tokenEndpointURL,omitempty"`
	// AdditionalEndpointParams 指定要发送的附加参数
	// 此字段将在 OIDC 令牌生成器中转换为 map[string][]string。
	AdditionalEndpointParams map[string]string `json:"additionalEndpointParams,omitempty"`
}
