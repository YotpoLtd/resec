# ReSeC - Consul based highly available replication agent


## Description

ReSeC is a replacement to


## Usage

### 1. Set environment variables

Environment Variables | Description                                       |  Default
----------------------| ------------------------------------------------- | -----------------
ANNOUNCE_ADDR         |                                                   | 127.0.0.1:6379
CONSUL_SERVICE_NAME   | The `S3 bucket` to be proxied with this app.      | redis
CONSUL_LOCK_KEY       | If it's specified, the path always returns 200 OK | resec/.lock
CONSUL_CHECK_INTERVAL |                                                   | 5s
CONSUL_CHECK_TIMEOUT  |                                                   | 2s
REDIS_ADDR            |                                                   | 127.0.0.1:6379



### 2. Run the application


## Copyright and license

Code released under the [MIT license](https://github.com/YotpoLtd/ReSeC/blob/master/LICENSE).
