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

package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/fatedier/frp/pkg/config/types"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
)

// WordSepNormalizeFunc changes all flags that contain "_" separators
// 将所有包含 "_" 分隔符的标志转换为使用 "-" 分隔符
func WordSepNormalizeFunc(f *pflag.FlagSet, name string) pflag.NormalizedName {
	if strings.Contains(name, "_") { // 如果标志名称包含 "_"
		return pflag.NormalizedName(strings.ReplaceAll(name, "_", "-")) // 将 "_" 替换为 "-"
	}
	return pflag.NormalizedName(name) // 返回原始名称
}

type RegisterFlagOption func(*registerFlagOptions) // 定义一个函数类型，用于注册标志选项

type registerFlagOptions struct {
	sshMode bool // 是否启用 SSH 模式
}

func WithSSHMode() RegisterFlagOption {
	return func(o *registerFlagOptions) {
		o.sshMode = true // 设置 SSH 模式为 true
	}
}

type BandwidthQuantityFlag struct {
	V *types.BandwidthQuantity // 带宽数量的指针
}

func (f *BandwidthQuantityFlag) Set(s string) error {
	return f.V.UnmarshalString(s) // 从字符串解析带宽数量
}

func (f *BandwidthQuantityFlag) String() string {
	return f.V.String() // 返回带宽数量的字符串表示
}

func (f *BandwidthQuantityFlag) Type() string {
	return "string" // 返回标志类型为字符串
}

func RegisterProxyFlags(cmd *cobra.Command, c v1.ProxyConfigurer, opts ...RegisterFlagOption) {
	registerProxyBaseConfigFlags(cmd, c.GetBaseConfig(), opts...) // 注册基础配置标志

	switch cc := c.(type) { // 根据不同的代理配置类型注册不同的标志
	case *v1.TCPProxyConfig:
		cmd.Flags().IntVarP(&cc.RemotePort, "remote_port", "r", 0, "remote port") // 注册远程端口标志
	case *v1.UDPProxyConfig:
		cmd.Flags().IntVarP(&cc.RemotePort, "remote_port", "r", 0, "remote port")
	case *v1.HTTPProxyConfig:
		registerProxyDomainConfigFlags(cmd, &cc.DomainConfig)                                               // 注册域名配置标志
		cmd.Flags().StringSliceVarP(&cc.Locations, "locations", "", []string{}, "locations")                // 注册位置标志
		cmd.Flags().StringVarP(&cc.HTTPUser, "http_user", "", "", "http auth user")                         // 注册 HTTP 用户标志
		cmd.Flags().StringVarP(&cc.HTTPPassword, "http_pwd", "", "", "http auth password")                  // 注册 HTTP 密码标志
		cmd.Flags().StringVarP(&cc.HostHeaderRewrite, "host_header_rewrite", "", "", "host header rewrite") // 注册主机头重写标志
	case *v1.HTTPSProxyConfig:
		registerProxyDomainConfigFlags(cmd, &cc.DomainConfig)
	case *v1.TCPMuxProxyConfig:
		registerProxyDomainConfigFlags(cmd, &cc.DomainConfig)
		cmd.Flags().StringVarP(&cc.Multiplexer, "mux", "", "", "multiplexer") // 注册多路复用器标志
		cmd.Flags().StringVarP(&cc.HTTPUser, "http_user", "", "", "http auth user")
		cmd.Flags().StringVarP(&cc.HTTPPassword, "http_pwd", "", "", "http auth password")
	case *v1.STCPProxyConfig:
		cmd.Flags().StringVarP(&cc.Secretkey, "sk", "", "", "secret key")                                 // 注册密钥标志
		cmd.Flags().StringSliceVarP(&cc.AllowUsers, "allow_users", "", []string{}, "allow visitor users") // 注册允许用户标志
	case *v1.SUDPProxyConfig:
		cmd.Flags().StringVarP(&cc.Secretkey, "sk", "", "", "secret key")
		cmd.Flags().StringSliceVarP(&cc.AllowUsers, "allow_users", "", []string{}, "allow visitor users")
	case *v1.XTCPProxyConfig:
		cmd.Flags().StringVarP(&cc.Secretkey, "sk", "", "", "secret key")
		cmd.Flags().StringSliceVarP(&cc.AllowUsers, "allow_users", "", []string{}, "allow visitor users")
	}
}

