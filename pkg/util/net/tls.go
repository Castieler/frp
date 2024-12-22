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

package net

import (
	"crypto/tls" // 导入TLS加密库
	"fmt"        // 导入格式化I/O库
	"net"        // 导入网络库
	"time"       // 导入时间库

	libnet "github.com/fatedier/golib/net" // 导入自定义网络库
)

var FRPTLSHeadByte = 0x17 // 定义一个常量，表示自定义TLS头字节

// CheckAndEnableTLSServerConnWithTimeout 函数用于检查并启用带超时的TLS服务器连接
func CheckAndEnableTLSServerConnWithTimeout(
	c net.Conn, tlsConfig *tls.Config, tlsOnly bool, timeout time.Duration,
) (out net.Conn, isTLS bool, custom bool, err error) {
	sc, r := libnet.NewSharedConnSize(c, 2)        // 创建一个共享连接，允许同时读取和写入
	buf := make([]byte, 1)                         // 创建一个字节缓冲区，用于读取数据
	var n int                                      // 定义一个变量用于存储读取的字节数
	_ = c.SetReadDeadline(time.Now().Add(timeout)) // 设置读取超时时间
	n, err = r.Read(buf)                           // 从连接中读取一个字节
	_ = c.SetReadDeadline(time.Time{})             // 重置读取超时时间
	if err != nil {                                // 如果读取出错，返回错误
		return
	}

	switch {
	case n == 1 && int(buf[0]) == FRPTLSHeadByte: // 如果读取到的字节是自定义TLS头字节
		out = tls.Server(c, tlsConfig) // 使用TLS配置创建TLS服务器连接
		isTLS = true                   // 标记为TLS连接
		custom = true                  // 标记为自定义TLS连接
	case n == 1 && int(buf[0]) == 0x16: // 如果读取到的字节是标准TLS头字节
		out = tls.Server(sc, tlsConfig) // 使用TLS配置创建TLS服务器连接
		isTLS = true                    // 标记为TLS连接
	default: // 如果读取到的字节不是TLS头字节
		if tlsOnly { // 如果服务器只接受TLS连接
			err = fmt.Errorf("non-TLS connection received on a TlsOnly server") // 返回错误
			return
		}
		out = sc // 返回原始连接
	}
	return // 返回结果
}
