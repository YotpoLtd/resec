package resec

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis"
	log "github.com/sirupsen/logrus"
)

type redisConnection struct {
	logger  *log.Entry      // logging specificall for Redis
	client  *redis.Client   // redis client
	config  *redisConfig    // redis config
	state   *redisState     // redis state
	stateCh chan redisState // redis state channel to publish updates to the reconsiler
}

type redisConfig struct {
	address  string // address (IP+Port) used to talk to Redis
	password string // password used to talk to Redis
}

// Redis state represent the full state of the connection with Redis
type redisState struct {
	connected         bool                  // are we able to connect to Redis?
	ready             bool                  // are we ready to provide state for the reconsiler?
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

// emit will send a state update to the reconsiler
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
	// go rc.watchServerStatus()
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

		kvPair := parseKeyValue(result)

		replicationState := redisReplicationState{}
		replicationState.role = kvPair["role"]

		if masterHost, ok := kvPair["master_host"]; ok {
			replicationState.masterHost = masterHost
		}

		if masterPortString, ok := kvPair["master_port"]; ok {
			masterPort, err := strconv.Atoi(masterPortString)
			if err == nil {
				replicationState.masterPort = masterPort
			}
		}

		if replicationState.changed(rc.state.replication) == false {
			// rc.logger.Debugf("Redis replication state did not change")
			continue
		}

		rc.state.replication = replicationState
		rc.state.replicationString = result
		rc.emit()
	}
}

// watchServerStatus checks redis server uptime
func (rc *redisConnection) watchServerStatus() {
	lastUptime := 0
	connectionErrors := 0
	allowedConnectionErrors := 3

	ticker := time.NewTicker(time.Second)
	for ; true; <-ticker.C {
		rc.logger.Debug("Checking redis server info")

		result, err := rc.client.Info("server").Result()
		if err != nil {
			rc.logger.Warnf("Could not query for server info: %s", err)
			connectionErrors++

			if connectionErrors > allowedConnectionErrors {
				rc.logger.Error("Too many connection errors, shutting down")
				// TODO: trigger event to stop
			}

			continue
		}
		connectionErrors = 0

		parsed := parseKeyValue(result)
		uptimeString, ok := parsed["uptime_in_seconds"]
		if !ok {
			rc.logger.Error("Could not find 'uptime_in_seconds' in server info respone")
			continue
		}

		uptime, err := strconv.Atoi(uptimeString)
		if err != nil {
			rc.logger.Error("Could not parse 'uptime_in_seconds' to integer")
			continue
		}

		if uptime < lastUptime {
			rc.logger.Errorf("Current uptime (%d) is less than previous (%d) - Redis likely restarted - stopping resec", uptime, lastUptime)
			// TODO: trigger event to stop
			continue
		}

		lastUptime = uptime
	}
}

// waitForRedisToBeReady will check if we got the initial redis state we need
// for the reconsiler to do its job right out of the box
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

func parseKeyValue(str string) map[string]string {
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

func newRedisConnection(config *config) (*redisConnection, error) {
	redisConfig := &redisConfig{}
	redisConfig.address = "127.0.0.1:6379"

	if redisAddr := os.Getenv(RedisAddr); redisAddr != "" {
		redisConfig.address = redisAddr
	}

	if redisPassword := os.Getenv(RedisPassword); redisPassword != "" {
		redisConfig.password = redisPassword
	}

	redisOptions := &redis.Options{
		Addr:        redisConfig.address,
		DialTimeout: config.healthCheckTimeout,
		ReadTimeout: config.healthCheckTimeout,
	}

	if redisConfig.password != "" {
		redisOptions.Password = redisConfig.password
	}

	connection := &redisConnection{}
	connection.logger = log.WithField("system", "redis")
	connection.config = redisConfig
	connection.client = redis.NewClient(redisOptions)
	if err := connection.client.Ping().Err(); err != nil {
		return nil, fmt.Errorf("[CRITICAL] Can't communicate to Redis server: %s", err)
	}
	connection.state = &redisState{}
	connection.stateCh = make(chan redisState, 1)

	return connection, nil
}
