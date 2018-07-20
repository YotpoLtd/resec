package redis

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/YotpoLtd/resec/resec/state"
	"github.com/go-redis/redis"
	log "github.com/sirupsen/logrus"
	"gopkg.in/urfave/cli.v1"
)

type connection struct {
	logger    *log.Entry       // logging specificall for Redis
	client    *redis.Client    // redis client
	config    *RedisConfig     // redis config
	state     *state.Redis     // redis state
	StateCh   chan state.Redis // redis state channel to publish updates to the reconciler
	CommandCh chan Command
	stopCh    chan interface{}
}

type RedisConfig struct {
	Address string // address (IP+Port) used to talk to Redis
}

// emit will send a state update to the reconciler
func (rc *connection) emit() {
	rc.StateCh <- *rc.state
}

// runAsMaster sets the instance to be the master
func (rc *connection) runAsMaster() error {
	if err := rc.client.SlaveOf("no", "one").Err(); err != nil {
		return err
	}

	rc.logger.Info("Promoted redis to Master")
	return nil
}

// runAsSlave sets the instance to be a slave for the master
func (rc *connection) runAsSlave(masterAddress string, masterPort int) error {
	rc.logger.Infof("Enslaving redis %s to be slave of %s:%d", rc.config.Address, masterAddress, masterPort)

	if err := rc.client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err(); err != nil {
		return fmt.Errorf("Could not enslave redis %s to be slave of %s:%d (%v)", rc.config.Address, masterAddress, masterPort, err)
	}

	rc.logger.Infof("Enslaved redis %s to be slave of %s:%d", rc.config.Address, masterAddress, masterPort)
	return nil
}

func (rc *connection) cleanup() {
	close(rc.stopCh)
}

func (rc *connection) start() {
	go rc.watchReplicationStatus()
	rc.waitForRedisToBeReady()
}

func (rc *connection) CommandRunner() {
	for {
		select {

		case <-rc.stopCh:
			return

		case payload := <-rc.CommandCh:
			switch payload.command {
			case StartCommand:
				rc.start()

			case StopCommand:
				rc.cleanup()

			case RunAsMasterCommand:
				rc.runAsMaster()

			case RunAsSlaveCommand:
				rc.runAsSlave(payload.consulState.MasterAddr, payload.consulState.MasterPort)
			}
		}
	}
}

// watchReplicationStatus checks redis replication status
func (rc *connection) watchReplicationStatus() {
	ticker := time.NewTicker(time.Second)

	for ; true; <-ticker.C {
		result, err := rc.client.Info("replication").Result()
		// any failure will trigger a disconnect event
		if err != nil {
			rc.state.Connected = false
			rc.emit()

			rc.logger.Errorf("Can't connect to redis: %+v", err)
			continue
		}

		// if we previously was disconnected, but now succeded again, emit a (re)connected event
		if rc.state.Connected == false {
			rc.state.Connected = true
			rc.emit()
		}

		kvPair := rc.parseKeyValue(result)

		// Create new replication state
		replicationState := state.RedisReplicationState{
			Role: kvPair["role"],
		}

		// Add master_host to state (if set) - only available for slaves
		if masterHost, ok := kvPair["master_host"]; ok {
			replicationState.MasterHost = masterHost
		}

		// Add master_port to state (if set) - only available for slaves
		if masterPortString, ok := kvPair["master_port"]; ok {
			masterPort, err := strconv.Atoi(masterPortString)
			if err == nil {
				replicationState.MasterPort = masterPort
			}
		}

		// compare current and new state, if no changes, don't publish
		// a new state to the reconciler
		if replicationState.Changed(rc.state.Replication) == false {
			continue
		}

		rc.state.Replication = replicationState
		rc.state.ReplicationString = result
		rc.emit()
	}
}

// waitForRedisToBeReady will check if we got the initial redis state we need
// for the reconciler to do its job right out of the box
func (rc *connection) waitForRedisToBeReady() {
	t := time.NewTicker(500 * time.Millisecond)

	for ; true; <-t.C {
		// if we got replication data from redis, we are ready
		if rc.state.Replication.Role != "" {
			rc.state.Ready = true
			rc.emit()

			return
		}
	}
}

func (rc *connection) parseKeyValue(str string) map[string]string {
	res := make(map[string]string)

	lines := strings.Split(str, "\r\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}

		pair := strings.Split(line, ":")
		if len(pair) != 2 {
			continue
		}

		res[pair[0]] = pair[1]
	}

	return res
}

func (rc *connection) Config() RedisConfig {
	return *rc.config
}

func NewRedisConnection(c *cli.Context) (*connection, error) {
	redisConfig := &RedisConfig{
		Address: c.String("redis-addr"),
	}

	instance := &connection{
		client: redis.NewClient(&redis.Options{
			Addr:        redisConfig.Address,
			DialTimeout: c.Duration("healthcheck-timeout"),
			Password:    c.String("redis-password"),
			ReadTimeout: c.Duration("healthcheck-timeout"),
		}),
		config:    redisConfig,
		logger:    log.WithField("system", "redis"),
		state:     &state.Redis{},
		StateCh:   make(chan state.Redis, 1),
		CommandCh: make(chan Command, 1),
		stopCh:    make(chan interface{}, 1),
	}

	if err := instance.client.Ping().Err(); err != nil {
		return nil, fmt.Errorf("Can't communicate to Redis server: %s", err)
	}

	return instance, nil
}
