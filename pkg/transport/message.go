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

package transport

import (
	"context"
	"reflect"
	"sync"

	"github.com/fatedier/golib/errors"

	"github.com/fatedier/frp/pkg/msg"
)

// MessageTransporter 接口定义了消息传输器的行为
type MessageTransporter interface {
	// Send 发送消息
	Send(msg.Message) error
	// Do 先发送消息，然后接收与发送消息相同通道和指定类型的消息
	Do(ctx context.Context, req msg.Message, laneKey, recvMsgType string) (msg.Message, error)
	// Dispatch 根据消息类型和通道键将消息分派到相关通道
	Dispatch(m msg.Message, laneKey string) bool
	// DispatchWithType 根据指定的消息类型和通道键将消息分派到相关通道
	DispatchWithType(m msg.Message, msgType, laneKey string) bool
}

// NewMessageTransporter 创建一个新的消息传输器实例
func NewMessageTransporter(sendCh chan msg.Message) MessageTransporter {
	return &transporterImpl{
		sendCh:   sendCh,                                       // 发送消息的通道
		registry: make(map[string]map[string]chan msg.Message), // 消息类型和通道键到通道的映射
	}
}

// transporterImpl 实现了MessageTransporter接口
type transporterImpl struct {
	sendCh chan msg.Message // 发送消息的通道

	// First key is message type and second key is lane key.
	// Dispatch will dispatch message to related channel by its message type
	// and lane key.
	registry map[string]map[string]chan msg.Message // 消息类型和通道键到通道的映射
	mu       sync.RWMutex                           // 读写互斥锁，用于保护registry的并发访问
}

// Send 发送消息到sendCh通道
func (impl *transporterImpl) Send(m msg.Message) error {
	return errors.PanicToError(func() {
		impl.sendCh <- m // 将消息发送到通道
	})
}

// Do 先发送消息，然后接收与发送消息相同通道和指定类型的消息
func (impl *transporterImpl) Do(ctx context.Context, req msg.Message, laneKey, recvMsgType string) (msg.Message, error) {
	ch := make(chan msg.Message, 1)                                // 创建一个缓冲通道用于接收消息
	defer close(ch)                                                // 函数结束时关闭通道
	unregisterFn := impl.registerMsgChan(ch, laneKey, recvMsgType) // 注册通道
	defer unregisterFn()                                           // 函数结束时取消注册

	if err := impl.Send(req); err != nil { // 发送消息
		return nil, err
	}

	select {
	case <-ctx.Done(): // 上下文取消或超时
		return nil, ctx.Err()
	case resp := <-ch: // 接收到消息
		return resp, nil
	}
}

// DispatchWithType 根据指定的消息类型和通道键将消息分派到相关通道
func (impl *transporterImpl) DispatchWithType(m msg.Message, msgType, laneKey string) bool {
	var ch chan msg.Message
	impl.mu.RLock() // 加读锁
	byLaneKey, ok := impl.registry[msgType]
	if ok {
		ch = byLaneKey[laneKey]
	}
	impl.mu.RUnlock() // 解读锁

	if ch == nil {
		return false // 没有找到对应的通道
	}

	if err := errors.PanicToError(func() {
		ch <- m // 将消息发送到通道
	}); err != nil {
		return false
	}
	return true
}

// Dispatch 根据消息类型和通道键将消息分派到相关通道
func (impl *transporterImpl) Dispatch(m msg.Message, laneKey string) bool {
	msgType := reflect.TypeOf(m).Elem().Name() // 获取消息的类型名称
	return impl.DispatchWithType(m, msgType, laneKey)
}

// registerMsgChan 注册消息通道到registry中
func (impl *transporterImpl) registerMsgChan(recvCh chan msg.Message, laneKey string, msgType string) (unregister func()) {
	impl.mu.Lock() // 加写锁
	byLaneKey, ok := impl.registry[msgType]
	if !ok {
		byLaneKey = make(map[string]chan msg.Message)
		impl.registry[msgType] = byLaneKey
	}
	byLaneKey[laneKey] = recvCh
	impl.mu.Unlock() // 解写锁

	unregister = func() {
		impl.mu.Lock()             // 加写锁
		delete(byLaneKey, laneKey) // 从registry中删除通道
		impl.mu.Unlock()           // 解写锁
	}
	return
}