func registerProxyBaseConfigFlags(cmd *cobra.Command, c *v1.ProxyBaseConfig, opts ...RegisterFlagOption) {
	if c == nil {
		return // 如果基础配置为空，直接返回
	}
	options := &registerFlagOptions{} // 创建标志选项实例
	for _, opt := range opts {
		opt(options) // 应用每个选项
	}

	cmd.Flags().StringVarP(&c.Name, "proxy_name", "n", "", "proxy name") // 注册代理名称标志

	if !options.sshMode { // 如果不在 SSH 模式下
		cmd.Flags().StringVarP(&c.LocalIP, "local_ip", "i", "127.0.0.1", "local ip")                                                                // 注册本地 IP 标志
		cmd.Flags().IntVarP(&c.LocalPort, "local_port", "l", 0, "local port")                                                                       // 注册本地端口标志
		cmd.Flags().BoolVarP(&c.Transport.UseEncryption, "ue", "", false, "use encryption")                                                         // 注册加密标志
		cmd.Flags().BoolVarP(&c.Transport.UseCompression, "uc", "", false, "use compression")                                                       // 注册压缩标志
		cmd.Flags().StringVarP(&c.Transport.BandwidthLimitMode, "bandwidth_limit_mode", "", types.BandwidthLimitModeClient, "bandwidth limit mode") // 注册带宽限制模式标志
		cmd.Flags().VarP(&BandwidthQuantityFlag{V: &c.Transport.BandwidthLimit}, "bandwidth_limit", "", "bandwidth limit (e.g. 100KB or 1MB)")      // 注册带宽限制标志
	}
}

func registerProxyDomainConfigFlags(cmd *cobra.Command, c *v1.DomainConfig) {
	if c == nil {
		return // 如果域名配置为空，直接返回
	}
	cmd.Flags().StringSliceVarP(&c.CustomDomains, "custom_domain", "d", []string{}, "custom domains") // 注册自定义域名标志
	cmd.Flags().StringVarP(&c.SubDomain, "sd", "", "", "sub domain")                                  // 注册子域名标志
}

func RegisterVisitorFlags(cmd *cobra.Command, c v1.VisitorConfigurer, opts ...RegisterFlagOption) {
	registerVisitorBaseConfigFlags(cmd, c.GetBaseConfig(), opts...) // 注册访客基础配置标志

	// add visitor flags if exist
	// 如果存在，添加访客标志
}

func registerVisitorBaseConfigFlags(cmd *cobra.Command, c *v1.VisitorBaseConfig, _ ...RegisterFlagOption) {
	if c == nil {
		return // 如果基础配置为空，直接返回
	}
	cmd.Flags().StringVarP(&c.Name, "visitor_name", "n", "", "visitor name")              // 注册访客名称标志
	cmd.Flags().BoolVarP(&c.Transport.UseEncryption, "ue", "", false, "use encryption")   // 注册加密标志
	cmd.Flags().BoolVarP(&c.Transport.UseCompression, "uc", "", false, "use compression") // 注册压缩标志
	cmd.Flags().StringVarP(&c.SecretKey, "sk", "", "", "secret key")                      // 注册密钥标志
	cmd.Flags().StringVarP(&c.ServerName, "server_name", "", "", "server name")           // 注册服务器名称标志
	cmd.Flags().StringVarP(&c.ServerUser, "server-user", "", "", "server user")           // 注册服务器用户标志
	cmd.Flags().StringVarP(&c.BindAddr, "bind_addr", "", "", "bind addr")                 // 注册绑定地址标志
	cmd.Flags().IntVarP(&c.BindPort, "bind_port", "", 0, "bind port")                     // 注册绑定端口标志
}

