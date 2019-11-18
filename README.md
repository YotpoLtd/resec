[![Gitter](https://badges.gitter.im/redis-service-consul/Lobby.svg)](https://gitter.im/redis-service-consul/Lobby?utm_source=badge&utm_medium=badge&utm_campaign=pr-badge) [![Go Report Card](https://goreportcard.com/badge/github.com/seatgeek/resec)](https://goreportcard.com/report/github.com/seatgeek/resec) [![Build Status](https://travis-ci.org/seatgeek/resec.svg?branch=master)](https://travis-ci.org/seatgeek/resec)

<p align="center">
  <img src="https://s.gravatar.com/avatar/96b073f48aae741171d137f21c849d84?s=160" alt="Resec - Consul based highly available Redis replication agent" />
</p>

# Resec - Consul based highly available Redis replication agent

## Description

Resec is a successor to [Redis Sentinel](https://redis.io/topics/sentinel) and [redishappy](https://github.com/mdevilliers/redishappy) for handling high availability failover for Redis.

It avoids Redis Sentinel problems of remembering all the sentinels and all the redis servers that ever appeared in the replication cluster.

Resec master election is based on [Consul Locks](https://www.consul.io/docs/commands/lock.html) to provide single redis master instance.

Resec continuously monitors the status of redis instance and if it's alive, It starts 2 following processes:
* Monitor service of *master* for changes
    * if lock is not acquired, on every change of master it runs *SLAVE OF `Master.Address`*
* Trying to acquire lock to became master itself
    * once lock acquired it stops watching for master service changes
    * promotes redis to be *SLAVE OF NO ONE*

### Services and health checks
Resec registers service with [TTL](https://www.consul.io/docs/agent/checkshtml#TTL) health check with TTL twice as big as `HEALTHCHECK_INTERVAL` and updates consul every `HEALTHCHECK_INTERVAL` to maintain service in passing state

There are 2 options to work with services:
* Use `CONSUL_SERVICE_NAME` for tag based master/slave discovery
  * `MASTER_TAGS` must be provided for ability to watch master instance for changes.
* Use `CONSUL_SERVICE_PREFIX` for service name only based discovery
  * services in consul will look like `CONSUL_SERVICE_PREFIX`-`Replication.Role`

* If `ANNOUNCE_ADDR` is set it will be used for registration in consul, if it's not provided `REDIS_ADDR` will be used for registration in consul.
  * If `REDIS_ADDR` is localhost, only port will be announced to the consul.

### Redis Health
* If redis becomes unhealthy resec will stop the leader election. As soon as redis will become healthy again, resec will start the operation from the beginning.

## Usage

### Environment variables

Environment Variables |  Default       | Description
----------------------| ---------------| -------------------------------------------------
ANNOUNCE_ADDR         |                | IP:Port of Redis to be announced, by default service will be registered wi
CONSUL_SERVICE_NAME   |                | Consul service name for tag based service discovery
CONSUL_SERVICE_PREFIX | redis          | Name Prefix, will be followed by "-(master/slave)", ignored if CONSUL_SERVICE_NAME is used
CONSUL_LOCK_KEY       | resec/.lock    | KV lock location, should be overriden if multiple instances running in the same consul DC
CONSUL_LOCK_SESSION_NAME       | resec | Lock session Name to distinguish multiple resec masters on one host
CONSUL_LOCK_MONITOR_RETRIES | 3        | Number of retries of lock receives 500 Error from Consul
CONSUL_LOCK_MONITOR_RETRY_INTERVAL | 1s | Retry interval if lock receives 500 Error from Consul
CONSUL_DEREGISTER_SERVICE_AFTER | 72h  |
CONSUL_LOCK_TTL       | 15s            |
MASTER_TAGS           |                | Comma separated list of tags to be added to master instance. The first tag (index 0) is used to configure the role of the Redis/resec task, and *must* be different from index 0 in `SLAVE_TAGS`.
SLAVE_TAGS            |                | Comma separated list of tags to be added to slave instance. The first tag (index 0) is used to configure the role of the Redis/resec task, and *must* be different from index 0 in `MASTER_TAGS`.
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
        image = "redis:alpine"
        command = "redis-server"
        args = [
          "/local/redis.conf"
        ]
        port_map {
          db = 6379
        }
      }
      // Let Redis know how much memory he can use not to be killed by OOM
      template {
        data = <<EORC
maxmemory {{ env "NOMAD_MEMORY_LIMIT" | parseInt | subtract 16 }}mb
EORC
        destination   = "local/redis.conf"
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
        image = "seatgeek/resec"
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
  image: seatgeek/resec
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

## Debugging

### Dump state via signal

If you send `USR1` signal to Resec, it will dump the state of the reconciler to `stdout` no matter the log level you started Resec with.

For exampel running `killall -USR1 resec` will yield logging output shown below

```text
(state.Consul) {
 Ready: (bool) true,
 Healthy: (bool) true,
 Master: (bool) true,
 MasterAddr: (string) "",
 MasterPort: (int) 0,
 Stopped: (bool) false
}
(state.Redis) {
 Healthy: (bool) false,
 Ready: (bool) false,
 Info: (state.RedisStatus) {
  Role: (string) "",
  Loading: (bool) false,
  MasterLinkUp: (bool) false,
  MasterLinkDownSince: (time.Duration) 0s,
  MasterSyncInProgress: (bool) false,
  MasterHost: (string) "",
  MasterPort: (int) 0
 },
 InfoString: (string) "",
 Stopped: (bool) false
}
```

if you send `USR2` signal to Resec, it will dump the full reconciler state + all related structs managed by the reconciler.

For example running `killall -USR2 resec` will yield logging output shown below

```text
(*reconciler.Reconciler)(0xc0000fc000)({
 consulCommandCh: (chan<- consul.Command) (cap=10) 0xc00001e660,
 consulState: (state.Consul) {
  Ready: (bool) true,
  Healthy: (bool) true,
  Master: (bool) true,
  MasterAddr: (string) "",
  MasterPort: (int) 0,
  Stopped: (bool) false
 },
 consulStateCh: (<-chan state.Consul) (cap=10) 0xc00001e720,
 forceReconcileInterval: (time.Duration) 2s,
 logger: (*logrus.Entry)(0xc00008c9b0)({
  Logger: (*logrus.Logger)(0xc00008c190)({
   Out: (*os.File)(0xc00000c020)({
    file: (*os.file)(0xc00001e180)({
     pfd: (poll.FD) {
      fdmu: (poll.fdMutex) {
       state: (uint64) 0,
       rsema: (uint32) 0,
       wsema: (uint32) 0
      },
      Sysfd: (int) 2,
      pd: (poll.pollDesc) {
       runtimeCtx: (uintptr) <nil>
      },
      iovecs: (*[]syscall.Iovec)(<nil>),
      csema: (uint32) 0,
      isBlocking: (uint32) 1,
      IsStream: (bool) true,
      ZeroReadIsEOF: (bool) true,
      isFile: (bool) true
     },
     name: (string) (len=11) "/dev/stderr",
     dirinfo: (*os.dirInfo)(<nil>),
     nonblock: (bool) false,
     stdoutOrErr: (bool) true
    })
   }),
   Hooks: (logrus.LevelHooks) {
   },
   Formatter: (*logrus.TextFormatter)(0xc0000710b0)({
    ForceColors: (bool) false,
    DisableColors: (bool) false,
    DisableTimestamp: (bool) false,
    FullTimestamp: (bool) true,
    TimestampFormat: (string) "",
    DisableSorting: (bool) false,
    QuoteEmptyFields: (bool) false,
    isTerminal: (bool) true,
    Once: (sync.Once) {
     m: (sync.Mutex) {
      state: (int32) 0,
      sema: (uint32) 0
     },
     done: (uint32) 1
    }
   }),
   Level: (logrus.Level) info,
   mu: (logrus.MutexWrap) {
    lock: (sync.Mutex) {
     state: (int32) 0,
     sema: (uint32) 0
    },
    disabled: (bool) false
   },
   entryPool: (sync.Pool) {
    noCopy: (sync.noCopy) {
    },
    local: (unsafe.Pointer) 0xc0000e0600,
    localSize: (uintptr) 0xc,
    New: (func() interface {}) <nil>
   }
  }),
  Data: (logrus.Fields) (len=1) {
   (string) (len=6) "system": (string) (len=10) "reconciler"
  },
  Time: (time.Time) 0001-01-01 00:00:00 +0000 UTC,
  Level: (logrus.Level) panic,
  Message: (string) "",
  Buffer: (*bytes.Buffer)(<nil>)
 }),
 reconcile: (bool) false,
 reconcileInterval: (time.Duration) 100ms,
 redisCommandCh: (chan<- redis.Command) (cap=10) 0xc00001e4e0,
 redisState: (state.Redis) {
  Healthy: (bool) false,
  Ready: (bool) false,
  Info: (state.RedisStatus) {
   Role: (string) "",
   Loading: (bool) false,
   MasterLinkUp: (bool) false,
   MasterLinkDownSince: (time.Duration) 0s,
   MasterSyncInProgress: (bool) false,
   MasterHost: (string) "",
   MasterPort: (int) 0
  },
  InfoString: (string) "",
  Stopped: (bool) false
 },
 redisStateCh: (<-chan state.Redis) (cap=10) 0xc00001e480,
 signalCh: (chan os.Signal) (cap=1) 0xc00001e840,
 debugSignalCh: (chan os.Signal) (cap=1) 0xc00001e900,
 stopCh: (chan interface {}) (cap=1) 0xc00001e8a0,
 Mutex: (sync.Mutex) {
  state: (int32) 0,
  sema: (uint32) 0
 }
})
```

### Tracking state changing via logging

If you run `resec` with `LOG_LEVEL=debug`, there will be a log line for each Consul/Redis state change the reconsider sees.

Example can be seen below

```text
DEBU[2019-01-01T16:18:01+01:00] New Consul state                              system=reconciler
DEBU[2019-01-01T16:18:01+01:00] modified: .Master = false                     system=reconciler
DEBU[2019-01-01T16:17:56+01:00] New Redis state                               system=reconciler
DEBU[2019-01-01T16:17:56+01:00] modified: .Healthy = true                     system=reconciler
DEBU[2019-01-01T16:17:56+01:00] New Redis state                               system=reconciler
DEBU[2019-01-01T16:17:56+01:00] modified: .Ready = true                       system=reconciler
DEBU[2019-01-01T16:17:56+01:00] modified: .Info.Role = "master"               system=reconciler
```

## Copyright and license

Code released under the [MIT license](https://github.com/seatgeek/ReSeC/blob/master/LICENSE).
