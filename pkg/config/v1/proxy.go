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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/samber/lo"

	"github.com/fatedier/frp/pkg/config/types"
	"github.com/fatedier/frp/pkg/msg"
	"github.com/fatedier/frp/pkg/util/util"
)

// ProxyTransport 定义了代理传输的配置选项。
type ProxyTransport struct {
	// UseEncryption 控制与服务器的通信是否加密。加密使用服务器和客户端配置中提供的令牌。
	UseEncryption bool `json:"useEncryption,omitempty"`
	// UseCompression 控制与服务器的通信是否压缩。
	UseCompression bool `json:"useCompression,omitempty"`
	// BandwidthLimit 限制带宽，0 表示无限制。
	BandwidthLimit types.BandwidthQuantity `json:"bandwidthLimit,omitempty"`
	// BandwidthLimitMode 指定带宽限制是在客户端还是服务器端生效。有效值为 "client" 和 "server"，默认为 "client"。
	BandwidthLimitMode string `json:"bandwidthLimitMode,omitempty"`
	// ProxyProtocolVersion 指定使用的代理协议版本。有效值为 "v1"、"v2" 和 ""（自动选择），默认为 ""。
	ProxyProtocolVersion string `json:"proxyProtocolVersion,omitempty"`
}

// LoadBalancerConfig 定义了负载均衡的配置选项。
type LoadBalancerConfig struct {
	// Group 指定代理所属的组。服务器将使用此信息对同一组中的代理进行负载均衡。如果为空，则不属于任何组。
	Group string `json:"group"`
	// GroupKey 指定组密钥，同一组中的代理应使用相同的组密钥。
	GroupKey string `json:"groupKey,omitempty"`
}

// ProxyBackend 定义了代理后端的配置选项。
type ProxyBackend struct {
	// LocalIP 指定后端的 IP 地址或主机名。
	LocalIP string `json:"localIP,omitempty"`
	// LocalPort 指定后端的端口。
	LocalPort int `json:"localPort,omitempty"`
	// Plugin 指定用于处理连接的插件。如果设置了此值，LocalIP 和 LocalPort 将被忽略。
	Plugin TypedClientPluginOptions `json:"plugin,omitempty"`
}

// HealthCheckConfig 定义了健康检查的配置选项，用于检测和移除故障服务。
type HealthCheckConfig struct {
	// Type 指定健康检查使用的协议类型。有效值为 "tcp"、"http" 和 ""（不进行健康检查）。
	Type string `json:"type"` // tcp | http
	// TimeoutSeconds 指定健康检查尝试连接的超时时间（秒）。如果超时，则视为健康检查失败，默认为 3。
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// MaxFailed 指定允许的失败次数，超过此值后代理将停止，默认为 1。
	MaxFailed int `json:"maxFailed,omitempty"`
	// IntervalSeconds 指定健康检查之间的间隔时间（秒），默认为 10。
	IntervalSeconds int `json:"intervalSeconds"`
	// Path 指定健康检查的路径（仅当类型为 "http" 时有效）。
	Path string `json:"path,omitempty"`
	// HTTPHeaders 指定健康检查请求的 HTTP 头（仅当类型为 "http" 时有效）。
	HTTPHeaders []HTTPHeader `json:"httpHeaders,omitempty"`
}

// DomainConfig 定义了域名相关的配置选项。
type DomainConfig struct {
	// CustomDomains 指定自定义域名列表。
	CustomDomains []string `json:"customDomains,omitempty"`
	// SubDomain 指定子域名。
	SubDomain string `json:"subdomain,omitempty"`
}

// ProxyBaseConfig 定义了代理的基本配置选项。
type ProxyBaseConfig struct {
	Name         string             `json:"name"`                   // 代理名称
	Type         string             `json:"type"`                   // 代理类型
	Annotations  map[string]string  `json:"annotations,omitempty"`  // 代理的注解
	Transport    ProxyTransport     `json:"transport,omitempty"`    // 传输配置
	Metadatas    map[string]string  `json:"metadatas,omitempty"`    // 代理的元数据
	LoadBalancer LoadBalancerConfig `json:"loadBalancer,omitempty"` // 负载均衡配置
	HealthCheck  HealthCheckConfig  `json:"healthCheck,omitempty"`  // 健康检查配置
	ProxyBackend                    // 代理后端配置
}

// GetBaseConfig 返回代理的基本配置。
func (c *ProxyBaseConfig) GetBaseConfig() *ProxyBaseConfig {
	return c
}

