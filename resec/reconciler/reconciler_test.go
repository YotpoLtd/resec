package reconciler

import (
	"os"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	"github.com/YotpoLtd/resec/resec/state"
	"github.com/stretchr/testify/assert"
)

type helper struct {
	consulCommandCh  chan consul.Command
	consulCommands   []consul.Command
	consulStateCh    chan state.Consul
	consulStateQueue []state.Consul
	consulStateSent  int
	reconciler       *Reconciler
	redisCommandCh   chan redis.Command
	redisCommands    []redis.Command
	redisStateCh     chan state.Redis
	redisStateQueue  []state.Redis
	redisStateSent   int
}

func (h *helper) consume() {
	go func() {
		for {
			select {
			case <-h.reconciler.stopCh:
				return
			case cmd := <-h.redisCommandCh:
				h.redisCommands = append(h.redisCommands, cmd)
			case cmd := <-h.consulCommandCh:
				h.consulCommands = append(h.consulCommands, cmd)
			}
		}
	}()
}

func (h *helper) runFor(t time.Duration) {
	go h.reconciler.Run()

	go func() {
		for _, state := range h.redisStateQueue {
			spew.Dump(state)
			h.redisStateCh <- state
			h.redisStateSent++
			time.Sleep(h.reconciler.reconcileInterval * 2)
		}
	}()

	go func() {
		for _, state := range h.consulStateQueue {
			spew.Dump(state)
			h.consulStateCh <- state
			h.consulStateSent++
			time.Sleep(h.reconciler.reconcileInterval * 2)
		}
	}()

	time.Sleep(t)
	close(h.reconciler.stopCh)
	time.Sleep(10 * time.Millisecond)
}

func (h *helper) queueConsulState(s state.Consul) {
	h.consulStateQueue = append(h.consulStateQueue, s)
}

func (h *helper) queueRedisState(s state.Redis) {
	h.redisStateQueue = append(h.redisStateQueue, s)
}

func (h *helper) limitConsulCommands(limit int) {
	if len(h.consulCommands) > limit {
		h.consulCommands = h.consulCommands[:limit]
	}
}

func (h *helper) limitRedisCommands(limit int) {
	if len(h.redisCommands) > limit {
		h.redisCommands = h.redisCommands[:limit]
	}
}

func (h *helper) listConsulCommands() []string {
	r := make([]string, 0)

	for _, cmd := range h.consulCommands {
		r = append(r, cmd.Name())
	}

	return r
}

func (h *helper) listRedisCommands() []string {
	r := make([]string, 0)

	for _, cmd := range h.redisCommands {
		r = append(r, cmd.Name())
	}

	return r
}

func newTestReconsiler() *helper {
	consulCommandCh := make(chan consul.Command, 1)
	consulStateCh := make(chan state.Consul, 1)

	redisCommandCh := make(chan redis.Command, 1)
	redisStateCh := make(chan state.Redis, 1)

	r := &Reconciler{
		consulCommandCh:        consulCommandCh,
		consulStateCh:          consulStateCh,
		forceReconcileInterval: 5 * time.Millisecond,
		reconcileInterval:      time.Millisecond,
		redisCommandCh:         redisCommandCh,
		redisStateCh:           redisStateCh,
		signalCh:               make(chan os.Signal, 0),
		stopCh:                 make(chan interface{}, 0),
	}

	redisCommands := make([]redis.Command, 0)
	consulCommands := make([]consul.Command, 0)

	return &helper{
		reconciler:       r,
		consulCommandCh:  consulCommandCh,
		consulStateCh:    consulStateCh,
		redisCommandCh:   redisCommandCh,
		redisStateCh:     redisStateCh,
		redisCommands:    redisCommands,
		consulCommands:   consulCommands,
		redisStateQueue:  make([]state.Redis, 0),
		consulStateQueue: make([]state.Consul, 0),
	}
}

func TestReconciler_RunWithMaster(t *testing.T) {
	helper := newTestReconsiler()
	helper.consume()
	helper.queueConsulState(state.Consul{Ready: true, Healthy: true, LockIsHeld: true})
	helper.queueRedisState(state.Redis{Ready: true, Connected: true, Replication: state.RedisReplicationState{Role: "master"}})
	helper.runFor(50 * time.Millisecond)

	helper.limitConsulCommands(3)

	assert.Equal(t, []string{"start", "update_service", "update_service"}, helper.listConsulCommands())
	assert.Equal(t, []string{"start"}, helper.listRedisCommands())
}

func TestReconciler_RunBecomeMaster(t *testing.T) {
	helper := newTestReconsiler()
	helper.consume()
	helper.queueConsulState(state.Consul{Ready: true, Healthy: true, LockIsHeld: false})
	helper.queueConsulState(state.Consul{Ready: true, Healthy: true, LockIsHeld: true})
	helper.queueRedisState(state.Redis{Ready: true, Connected: true, Replication: state.RedisReplicationState{Role: ""}})
	helper.queueRedisState(state.Redis{Ready: true, Connected: true, Replication: state.RedisReplicationState{Role: ""}})
	helper.queueRedisState(state.Redis{Ready: true, Connected: true, Replication: state.RedisReplicationState{Role: "master"}})
	helper.runFor(50 * time.Millisecond)

	helper.limitConsulCommands(4)

	assert.Equal(t, []string{"start", "register_service", "update_service", "update_service"}, helper.listConsulCommands())
	assert.Equal(t, []string{"start", "run_as_master"}, helper.listRedisCommands())
}
