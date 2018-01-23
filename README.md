[![Gitter](https://badges.gitter.im/redis-service-consul/Lobby.svg)](https://gitter.im/redis-service-consul/Lobby?utm_source=badge&utm_medium=badge&utm_campaign=pr-badge) [![Go Report Card](https://goreportcard.com/badge/github.com/YotpoLtd/resec)](https://goreportcard.com/report/github.com/YotpoLtd/resec) [![Build Status](https://travis-ci.org/YotpoLtd/resec.svg?branch=master)](https://travis-ci.org/YotpoLtd/resec)

<p align="center">
  <img src="https://s.gravatar.com/avatar/96b073f48aae741171d137f21c849d84?s=160" alt="Resec - Consul based highly available Redis replication agent" />
</p>

# Resec - Consul based highly available Redis replication agent

## Description

Resec is a successor to [Redis Sentinel](https://redis.io/topics/sentinel) and [redishappy](https://github.com/mdevilliers/redishappy) for handling high availability failover for Redis.

It avoids Redis Sentinel problems of remembering all the sentinels and all the redis servers that ever appeared in the replication cluster.

Resec master election is based on [Consul Locks](https://www.consul.io/docs/commands/lock.html) to provide single redis master instance.

Resec continuously monitors the status of redis instance and if it's alive, It starts 2 following processes:
* Monitor ${CONSUL_SERVICE_PREFIX}-master service for changes
    * if lock is not acquired, on every change of ${CONSUL_SERVICE_PREFIX}-master it runs *SLAVE OF ${CONSUL_SERVICE_PREFIX}-master*
* Trying to acquire lock to became master itself
    * once lock acquired it stops watching for ${CONSUL_SERVICE_PREFIX}-master service changes
    * promotes redis to be *SLAVE OF NO ONE*

### Services and health checks
* Resec registers service ${CONSUL_SERVICE_PREFIX}-${REPLICATION_ROLE} with [TTL](https://www.consul.io/docs/agent/checks.html#TTL) health check with TTL twice as big as HEALTHCHECK_INTERVAL and updates consul every HEALTHCHECK_INTERVAL to maintain service in passing state

### Redis Health
* If redis becomes unhealthy resec will stop the leader election. As soon as redis will become healthy again, resec will start the operation from the beginning.

## Usage

### Environment variables

Environment Variables |  Default       | Description                                       
----------------------| ---------------| ------------------------------------------------- 
ANNOUNCE_ADDR         | :6379          | IP:Port of Redis to be announced                  
CONSUL_SERVICE_PREFIX | redis          | Prefix will be followed by "-(master|slave)"      
CONSUL_LOCK_KEY       | resec/.lock    | KV lock location, should be overriden if multiple instances running in the same consul DC
CONSUL_DEREGISTER_SERVICE_AFTER | 72h |
CONSUL_LOCK_TTL       | 15s         |
HEALTHCHECK_INTERVAL  | 5s             |                                                   
HEALTHCHECK_TIMEOUT   | 2s             |                                                   
REDIS_ADDR            | 127.0.0.1:6379 |                                                   
REDIS_PASSWORD        |                |
LOG_LEVEL             | INFO           | Options are "DEBUG", "INFO", "WARN", "ERROR"

##### Environment variables to configure communication with consul are similar to [Consul CLI](https://www.consul.io/docs/commands/index.html#environment-variables)

### Permissions

Resec requires permissions for Consul in order to function correctly.
The Consul ACL token is passed as the environment variable `CONSUL_HTTP_TOKEN` .

#### Consul ACL Token Permissions

If the Consul cluster being used is running ACLs; the following ACL policy will allow Replicator the required access to perform all functions based on its default configuration:

```hcl
key "resec/" {
  policy = "write"
}
session "" {
  policy = "write"
}
service "" {
  policy = "write"
}
```

### Run the application

* with nomad:
```hcl
job "resec" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel = 1
    stagger      = "10s"
  }

  group "cache" {
    count = 3

    task "redis" {
      driver = "docker"
      config {
        image = "redis"
        port_map {
          db = 6379
        }
      }

      resources {
        cpu    = 500
        memory = 256
        network {
          mbits = 10
          port "db" {}
        }
      }
    }
    
    task "resec" {
      driver = "docker"
      config {
        image = "yotpo/resec"
      }
      
      env {
        CONSUL_HTTP_ADDR = "http://${attr.unique.network.ip-address}:8500"
        REDIS_ADDR = "${NOMAD_ADDR_redis_db}"
      }

      resources {
        cpu    = 100
        memory = 64
        network {
          mbits = 10
        }
      }
    }
  }
}
```

* with docker-compose.yml:  

```yaml
resec:
  image: yotpo/resec
  environment:
    - CONSUL_HTTP_ADDR=1.2.3.4:8500
    - REDIS_ADDR=redis:6379
  container_name: resec
```

* with SystemD:
```
[Unit]
Description=resec - redis ha replication daemon
Requires=network-online.target
After=network-online.target

[Service]
EnvironmentFile=-/etc/default/resec
ExecStart=/usr/local/bin/resec
KillSignal=SIGQUIT
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target

```

## Copyright and license

Code released under the [MIT license](https://github.com/YotpoLtd/ReSeC/blob/master/LICENSE).
