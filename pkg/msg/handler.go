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

package msg

import (
	"io"
	"reflect"
)

// AsyncHandler 返回一个新的函数，该函数会异步调用传入的函数 f。
func AsyncHandler(f func(Message)) func(Message) {
	return func(m Message) {
		go f(m) // 使用 goroutine 异步执行函数 f。
	}
}

// Dispatcher 用于发送消息到 net.Conn 或注册从 net.Conn 读取消息的处理器。
type Dispatcher struct {
	rw io.ReadWriter // 读写接口，用于消息的输入输出。

	sendCh         chan Message                   // 用于发送消息的通道。
	doneCh         chan struct{}                  // 用于通知完成的通道。
	msgHandlers    map[reflect.Type]func(Message) // 消息类型到处理函数的映射。
	defaultHandler func(Message)                  // 默认的消息处理函数。
}

// NewDispatcher 创建一个新的 Dispatcher 实例。
func NewDispatcher(rw io.ReadWriter) *Dispatcher {
	return &Dispatcher{
		rw:          rw,
		sendCh:      make(chan Message, 100),              // 初始化发送通道，缓冲区大小为 100。
		doneCh:      make(chan struct{}),                  // 初始化完成通道。
		msgHandlers: make(map[reflect.Type]func(Message)), // 初始化消息处理器映射。
	}
}

// Run 启动发送和读取循环，直到发生 io.EOF 或其他错误。
func (d *Dispatcher) Run() {
	go d.sendLoop() // 启动发送循环。
	go d.readLoop() // 启动读取循环。
}

// sendLoop 负责从 sendCh 通道中读取消息并发送。
func (d *Dispatcher) sendLoop() {
	for {
		select {
		case <-d.doneCh: // 如果 doneCh 被关闭，退出循环。
			return
		case m := <-d.sendCh: // 从 sendCh 通道中读取消息。
			_ = WriteMsg(d.rw, m) // 将消息写入 rw。
		}
	}
}

// readLoop 负责从 rw 读取消息并调用相应的处理器。
func (d *Dispatcher) readLoop() {
	for {
		m, err := ReadMsg(d.rw) // 从 rw 读取消息。
		if err != nil {         // 如果读取出错，关闭 doneCh 并退出。
			close(d.doneCh)
			return
		}

		if handler, ok := d.msgHandlers[reflect.TypeOf(m)]; ok { // 查找消息类型对应的处理器。
			handler(m) // 调用处理器。
		} else if d.defaultHandler != nil { // 如果没有找到处理器，调用默认处理器。
			d.defaultHandler(m)
		}
	}
}

// Send 将消息发送到 sendCh 通道。
func (d *Dispatcher) Send(m Message) error {
	select {
	case <-d.doneCh: // 如果 doneCh 被关闭，返回 io.EOF 错误。
		return io.EOF
	case d.sendCh <- m: // 将消息发送到 sendCh 通道。
		return nil
	}
}

// SendChannel 返回 sendCh 通道。
func (d *Dispatcher) SendChannel() chan Message {
	return d.sendCh
}

// RegisterHandler 注册特定消息类型的处理器。
func (d *Dispatcher) RegisterHandler(msg Message, handler func(Message)) {
	d.msgHandlers[reflect.TypeOf(msg)] = handler
}

// RegisterDefaultHandler 注册默认的消息处理器。
func (d *Dispatcher) RegisterDefaultHandler(handler func(Message)) {
	d.defaultHandler = handler
}

// Done 返回 doneCh 通道。
func (d *Dispatcher) Done() chan struct{} {
	return d.doneCh
}
