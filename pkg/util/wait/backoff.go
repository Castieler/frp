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

package wait

import (
	"math/rand/v2"
	"time"

	"github.com/fatedier/frp/pkg/util/util"
)

// BackoffFunc 是一个函数类型，用于计算下一个退避时间。
type BackoffFunc func(previousDuration time.Duration, previousConditionError bool) time.Duration

// Backoff 方法调用 BackoffFunc 类型的函数，返回下一个退避时间。
func (f BackoffFunc) Backoff(previousDuration time.Duration, previousConditionError bool) time.Duration {
	return f(previousDuration, previousConditionError)
}

// BackoffManager 接口定义了一个 Backoff 方法，用于计算下一个退避时间。
type BackoffManager interface {
	Backoff(previousDuration time.Duration, previousConditionError bool) time.Duration
}

// FastBackoffOptions 结构体定义了快速退避的配置选项。
type FastBackoffOptions struct {
	Duration           time.Duration // 默认退避时间
	Factor             float64       // 退避时间的倍数因子
	Jitter             float64       // 退避时间的抖动因子
	MaxDuration        time.Duration // 最大退避时间
	InitDurationIfFail time.Duration // 初始失败时的退避时间

	// FastRetryCount > 0 时，在 FastRetryWindow 时间窗口内，
	// 前 FastRetryCount 次重试将使用 FastRetryDelay 的延迟。
	FastRetryCount  int           // 快速重试的次数
	FastRetryDelay  time.Duration // 快速重试的延迟时间
	FastRetryJitter float64       // 快速重试的抖动因子
	FastRetryWindow time.Duration // 快速重试的时间窗口
}

// fastBackoffImpl 结构体实现了 BackoffManager 接口，使用 FastBackoffOptions 进行配置。
type fastBackoffImpl struct {
	options FastBackoffOptions // 快速退避的配置选项

	lastCalledTime      time.Time // 上次调用的时间
	consecutiveErrCount int       // 连续错误的计数

	fastRetryCutoffTime     time.Time // 快速重试的截止时间
	countsInFastRetryWindow int       // 快速重试窗口内的计数
}

// NewFastBackoffManager 创建一个新的 fastBackoffImpl 实例。
func NewFastBackoffManager(options FastBackoffOptions) BackoffManager {
	return &fastBackoffImpl{
		options:                 options,
		countsInFastRetryWindow: 1,
	}
}

// Backoff 方法计算下一个退避时间。
func (f *fastBackoffImpl) Backoff(previousDuration time.Duration, previousConditionError bool) time.Duration {
	if f.lastCalledTime.IsZero() { // 如果是第一次调用
		f.lastCalledTime = time.Now() // 记录当前时间
		return f.options.Duration     // 返回默认退避时间
	}
	now := time.Now()      // 获取当前时间
	f.lastCalledTime = now // 更新上次调用时间

	if previousConditionError { // 如果上次调用有错误
		f.consecutiveErrCount++ // 增加连续错误计数
	} else {
		f.consecutiveErrCount = 0 // 重置连续错误计数
	}

	if f.options.FastRetryCount > 0 && previousConditionError { // 如果启用了快速重试
		f.countsInFastRetryWindow++                                // 增加快速重试窗口内的计数
		if f.countsInFastRetryWindow <= f.options.FastRetryCount { // 如果在快速重试次数内
			return Jitter(f.options.FastRetryDelay, f.options.FastRetryJitter) // 返回快速重试的退避时间
		}
		if now.After(f.fastRetryCutoffTime) { // 如果超过快速重试窗口
			f.fastRetryCutoffTime = now.Add(f.options.FastRetryWindow) // 重置快速重试截止时间
			f.countsInFastRetryWindow = 0                              // 重置快速重试计数
		}
	}

	if previousConditionError { // 如果上次调用有错误
		var duration time.Duration
		if f.consecutiveErrCount == 1 { // 如果是第一次错误
			duration = util.EmptyOr(f.options.InitDurationIfFail, previousDuration) // 使用初始失败时间
		} else {
			duration = previousDuration // 使用上次的退避时间
		}

		duration = util.EmptyOr(duration, time.Second) // 确保退避时间不为零
		if f.options.Factor != 0 {                     // 如果设置了倍数因子
			duration = time.Duration(float64(duration) * f.options.Factor) // 计算新的退避时间
		}
		if f.options.Jitter > 0 { // 如果设置了抖动因子
			duration = Jitter(duration, f.options.Jitter) // 应用抖动
		}
		if f.options.MaxDuration > 0 && duration > f.options.MaxDuration { // 如果超过最大退避时间
			duration = f.options.MaxDuration // 使用最大退避时间
		}
		return duration // 返回计算后的退避时间
	}
	return f.options.Duration // 如果没有错误，返回默认退避时间
}

// BackoffUntil 函数在条件满足或停止信号接收到之前，持续调用函数 f。
func BackoffUntil(f func() (bool, error), backoff BackoffManager, sliding bool, stopCh <-chan struct{}) {
	var delay time.Duration // 初始化延迟时间
	previousError := false  // 初始化上次错误状态

	ticker := time.NewTicker(backoff.Backoff(delay, previousError)) // 创建一个新的定时器
	defer ticker.Stop()                                             // 确保在函数退出时停止定时器

	for {
		select {
		case <-stopCh: // 如果接收到停止信号
			return // 退出函数
		default:
		}

		if !sliding { // 如果不使用滑动窗口
			delay = backoff.Backoff(delay, previousError) // 计算新的延迟时间
		}

		if done, err := f(); done { // 调用函数 f
			return // 如果完成，退出函数
		} else if err != nil { // 如果有错误
			previousError = true // 设置上次错误状态
		} else {
			previousError = false // 重置上次错误状态
		}

		if sliding { // 如果使用滑动窗口
			delay = backoff.Backoff(delay, previousError) // 计算新的延迟时间
		}

		ticker.Reset(delay) // 重置定时器
		select {
		case <-stopCh: // 如果接收到停止信号
			return // 退出函数
		case <-ticker.C: // 等待定时器触发
		}
	}
}

// Jitter 函数返回一个在 duration 和 duration + maxFactor * duration 之间的时间。
func Jitter(duration time.Duration, maxFactor float64) time.Duration {
	if maxFactor <= 0.0 { // 如果 maxFactor 小于等于 0
		maxFactor = 1.0 // 使用默认值 1.0
	}
	wait := duration + time.Duration(rand.Float64()*maxFactor*float64(duration)) // 计算抖动后的时间
	return wait                                                                  // 返回计算结果
}

// Until 函数在停止信号接收到之前，持续调用函数 f。
func Until(f func(), period time.Duration, stopCh <-chan struct{}) {
	ff := func() (bool, error) { // 包装函数 f
		f()               // 调用 f
		return false, nil // 返回 false 和 nil
	}
	BackoffUntil(ff, BackoffFunc(func(time.Duration, bool) time.Duration {
		return period // 返回固定的周期
	}), true, stopCh) // 使用 BackoffUntil 调用包装后的函数
}
