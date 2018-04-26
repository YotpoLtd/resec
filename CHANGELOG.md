## 0.4.0 (April 26, 2018)

FEATURES:

  * Service check now registered as initially passing to avoid service flapping

FIXES:

  * Handling slave session invalidation
  * Prevent from shutting down if local consul agent is not healthy

## 0.3.0 (April 23, 2018)

FEATURES:

  * Consul lock session name can be adjusted
  * Consul lock retry option added
  * Dependencies updated (Consul to 1.0.7, go-redis to 6.10.2)

## 0.2.0 (March 13, 2018)

FEATURES:

  * Consul service name for tag based service discovery
  * Option to ADD (MASTER|SLAVE)_TAGS to be populated to consul
  * Managing dependencies using [dep](https://github.com/golang/dep)
  * Go version bumped to 0.10.x

## 0.1.0 (January 22, 2018)

 * Initial release

FEATURES:

  * Redis Health monitoring
  * Consul Agent outage handling
  * Looks working =)
