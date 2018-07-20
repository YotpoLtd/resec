package resec

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis"
	log "github.com/sirupsen/logrus"
	"gopkg.in/urfave/cli.v1"
)

type redisConnection struct {
	logger  *log.Entry      // logging specificall for Redis
	client  *redis.Client   // redis client
	config  *redisConfig    // redis config
	state   *redisState     // redis state
	stateCh chan redisState // redis state channel to publish updates to the reconciler
}

type redisConfig struct {
	address string // address (IP+Port) used to talk to Redis
}

// Redis state represent the full state of the connection with Redis
type redisState struct {
	connected         bool                  // are we able to connect to Redis?
	ready             bool                  // are we ready to provide state for the reconciler?
	replication       redisReplicationState // current replication data
	replicationString string                // raw replication info
}

type redisReplicationState struct {
	role       string // current redis role (master or slave)
	masterHost string // if slave, the master hostname its replicating from
	masterPort int    // if slave, the master port its replicating from
}

// changed will test if the current replication state is different from
// the new one passed in as argument
func (r *redisReplicationState) changed(new redisReplicationState) bool {
	if r.role != new.role {
		return true
	}

	if r.masterHost != new.masterHost {
		return true
	}

	if r.masterPort != new.masterPort {
		return true
	}

	return false
}

// emit will send a state update to the reconciler
func (rc *redisConnection) emit() {
	rc.stateCh <- *rc.state
}

// runAsMaster sets the instance to be the master
func (rc *redisConnection) runAsMaster() error {
	if err := rc.client.SlaveOf("no", "one").Err(); err != nil {
		return err
	}

	rc.logger.Info("Promoted redis to Master")
	return nil
}

// runAsSlave sets the instance to be a slave for the master
func (rc *redisConnection) runAsSlave(masterAddress string, masterPort int) error {
	rc.logger.Infof("Enslaving redis %s to be slave of %s:%d", rc.config.address, masterAddress, masterPort)

	if err := rc.client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err(); err != nil {
		return fmt.Errorf("Could not enslave redis %s to be slave of %s:%d (%v)", rc.config.address, masterAddress, masterPort, err)
	}

	rc.logger.Infof("Enslaved redis %s to be slave of %s:%d", rc.config.address, masterAddress, masterPort)
	return nil
}

func (rc *redisConnection) start() {
	go rc.watchReplicationStatus()
	rc.waitForRedisToBeReady()
}

// watchReplicationStatus checks redis replication status
func (rc *redisConnection) watchReplicationStatus() {
	ticker := time.NewTicker(time.Second)

	for ; true; <-ticker.C {
		// rc.logger.Debug("Checking redis replication status")

		result, err := rc.client.Info("replication").Result()
		if err != nil {
			err = fmt.Errorf("Can't connect to redis running on %s", rc.config.address)

			rc.state.connected = false
			rc.emit()

			rc.logger.Error(err)
			continue
		}

		if rc.state.connected == false {
			rc.state.connected = true
			rc.emit()
		}

		kvPair := rc.parseKeyValue(result)

		// Create new replication state
		replicationState := redisReplicationState{
			role: kvPair["role"],
		}

		// Add master_host to state (if set) - only available for slaves
		if masterHost, ok := kvPair["master_host"]; ok {
			replicationState.masterHost = masterHost
		}

		// Add master_port to state (if set) - only available for slaves
		if masterPortString, ok := kvPair["master_port"]; ok {
			masterPort, err := strconv.Atoi(masterPortString)
			if err == nil {
				replicationState.masterPort = masterPort
			}
		}

		// compare current and new state, if no changes, don't publish
		// a new state to the reconciler
		if replicationState.changed(rc.state.replication) == false {
			continue
		}

		rc.state.replication = replicationState
		rc.state.replicationString = result
		rc.emit()
	}
}

// waitForRedisToBeReady will check if we got the initial redis state we need
// for the reconciler to do its job right out of the box
func (rc *redisConnection) waitForRedisToBeReady() {
	t := time.NewTicker(time.Second)

	for ; true; <-t.C {
		// if we got replication data from redis, we are ready
		if rc.state.replication.role != "" {
			rc.state.ready = true
			rc.emit()

			return
		}
	}
}

func (rc *redisConnection) parseKeyValue(str string) map[string]string {
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

func newRedisConnection(c *cli.Context) (*redisConnection, error) {
	redisConfig := &redisConfig{
		address: c.String("redis-addr"),
	}

	connection := &redisConnection{
		client: redis.NewClient(&redis.Options{
			Addr:        redisConfig.address,
			DialTimeout: c.Duration("healthcheck-timeout"),
			Password:    c.String("redis-password"),
			ReadTimeout: c.Duration("healthcheck-timeout"),
		}),
		config:  redisConfig,
		logger:  log.WithField("system", "redis"),
		state:   &redisState{},
		stateCh: make(chan redisState, 1),
	}

	if err := connection.client.Ping().Err(); err != nil {
		return nil, fmt.Errorf("Can't communicate to Redis server: %s", err)
	}

	return connection, nil
}
