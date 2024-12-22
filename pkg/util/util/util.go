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

package util

import (
	"crypto/md5"            // 导入用于MD5哈希的包
	"crypto/rand"           // 导入用于生成随机数的包
	"crypto/subtle"         // 导入用于安全比较的包
	"encoding/hex"          // 导入用于十六进制编码的包
	"fmt"                   // 导入格式化I/O的包
	mathrand "math/rand/v2" // 导入用于随机数生成的包
	"net"                   // 导入网络相关的包
	"strconv"               // 导入字符串与其他类型转换的包
	"strings"               // 导入字符串操作的包
	"time"                  // 导入时间相关的包
)

// RandID 返回一个用于frp的随机字符串。
func RandID() (id string, err error) {
	return RandIDWithLen(16) // 调用RandIDWithLen函数生成长度为16的随机字符串
}

// RandIDWithLen 返回一个指定长度的随机字符串。
func RandIDWithLen(idLen int) (id string, err error) {
	if idLen <= 0 { // 如果长度小于等于0，返回空字符串
		return "", nil
	}
	b := make([]byte, idLen/2+1) // 创建一个字节切片，长度为idLen的一半加一
	_, err = rand.Read(b)        // 使用crypto/rand包生成随机字节
	if err != nil {              // 如果生成随机字节出错，返回错误
		return
	}

	id = fmt.Sprintf("%x", b) // 将字节切片格式化为十六进制字符串
	return id[:idLen], nil    // 返回指定长度的字符串
}

// GetAuthKey 生成一个基于token和时间戳的认证密钥。
func GetAuthKey(token string, timestamp int64) (key string) {
	md5Ctx := md5.New()                                    // 创建一个新的MD5哈希对象
	md5Ctx.Write([]byte(token))                            // 将token写入哈希对象
	md5Ctx.Write([]byte(strconv.FormatInt(timestamp, 10))) // 将时间戳转换为字符串并写入哈希对象
	data := md5Ctx.Sum(nil)                                // 计算哈希值
	return hex.EncodeToString(data)                        // 返回哈希值的十六进制编码
}

// CanonicalAddr 返回标准化的地址字符串。
func CanonicalAddr(host string, port int) (addr string) {
	if port == 80 || port == 443 { // 如果端口是80或443
		addr = host // 只返回主机名
	} else {
		addr = net.JoinHostPort(host, strconv.Itoa(port)) // 否则返回主机名和端口
	}
	return
}

// ParseRangeNumbers 解析一个范围字符串为整数切片。
func ParseRangeNumbers(rangeStr string) (numbers []int64, err error) {
	rangeStr = strings.TrimSpace(rangeStr) // 去除字符串两端的空白
	numbers = make([]int64, 0)             // 初始化整数切片
	// 例如：1000-2000,2001,2002,3000-4000
	numRanges := strings.Split(rangeStr, ",") // 按逗号分割字符串
	for _, numRangeStr := range numRanges {
		// 1000-2000 或 2001
		numArray := strings.Split(numRangeStr, "-") // 按短横线分割字符串
		// 长度：只有1或2是正确的
		rangeType := len(numArray)
		switch rangeType {
		case 1:
			// 单个数字
			singleNum, errRet := strconv.ParseInt(strings.TrimSpace(numArray[0]), 10, 64) // 解析单个数字
			if errRet != nil {
				err = fmt.Errorf("range number is invalid, %v", errRet) // 如果解析出错，返回错误
				return
			}
			numbers = append(numbers, singleNum) // 将数字添加到切片
		case 2:
			// 范围数字
			minValue, errRet := strconv.ParseInt(strings.TrimSpace(numArray[0]), 10, 64) // 解析最小值
			if errRet != nil {
				err = fmt.Errorf("range number is invalid, %v", errRet) // 如果解析出错，返回错误
				return
			}
			maxValue, errRet := strconv.ParseInt(strings.TrimSpace(numArray[1]), 10, 64) // 解析最大值
			if errRet != nil {
				err = fmt.Errorf("range number is invalid, %v", errRet) // 如果解析出错，返回错误
				return
			}
			if maxValue < minValue { // 如果最大值小于最小值，返回错误
				err = fmt.Errorf("range number is invalid")
				return
			}
			for i := minValue; i <= maxValue; i++ { // 将范围内的数字添加到切片
				numbers = append(numbers, i)
			}
		default:
			err = fmt.Errorf("range number is invalid") // 如果格式不正确，返回错误
			return
		}
	}
	return
}

// GenerateResponseErrorString 生成响应错误字符串。
func GenerateResponseErrorString(summary string, err error, detailed bool) string {
	if detailed {
		return err.Error() // 如果需要详细信息，返回错误的详细信息
	}
	return summary // 否则返回概要信息
}

// RandomSleep 随机休眠一段时间。
func RandomSleep(duration time.Duration, minRatio, maxRatio float64) time.Duration {
	minValue := int64(minRatio * 1000.0) // 计算最小值
	maxValue := int64(maxRatio * 1000.0) // 计算最大值
	var n int64
	if maxValue <= minValue { // 如果最大值小于等于最小值
		n = minValue // 使用最小值
	} else {
		n = mathrand.Int64N(maxValue-minValue) + minValue // 否则生成一个随机值
	}
	d := duration * time.Duration(n) / time.Duration(1000) // 计算休眠时间
	time.Sleep(d)                                          // 休眠
	return d                                               // 返回实际休眠时间
}

// ConstantTimeEqString 安全地比较两个字符串是否相等。
func ConstantTimeEqString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 // 使用常量时间比较函数
}
