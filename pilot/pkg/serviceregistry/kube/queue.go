// Copyright 2017 Istio Authors
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

package kube

import (
	"sync"
	"time"

	"istio.io/istio/pilot/pkg/model"
	"istio.io/pkg/log"
)

// Queue of work tickets processed using a rate-limiting loop
type Queue interface {
	// Push a ticket
	Push(Task)
	// Run the loop until a signal on the channel
	Run(<-chan struct{})
}

// Handler specifies a function to apply on an object for a given event type
type Handler func(obj interface{}, event model.Event) error

// Task object for the event watchers; processes until handler succeeds
type Task struct {
	Handler Handler
	Obj     interface{}
	Event   model.Event
}

// NewTask creates a task from a work item
func NewTask(handler Handler, obj interface{}, event model.Event) Task {
	return Task{Handler: handler, Obj: obj, Event: event}
}

type queueImpl struct {
	delay   time.Duration
	queue   []Task
	cond    *sync.Cond
	closing bool
}

// NewQueue instantiates a queue with a processing function
func NewQueue(errorDelay time.Duration) Queue {
	return &queueImpl{
		delay:   errorDelay,
		queue:   make([]Task, 0),
		closing: false,
		cond:    sync.NewCond(&sync.Mutex{}),
	}
}

func (q *queueImpl) Push(item Task) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	if !q.closing {
		q.queue = append(q.queue, item)
	}
	q.cond.Signal()
}

// 事件嘟咧
func (q *queueImpl) Run(stop <-chan struct{}) {
	go func() {
		<-stop
		q.cond.L.Lock()
		q.closing = true
		q.cond.L.Unlock()
	}()

	for {
		q.cond.L.Lock()
		for !q.closing && len(q.queue) == 0 {
			q.cond.Wait()
		}

		if len(q.queue) == 0 {
			q.cond.L.Unlock()
			// We must be shutting down.
			return
		}

		var item Task
		item, q.queue = q.queue[0], q.queue[1:]
		q.cond.L.Unlock()
		// 调用相应的处理函数，实际上就是下面的 Apply 函数
		if err := item.Handler(item.Obj, item.Event); err != nil {
			log.Infof("Work item handle failed (%v), retry after delay %v", err, q.delay)
			time.AfterFunc(q.delay, func() {
				q.Push(item)
			})
		}

	}
}

// ChainHandler applies handlers in a sequence
type ChainHandler struct {
	Funcs []Handler
}

// Apply is the handler function
func (ch *ChainHandler) Apply(obj interface{}, event model.Event) error {
	for _, f := range ch.Funcs {
		if err := f(obj, event); err != nil {
			return err
		}
	}
	return nil
}

// Append a handler as the last handler in the chain
func (ch *ChainHandler) Append(h Handler) {
	ch.Funcs = append(ch.Funcs, h)
}