// Complete 完成代理配置的初始化，设置默认值。
func (c *ProxyBaseConfig) Complete(namePrefix string) {
	c.Name = lo.Ternary(namePrefix == "", "", namePrefix+".") + c.Name                                            // 设置代理名称前缀
	c.LocalIP = util.EmptyOr(c.LocalIP, "127.0.0.1")                                                              // 如果 LocalIP 为空，则设置为默认值 127.0.0.1
	c.Transport.BandwidthLimitMode = util.EmptyOr(c.Transport.BandwidthLimitMode, types.BandwidthLimitModeClient) // 设置带宽限制模式默认值

	// 如果插件配置存在，则完成插件的初始化。
	if c.Plugin.ClientPluginOptions != nil {
		c.Plugin.ClientPluginOptions.Complete()
	}
}

// MarshalToMsg 将代理配置序列化为 msg.NewProxy 消息。
func (c *ProxyBaseConfig) MarshalToMsg(m *msg.NewProxy) {
	m.ProxyName = c.Name
	m.ProxyType = c.Type
	m.UseEncryption = c.Transport.UseEncryption
	m.UseCompression = c.Transport.UseCompression
	m.BandwidthLimit = c.Transport.BandwidthLimit.String()
	// 如果带宽限制模式不是默认值，则设置到消息中。
	if c.Transport.BandwidthLimitMode != "client" {
		m.BandwidthLimitMode = c.Transport.BandwidthLimitMode
	}
	m.Group = c.LoadBalancer.Group
	m.GroupKey = c.LoadBalancer.GroupKey
	m.Metas = c.Metadatas
	m.Annotations = c.Annotations
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化代理配置。
func (c *ProxyBaseConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.Name = m.ProxyName
	c.Type = m.ProxyType
	c.Transport.UseEncryption = m.UseEncryption
	c.Transport.UseCompression = m.UseCompression
	if m.BandwidthLimit != "" {
		c.Transport.BandwidthLimit, _ = types.NewBandwidthQuantity(m.BandwidthLimit)
	}
	if m.BandwidthLimitMode != "" {
		c.Transport.BandwidthLimitMode = m.BandwidthLimitMode
	}
	c.LoadBalancer.Group = m.Group
	c.LoadBalancer.GroupKey = m.GroupKey
	c.Metadatas = m.Metas
	c.Annotations = m.Annotations
}

// TypedProxyConfig 定义了带类型的代理配置。
type TypedProxyConfig struct {
	Type string `json:"type"` // 代理类型
	ProxyConfigurer
}

// UnmarshalJSON 从 JSON 数据中反序列化 TypedProxyConfig。
func (c *TypedProxyConfig) UnmarshalJSON(b []byte) error {
	if len(b) == 4 && string(b) == "null" {
		return errors.New("type is required")
	}

	typeStruct := struct {
		Type string `json:"type"`
	}{}
	if err := json.Unmarshal(b, &typeStruct); err != nil {
		return err
	}

	c.Type = typeStruct.Type
	configurer := NewProxyConfigurerByType(ProxyType(typeStruct.Type))
	if configurer == nil {
		return fmt.Errorf("unknown proxy type: %s", typeStruct.Type)
	}
	decoder := json.NewDecoder(bytes.NewBuffer(b))
	if DisallowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(configurer); err != nil {
		return fmt.Errorf("unmarshal ProxyConfig error: %v", err)
	}
	c.ProxyConfigurer = configurer
	return nil
}

// MarshalJSON 将 TypedProxyConfig 序列化为 JSON 数据。
func (c *TypedProxyConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.ProxyConfigurer)
}

// ProxyConfigurer 是代理配置的接口，定义了代理配置的通用方法。
type ProxyConfigurer interface {
	Complete(namePrefix string)
	GetBaseConfig() *ProxyBaseConfig
	MarshalToMsg(*msg.NewProxy)
	UnmarshalFromMsg(*msg.NewProxy)
}

// ProxyType 定义了代理类型的枚举。
type ProxyType string

const (
	ProxyTypeTCP    ProxyType = "tcp"
	ProxyTypeUDP    ProxyType = "udp"
	ProxyTypeTCPMUX ProxyType = "tcpmux"
	ProxyTypeHTTP   ProxyType = "http"
	ProxyTypeHTTPS  ProxyType = "https"
	ProxyTypeSTCP   ProxyType = "stcp"
	ProxyTypeXTCP   ProxyType = "xtcp"
	ProxyTypeSUDP   ProxyType = "sudp"
)