func RegisterClientCommonConfigFlags(cmd *cobra.Command, c *v1.ClientCommonConfig, opts ...RegisterFlagOption) {
	options := &registerFlagOptions{} // 创建标志选项实例
	for _, opt := range opts {
		opt(options) // 应用每个选项
	}

	if !options.sshMode { // 如果不在 SSH 模式下
		cmd.PersistentFlags().StringVarP(&c.ServerAddr, "server_addr", "s", "127.0.0.1", "frp server's address") // 注册服务器地址标志
		cmd.PersistentFlags().IntVarP(&c.ServerPort, "server_port", "P", 7000, "frp server's port")              // 注册服务器端口标志
		cmd.PersistentFlags().StringVarP(&c.Transport.Protocol, "protocol", "p", "tcp",
			fmt.Sprintf("optional values are %v", validation.SupportedTransportProtocols)) // 注册协议标志
		cmd.PersistentFlags().StringVarP(&c.Log.Level, "log_level", "", "info", "log level")                                                          // 注册日志级别标志
		cmd.PersistentFlags().StringVarP(&c.Log.To, "log_file", "", "console", "console or file path")                                                // 注册日志输出标志
		cmd.PersistentFlags().Int64VarP(&c.Log.MaxDays, "log_max_days", "", 3, "log file reversed days")                                              // 注册日志保留天数标志
		cmd.PersistentFlags().BoolVarP(&c.Log.DisablePrintColor, "disable_log_color", "", false, "disable log color in console")                      // 注册禁用日志颜色标志
		cmd.PersistentFlags().StringVarP(&c.Transport.TLS.ServerName, "tls_server_name", "", "", "specify the custom server name of tls certificate") // 注册 TLS 服务器名称标志
		cmd.PersistentFlags().StringVarP(&c.DNSServer, "dns_server", "", "", "specify dns server instead of using system default one")                // 注册 DNS 服务器标志
		c.Transport.TLS.Enable = cmd.PersistentFlags().BoolP("tls_enable", "", true, "enable frpc tls")                                               // 注册启用 TLS 标志
	}
	cmd.PersistentFlags().StringVarP(&c.User, "user", "u", "", "user")              // 注册用户标志
	cmd.PersistentFlags().StringVarP(&c.Auth.Token, "token", "t", "", "auth token") // 注册认证令牌标志
}

type PortsRangeSliceFlag struct {
	V *[]types.PortsRange // 端口范围切片的指针
}

func (f *PortsRangeSliceFlag) String() string {
	if f.V == nil {
		return "" // 如果切片为空，返回空字符串
	}
	return types.PortsRangeSlice(*f.V).String() // 返回端口范围切片的字符串表示
}

func (f *PortsRangeSliceFlag) Set(s string) error {
	slice, err := types.NewPortsRangeSliceFromString(s) // 从字符串解析端口范围切片
	if err != nil {
		return err // 如果解析出错，返回错误
	}
	*f.V = slice // 设置解析后的切片
	return nil
}

func (f *PortsRangeSliceFlag) Type() string {
	return "string" // 返回标志类型为字符串
}

type BoolFuncFlag struct {
	TrueFunc  func() // 当标志为 true 时执行的函数
	FalseFunc func() // 当标志为 false 时执行的函数

	v bool // 标志的布尔值
}

func (f *BoolFuncFlag) String() string {
	return strconv.FormatBool(f.v) // 返回布尔值的字符串表示
}

func (f *BoolFuncFlag) Set(s string) error {
	f.v = strconv.FormatBool(f.v) == "true" // 设置布尔值

	if !f.v { // 如果布尔值为 false
		if f.FalseFunc != nil {
			f.FalseFunc() // 执行 FalseFunc
		}
		return nil
	}

	if f.TrueFunc != nil {
		f.TrueFunc() // 执行 TrueFunc
	}
	return nil
}

func (f *BoolFuncFlag) Type() string {
	return "bool" // 返回标志类型为布尔
}

