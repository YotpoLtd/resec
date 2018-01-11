# Resec - Consul based highly available replication agent


## Description

Resec is a replacement to [Redis Sentinel](https://redis.io/topics/sentinel) for handling high availability for Redis.



## Usage

### Environment variables

Environment Variables |  Default       | Description                                       
----------------------| ---------------| ------------------------------------------------- 
ANNOUNCE_ADDR         | :6379          | IP:Port of Redis to be announced                  
CONSUL_SERVICE_PREFIX | redis          | Prefix will be followed by "-(master|slave)"      
CONSUL_LOCK_KEY       | resec/.lock    | KV lock location, should be overriden if multiple instances running in the same consul DC
HEALTHCHECK_INTERVAL  | 5s             |                                                   
HEALTHCHECK_TIMEOUT   | 2s             |                                                   
REDIS_ADDR            | 127.0.0.1:6379 |                                                   
REDIS_PASSWORD        |                |

##### Environment variables to configure communication with consul are similar to [Consul CLI](https://www.consul.io/docs/commands/index.html#environment-variables)

### Permissions

Resec requires permissions to Consul in order to function correctly.
The Consul ACL token is passed as the environment variable `CONSUL_HTTP_TOKEN` .

#### Consul ACL Token Permissions

If the Consul cluster being used is running ACLs; the following ACL policy will allow Replicator the required access to perform all functions based on its default configuration:

```hcl
key "resec/.lock" {
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
        CONSUL_HTTP_ADDR = "${NOMAD_IP_redis}:8500"
        REDIS_ADDR = "${NOMAD_ADDR_redis_db}"
      }

      resources {
        cpu    = 500
        memory = 256
        network {
          mbits = 10
        }
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

### Services and health checks


## Copyright and license

Code released under the [MIT license](https://github.com/YotpoLtd/ReSeC/blob/master/LICENSE).