// proxyConfigTypeMap 是代理类型与配置类型的映射。
var proxyConfigTypeMap = map[ProxyType]reflect.Type{
	ProxyTypeTCP:    reflect.TypeOf(TCPProxyConfig{}),
	ProxyTypeUDP:    reflect.TypeOf(UDPProxyConfig{}),
	ProxyTypeHTTP:   reflect.TypeOf(HTTPProxyConfig{}),
	ProxyTypeHTTPS:  reflect.TypeOf(HTTPSProxyConfig{}),
	ProxyTypeTCPMUX: reflect.TypeOf(TCPMuxProxyConfig{}),
	ProxyTypeSTCP:   reflect.TypeOf(STCPProxyConfig{}),
	ProxyTypeXTCP:   reflect.TypeOf(XTCPProxyConfig{}),
	ProxyTypeSUDP:   reflect.TypeOf(SUDPProxyConfig{}),
}

// NewProxyConfigurerByType 根据代理类型创建对应的代理配置器。
func NewProxyConfigurerByType(proxyType ProxyType) ProxyConfigurer {
	v, ok := proxyConfigTypeMap[proxyType]
	if !ok {
		return nil
	}
	pc := reflect.New(v).Interface().(ProxyConfigurer)
	pc.GetBaseConfig().Type = string(proxyType)
	return pc
}

// TCPProxyConfig 是 TCP 代理的配置。
type TCPProxyConfig struct {
	ProxyBaseConfig
	RemotePort int `json:"remotePort,omitempty"` // 远程端口
}

// MarshalToMsg 将 TCPProxyConfig 序列化为 msg.NewProxy 消息。
func (c *TCPProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)
	m.RemotePort = c.RemotePort
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化 TCPProxyConfig。
func (c *TCPProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)
	c.RemotePort = m.RemotePort
}

// UDPProxyConfig 是 UDP 代理的配置。
type UDPProxyConfig struct {
	ProxyBaseConfig
	RemotePort int `json:"remotePort,omitempty"` // 远程端口
}

// MarshalToMsg 将 UDPProxyConfig 序列化为 msg.NewProxy 消息。
func (c *UDPProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)
	m.RemotePort = c.RemotePort
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化 UDPProxyConfig。
func (c *UDPProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)
	c.RemotePort = m.RemotePort
}

// HTTPProxyConfig 是 HTTP 代理的配置。
type HTTPProxyConfig struct {
	ProxyBaseConfig
	DomainConfig
	Locations         []string         `json:"locations,omitempty"`         // 路径列表
	HTTPUser          string           `json:"httpUser,omitempty"`          // HTTP 用户名
	HTTPPassword      string           `json:"httpPassword,omitempty"`      // HTTP 密码
	HostHeaderRewrite string           `json:"hostHeaderRewrite,omitempty"` // 主机头重写
	RequestHeaders    HeaderOperations `json:"requestHeaders,omitempty"`    // 请求头操作
	ResponseHeaders   HeaderOperations `json:"responseHeaders,omitempty"`   // 响应头操作
	RouteByHTTPUser   string           `json:"routeByHTTPUser,omitempty"`   // 根据 HTTP 用户路由
}

// MarshalToMsg 将 HTTPProxyConfig 序列化为 msg.NewProxy 消息。
func (c *HTTPProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)
	m.CustomDomains = c.CustomDomains
	m.SubDomain = c.SubDomain
	m.Locations = c.Locations
	m.HostHeaderRewrite = c.HostHeaderRewrite
	m.HTTPUser = c.HTTPUser
	m.HTTPPwd = c.HTTPPassword
	m.Headers = c.RequestHeaders.Set
	m.ResponseHeaders = c.ResponseHeaders.Set
	m.RouteByHTTPUser = c.RouteByHTTPUser
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化 HTTPProxyConfig。
func (c *HTTPProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)
	c.CustomDomains = m.CustomDomains
	c.SubDomain = m.SubDomain
	c.Locations = m.Locations
	c.HostHeaderRewrite = m.HostHeaderRewrite
	c.HTTPUser = m.HTTPUser
	c.HTTPPassword = m.HTTPPwd
	c.RequestHeaders.Set = m.Headers
	c.ResponseHeaders.Set = m.ResponseHeaders
	c.RouteByHTTPUser = m.RouteByHTTPUser
}