func RegisterServerConfigFlags(cmd *cobra.Command, c *v1.ServerConfig, opts ...RegisterFlagOption) {
	cmd.PersistentFlags().StringVarP(&c.BindAddr, "bind_addr", "", "0.0.0.0", "bind address")                                // 注册绑定地址标志
	cmd.PersistentFlags().IntVarP(&c.BindPort, "bind_port", "p", 7000, "bind port")                                          // 注册绑定端口标志
	cmd.PersistentFlags().IntVarP(&c.KCPBindPort, "kcp_bind_port", "", 0, "kcp bind udp port")                               // 注册 KCP 绑定端口标志
	cmd.PersistentFlags().IntVarP(&c.QUICBindPort, "quic_bind_port", "", 0, "quic bind udp port")                            // 注册 QUIC 绑定端口标志
	cmd.PersistentFlags().StringVarP(&c.ProxyBindAddr, "proxy_bind_addr", "", "0.0.0.0", "proxy bind address")               // 注册代理绑定地址标志
	cmd.PersistentFlags().IntVarP(&c.VhostHTTPPort, "vhost_http_port", "", 0, "vhost http port")                             // 注册虚拟主机 HTTP 端口标志
	cmd.PersistentFlags().IntVarP(&c.VhostHTTPSPort, "vhost_https_port", "", 0, "vhost https port")                          // 注册虚拟主机 HTTPS 端口标志
	cmd.PersistentFlags().Int64VarP(&c.VhostHTTPTimeout, "vhost_http_timeout", "", 60, "vhost http response header timeout") // 注册虚拟主机 HTTP 超时标志
	cmd.PersistentFlags().StringVarP(&c.WebServer.Addr, "dashboard_addr", "", "0.0.0.0", "dashboard address")                // 注册仪表板地址标志
	cmd.PersistentFlags().IntVarP(&c.WebServer.Port, "dashboard_port", "", 0, "dashboard port")                              // 注册仪表板端口标志
	cmd.PersistentFlags().StringVarP(&c.WebServer.User, "dashboard_user", "", "admin", "dashboard user")                     // 注册仪表板用户标志
	cmd.PersistentFlags().StringVarP(&c.WebServer.Password, "dashboard_pwd", "", "admin", "dashboard password")              // 注册仪表板密码标志
	cmd.PersistentFlags().BoolVarP(&c.EnablePrometheus, "enable_prometheus", "", false, "enable prometheus dashboard")       // 注册启用 Prometheus 仪表板标志
	cmd.PersistentFlags().StringVarP(&c.Log.To, "log_file", "", "console", "log file")                                       // 注册日志文件标志
	cmd.PersistentFlags().StringVarP(&c.Log.Level, "log_level", "", "info", "log level")                                     // 注册日志级别标志
	cmd.PersistentFlags().Int64VarP(&c.Log.MaxDays, "log_max_days", "", 3, "log max days")                                   // 注册日志最大天数标志
	cmd.PersistentFlags().BoolVarP(&c.Log.DisablePrintColor, "disable_log_color", "", false, "disable log color in console") // 注册禁用日志颜色标志
	cmd.PersistentFlags().StringVarP(&c.Auth.Token, "token", "t", "", "auth token")                                          // 注册认证令牌标志
	cmd.PersistentFlags().StringVarP(&c.SubDomainHost, "subdomain_host", "", "", "subdomain host")                           // 注册子域名主机标志
	cmd.PersistentFlags().VarP(&PortsRangeSliceFlag{V: &c.AllowPorts}, "allow_ports", "", "allow ports")                     // 注册允许端口标志
	cmd.PersistentFlags().Int64VarP(&c.MaxPortsPerClient, "max_ports_per_client", "", 0, "max ports per client")             // 注册每个客户端最大端口数标志
	cmd.PersistentFlags().BoolVarP(&c.Transport.TLS.Force, "tls_only", "", false, "frps tls only")                           // 注册仅 TLS 标志

	webServerTLS := v1.TLSConfig{}                                                                                         // 创建 TLS 配置实例
	cmd.PersistentFlags().StringVarP(&webServerTLS.CertFile, "dashboard_tls_cert_file", "", "", "dashboard tls cert file") // 注册仪表板 TLS 证书文件标志
	cmd.PersistentFlags().StringVarP(&webServerTLS.KeyFile, "dashboard_tls_key_file", "", "", "dashboard tls key file")    // 注册仪表板 TLS 密钥文件标志
	cmd.PersistentFlags().VarP(&BoolFuncFlag{
		TrueFunc: func() { c.WebServer.TLS = &webServerTLS }, // 如果启用 TLS 模式，设置 WebServer 的 TLS 配置
	}, "dashboard_tls_mode", "", "if enable dashboard tls mode") // 注册启用仪表板 TLS 模式标志
}
