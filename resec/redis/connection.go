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

// emit will send a state update to the reconciler
func (c *Connection) emit() {
	c.stateCh <- *c.state
}

// runAsMaster sets the instance to be the master
func (c *Connection) runAsMaster() error {
	if err := c.client.SlaveOf("no", "one").Err(); err != nil {
		return err
	}

	c.logger.Info("Promoted redis to Master")
	return nil
}

// runAsSlave sets the instance to be a slave for the master
func (c *Connection) runAsSlave(masterAddress string, masterPort int) error {
	c.logger.Infof("Enslaving redis %s to be slave of %s:%d", c.config.Address, masterAddress, masterPort)

	if err := c.client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err(); err != nil {
		return fmt.Errorf("Could not enslave redis %s to be slave of %s:%d (%v)", c.config.Address, masterAddress, masterPort, err)
	}

	c.logger.Infof("Enslaved redis %s to be slave of %s:%d", c.config.Address, masterAddress, masterPort)
	return nil
}

func (c *Connection) cleanup() {
	close(c.stopCh)

	c.state.Stopped = true
	c.emit()
}

func (c *Connection) start() {
	go c.watchReplicationStatus()
	c.waitForRedisToBeReady()
}

func (c *Connection) CommandRunner() {
	for {
		select {

		case <-c.stopCh:
			return

		case payload := <-c.commandCh:
			switch payload.name {
			case StartCommand:
				c.start()

			case StopCommand:
				c.cleanup()

			case RunAsMasterCommand:
				c.runAsMaster()

			case RunAsSlaveCommand:
				c.runAsSlave(payload.consulState.MasterAddr, payload.consulState.MasterPort)
			}
		}
	}
}

// watchReplicationStatus checks redis replication status
func (c *Connection) watchReplicationStatus() {
	ticker := time.NewTicker(time.Second)

	for ; true; <-ticker.C {
		result, err := c.client.Info("replication").Result()
		// any failure will trigger a disconnect event
		if err != nil {
			c.state.Healthy = false
			c.emit()

			c.logger.Errorf("Can't connect to redis: %+v", err)
			continue
		}

		// if we previously was disconnected, but now succeded again, emit a (re)connected event
		if c.state.Healthy == false {
			c.state.Healthy = true
			c.emit()
		}

		kvPair := c.parseKeyValue(result)

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
		if replicationState.Changed(c.state.Replication) == false {
			continue
		}

		c.state.Replication = replicationState
		c.state.ReplicationString = result
		c.emit()
	}
}

// waitForRedisToBeReady will check if we got the initial redis state we need
// for the reconciler to do its job right out of the box
func (c *Connection) waitForRedisToBeReady() {
	t := time.NewTicker(500 * time.Millisecond)

	for ; true; <-t.C {
		// if we got replication data from redis, we are ready
		if c.state.Replication.Role != "" {
			c.state.Ready = true
			c.emit()

			return
		}
	}
}

func (c *Connection) parseKeyValue(str string) map[string]string {
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

func (c *Connection) Config() RedisConfig {
	return *c.config
}

func (c *Connection) StateChReader() <-chan state.Redis {
	return c.stateCh
}

func (c *Connection) CommandChWriter() chan<- Command {
	return c.commandCh
}

func NewConnection(c *cli.Context) (*Connection, error) {
	redisConfig := &RedisConfig{
		Address: c.String("redis-addr"),
	}

	instance := &Connection{
		client: redis.NewClient(&redis.Options{
			Addr:        redisConfig.Address,
			DialTimeout: c.Duration("healthcheck-timeout"),
			Password:    c.String("redis-password"),
			ReadTimeout: c.Duration("healthcheck-timeout"),
		}),
		config:    redisConfig,
		logger:    log.WithField("system", "redis"),
		state:     &state.Redis{},
		stateCh:   make(chan state.Redis, 1),
		commandCh: make(chan Command, 1),
		stopCh:    make(chan interface{}, 1),
	}

	if err := instance.client.Ping().Err(); err != nil {
		return nil, fmt.Errorf("Can't communicate to Redis server: %s", err)
	}

	return instance, nil
}