// HTTPSProxyConfig 是 HTTPS 代理的配置。
type HTTPSProxyConfig struct {
	ProxyBaseConfig
	DomainConfig
}

// MarshalToMsg 将 HTTPSProxyConfig 序列化为 msg.NewProxy 消息。
func (c *HTTPSProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)
	m.CustomDomains = c.CustomDomains
	m.SubDomain = c.SubDomain
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化 HTTPSProxyConfig。
func (c *HTTPSProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)
	c.CustomDomains = m.CustomDomains
	c.SubDomain = m.SubDomain
}

// TCPMultiplexerType 定义了 TCP 多路复用器的类型。
type TCPMultiplexerType string

const (
	TCPMultiplexerHTTPConnect TCPMultiplexerType = "httpconnect"
)

// TCPMuxProxyConfig 是 TCP 多路复用代理的配置。
type TCPMuxProxyConfig struct {
	ProxyBaseConfig
	DomainConfig
	HTTPUser        string `json:"httpUser,omitempty"`        // HTTP 用户名
	HTTPPassword    string `json:"httpPassword,omitempty"`    // HTTP 密码
	RouteByHTTPUser string `json:"routeByHTTPUser,omitempty"` // 根据 HTTP 用户路由
	Multiplexer     string `json:"multiplexer,omitempty"`     // 多路复用器类型
}

// MarshalToMsg 将 TCPMuxProxyConfig 序列化为 msg.NewProxy 消息。
func (c *TCPMuxProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)
	m.CustomDomains = c.CustomDomains
	m.SubDomain = c.SubDomain
	m.Multiplexer = c.Multiplexer
	m.HTTPUser = c.HTTPUser
	m.HTTPPwd = c.HTTPPassword
	m.RouteByHTTPUser = c.RouteByHTTPUser
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化 TCPMuxProxyConfig。
func (c *TCPMuxProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)
	c.CustomDomains = m.CustomDomains
	c.SubDomain = m.SubDomain
	c.Multiplexer = m.Multiplexer
	c.HTTPUser = m.HTTPUser
	c.HTTPPassword = m.HTTPPwd
	c.RouteByHTTPUser = m.RouteByHTTPUser
}

// STCPProxyConfig 是 STCP 代理的配置。
type STCPProxyConfig struct {
	ProxyBaseConfig
	Secretkey  string   `json:"secretKey,omitempty"`  // 密钥
	AllowUsers []string `json:"allowUsers,omitempty"` // 允许的用户列表
}

// MarshalToMsg 将 STCPProxyConfig 序列化为 msg.NewProxy 消息。
func (c *STCPProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)
	m.Sk = c.Secretkey
	m.AllowUsers = c.AllowUsers
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化 STCPProxyConfig。
func (c *STCPProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)
	c.Secretkey = m.Sk
	c.AllowUsers = m.AllowUsers
}

// XTCPProxyConfig 是 XTCP 代理的配置。
type XTCPProxyConfig struct {
	ProxyBaseConfig
	Secretkey  string   `json:"secretKey,omitempty"`  // 密钥
	AllowUsers []string `json:"allowUsers,omitempty"` // 允许的用户列表
}

// MarshalToMsg 将 XTCPProxyConfig 序列化为 msg.NewProxy 消息。
func (c *XTCPProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)
	m.Sk = c.Secretkey
	m.AllowUsers = c.AllowUsers
}

// UnmarshalFromMsg 从 msg.NewProxy 消息中反序列化 XTCPProxyConfig。
func (c *XTCPProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)
	c.Secretkey = m.Sk
	c.AllowUsers = m.AllowUsers
}

var _ ProxyConfigurer = &SUDPProxyConfig{}

type SUDPProxyConfig struct {
	ProxyBaseConfig

	Secretkey  string   `json:"secretKey,omitempty"`
	AllowUsers []string `json:"allowUsers,omitempty"`
}

func (c *SUDPProxyConfig) MarshalToMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.MarshalToMsg(m)

	m.Sk = c.Secretkey
	m.AllowUsers = c.AllowUsers
}

func (c *SUDPProxyConfig) UnmarshalFromMsg(m *msg.NewProxy) {
	c.ProxyBaseConfig.UnmarshalFromMsg(m)

	c.Secretkey = m.Sk
	c.AllowUsers = m.AllowUsers
}
